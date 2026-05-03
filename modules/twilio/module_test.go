package twilio

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComputeTwilioSignature pins the HMAC implementation against a hand-
// computed reference vector so any regression is caught immediately.
func TestComputeTwilioSignature(t *testing.T) {
	token := "12345"
	full := "https://example.com/twilio/sms"
	form := url.Values{
		"From": {"+15551234567"},
		"To":   {"+15557654321"},
		"Body": {"Hello!"},
	}

	got := computeTwilioSignature(token, full, form)

	mac := hmac.New(sha1.New, []byte(token))
	mac.Write([]byte("https://example.com/twilio/smsBodyHello!From+15551234567To+15557654321"))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	assert.Equal(t, want, got)
}

// TestSMSWebhook verifies that a properly-signed SMS webhook inserts a row
// into the inbox and that an unsigned request is rejected.
func TestSMSWebhook(t *testing.T) {
	db := engine.OpenTestDB(t)
	mod := newTestModule(t, db, "topsecret", 30)

	form := url.Values{
		"MessageSid": {"SM_abc"},
		"From":       {"+15551112222"},
		"To":         {"+15553334444"},
		"Body":       {"hello world"},
	}
	postSigned(t, mod.handleSMS, "https://example.com/twilio/sms", "topsecret", form)

	var got string
	require.NoError(t, db.QueryRow(
		"SELECT body FROM twilio_messages WHERE twilio_sid = 'SM_abc'").Scan(&got))
	assert.Equal(t, "hello world", got)

	// Unsigned request is rejected.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://example.com/twilio/sms",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mod.handleSMS(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestVoicemailWebhookInsert verifies that a recording callback creates a
// voicemail row that the workqueue can later pick up.
func TestVoicemailWebhookInsert(t *testing.T) {
	db := engine.OpenTestDB(t)
	mod := newTestModule(t, db, "topsecret", 30)

	form := url.Values{
		"CallSid":           {"CA_call"},
		"RecordingSid":      {"RE_rec"},
		"RecordingUrl":      {"https://api.twilio.com/2010/Recordings/RE_rec"},
		"From":              {"+15550000000"},
		"To":                {"+15551111111"},
		"RecordingDuration": {"7"},
	}
	postSigned(t, mod.handleVoiceRecording,
		"https://example.com/twilio/voice/recording", "topsecret", form)

	job, err := mod.GetItem(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://api.twilio.com/2010/Recordings/RE_rec", job.RecordingURL)
}

// TestRetentionCleanup verifies messages older than the configured window
// are deleted by cleanupExpired.
func TestRetentionCleanup(t *testing.T) {
	db := engine.OpenTestDB(t)
	mod := newTestModule(t, db, "", 1)

	twoDaysAgo := time.Now().Unix() - 2*86400
	hourAgo := time.Now().Unix() - 3600
	_, err := db.Exec(`INSERT INTO twilio_messages (kind, twilio_sid, body, created)
		VALUES ('sms', 'old', 'old', ?), ('sms', 'new', 'new', ?)`,
		twoDaysAgo, hourAgo)
	require.NoError(t, err)

	mod.cleanupExpired(context.Background())

	var sid string
	require.NoError(t, db.QueryRow("SELECT twilio_sid FROM twilio_messages").Scan(&sid))
	assert.Equal(t, "new", sid)
}

// TestDownloadBackoff exercises UpdateItem's backoff math without making any
// network calls: a failed item should have its attempt count incremented and
// download_next_at advanced.
func TestDownloadBackoff(t *testing.T) {
	db := engine.OpenTestDB(t)
	mod := newTestModule(t, db, "topsecret", 30)

	_, err := db.Exec(`INSERT INTO twilio_messages
		(kind, twilio_sid, recording_url) VALUES ('voicemail', 'CA_x', 'https://example.com/r')`)
	require.NoError(t, err)

	job, err := mod.GetItem(context.Background())
	require.NoError(t, err)

	require.NoError(t, mod.UpdateItem(context.Background(), job, false))

	var attempts int
	var next int64
	require.NoError(t, db.QueryRow(
		"SELECT download_attempts, download_next_at FROM twilio_messages WHERE id = ?",
		job.ID).Scan(&attempts, &next))
	assert.Equal(t, 1, attempts)
	assert.Greater(t, next, time.Now().Unix())
}

// ---------------- helpers ----------------

func newTestModule(t *testing.T, db *sql.DB, authToken string, retentionDays int) *Module {
	t.Helper()
	self, _ := url.Parse("https://example.com")
	mod := New(db, self, engine.NewEventLogger(db, "twilio"))

	_, err := db.Exec(`INSERT INTO twilio_config
		(account_sid, auth_token, voice_greeting, retention_days, transcription_enabled)
		VALUES ('AC_test', ?, '', ?, 0)`, authToken, retentionDays)
	require.NoError(t, err)

	reg := config.NewRegistry(db)
	reg.MustRegister(mod.ConfigSpec())
	mod.SetConfigLoader(config.NewStore(db, reg))
	return mod
}

func postSigned(t *testing.T, fn http.HandlerFunc, fullURL, token string, form url.Values) {
	t.Helper()
	sig := computeTwilioSignature(token, fullURL, form)
	req := httptest.NewRequest(http.MethodPost, fullURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", sig)
	rec := httptest.NewRecorder()
	fn(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("handler returned %d: %s", rec.Code, rec.Body.String())
	}
}
