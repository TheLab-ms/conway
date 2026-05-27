package discordbot

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/members"
	"github.com/stretchr/testify/require"
)

// fakeQueuer captures QueueMessage calls; the bot tests only exercise
// inbound interactions, so the outbound path is asserted elsewhere.
type fakeQueuer struct {
	mu   sync.Mutex
	msgs []queuedMsg
}

type queuedMsg struct{ url, payload string }

func (f *fakeQueuer) QueueMessage(_ context.Context, url, payload string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, queuedMsg{url, payload})
	return nil
}

func (f *fakeQueuer) QueueTemplateMessage(_ context.Context, _, _ string, _ map[string]string) error {
	return nil
}

// testKey returns a freshly-generated key pair plus the hex-encoded public
// key, suitable for injecting into the module config.
func testKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return priv, hex.EncodeToString(pub)
}

// signedPost builds an http.Request POSTed to /discord/interactions with the
// correct Discord signature headers for `body`.
func signedPost(t *testing.T, priv ed25519.PrivateKey, body []byte) *http.Request {
	t.Helper()
	ts := "1700000000"
	sig := ed25519.Sign(priv, append([]byte(ts), body...))
	r := httptest.NewRequest(http.MethodPost, "/discord/interactions", bytes.NewReader(body))
	r.Header.Set("X-Signature-Ed25519", hex.EncodeToString(sig))
	r.Header.Set("X-Signature-Timestamp", ts)
	return r
}

// newTestHandler returns a fully-wired http.HandlerFunc plus the underlying
// *Module, with the config injected directly (no config.Store needed).
func newTestHandler(t *testing.T, cfg Config) (http.HandlerFunc, *Module, *fakeQueuer) {
	t.Helper()
	db := members.NewTestDB(t)
	fq := &fakeQueuer{}
	m := New(db, engine.NewEventLogger(db, "discordbot"), fq)
	handler := func(w http.ResponseWriter, r *http.Request) {
		m.handleInteractionWithConfig(w, r, &cfg)
	}
	return handler, m, fq
}

func insertMember(t *testing.T, m *Module, email string) int64 {
	t.Helper()
	res, err := m.db.Exec(
		"INSERT INTO members (name, email, created) VALUES (?, ?, strftime('%s','now'))",
		"Test User", email)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) interactionResponse {
	t.Helper()
	var out interactionResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	return out
}

func TestHandleInteraction_PingReturnsPong(t *testing.T) {
	t.Parallel()
	priv, pubHex := testKey(t)
	handler, _, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: pubHex})

	body := []byte(`{"type":1}`)
	r := signedPost(t, priv, body)
	w := httptest.NewRecorder()
	handler(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, responseTypePong, decodeResp(t, w).Type)
}

func TestHandleInteraction_BadSignatureRejected(t *testing.T) {
	t.Parallel()
	priv, pubHex := testKey(t)
	handler, _, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: pubHex})

	// Sign one body, send a different one.
	good := []byte(`{"type":1}`)
	r := signedPost(t, priv, good)
	r.Body = http.NoBody
	r = httptest.NewRequest(http.MethodPost, "/discord/interactions",
		bytes.NewReader([]byte(`{"type":2}`)))
	// Re-attach the headers from the signed request.
	signed := signedPost(t, priv, good)
	r.Header = signed.Header

	w := httptest.NewRecorder()
	handler(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestHandleInteraction_NotConfigured(t *testing.T) {
	t.Parallel()
	priv, _ := testKey(t)
	handler, _, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: ""})

	body := []byte(`{"type":1}`)
	r := signedPost(t, priv, body)
	w := httptest.NewRecorder()
	handler(w, r)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestHandleInteraction_ComponentUpdatesDiscount(t *testing.T) {
	t.Parallel()
	priv, pubHex := testKey(t)
	handler, m, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: pubHex})

	memberID := insertMember(t, m, "applicant@example.com")
	body := componentBody(t, memberID, "student", "discord-uid-1", "alice")

	r := signedPost(t, priv, body)
	w := httptest.NewRecorder()
	handler(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	resp := decodeResp(t, w)
	require.Equal(t, responseTypeUpdateMessage, resp.Type)
	require.NotNil(t, resp.Data)
	require.NotNil(t, resp.Data.Components, "must be a non-nil slice so JSON is [] not null")
	require.Len(t, resp.Data.Components, 0, "empty components array removes the picker")
	require.Len(t, resp.Data.Embeds, 1)
	require.Contains(t, resp.Data.Embeds[0].Description, "Student")
	require.Contains(t, resp.Data.Embeds[0].Description, "<@discord-uid-1>")

	// Verify the DB was actually updated.
	var stored string
	require.NoError(t, m.db.QueryRow(
		"SELECT discount_type FROM members WHERE id=?", memberID).Scan(&stored))
	require.Equal(t, "student", stored)

	// And an audit event was logged.
	var n int
	require.NoError(t, m.db.QueryRow(
		`SELECT COUNT(*) FROM module_events WHERE module='discordbot' AND event_type='DiscountSetViaDiscord'`).Scan(&n))
	require.Equal(t, 1, n)
}

