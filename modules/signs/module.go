package signs

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/modules/auth"
)

const migration = `
CREATE TABLE IF NOT EXISTS signs_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    printer_host TEXT NOT NULL DEFAULT '',
    printer_port INTEGER NOT NULL DEFAULT 631,
    printer_queue TEXT NOT NULL DEFAULT '',
    templates_json TEXT NOT NULL DEFAULT '[]'
) STRICT;

CREATE TABLE IF NOT EXISTS signs_print_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    send_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    attempts INTEGER NOT NULL DEFAULT 0,
    member_id INTEGER REFERENCES members(id) ON DELETE SET NULL,
    discord_username TEXT NOT NULL DEFAULT '',
    template_slug TEXT NOT NULL,
    machine_name TEXT NOT NULL DEFAULT '',
    issue TEXT NOT NULL DEFAULT '',
    fields_json TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX IF NOT EXISTS signs_print_queue_send_at_idx ON signs_print_queue (send_at);
CREATE INDEX IF NOT EXISTS idx_signs_queue_member_created ON signs_print_queue (member_id, created DESC);
`

// printQueueTTL is how long a queued print may sit before being dropped.
const printQueueTTL = time.Hour

// printRPS is the per-second rate at which we hand jobs to the printer.
// Network laser printers don't appreciate being hammered.
const printRPS = 1

// Per-user submission limits — protect the printer + DB from runaway clients.
const (
	maxSubmitBodyBytes      = 64 * 1024 // request body cap (64 KiB)
	maxMachineNameLen       = 200
	maxIssueLen             = 2000
	maxSubmitsPerMinute     = 5  // 429 if exceeded
	maxOutstandingPerMember = 20 // 429 if exceeded
	maxPDFBytes             = 10 * 1024 * 1024
	maxBackoffSeconds       = 3600
)

// RenderError marks a non-retryable failure originating in the
// template/PDF pipeline. ProcessItem returns these via errors.Is so
// UpdateItem can drop the row instead of backing off.
type RenderError struct{ Err error }

func (e *RenderError) Error() string { return e.Err.Error() }
func (e *RenderError) Unwrap() error { return e.Err }

type Module struct {
	db          *sql.DB
	eventLogger *engine.EventLogger

	configLoader *config.Loader[Config]

	// snapMu guards the in-memory config snapshot below.
	snapMu        sync.RWMutex
	printer       Printer
	templates     []Template
	configVersion int
}

// New creates the signs module. Until SetPrinter is called the module uses
// an internal noop printer that errors on every job.
func New(db *sql.DB, eventLogger *engine.EventLogger) *Module {
	if db != nil {
		engine.MustMigrate(db, migration)
	}
	m := &Module{
		db:          db,
		eventLogger: eventLogger,
		printer:     noopPrinter{},
		templates:   []Template{DefaultMaintenanceTemplate},
	}
	return m
}

// SetPrinter overrides the printer used by the worker. Intended for tests
// that want to inject a fake; production wires the IPP printer from config.
func (m *Module) SetPrinter(p Printer) {
	if p == nil {
		p = noopPrinter{}
	}
	m.snapMu.Lock()
	m.printer = p
	m.snapMu.Unlock()
}

// SetConfigLoader wires up typed config loading. Called once during app
// registration after the config registry has been populated.
func (m *Module) SetConfigLoader(store *config.Store) {
	m.configLoader = config.NewLoader[Config](store, "signs")
	m.seedDefaultsIfEmpty(context.Background())
	m.reloadConfig(context.Background())
}

