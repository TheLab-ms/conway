// Package twilio integrates Conway with Twilio for inbound SMS and voicemail.
//
// It exposes signature-verified webhook endpoints that Twilio calls when a
// call or text comes in, persists each message to a unified inbox table,
// asynchronously downloads voicemail recordings, and serves an admin inbox
// UI for leadership users. All messages and their recordings are deleted
// after a configurable retention window (default 30 days).
package twilio

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/modules/admin"
)

const (
	defaultRetentionDays = 30
	maxRetentionDays     = 365 * 5 // sane upper bound
	maxRecordingBytes    = 25 * 1024 * 1024
	downloadRPS          = 2
)

// Migration creates the inbox tables. The config table follows the
// engine/config conventions (column per Config field + auto-increment
// version) so the generic admin form can read and write it.
const migration = `
CREATE TABLE IF NOT EXISTS twilio_config (
	version INTEGER PRIMARY KEY AUTOINCREMENT,
	created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
	account_sid TEXT NOT NULL DEFAULT '',
	auth_token TEXT NOT NULL DEFAULT '',
	voice_greeting TEXT NOT NULL DEFAULT '',
	retention_days INTEGER NOT NULL DEFAULT 30,
	transcription_enabled INTEGER NOT NULL DEFAULT 1
) STRICT;

CREATE TABLE IF NOT EXISTS twilio_messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
	kind TEXT NOT NULL,
	twilio_sid TEXT NOT NULL UNIQUE,
	from_number TEXT NOT NULL DEFAULT '',
	to_number TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL DEFAULT '',
	recording_sid TEXT NOT NULL DEFAULT '',
	recording_url TEXT NOT NULL DEFAULT '',
	recording_data BLOB,
	recording_content_type TEXT NOT NULL DEFAULT '',
	duration_seconds INTEGER NOT NULL DEFAULT 0,
	read_at INTEGER,
	download_attempts INTEGER NOT NULL DEFAULT 0,
	download_next_at INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX IF NOT EXISTS twilio_messages_created_idx ON twilio_messages (created DESC);
CREATE INDEX IF NOT EXISTS twilio_messages_unread_idx ON twilio_messages (read_at) WHERE read_at IS NULL;
`

// Module is the Twilio inbox module.
type Module struct {
	db           *sql.DB
	self         *url.URL
	httpClient   *http.Client
	configLoader *config.Loader[Config]
	eventLogger  *engine.EventLogger
	navProvider  func() []*admin.NavTab
}

// SetNavProvider supplies a callback that returns the current admin navbar
// tabs. The inbox pages render the same navbar as the rest of the admin UI.
func (m *Module) SetNavProvider(fn func() []*admin.NavTab) { m.navProvider = fn }

// New constructs the module and applies its migration.
func New(db *sql.DB, self *url.URL, eventLogger *engine.EventLogger) *Module {
	engine.MustMigrate(db, migration)
	return &Module{
		db:          db,
		self:        self,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		eventLogger: eventLogger,
	}
}

// SetConfigLoader wires up the typed config loader; called by the App after
// modules are registered with the config registry.
func (m *Module) SetConfigLoader(store *config.Store) {
	m.configLoader = config.NewLoader[Config](store, "twilio")
}

func (m *Module) loadConfig(ctx context.Context) (*Config, error) {
	if m.configLoader == nil {
		return &Config{RetentionDays: defaultRetentionDays}, nil
	}
	cfg, err := m.configLoader.Load(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.RetentionDays < 1 {
		cfg.RetentionDays = defaultRetentionDays
	}
	return cfg, nil
}

// AttachRoutes registers public webhook routes and admin inbox routes.
func (m *Module) AttachRoutes(router *engine.Router) {
	// Public webhooks (signature-verified, no auth gating).
	router.HandleFunc("POST /twilio/voice", m.handleVoiceIncoming)
	router.HandleFunc("POST /twilio/voice/recording", m.handleVoiceRecording)
	router.HandleFunc("POST /twilio/voice/transcription", m.handleVoiceTranscription)
	router.HandleFunc("POST /twilio/sms", m.handleSMS)

	// Admin inbox (leadership only).
	router.HandleFunc("GET /admin/inbox", router.WithLeadership(m.handleInboxList))
	router.HandleFunc("GET /admin/inbox/{id}", router.WithLeadership(m.handleInboxDetail))
	router.HandleFunc("POST /admin/inbox/{id}/read", router.WithLeadership(m.handleInboxToggleRead))
	router.HandleFunc("POST /admin/inbox/{id}/delete", router.WithLeadership(m.handleInboxDelete))
	router.HandleFunc("GET /admin/inbox/{id}/audio", router.WithLeadership(m.handleInboxAudio))
}

// AttachWorkers attaches the recording-downloader queue and the retention
// cleanup poll.
func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(5*time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, downloadRPS))))
	mgr.Add(engine.Poll(time.Hour, m.cleanupExpired))
}

// cleanupExpired deletes messages whose retention window has elapsed.
func (m *Module) cleanupExpired(ctx context.Context) bool {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		return false
	}
	res, err := m.db.ExecContext(ctx,
		"DELETE FROM twilio_messages WHERE unixepoch() - created > $1", cfg.RetentionDays*86400)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			m.eventLogger.LogEvent(ctx, 0, "InboxCleanup", "", "", true, fmt.Sprintf("deleted %d expired messages", n))
		}
	}
	return false
}

// verifyTwilioSignature validates the X-Twilio-Signature header against the
// stored auth token. Returns the configured Config (for use by the handler)
// or an error suitable for an HTTP response.
//
// Algorithm (per Twilio docs):
//
//	signature = base64(HMAC-SHA1(authToken,
//	    fullURL + sorted(form-key + form-value...)))
//
// The full URL must be exactly the URL Twilio called, including scheme,
// host, port, path, and query string. We reconstruct it from the request,
// trusting standard reverse-proxy headers when present (X-Forwarded-Proto,
// X-Forwarded-Host).
func (m *Module) verifyTwilioSignature(r *http.Request) (*Config, error) {
	cfg, err := m.loadConfig(r.Context())
	if err != nil {
		return nil, err
	}
	if cfg.AuthToken == "" {
		return nil, fmt.Errorf("twilio is not configured")
	}

	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("parse form: %w", err)
	}

	sig := r.Header.Get("X-Twilio-Signature")
	if sig == "" {
		return nil, fmt.Errorf("missing signature header")
	}

	expected := computeTwilioSignature(cfg.AuthToken, fullRequestURL(r), r.PostForm)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return nil, fmt.Errorf("signature mismatch")
	}
	return cfg, nil
}

// fullRequestURL reconstructs the URL Twilio used to reach us, honoring
// proxy headers commonly set by Cloudflare/nginx.
func fullRequestURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = v
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = v
	}
	u := &url.URL{Scheme: scheme, Host: host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
	return u.String()
}

// computeTwilioSignature implements Twilio's request-validation algorithm.
// Exposed (lowercase) for testing within the package.
func computeTwilioSignature(authToken, fullURL string, form url.Values) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(fullURL)
	for _, k := range keys {
		// If a parameter appears multiple times, Twilio concatenates each
		// value individually after the key. Form values are already
		// strings; in practice webhooks use single-valued params.
		for _, v := range form[k] {
			b.WriteString(k)
			b.WriteString(v)
		}
	}

	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