func TestHandleInteraction_ComponentNoneClearsDiscount(t *testing.T) {
	t.Parallel()
	priv, pubHex := testKey(t)
	handler, m, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: pubHex})

	memberID := insertMember(t, m, "x@y.z")
	// Preset a non-null discount so we can prove None clears it.
	_, err := m.db.Exec("UPDATE members SET discount_type='student' WHERE id=?", memberID)
	require.NoError(t, err)

	body := componentBody(t, memberID, noneSentinel, "u1", "alice")
	r := signedPost(t, priv, body)
	w := httptest.NewRecorder()
	handler(w, r)
	require.Equal(t, http.StatusOK, w.Code)

	var stored *string
	require.NoError(t, m.db.QueryRow(
		"SELECT discount_type FROM members WHERE id=?", memberID).Scan(&stored))
	require.Nil(t, stored, "_none sentinel must clear discount_type to NULL")
}

func TestHandleInteraction_ComponentRejectsUnknownDiscount(t *testing.T) {
	t.Parallel()
	priv, pubHex := testKey(t)
	handler, m, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: pubHex})

	memberID := insertMember(t, m, "x@y.z")
	body := componentBody(t, memberID, "made-up-tier", "u1", "alice")

	r := signedPost(t, priv, body)
	w := httptest.NewRecorder()
	handler(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	resp := decodeResp(t, w)
	require.Equal(t, responseTypeChannelMsg, resp.Type, "ephemeral error reply")
	require.NotNil(t, resp.Data)
	require.Equal(t, flagEphemeral, resp.Data.Flags)
	require.Contains(t, strings.ToLower(resp.Data.Content), "unknown")

	// DB untouched.
	var stored *string
	require.NoError(t, m.db.QueryRow(
		"SELECT discount_type FROM members WHERE id=?", memberID).Scan(&stored))
	require.Nil(t, stored)
}

func TestHandleInteraction_ComponentMissingMember(t *testing.T) {
	t.Parallel()
	priv, pubHex := testKey(t)
	handler, _, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: pubHex})

	body := componentBody(t, 999999, "student", "u1", "alice")
	r := signedPost(t, priv, body)
	w := httptest.NewRecorder()
	handler(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	resp := decodeResp(t, w)
	require.Equal(t, responseTypeChannelMsg, resp.Type)
	require.Equal(t, flagEphemeral, resp.Data.Flags)
}

// componentBody returns the JSON body Discord would POST for a string-select
// click on the signup message.
func componentBody(t *testing.T, memberID int64, value, userID, username string) []byte {
	t.Helper()
	b := map[string]any{
		"type": interactionTypeComponent,
		"data": map[string]any{
			"custom_id": fmt.Sprintf("%s%d", customIDPrefix, memberID),
			"values":    []string{value},
		},
		"member": map[string]any{
			"user": map[string]any{"id": userID, "username": username},
		},
	}
	out, err := json.Marshal(b)
	require.NoError(t, err)
	return out
}
