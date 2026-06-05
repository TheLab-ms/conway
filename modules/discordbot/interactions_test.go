package discordbot

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/members"
	"github.com/stretchr/testify/require"
)

// fakeQueuer captures QueueMessage calls; the bot tests only exercise inbound
// interactions, so the outbound path is asserted elsewhere.
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

// testKey returns a freshly-generated key pair plus the hex-encoded public key.
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
// *Module, with the config injected directly via the configOverride seam.
func newTestHandler(t *testing.T, cfg Config) (http.HandlerFunc, *Module, *fakeQueuer) {
	t.Helper()
	db := members.NewTestDB(t)
	fq := &fakeQueuer{}
	m := New(db, engine.NewEventLogger(db, "discordbot"), fq)
	m.configOverride = func(context.Context) (*Config, error) { return &cfg, nil }
	return m.handleInteraction, m, fq
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

// insertRequestedMember inserts a member that has already requested the given
// discount (discount_status='requested').
func insertRequestedMember(t *testing.T, m *Module, email, discountType string) int64 {
	t.Helper()
	id := insertMember(t, m, email)
	_, err := m.db.Exec(
		"UPDATE members SET discount_type=?, discount_status='requested' WHERE id=?",
		discountType, id)
	require.NoError(t, err)
	return id
}

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) interactionResponse {
	t.Helper()
	var out interactionResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	return out
}

func discountStatus(t *testing.T, m *Module, id int64) *string {
	t.Helper()
	var s *string
	require.NoError(t, m.db.QueryRow("SELECT discount_status FROM members WHERE id=?", id).Scan(&s))
	return s
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

	r := signedPost(t, priv, []byte(`{"type":1}`))
	r.Body = io.NopCloser(bytes.NewReader([]byte(`{"type":2}`)))

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

// TestHandleInteraction_ApproveSetsApproved covers the happy path: clicking
// Approve on a pending request flips discount_status to 'approved', removes the
// button (UPDATE_MESSAGE with empty components), and logs an audit event.
func TestHandleInteraction_ApproveSetsApproved(t *testing.T) {
	t.Parallel()
	priv, pubHex := testKey(t)
	handler, m, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: pubHex})

	memberID := insertRequestedMember(t, m, "applicant@example.com", "student")
	body := approveBody(t, memberID, "discord-uid-1", "alice")

	r := signedPost(t, priv, body)
	w := httptest.NewRecorder()
	handler(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	resp := decodeResp(t, w)
	require.Equal(t, responseTypeUpdateMessage, resp.Type)
	require.NotNil(t, resp.Data)
	require.NotNil(t, resp.Data.Components, "must be a non-nil slice so JSON is [] not null")
	require.Len(t, resp.Data.Components, 0, "empty components array removes the button")
	require.Len(t, resp.Data.Embeds, 1)
	require.Equal(t, "Discount approved", resp.Data.Embeds[0].Title)
	require.Contains(t, resp.Data.Embeds[0].Description, "Student")
	require.Contains(t, resp.Data.Embeds[0].Description, "<@discord-uid-1>")

	status := discountStatus(t, m, memberID)
	require.NotNil(t, status)
	require.Equal(t, "approved", *status)

	var n int
	require.NoError(t, m.db.QueryRow(
		`SELECT COUNT(*) FROM module_events WHERE module='discordbot' AND event_type='DiscountApprovedViaDiscord'`).Scan(&n))
	require.Equal(t, 1, n)
}

// TestHandleInteraction_ApproveFamilyMentionsLinkage: family approvals remind
// leadership to link the root account in the admin panel.
func TestHandleInteraction_ApproveFamilyMentionsLinkage(t *testing.T) {
	t.Parallel()
	priv, pubHex := testKey(t)
	handler, m, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: pubHex})

	memberID := insertRequestedMember(t, m, "fam@example.com", "family")
	r := signedPost(t, priv, approveBody(t, memberID, "u1", "alice"))
	w := httptest.NewRecorder()
	handler(w, r)

	resp := decodeResp(t, w)
	require.Contains(t, resp.Data.Embeds[0].Description, "root family account")
}

// TestHandleInteraction_ApproveWhenNotPending: clicking Approve on a request
// that was already withdrawn (or approved) leaves the row untouched and shows
// the "request closed" message.
func TestHandleInteraction_ApproveWhenNotPending(t *testing.T) {
	t.Parallel()
	priv, pubHex := testKey(t)
	handler, m, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: pubHex})

	// Member exists but has no pending request (member removed it).
	memberID := insertMember(t, m, "withdrawn@example.com")
	r := signedPost(t, priv, approveBody(t, memberID, "u1", "alice"))
	w := httptest.NewRecorder()
	handler(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	resp := decodeResp(t, w)
	require.Equal(t, responseTypeUpdateMessage, resp.Type)
	require.Equal(t, "Discount request closed", resp.Data.Embeds[0].Title)
	require.Len(t, resp.Data.Components, 0)

	// Status remains NULL; no event logged.
	require.Nil(t, discountStatus(t, m, memberID))
	var n int
	require.NoError(t, m.db.QueryRow(
		`SELECT COUNT(*) FROM module_events WHERE event_type='DiscountApprovedViaDiscord'`).Scan(&n))
	require.Equal(t, 0, n)
}

// TestHandleInteraction_ApproveMissingMember replies ephemerally.
func TestHandleInteraction_ApproveMissingMember(t *testing.T) {
	t.Parallel()
	priv, pubHex := testKey(t)
	handler, _, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: pubHex})

	r := signedPost(t, priv, approveBody(t, 999999, "u1", "alice"))
	w := httptest.NewRecorder()
	handler(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	resp := decodeResp(t, w)
	require.Equal(t, responseTypeChannelMsg, resp.Type)
	require.Equal(t, flagEphemeral, resp.Data.Flags)
}

// TestHandleInteraction_UnknownComponent replies ephemerally for a custom_id
// that isn't an approve button.
func TestHandleInteraction_UnknownComponent(t *testing.T) {
	t.Parallel()
	priv, pubHex := testKey(t)
	handler, _, _ := newTestHandler(t, Config{Enabled: true, ApplicationPublicKey: pubHex})

	body, err := json.Marshal(map[string]any{
		"type": interactionTypeComponent,
		"data": map[string]any{"custom_id": "conway:something_else:1"},
		"member": map[string]any{
			"user": map[string]any{"id": "u1", "username": "alice"},
		},
	})
	require.NoError(t, err)

	r := signedPost(t, priv, body)
	w := httptest.NewRecorder()
	handler(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	resp := decodeResp(t, w)
	require.Equal(t, responseTypeChannelMsg, resp.Type)
	require.Equal(t, flagEphemeral, resp.Data.Flags)
}

// approveBody returns the JSON body Discord POSTs for an Approve button click.
func approveBody(t *testing.T, memberID int64, userID, username string) []byte {
	t.Helper()
	b := map[string]any{
		"type": interactionTypeComponent,
		"data": map[string]any{
			"custom_id":      fmt.Sprintf("%s%d", approveCustomIDPrefix, memberID),
			"component_type": componentTypeButton,
		},
		"member": map[string]any{
			"user": map[string]any{"id": userID, "username": username},
		},
	}
	out, err := json.Marshal(b)
	require.NoError(t, err)
	return out
}
