package machines

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/discordwebhook"
	"github.com/TheLab-ms/conway/modules/machines/bambu"
)

const migration = `
CREATE TABLE IF NOT EXISTS bambu_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    printers_json TEXT NOT NULL DEFAULT '[]',
    poll_interval_seconds INTEGER NOT NULL DEFAULT 5
) STRICT;

CREATE TABLE IF NOT EXISTS bambu_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    event_type TEXT NOT NULL,
    printer_name TEXT,
    printer_serial TEXT,
    success INTEGER NOT NULL DEFAULT 1,
    details TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS bambu_events_created_idx ON bambu_events (created);
CREATE INDEX IF NOT EXISTS bambu_events_type_idx ON bambu_events (event_type, success);
`

var discordHandleRegex = regexp.MustCompile(`@([a-zA-Z0-9_.]+)`)

type printerConfig struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	AccessCode   string `json:"access_code"`
	SerialNumber string `json:"serial_number"`
}

type PrinterStatus struct {
	bambu.PrinterData

	PrinterName          string `json:"printer_name"`
	SerialNumber         string `json:"serial_number"`
	JobFinishedTimestamp *int64 `json:"job_finished_timestamp"`
	ErrorCode            string `json:"error_code"`
}

func (p PrinterStatus) OwnerDiscordHandle() string {
	match := discordHandleRegex.FindStringSubmatch(p.SubtaskName)
	if len(match) >= 2 {
		return match[1]
	}
	return ""
}

type Module struct {
	state atomic.Pointer[[]PrinterStatus]

	db            *sql.DB
	messageQueuer discordwebhook.MessageQueuer

	// lastNotifiedState tracks the last state that triggered a notification per printer.
	// Key: serial number, Value: notifiedState
	lastNotifiedState sync.Map

	printers     []*bambu.Printer
	configs      []*printerConfig
	serialToName map[string]string

	streams map[string]*engine.StreamMux

	// pollInterval is the interval between printer status polls (from config or default)
	pollInterval time.Duration

	// configVersion tracks the current loaded config version to detect changes
	configVersion int64

	testMode bool // When true, skip polling and use injected state
}

// notifiedState tracks what state we last notified about for a printer.
type notifiedState struct {
	hadJob               bool
	errorCode            string
	gcodeFile            string
	subtaskName          string // User-editable plate name from Bambu Studio (contains Discord username)
	ownerDiscordUsername string
	printerName          string
}

func New(db *sql.DB) *Module {
	if db != nil {
		engine.MustMigrate(db, migration)
	}

	m := &Module{
		db:           db,
		serialToName: map[string]string{},
		streams:      map[string]*engine.StreamMux{},
		pollInterval: time.Second * 5, // default
	}

	// Load and apply config from database
	m.reloadConfig(context.Background())

	zero := []PrinterStatus{}
	m.state.Store(&zero)
	return m
}

// SetWebhookQueuer sets the Discord webhook queuer for notifications.
// This is called after module creation to support wiring dependencies.
func (m *Module) SetWebhookQueuer(queuer discordwebhook.MessageQueuer) {
	m.messageQueuer = queuer
}

// loadPrintWebhookURL loads the print notification webhook URL from the database.
func (m *Module) loadPrintWebhookURL(ctx context.Context) string {
	if m.db == nil {
		return ""
	}
	var webhookURL string
	err := m.db.QueryRowContext(ctx,
		`SELECT print_webhook_url FROM discord_config ORDER BY version DESC LIMIT 1`).Scan(&webhookURL)
	if err != nil {
		return ""
	}
	return webhookURL
}

// NewForTesting creates a Module with mock printer data for e2e tests.
// The printers slice defines what the UI will render - no real connections are made.
func NewForTesting(printers []PrinterStatus) *Module {
	m := &Module{
		streams:  map[string]*engine.StreamMux{},
		testMode: true,
	}
	m.state.Store(&printers)
	return m
}

// SetTestState updates the printer state (for testing only).
func (m *Module) SetTestState(printers []PrinterStatus) {
	m.state.Store(&printers)
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /machines", router.WithAuthn(m.renderView))
	router.HandleFunc("GET /machines/stream/{serial}", router.WithAuthn(m.serveMJPEGStream))
	router.HandleFunc("POST /machines/{serial}/stop", router.WithAuthn(m.stopPrint))
}