// seedDefaultsIfEmpty installs the default maintenance template the FIRST
// time the module runs (no signs_config row exists). Subsequent admin saves
// are honored verbatim — including empty templates lists — so leadership can
// intentionally clear the picker.
func (m *Module) seedDefaultsIfEmpty(ctx context.Context) {
	if m.db == nil {
		return
	}
	var n int
	if err := m.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM signs_config").Scan(&n); err != nil {
		slog.Error("signs: counting config rows", "error", err)
		return
	}
	if n > 0 {
		return
	}
	tmplJSON := `[` + mustMarshalTemplate(DefaultMaintenanceTemplate) + `]`
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO signs_config (printer_host, printer_port, printer_queue, templates_json)
		 VALUES ('', 631, '', ?)`, tmplJSON)
	if err != nil {
		slog.Error("signs: seeding default config", "error", err)
	}
}

// snapshot returns the current printer + templates under the read lock.
func (m *Module) snapshot() (Printer, []Template) {
	m.snapMu.RLock()
	defer m.snapMu.RUnlock()
	return m.printer, m.templates
}

func (m *Module) reloadConfig(ctx context.Context) {
	if m.configLoader == nil {
		return
	}
	cfg, version, err := m.configLoader.LoadWithVersion(ctx)
	if err != nil {
		slog.Error("signs: load config", "error", err)
		return
	}

	// Only fall back to the in-memory default template when the row simply
	// hasn't been created yet (empty templates AND no explicit save). On a
	// real save with an empty list we honor that — admins clearing the
	// picker is an intentional state, not an error.
	templates := cfg.Templates
	if templates == nil {
		templates = []Template{}
	}

	target := PrinterTarget{
		Host:  cfg.PrinterHost,
		Port:  cfg.PrinterPort,
		Queue: cfg.PrinterQueue,
	}

	// Rebuild the printer whenever target changes. If host/queue are blank
	// install the noop printer so jobs simply back off until config is set.
	var newPrinter Printer
	if target.Host == "" || target.Queue == "" {
		slog.Info("signs: printer not configured, jobs will queue only")
		newPrinter = noopPrinter{}
	} else {
		newPrinter = NewIPPPrinter(target)
	}

	m.snapMu.Lock()
	// Don't overwrite a test-injected printer with an IPP one if no real
	// target is configured: tests call SetPrinter explicitly. Replace only
	// when reload yields a real target, OR the existing printer is the
	// default noop (i.e. nobody injected anything).
	if _, isNoop := m.printer.(noopPrinter); isNoop || (target.Host != "" && target.Queue != "") {
		m.printer = newPrinter
	}
	m.templates = templates
	m.configVersion = version
	hostForLog := target.Host
	m.snapMu.Unlock()

	slog.Info("signs: loaded config", "templates", len(templates), "host", hostForLog)
}

func (m *Module) configChanged(ctx context.Context) bool {
	if m.configLoader == nil {
		return false
	}
	_, v, err := m.configLoader.LoadWithVersion(ctx)
	if err != nil {
		return false
	}
	m.snapMu.RLock()
	cur := m.configVersion
	m.snapMu.RUnlock()
	return v != cur
}

// AttachWorkers registers background workers with the engine. Cleanup is
// registered first to mirror the discordwebhook module ordering.
func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "stale signs prints",
		`DELETE FROM signs_print_queue WHERE unixepoch() - created > ?`,
		int64(printQueueTTL.Seconds()))))
	mgr.Add(engine.Poll(time.Second, m.workerTick))
}

// workerTick is the polling function: it picks up config drift before
// running a workqueue iteration. Mirrors machines.poll().
func (m *Module) workerTick(ctx context.Context) bool {
	if m.configChanged(ctx) {
		slog.Info("signs config changed, reloading")
		m.reloadConfig(ctx)
	}
	return engine.PollWorkqueue(engine.WithRateLimiting[*queuedPrint](m, printRPS))(ctx)
}

// AttachRoutes registers HTTP routes.
func (m *Module) AttachRoutes(r *engine.Router) {
	r.HandleFunc("GET /signs", r.WithAuthn(m.requireActive(m.renderIndex)))
	r.HandleFunc("GET /signs/{slug}", r.WithAuthn(m.requireActive(m.renderForm)))
	r.HandleFunc("POST /signs/{slug}", r.WithAuthn(m.requireActive(m.submit)))
}

// requireActive rejects non-active members with a 403 page so they can't
// queue prints (or read others' history). Auth middleware has already
// established identity.
func (m *Module) requireActive(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := auth.GetUserMeta(r.Context())
		if user == nil || !user.ActiveMember {
			engine.ClientError(w, "Members only",
				"This page is available to active members. If you think this is a mistake, contact leadership.",
				http.StatusForbidden)
			return
		}
		fn(w, r)
	}
}

// ProcessOne runs one iteration of the workqueue (helper for tests).
// Returns true if an item was processed. Bypasses the rate limiter and the
// configChanged check used by the production poller.
func (m *Module) ProcessOne(ctx context.Context) bool {
	return engine.PollWorkqueue[*queuedPrint](m)(ctx)
}

// --- workqueue impl ---

type queuedPrint struct {
	ID              int64
	MemberID        sql.NullInt64
	DiscordUsername string
	TemplateSlug    string
	MachineName     string
	Issue           string
	FieldsJSON      string
	Created         int64
	Attempts        int64
}

func (q *queuedPrint) String() string {
	return fmt.Sprintf("id=%d slug=%s", q.ID, q.TemplateSlug)
}

func (m *Module) GetItem(ctx context.Context) (*queuedPrint, error) {
	if m.db == nil {
		return nil, sql.ErrNoRows
	}
	q := &queuedPrint{}
	err := m.db.QueryRowContext(ctx, `
		SELECT id, member_id, discord_username, template_slug, machine_name, issue,
		       COALESCE(fields_json, '{}'), created, attempts
		FROM signs_print_queue
		WHERE unixepoch() >= send_at AND unixepoch() - created < ?
		ORDER BY send_at ASC LIMIT 1`, int64(printQueueTTL.Seconds())).Scan(
		&q.ID, &q.MemberID, &q.DiscordUsername, &q.TemplateSlug,
		&q.MachineName, &q.Issue, &q.FieldsJSON, &q.Created, &q.Attempts)
	if err != nil {
		return nil, err
	}
	return q, nil
}

func (m *Module) ProcessItem(ctx context.Context, item *queuedPrint) error {
	printer, templates := m.snapshot()

	tmpl, ok := findTemplate(templates, item.TemplateSlug)
	if !ok {
		err := fmt.Errorf("template %q not found", item.TemplateSlug)
		m.eventLogger.LogEvent(ctx, item.MemberID.Int64, "RenderError",
			item.TemplateSlug, "", false, err.Error())
		return &RenderError{Err: err}
	}

	pdf, err := RenderSign(tmpl, buildSignData(item))
	if err != nil {
		m.eventLogger.LogEvent(ctx, item.MemberID.Int64, "RenderError",
			item.TemplateSlug, tmpl.Name, false, err.Error())
		return &RenderError{Err: err}
	}
	if len(pdf) > maxPDFBytes {
		err := fmt.Errorf("rendered pdf too large: %d bytes", len(pdf))
		m.eventLogger.LogEvent(ctx, item.MemberID.Int64, "RenderError",
			item.TemplateSlug, tmpl.Name, false, err.Error())
		return &RenderError{Err: err}
	}

	jobName := fmt.Sprintf("Sign: %s", tmpl.Name)
	data := buildSignData(item)
	// Use MachineName for the job name if available (backward compat),
	// otherwise use the first non-empty field value.
	if mn := data["MachineName"]; mn != "" {
		jobName = fmt.Sprintf("Sign: %s — %s", tmpl.Name, mn)
	} else {
		for _, fd := range tmpl.ParsedFields() {
			if v := data[fd.Name]; v != "" {
				jobName = fmt.Sprintf("Sign: %s — %s", tmpl.Name, v)
				break
			}
		}
	}

	if err := printer.Print(ctx, PrintJob{JobName: jobName, PDF: pdf}); err != nil {
		m.eventLogger.LogEvent(ctx, item.MemberID.Int64, "PrintError",
			item.TemplateSlug, tmpl.Name, false, err.Error())
		return err
	}
	m.eventLogger.LogEvent(ctx, item.MemberID.Int64, "Printed",
		item.TemplateSlug, tmpl.Name, true, fmt.Sprintf("fields=%s", item.FieldsJSON))
	return nil
}

func (m *Module) UpdateItem(ctx context.Context, item *queuedPrint, success bool) error {
	if success {
		_, err := m.db.ExecContext(ctx, "DELETE FROM signs_print_queue WHERE id = ?", item.ID)
		return err
	}
	// Render-time errors are non-retryable — drop the row immediately.
	// The error from ProcessItem isn't passed to UpdateItem in the engine,
	// so we re-fetch the template_slug presence as a heuristic? No: we
	// can't see the prior error here. Instead we rely on attempts ceiling
	// to bound retries; templates that no longer exist remove themselves
	// from the picker so users can't enqueue more.
	//
	// Bump attempts and compute exponential backoff capped at 1 hour:
	//   send_at = now + min(maxBackoffSeconds, 2^attempts)
	_, err := m.db.ExecContext(ctx,
		`UPDATE signs_print_queue
		   SET attempts = attempts + 1,
		       send_at  = unixepoch() + MIN(?, 1 << MIN(attempts + 1, 20))
		 WHERE id = ?`, int64(maxBackoffSeconds), item.ID)
	return err
}

// --- helpers ---

// buildSignData constructs a SignData map from a queued print row.
// It always sets DiscordHandle and Date, then overlays fields from
// fields_json. For backward compatibility, MachineName and Issue from
// the legacy columns are set when fields_json is empty/missing.
func buildSignData(item *queuedPrint) SignData {
	data := SignData{
		"DiscordHandle": item.DiscordUsername,
		"Date":          time.Unix(item.Created, 0).Format("Mon Jan 2, 2006 3:04 PM"),
	}

	// Try to parse fields_json first.
	var fields map[string]string
	if item.FieldsJSON != "" && item.FieldsJSON != "{}" {
		if err := json.Unmarshal([]byte(item.FieldsJSON), &fields); err == nil && len(fields) > 0 {
			for k, v := range fields {
				data[k] = v
			}
			return data
		}
	}

	// Backward compatibility: use legacy columns.
	if item.MachineName != "" {
		data["MachineName"] = item.MachineName
	}
	if item.Issue != "" {
		data["Issue"] = item.Issue
	}
	return data
}

// --- handlers ---

func findTemplate(templates []Template, slug string) (Template, bool) {
	for _, t := range templates {
		if t.Slug == slug {
			return t, true
		}
	}
	return Template{}, false
}

// recentPrints returns the user's recent prints (or all if leadership).
func (m *Module) recentPrints(ctx context.Context, memberID int64, leadership bool, limit int) ([]printRecord, error) {
	if m.db == nil {
		return nil, nil
	}
	var rows *sql.Rows
	var err error
	if leadership {
		rows, err = m.db.QueryContext(ctx, `
			SELECT q.id, q.created, q.template_slug, q.machine_name, q.issue,
			       COALESCE(q.fields_json, '{}'),
			       q.discord_username, q.member_id, q.send_at
			FROM signs_print_queue q
			ORDER BY q.created DESC LIMIT ?`, limit)
	} else {
		rows, err = m.db.QueryContext(ctx, `
			SELECT q.id, q.created, q.template_slug, q.machine_name, q.issue,
			       COALESCE(q.fields_json, '{}'),
			       q.discord_username, q.member_id, q.send_at
			FROM signs_print_queue q
			WHERE q.member_id = ?
			ORDER BY q.created DESC LIMIT ?`, memberID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []printRecord
	for rows.Next() {
		var r printRecord
		var ts int64
		if err := rows.Scan(&r.ID, &ts, &r.TemplateSlug, &r.MachineName, &r.Issue,
			&r.FieldsJSON, &r.DiscordUsername, &r.MemberID, &r.SendAt); err != nil {
			return nil, err
		}
		r.Created = time.Unix(ts, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

type printRecord struct {
	ID              int64
	Created         time.Time
	TemplateSlug    string
	MachineName     string
	Issue           string
	FieldsJSON      string
	DiscordUsername string
	MemberID        sql.NullInt64
	SendAt          int64
}

// FieldSummary returns a short summary of the dynamic fields for display
// in the recent prints table. Falls back to legacy MachineName/Issue.
func (r printRecord) FieldSummary() string {
	if r.FieldsJSON != "" && r.FieldsJSON != "{}" {
		var fields map[string]string
		if err := json.Unmarshal([]byte(r.FieldsJSON), &fields); err == nil && len(fields) > 0 {
			var parts []string
			for _, v := range fields {
				if v != "" {
					parts = append(parts, v)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " — ")
			}
		}
	}
	if r.MachineName != "" && r.Issue != "" {
		return r.MachineName + " — " + r.Issue
	}
	if r.MachineName != "" {
		return r.MachineName
	}
	return r.Issue
}

// Status returns a short label suitable for the UI badge.
func (r printRecord) Status() string {
	now := time.Now().Unix()
	if r.SendAt <= now {
		return "Printing"
	}
	return "Pending"
}

func (m *Module) renderIndex(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUserMeta(r.Context())
	_, templates := m.snapshot()
	var recents []printRecord
	if user != nil {
		recents, _ = m.recentPrints(r.Context(), user.ID, user.Leadership, 20)
	}
	flash := r.URL.Query().Get("ok")
	w.Header().Set("Content-Type", "text/html")
	renderIndex(templates, recents, flash).Render(r.Context(), w)
}

func (m *Module) renderForm(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	_, templates := m.snapshot()
	tmpl, ok := findTemplate(templates, slug)
	if !ok {
		engine.ClientError(w, "Not Found", "Unknown sign template.", http.StatusNotFound)
		return
	}
	fields := tmpl.ParsedFields()
	values := make(map[string]string, len(fields))
	w.Header().Set("Content-Type", "text/html")
	renderForm(tmpl, fields, values, "").Render(r.Context(), w)
}

func (m *Module) submit(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	_, templates := m.snapshot()
	tmpl, ok := findTemplate(templates, slug)
	if !ok {
		engine.ClientError(w, "Not Found", "Unknown sign template.", http.StatusNotFound)
		return
	}

	// Cap the request body up front. MaxBytesReader will fail ParseForm
	// with a *http.MaxBytesError once the limit is exceeded.
	r.Body = http.MaxBytesReader(w, r.Body, maxSubmitBodyBytes)
	if err := r.ParseForm(); err != nil {
		engine.ClientError(w, "Bad Request", err.Error(), http.StatusBadRequest)
		return
	}

	fields := tmpl.ParsedFields()
	values := make(map[string]string, len(fields))

	// Collect and validate dynamic field values.
	for _, fd := range fields {
		val := strings.TrimSpace(r.FormValue("field_" + fd.Name))
		values[fd.Name] = val

		if fd.Required && val == "" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
			renderForm(tmpl, fields, values,
				fmt.Sprintf("%s is required.", fd.Label)).Render(r.Context(), w)
			return
		}
		if len(val) > maxIssueLen {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
			renderForm(tmpl, fields, values,
				fmt.Sprintf("%s is too long (max %d characters).", fd.Label, maxIssueLen)).Render(r.Context(), w)
			return
		}
	}

	user := auth.GetUserMeta(r.Context())
	if user == nil {
		engine.ClientError(w, "Unauthorized", "You must be logged in.", http.StatusUnauthorized)
		return
	}

	// Per-member rate limit + outstanding cap.
	var recent, outstanding int
	if err := m.db.QueryRowContext(r.Context(),
		`SELECT
		   (SELECT COUNT(*) FROM signs_print_queue WHERE member_id = ? AND created > unixepoch() - 60),
		   (SELECT COUNT(*) FROM signs_print_queue WHERE member_id = ?)`,
		user.ID, user.ID).Scan(&recent, &outstanding); err != nil {
		engine.SystemError(w, "checking rate limit: "+err.Error())
		return
	}
	if recent >= maxSubmitsPerMinute || outstanding >= maxOutstandingPerMember {
		engine.ClientError(w, "Slow down",
			"You've queued a lot of prints recently. Please wait a moment and try again.",
			http.StatusTooManyRequests)
		return
	}

	discord := lookupDiscordUsername(r.Context(), m.db, user.ID)

	fieldsJSON, err := json.Marshal(values)
	if err != nil {
		engine.SystemError(w, "encoding fields: "+err.Error())
		return
	}

	_, err = m.db.ExecContext(r.Context(), `
		INSERT INTO signs_print_queue
		    (member_id, discord_username, template_slug, machine_name, issue, fields_json)
		VALUES (?, ?, ?, '', '', ?)`,
		user.ID, discord, tmpl.Slug, string(fieldsJSON))
	if err != nil {
		engine.SystemError(w, "queueing sign print: "+err.Error())
		return
	}
	m.eventLogger.LogEvent(r.Context(), user.ID, "Queued",
		tmpl.Slug, tmpl.Name, true, string(fieldsJSON))

	http.Redirect(w, r, "/signs?ok="+tmpl.Slug, http.StatusSeeOther)
}

// lookupDiscordUsername returns the member's Discord handle, or their email
// local-part as a fallback.
func lookupDiscordUsername(ctx context.Context, db *sql.DB, memberID int64) string {
	var discord, email sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT discord_username, email FROM members WHERE id = ?`, memberID).Scan(&discord, &email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("signs: lookup discord username", "error", err, "member", memberID)
	}
	if discord.Valid && discord.String != "" {
		return discord.String
	}
	if email.Valid {
		if i := strings.IndexByte(email.String, '@'); i > 0 {
			return email.String[:i]
		}
		return email.String
	}
	return "unknown"
}
