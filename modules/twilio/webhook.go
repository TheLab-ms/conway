package twilio

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/TheLab-ms/conway/engine"
)

// kindSMS and kindVoicemail tag rows in twilio_messages.
const (
	kindSMS       = "sms"
	kindVoicemail = "voicemail"
)

// handleVoiceIncoming responds to Twilio's "incoming call" webhook with TwiML
// that greets the caller and records a voicemail. We point the recording
// callback at our own /twilio/voice/recording endpoint so the recording is
// queued for download and stored in the inbox.
func (m *Module) handleVoiceIncoming(w http.ResponseWriter, r *http.Request) {
	cfg, err := m.verifyTwilioSignature(r)
	if err != nil {
		http.Error(w, "unauthorized: "+err.Error(), http.StatusForbidden)
		return
	}

	greeting := strings.TrimSpace(cfg.VoiceGreeting)
	if greeting == "" {
		greeting = "Hello, you've reached the makerspace. Please leave a message after the tone, then hang up."
	}

	transcribe := "false"
	if cfg.TranscriptionEnabled {
		transcribe = "true"
	}

	base := selfBaseURL(r, m.self)
	twiml := fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>
<Response>
	<Say voice="alice">%s</Say>
	<Record action="%s/twilio/voice/recording" method="POST" maxLength="180" finishOnKey="#" playBeep="true" transcribe="%s" transcribeCallback="%s/twilio/voice/transcription"/>
	<Say voice="alice">We did not receive a recording. Goodbye.</Say>
	<Hangup/>
</Response>`,
		xmlEscape(greeting),
		base,
		transcribe,
		base,
	)

	w.Header().Set("Content-Type", "application/xml")
	w.Write([]byte(twiml))
}

// handleVoiceRecording is hit after the caller finishes recording a voicemail.
// Twilio posts CallSid, From, To, RecordingSid, RecordingUrl, RecordingDuration.
// We insert the row immediately (so it shows up in the inbox even before the
// audio downloads) and let the workqueue fetch the recording bytes.
func (m *Module) handleVoiceRecording(w http.ResponseWriter, r *http.Request) {
	if _, err := m.verifyTwilioSignature(r); err != nil {
		http.Error(w, "unauthorized: "+err.Error(), http.StatusForbidden)
		return
	}

	callSid := r.PostFormValue("CallSid")
	recordingSid := r.PostFormValue("RecordingSid")
	recordingURL := r.PostFormValue("RecordingUrl")
	from := r.PostFormValue("From")
	to := r.PostFormValue("To")
	duration, _ := strconv.Atoi(r.PostFormValue("RecordingDuration"))

	if recordingSid == "" || recordingURL == "" {
		http.Error(w, "missing recording fields", http.StatusBadRequest)
		return
	}

	// Use the call SID as the unique key when present so we can dedupe
	// retries from Twilio without losing the recording metadata.
	twilioSid := callSid
	if twilioSid == "" {
		twilioSid = recordingSid
	}

	_, err := m.db.ExecContext(r.Context(), `
		INSERT INTO twilio_messages (kind, twilio_sid, from_number, to_number, recording_sid, recording_url, duration_seconds)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(twilio_sid) DO UPDATE SET
			recording_sid = excluded.recording_sid,
			recording_url = excluded.recording_url,
			duration_seconds = excluded.duration_seconds`,
		kindVoicemail, twilioSid, from, to, recordingSid, recordingURL, duration)
	if err != nil {
		engine.SystemError(w, "insert voicemail: "+err.Error())
		return
	}

	m.eventLogger.LogEvent(r.Context(), 0, "VoicemailReceived", recordingSid, from, true,
		fmt.Sprintf("from=%s duration=%ds", from, duration))

	// Empty 200 — Twilio doesn't need TwiML for a recording callback.
	w.WriteHeader(http.StatusOK)
}

// handleVoiceTranscription receives the async transcription callback from
// Twilio after the recording is processed. Posted fields include
// TranscriptionStatus, TranscriptionText, RecordingSid, CallSid.
func (m *Module) handleVoiceTranscription(w http.ResponseWriter, r *http.Request) {
	if _, err := m.verifyTwilioSignature(r); err != nil {
		http.Error(w, "unauthorized: "+err.Error(), http.StatusForbidden)
		return
	}

	status := r.PostFormValue("TranscriptionStatus")
	text := r.PostFormValue("TranscriptionText")
	recordingSid := r.PostFormValue("RecordingSid")
	callSid := r.PostFormValue("CallSid")

	if status != "completed" || text == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Match either the original CallSid (preferred) or the RecordingSid.
	res, err := m.db.ExecContext(r.Context(),
		"UPDATE twilio_messages SET body = $1 WHERE recording_sid = $2 OR twilio_sid = $3",
		text, recordingSid, callSid)
	if err != nil {
		engine.SystemError(w, "update transcription: "+err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Recording row may not exist yet if callbacks arrive out of order.
		// Insert a placeholder keyed on the recording SID so we don't lose
		// the transcription text.
		m.db.ExecContext(r.Context(), `
			INSERT INTO twilio_messages (kind, twilio_sid, recording_sid, body)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT(twilio_sid) DO UPDATE SET body = excluded.body`,
			kindVoicemail, recordingSid, recordingSid, text)
	}

	w.WriteHeader(http.StatusOK)
}

// handleSMS receives inbound SMS messages. Twilio posts MessageSid, From, To,
// Body. We respond with empty TwiML so Twilio doesn't auto-reply.
func (m *Module) handleSMS(w http.ResponseWriter, r *http.Request) {
	if _, err := m.verifyTwilioSignature(r); err != nil {
		http.Error(w, "unauthorized: "+err.Error(), http.StatusForbidden)
		return
	}

	messageSid := r.PostFormValue("MessageSid")
	from := r.PostFormValue("From")
	to := r.PostFormValue("To")
	body := r.PostFormValue("Body")

	if messageSid == "" {
		http.Error(w, "missing MessageSid", http.StatusBadRequest)
		return
	}

	_, err := m.db.ExecContext(r.Context(), `
		INSERT INTO twilio_messages (kind, twilio_sid, from_number, to_number, body)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT(twilio_sid) DO NOTHING`,
		kindSMS, messageSid, from, to, body)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		engine.SystemError(w, "insert sms: "+err.Error())
		return
	}

	m.eventLogger.LogEvent(r.Context(), 0, "SMSReceived", messageSid, from, true,
		fmt.Sprintf("from=%s len=%d", from, len(body)))

	w.Header().Set("Content-Type", "application/xml")
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Response/>`))
}

// selfBaseURL prefers the configured self URL (which is the canonical public
// URL the operator told Conway about) but falls back to reconstructing one
// from the request, including proxy headers.
func selfBaseURL(r *http.Request, self *url.URL) string {
	if self != nil {
		s := self.String()
		if s != "" {
			return strings.TrimRight(s, "/")
		}
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = v
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = v
	}
	return scheme + "://" + host
}

// xmlEscape escapes a string for safe inclusion as XML character data.
// Twilio's TwiML <Say> body is text content, so the standard five replacements
// are sufficient.
func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}