func (m *Module) stopPrint(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")

	// Find the printer for this serial number
	var printer *bambu.Printer
	for _, p := range m.printers {
		if p.GetSerial() == serial {
			printer = p
			break
		}
	}
	if printer == nil {
		engine.ClientError(w, "Not Found", "Printer not found", http.StatusNotFound)
		return
	}

	if err := printer.StopPrint(); err != nil {
		slog.Error("failed to stop print", "error", err, "serial", serial)
		engine.ClientError(w, "Stop Failed", "Failed to stop print", http.StatusInternalServerError)
		return
	}

	slog.Info("print stopped successfully", "serial", serial, "printer", m.serialToName[serial])

	// Redirect back to machines page
	http.Redirect(w, r, "/machines", http.StatusSeeOther)
}

func (m *Module) renderView(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	renderMachines(*m.state.Load()).Render(r.Context(), w)
}

func (m *Module) serveMJPEGStream(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")

	mux, ok := m.streams[serial]
	if !ok {
		engine.ClientError(w, "Not Found", "Printer not found", http.StatusNotFound)
		return
	}

	ch := mux.Subscribe()
	if ch == nil {
		engine.ClientError(w, "Stream Error", "Failed to start camera stream", http.StatusInternalServerError)
		return
	}
	defer mux.Unsubscribe(ch)

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Cache-Control", "no-cache")

	for {
		select {
		case data, ok := <-ch:
			if !ok {
				return
			}
			w.Write(data)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (m *Module) AttachWorkers(procs *engine.ProcMgr) {
	if m.testMode {
		return // Skip polling in test mode - use injected state
	}
	procs.Add(engine.Poll(m.pollInterval, m.poll))

	// Cleanup old Bambu events after 24 hours (same as Discord)
	const eventTTL = 24 * 60 * 60 // 24 hours in seconds
	procs.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "old Bambu events",
		"DELETE FROM bambu_events WHERE created < unixepoch() - ?", eventTTL)))
}

func (m *Module) poll(ctx context.Context) bool {
	// Check if config has changed and reload if needed
	if m.configChanged(ctx) {
		slog.Info("Bambu config changed, reloading")
		m.reloadConfig(ctx)
	}

	slog.Info("starting to get Bambu printer status")
	start := time.Now()

	oldState := m.state.Load()

	var state []PrinterStatus
	for _, printer := range m.printers {
		name := m.serialToName[printer.GetSerial()]
		serial := printer.GetSerial()
		data, err := printer.GetState()
		if err != nil {
			slog.Warn("unable to get status from Bambu printer", "error", err, "printer", name)
			m.logEvent(ctx, "PollError", name, serial, false, err.Error())
			continue
		}

		m.logEvent(ctx, "Poll", name, serial, true, fmt.Sprintf("state=%s", data.GcodeState))

		s := PrinterStatus{
			PrinterData:  data,
			PrinterName:  name,
			SerialNumber: serial,
			ErrorCode:    data.PrintErrorCode,
		}
		if s.ErrorCode == "0" {
			s.ErrorCode = ""
		}
		if data.RemainingPrintTime <= 1 {
			s.JobFinishedTimestamp = nil
		} else {
			// Calculate the finished timestamp based on the remaining time
			finishedTimestamp := time.Now().Add(time.Duration(data.RemainingPrintTime) * time.Minute).Unix()
			s.JobFinishedTimestamp = &finishedTimestamp
		}

		state = append(state, s)
	}

	m.state.Store(&state)

	// Detect state changes and send notifications
	if oldState != nil {
		m.detectStateChanges(ctx, *oldState, state)
	}

	slog.Info("finished getting Bambu printer status", "seconds", time.Since(start).Seconds())
	return false
}

// findPrinterBySerial finds a printer in a state slice by serial number.
func findPrinterBySerial(state []PrinterStatus, serial string) *PrinterStatus {
	for i := range state {
		if state[i].SerialNumber == serial {
			return &state[i]
		}
	}
	return nil
}

