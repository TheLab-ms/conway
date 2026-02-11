package voicemail

import (
	"crypto/hmac"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/TheLab-ms/conway/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := engine.OpenTestDB(t)

	// Create discord_webhook_queue table (workqueue inserts into it)
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS discord_webhook_queue (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
		send_at INTEGER DEFAULT (strftime('%s', 'now')),
		webhook_url TEXT NOT NULL,
		payload TEXT NOT NULL
	) STRICT`)
	require.NoError(t, err)

	// Create module_events table (EventLogger needs it)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS module_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
		module TEXT NOT NULL,
		member INTEGER,
		event_type TEXT NOT NULL,
		entity_id TEXT,
		entity_name TEXT,
		success INTEGER NOT NULL DEFAULT 1,
		details TEXT NOT NULL DEFAULT ''
	) STRICT`)
	require.NoError(t, err)

	engine.MustMigrate(db, migration)
	return db
}

func newTestModule(t *testing.T) (*Module, *sql.DB) {
	t.Helper()
	db := createTestDB(t)
	self, _ := url.Parse("https://conway.example.com")
	m := &Module{
		db:          db,
		self:        self,
		eventLogger: engine.NewEventLogger(db, "twilio"),
		httpClient:  http.DefaultClient,
	}
	return m, db
}

// computeSignature produces a valid Twilio-style HMAC-SHA1 signature for testing.
func computeSignature(authToken, fullURL string, params url.Values) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf strings.Builder
	buf.WriteString(fullURL)
	for _, k := range keys {
		buf.WriteString(k)
		buf.WriteString(params.Get(k))
	}

	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(buf.String()))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// --- TwiML tests ---

func TestVoiceTwiML(t *testing.T) {
	result := voiceTwiML("Hello, leave a message.", "https://example.com/callback")
	assert.Contains(t, result, `<Say voice="alice">Hello, leave a message.</Say>`)
	assert.Contains(t, result, `<Record maxLength="60"`)
	assert.Contains(t, result, `recordingStatusCallback="https://example.com/callback"`)
	assert.Contains(t, result, `<?xml version="1.0"`)
}

func TestVoiceTwiMLEscapesXML(t *testing.T) {
	result := voiceTwiML("Hello <world> & friends", "https://example.com/cb")
	assert.Contains(t, result, "Hello &lt;world&gt; &amp; friends")
}

func TestHangupTwiML(t *testing.T) {
	result := hangupTwiML()
	assert.Contains(t, result, "<Hangup/>")
	assert.Contains(t, result, "not available")
}

// --- Signature validation tests ---

func TestValidateTwilioSignature(t *testing.T) {
	authToken := "12345"
	fullURL := "https://mycompany.com/myapp.php?foo=1&bar=2"
	params := url.Values{
		"CallSid": {"CA1234567890ABCDE"},
		"Caller":  {"+14158675310"},
		"Digits":  {"1234"},
		"From":    {"+14158675310"},
		"To":      {"+18005551212"},
	}

	sig := computeSignature(authToken, fullURL, params)
	assert.True(t, validateTwilioSignature(authToken, fullURL, sig, params))
}

func TestValidateTwilioSignatureRejectsInvalid(t *testing.T) {
	assert.False(t, validateTwilioSignature("token", "https://example.com", "invalidsig", url.Values{}))
}

func TestValidateTwilioSignatureRejectsEmpty(t *testing.T) {
	assert.False(t, validateTwilioSignature("", "https://example.com", "sig", url.Values{}))
	assert.False(t, validateTwilioSignature("token", "https://example.com", "", url.Values{}))
}

// --- Webhook handler tests ---

func TestVoiceWebhookNoConfig(t *testing.T) {
	m, _ := newTestModule(t)

	req := httptest.NewRequest("POST", "/webhooks/twilio/voice", nil)
	w := httptest.NewRecorder()

	m.handleVoiceWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "<Hangup/>")
}

