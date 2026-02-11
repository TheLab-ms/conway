package voicemail

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/TheLab-ms/conway/engine"
)

const migration = `
CREATE TABLE IF NOT EXISTS twilio_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    account_sid TEXT NOT NULL DEFAULT '',
    auth_token TEXT NOT NULL DEFAULT '',
    greeting_text TEXT NOT NULL DEFAULT '',
    leadership_webhook_url TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE IF NOT EXISTS voicemail_recordings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    call_sid TEXT NOT NULL,
    recording_sid TEXT NOT NULL DEFAULT '',
    from_number TEXT NOT NULL DEFAULT '',
    to_number TEXT NOT NULL DEFAULT '',
    recording_url TEXT NOT NULL DEFAULT '',
    recording_data BLOB,
    recording_duration INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending'
) STRICT;

CREATE INDEX IF NOT EXISTS voicemail_recordings_status_idx ON voicemail_recordings (status);
CREATE INDEX IF NOT EXISTS voicemail_recordings_created_idx ON voicemail_recordings (created);
`

type Module struct {
	db          *sql.DB
	self        *url.URL
	eventLogger *engine.EventLogger
	httpClient  *http.Client
}

func New(db *sql.DB, self *url.URL, eventLogger *engine.EventLogger) *Module {
	engine.MustMigrate(db, migration)
	return &Module{
		db:          db,
		self:        self,
		eventLogger: eventLogger,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("POST /webhooks/twilio/voice", m.handleVoiceWebhook)
	router.HandleFunc("POST /webhooks/twilio/recording-status", m.handleRecordingStatusWebhook)
	router.HandleFunc("GET /voicemail/recordings/{id}", router.WithLeadership(m.serveRecording))
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(5*time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, 1))))
	mgr.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "old voicemail recordings",
		"DELETE FROM voicemail_recordings WHERE created < unixepoch() - (30 * 86400)")))
}

// handleVoiceWebhook handles incoming Twilio voice calls.
// It responds with TwiML to play a greeting and record a voicemail.
func (m *Module) handleVoiceWebhook(w http.ResponseWriter, r *http.Request) {
	cfg, err := m.loadConfig(r.Context())
	if err != nil {
		m.eventLogger.LogEvent(r.Context(), 0, "WebhookError", "", "", false, "config load: "+err.Error())
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(hangupTwiML()))
		return
	}

	if cfg.authToken == "" {
		slog.Warn("twilio voice webhook called but no auth token configured")
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(hangupTwiML()))
		return
	}

	if err := r.ParseForm(); err != nil {
		m.eventLogger.LogEvent(r.Context(), 0, "WebhookError", "", "", false, "form parse: "+err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	fullURL := m.self.String() + r.URL.Path
	sig := r.Header.Get("X-Twilio-Signature")
	if !validateTwilioSignature(cfg.authToken, fullURL, sig, r.PostForm) {
		m.eventLogger.LogEvent(r.Context(), 0, "WebhookError", "", "", false, "signature validation failed")
		w.WriteHeader(http.StatusForbidden)
		return
	}

	callSid := r.PostFormValue("CallSid")
	from := r.PostFormValue("From")
	to := r.PostFormValue("To")

	greeting := cfg.greetingText
	if greeting == "" {
		greeting = "Hello, you have reached the makerspace. Please leave a message after the beep."
	}

	callbackURL := m.self.String() + "/webhooks/twilio/recording-status"
	twiml := voiceTwiML(greeting, callbackURL)

	m.eventLogger.LogEvent(r.Context(), 0, "InboundCall", callSid, from, true,
		fmt.Sprintf("from=%s to=%s", from, to))

	w.Header().Set("Content-Type", "application/xml")
	w.Write([]byte(twiml))
}

// handleRecordingStatusWebhook handles the callback when a recording is ready.
func (m *Module) handleRecordingStatusWebhook(w http.ResponseWriter, r *http.Request) {
	cfg, err := m.loadConfig(r.Context())
	if err != nil {
		m.eventLogger.LogEvent(r.Context(), 0, "WebhookError", "", "", false, "config load: "+err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if cfg.authToken == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := r.ParseForm(); err != nil {
		m.eventLogger.LogEvent(r.Context(), 0, "WebhookError", "", "", false, "form parse: "+err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	fullURL := m.self.String() + r.URL.Path
	sig := r.Header.Get("X-Twilio-Signature")
	if !validateTwilioSignature(cfg.authToken, fullURL, sig, r.PostForm) {
		m.eventLogger.LogEvent(r.Context(), 0, "WebhookError", "", "", false, "recording callback signature validation failed")
		w.WriteHeader(http.StatusForbidden)
		return
	}

	status := r.PostFormValue("RecordingStatus")
	if status != "completed" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	callSid := r.PostFormValue("CallSid")
	recordingSid := r.PostFormValue("RecordingSid")
	recordingURL := r.PostFormValue("RecordingUrl")
	from := r.PostFormValue("From")
	to := r.PostFormValue("To")

	duration, _ := strconv.Atoi(r.PostFormValue("RecordingDuration"))

	_, err = m.db.ExecContext(r.Context(),
		`INSERT INTO voicemail_recordings (call_sid, recording_sid, from_number, to_number, recording_url, recording_duration)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		callSid, recordingSid, from, to, recordingURL, duration)
	if err != nil {
		m.eventLogger.LogEvent(r.Context(), 0, "WebhookError", recordingSid, from, false, "db insert: "+err.Error())
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	m.eventLogger.LogEvent(r.Context(), 0, "RecordingQueued", recordingSid, from, true,
		fmt.Sprintf("callSid=%s duration=%ds", callSid, duration))

	w.WriteHeader(http.StatusNoContent)
}

// serveRecording serves a voicemail recording to leadership users.
func (m *Module) serveRecording(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var data []byte
	var duration int
	err = m.db.QueryRowContext(r.Context(),
		`SELECT recording_data, recording_duration FROM voicemail_recordings
		 WHERE id = $1 AND status = 'downloaded' AND recording_data IS NOT NULL`,
		id).Scan(&data, &duration)

	if err == sql.ErrNoRows || len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		engine.HandleError(w, err)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}