// detectStateChanges compares old and new printer states to detect job transitions and send notifications.
func (m *Module) detectStateChanges(ctx context.Context, oldState, newState []PrinterStatus) {
	for _, newPrinter := range newState {
		oldPrinter := findPrinterBySerial(oldState, newPrinter.SerialNumber)

		// Get last notified state for this printer
		lastNotified := m.getLastNotifiedState(newPrinter.SerialNumber)

		// Determine current state
		hasJob := newPrinter.JobFinishedTimestamp != nil
		hasError := newPrinter.ErrorCode != ""

		// Job completed: had job before, no job now, no error
		if lastNotified.hadJob && !hasJob && !hasError {
			// Use the stored job metadata from when the job was running
			m.sendCompletedNotificationFromState(ctx, newPrinter)
			m.updateLastNotifiedState(newPrinter.SerialNumber, notifiedState{hadJob: false})
			slog.Info("print job completed", "printer", newPrinter.PrinterName)
			continue
		}

		// Job failed: error appeared (compare with previous poll, not last notified)
		if oldPrinter != nil && oldPrinter.ErrorCode == "" && hasError {
			m.sendFailedNotification(ctx, newPrinter)
			m.updateLastNotifiedState(newPrinter.SerialNumber, notifiedState{
				hadJob:               hasJob,
				errorCode:            newPrinter.ErrorCode,
				gcodeFile:            newPrinter.GcodeFile,
				subtaskName:          newPrinter.SubtaskName,
				ownerDiscordUsername: newPrinter.OwnerDiscordHandle(),
				printerName:          newPrinter.PrinterName,
			})
			slog.Info("print job failed", "printer", newPrinter.PrinterName, "error_code", newPrinter.ErrorCode)
			continue
		}

		// Job started: update tracking with job metadata (no notification needed for start)
		if (oldPrinter == nil || oldPrinter.JobFinishedTimestamp == nil) && hasJob && newPrinter.GcodeFile != "" {
			m.updateLastNotifiedState(newPrinter.SerialNumber, notifiedState{
				hadJob:               true,
				gcodeFile:            newPrinter.GcodeFile,
				subtaskName:          newPrinter.SubtaskName,
				ownerDiscordUsername: newPrinter.OwnerDiscordHandle(),
				printerName:          newPrinter.PrinterName,
			})
			slog.Info("print job started", "printer", newPrinter.PrinterName, "gcode", newPrinter.GcodeFile, "owner", newPrinter.OwnerDiscordHandle())
		}
	}
}

func (m *Module) getLastNotifiedState(serial string) notifiedState {
	if v, ok := m.lastNotifiedState.Load(serial); ok {
		return v.(notifiedState)
	}
	return notifiedState{}
}

func (m *Module) updateLastNotifiedState(serial string, state notifiedState) {
	m.lastNotifiedState.Store(serial, state)
}

func (m *Module) sendCompletedNotificationFromState(ctx context.Context, printer PrinterStatus) {
	if m.messageQueuer == nil || printer.OwnerDiscordHandle() == "" {
		return
	}
	webhookURL := m.loadPrintWebhookURL(ctx)
	if webhookURL == "" {
		return
	}
	userID := m.resolveDiscordUserID(ctx, printer.OwnerDiscordHandle())
	if userID == "" {
		return
	}

	payload := fmt.Sprintf(`{"content":"<@%s>: your print has completed successfully on %s.","username":"Conway Print Bot"}`, userID, printer.PrinterName)

	if err := m.messageQueuer.QueueMessage(ctx, webhookURL, payload); err != nil {
		slog.Error("failed to queue completion notification", "error", err, "printer", printer.PrinterName)
	}
}