func TestVoiceWebhookInvalidSignature(t *testing.T) {
	m, db := newTestModule(t)

	_, err := db.Exec(`INSERT INTO twilio_config (account_sid, auth_token, greeting_text)
		VALUES ('AC123', 'testtoken', 'Hello')`)
	require.NoError(t, err)

	body := strings.NewReader("CallSid=CA123&From=%2B15551234567&To=%2B15559876543")
	req := httptest.NewRequest("POST", "/webhooks/twilio/voice", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", "invalidsignature")
	w := httptest.NewRecorder()

	m.handleVoiceWebhook(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestVoiceWebhookValidSignature(t *testing.T) {
	m, db := newTestModule(t)

	authToken := "testtoken123"
	_, err := db.Exec(`INSERT INTO twilio_config (account_sid, auth_token, greeting_text)
		VALUES ('AC123', $1, 'Welcome to the makerspace.')`, authToken)
	require.NoError(t, err)

	params := url.Values{
		"CallSid": {"CA123"},
		"From":    {"+15551234567"},
		"To":      {"+15559876543"},
	}

	fullURL := "https://conway.example.com/webhooks/twilio/voice"
	sig := computeSignature(authToken, fullURL, params)

	body := strings.NewReader(params.Encode())
	req := httptest.NewRequest("POST", "/webhooks/twilio/voice", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", sig)
	w := httptest.NewRecorder()

	m.handleVoiceWebhook(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Welcome to the makerspace.")
	assert.Contains(t, w.Body.String(), `<Record maxLength="60"`)
	assert.Contains(t, w.Body.String(), "recording-status")
}

func TestRecordingStatusWebhookQueuesDownload(t *testing.T) {
	m, db := newTestModule(t)

	authToken := "testtoken123"
	_, err := db.Exec(`INSERT INTO twilio_config (account_sid, auth_token) VALUES ('AC123', $1)`, authToken)
	require.NoError(t, err)

	params := url.Values{
		"CallSid":           {"CA123"},
		"RecordingSid":      {"RE456"},
		"RecordingUrl":      {"/2010-04-01/Accounts/AC123/Recordings/RE456"},
		"RecordingDuration": {"30"},
		"RecordingStatus":   {"completed"},
		"From":              {"+15551234567"},
		"To":                {"+15559876543"},
	}

	fullURL := "https://conway.example.com/webhooks/twilio/recording-status"
	sig := computeSignature(authToken, fullURL, params)

	body := strings.NewReader(params.Encode())
	req := httptest.NewRequest("POST", "/webhooks/twilio/recording-status", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", sig)
	w := httptest.NewRecorder()

	m.handleRecordingStatusWebhook(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM voicemail_recordings WHERE status = 'pending'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var callSid, recordingSid, fromNumber string
	var duration int
	err = db.QueryRow("SELECT call_sid, recording_sid, from_number, recording_duration FROM voicemail_recordings").
		Scan(&callSid, &recordingSid, &fromNumber, &duration)
	require.NoError(t, err)
	assert.Equal(t, "CA123", callSid)
	assert.Equal(t, "RE456", recordingSid)
	assert.Equal(t, "+15551234567", fromNumber)
	assert.Equal(t, 30, duration)
}

func TestRecordingStatusWebhookIgnoresNonCompleted(t *testing.T) {
	m, db := newTestModule(t)

	authToken := "testtoken123"
	_, err := db.Exec(`INSERT INTO twilio_config (account_sid, auth_token) VALUES ('AC123', $1)`, authToken)
	require.NoError(t, err)

	params := url.Values{
		"RecordingStatus": {"in-progress"},
		"CallSid":         {"CA123"},
	}

	fullURL := "https://conway.example.com/webhooks/twilio/recording-status"
	sig := computeSignature(authToken, fullURL, params)

	body := strings.NewReader(params.Encode())
	req := httptest.NewRequest("POST", "/webhooks/twilio/recording-status", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", sig)
	w := httptest.NewRecorder()

	m.handleRecordingStatusWebhook(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM voicemail_recordings").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// --- Workqueue tests ---

func TestGetItemNoPending(t *testing.T) {
	m, _ := newTestModule(t)
	_, err := m.GetItem(t.Context())
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestGetItemReturnsPending(t *testing.T) {
	m, db := newTestModule(t)

	_, err := db.Exec(`INSERT INTO voicemail_recordings
		(call_sid, recording_sid, from_number, to_number, recording_url, recording_duration)
		VALUES ('CA123', 'RE456', '+15551234567', '+15559876543', '/recordings/RE456', 30)`)
	require.NoError(t, err)

	item, err := m.GetItem(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "RE456", item.RecordingSID)
	assert.Equal(t, "+15551234567", item.FromNumber)
	assert.Equal(t, 30, item.Duration)
}

// --- Serve recording tests ---

func TestServeRecordingNotFound(t *testing.T) {
	m, _ := newTestModule(t)

	req := httptest.NewRequest("GET", "/voicemail/recordings/999", nil)
	req.SetPathValue("id", "999")
	w := httptest.NewRecorder()

	m.serveRecording(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestServeRecordingSuccess(t *testing.T) {
	m, db := newTestModule(t)

	audioData := []byte("fake-mp3-data")
	_, err := db.Exec(`INSERT INTO voicemail_recordings
		(call_sid, recording_sid, from_number, to_number, recording_url, recording_duration, recording_data, status)
		VALUES ('CA123', 'RE456', '+15551234567', '+15559876543', '/recordings/RE456', 30, $1, 'downloaded')`,
		audioData)
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/voicemail/recordings/1", nil)
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	m.serveRecording(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "audio/mpeg", w.Header().Get("Content-Type"))
	assert.Equal(t, "fake-mp3-data", w.Body.String())
}

func TestServeRecordingPendingNotFound(t *testing.T) {
	m, db := newTestModule(t)

	_, err := db.Exec(`INSERT INTO voicemail_recordings
		(call_sid, recording_sid, from_number, to_number, recording_url, recording_duration)
		VALUES ('CA123', 'RE456', '+15551234567', '+15559876543', '/recordings/RE456', 30)`)
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/voicemail/recordings/1", nil)
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	m.serveRecording(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}