func (m *Module) sendFailedNotification(ctx context.Context, printer PrinterStatus) {
	if m.messageQueuer == nil {
		return
	}
	webhookURL := m.loadPrintWebhookURL(ctx)
	if webhookURL == "" {
		return
	}

	errorCode := printer.ErrorCode
	if errorCode == "" {
		errorCode = "unknown"
	}

	var payload string
	if userID := m.resolveDiscordUserID(ctx, printer.OwnerDiscordHandle()); userID == "" {
		payload = fmt.Sprintf(`{"content":"A print on %s has failed with error code: %s","username":"Conway Print Bot"}`, printer.PrinterName, errorCode)
	} else {
		payload = fmt.Sprintf(`{"content":"<@%s>: your print on %s has failed with error code: %s","username":"Conway Print Bot"}`, userID, printer.PrinterName, errorCode)
	}

	if err := m.messageQueuer.QueueMessage(ctx, webhookURL, payload); err != nil {
		slog.Error("failed to queue failure notification", "error", err, "printer", printer.PrinterName)
	}
}

func (m *Module) resolveDiscordUserID(ctx context.Context, username string) string {
	if m.db == nil || username == "" {
		return ""
	}

	var userID string
	err := m.db.QueryRowContext(ctx, `SELECT discord_user_id FROM members WHERE discord_username = ? AND discord_user_id IS NOT NULL`, username).Scan(&userID)
	if err == nil {
		return userID
	}
	if err != sql.ErrNoRows {
		slog.Warn("failed to look up Discord user ID", "error", err, "username", username)
	}
	return ""
}

// configChanged checks if the database config version differs from the loaded version.
func (m *Module) configChanged(ctx context.Context) bool {
	if m.db == nil {
		return false
	}
	var version int64
	err := m.db.QueryRowContext(ctx,
		`SELECT version FROM bambu_config ORDER BY version DESC LIMIT 1`).Scan(&version)
	if err != nil {
		return false
	}
	return version != m.configVersion
}

// reloadConfig loads the configuration from database and rebuilds printer connections.
func (m *Module) reloadConfig(ctx context.Context) {
	if m.db == nil {
		return
	}

	row := m.db.QueryRowContext(ctx,
		`SELECT version, printers_json, COALESCE(poll_interval_seconds, 5)
		 FROM bambu_config ORDER BY version DESC LIMIT 1`)

	var version int64
	var printersJSON string
	var pollIntervalSec int
	err := row.Scan(&version, &printersJSON, &pollIntervalSec)
	if err == sql.ErrNoRows {
		return // No config
	}
	if err != nil {
		slog.Error("failed to load Bambu config", "error", err)
		return
	}

	m.configVersion = version

	var configs []*printerConfig
	if err := json.Unmarshal([]byte(printersJSON), &configs); err != nil {
		slog.Error("failed to parse Bambu printers JSON", "error", err)
		return
	}

	// Apply poll interval
	if pollIntervalSec >= 1 {
		m.pollInterval = time.Duration(pollIntervalSec) * time.Second
	}

	// Rebuild printer connections
	m.configs = configs
	m.serialToName = make(map[string]string)
	m.printers = nil

	for _, cfg := range configs {
		m.serialToName[cfg.SerialNumber] = cfg.Name
		printer := bambu.NewPrinter(&bambu.PrinterConfig{
			Host:         cfg.Host,
			AccessCode:   cfg.AccessCode,
			SerialNumber: cfg.SerialNumber,
		})
		m.printers = append(m.printers, printer)
		// Only create stream mux if it doesn't exist
		if _, exists := m.streams[cfg.SerialNumber]; !exists {
			m.streams[cfg.SerialNumber] = engine.NewStreamMux(func(ctx context.Context) (io.ReadCloser, error) {
				return printer.CameraStream(ctx)
			})
		}
	}

	slog.Info("loaded Bambu config", "printers", len(configs), "pollInterval", m.pollInterval)
}

// logEvent logs a Bambu operation event to the database.
func (m *Module) logEvent(ctx context.Context, eventType, printerName, printerSerial string, success bool, details string) {
	if m.db == nil {
		return
	}
	successInt := 0
	if success {
		successInt = 1
	}
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO bambu_events (event_type, printer_name, printer_serial, success, details)
		 VALUES (?, ?, ?, ?, ?)`,
		eventType, printerName, printerSerial, successInt, details)
	if err != nil {
		slog.Error("failed to log Bambu event", "error", err, "eventType", eventType)
	}
}

// GetConfiguredPrinterCount returns the number of configured printers.
func (m *Module) GetConfiguredPrinterCount() int {
	return len(m.configs)
}
