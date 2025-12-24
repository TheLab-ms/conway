package machines

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/db"
	"github.com/torbenconto/bambulabs_api"
)

const migration = `
CREATE TABLE IF NOT EXISTS print_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    printer_serial TEXT NOT NULL,
    printer_name TEXT NOT NULL,
    gcode_file TEXT NOT NULL,
    discord_user_id TEXT,
    started_at INTEGER NOT NULL,
    estimated_finish_at INTEGER,
    completed_at INTEGER,
    status TEXT NOT NULL DEFAULT 'running',
    error_code TEXT,
    notification_sent INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX IF NOT EXISTS print_jobs_status_idx ON print_jobs (status);
CREATE INDEX IF NOT EXISTS print_jobs_printer_serial_idx ON print_jobs (printer_serial);
`

// NotificationQueuer is an interface for queuing Discord notifications.
type NotificationQueuer interface {
	QueueMessage(ctx context.Context, channelID, payload string) error
}

// discordUserIDPrefixPattern matches Discord snowflake IDs (17-19 digits) at the start of a filename
var discordUserIDPrefixPattern = regexp.MustCompile(`^(\d{17,19})_`)

// discordUserIDSuffixPattern matches Discord snowflake IDs (17-19 digits) at the end of a filename (before extension)
var discordUserIDSuffixPattern = regexp.MustCompile(`_(\d{17,19})\.`)

//go:generate go run github.com/a-h/templ/cmd/templ generate

type printerConfig struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	AccessCode   string `json:"access_code"`
	SerialNumber string `json:"serial_number"`
}

// PrinterStatus represents the current state of a printer for UI rendering.
type PrinterStatus struct {
	PrinterName          string `json:"printer_name"`
	SerialNumber         string `json:"serial_number"`
	JobFinishedTimestamp *int64 `json:"job_finished_timestamp"`
	ErrorCode            string `json:"error_code"`
	GcodeFile            string `json:"gcode_file"`
}

type Module struct {
	state atomic.Pointer[[]PrinterStatus]

	db                   *sql.DB
	notifier             NotificationQueuer
	notificationChannel  string
	printers             []*bambulabs_api.Printer
	configs              []*printerConfig
	serialToName         map[string]string

	streams map[string]*engine.StreamMux

	testMode bool // When true, skip polling and use injected state
}

func New(config string, database *sql.DB, notifier NotificationQueuer, notificationChannel string) *Module {
	if database != nil {
		db.MustMigrate(database, migration)
	}

	configs := []*printerConfig{}
	err := json.Unmarshal([]byte(config), &configs)
	if err != nil {
		panic(fmt.Sprintf("failed to parse Bambu printer config: %v", err))
	}

	m := &Module{
		db:                  database,
		notifier:            notifier,
		notificationChannel: notificationChannel,
		configs:             configs,
		serialToName:        map[string]string{},
		streams:             map[string]*engine.StreamMux{},
	}
	for _, cfg := range configs {
		m.serialToName[cfg.SerialNumber] = cfg.Name
		m.printers = append(m.printers, bambulabs_api.NewPrinter(&bambulabs_api.PrinterConfig{
			Host:         cfg.Host,
			AccessCode:   cfg.AccessCode,
			SerialNumber: cfg.SerialNumber,
		}))
		m.streams[cfg.SerialNumber] = m.newStreamMux(cfg)
	}
	zero := []PrinterStatus{}
	m.state.Store(&zero)
	return m
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
	var printer *bambulabs_api.Printer
	for _, p := range m.printers {
		if p.GetSerial() == serial {
			printer = p
			break
		}
	}
	if printer == nil {
		http.Error(w, "Printer not found", http.StatusNotFound)
		return
	}

	// Connect and stop the print
	if err := printer.Connect(); err != nil {
		slog.Error("failed to connect to printer for stop", "error", err, "serial", serial)
		http.Error(w, "Failed to connect to printer", http.StatusInternalServerError)
		return
	}

	if err := printer.StopPrint(); err != nil {
		slog.Error("failed to stop print", "error", err, "serial", serial)
		http.Error(w, "Failed to stop print", http.StatusInternalServerError)
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

func (m *Module) newStreamMux(cfg *printerConfig) *engine.StreamMux {
	rtspURL := fmt.Sprintf("rtsps://bblp:%s@%s:322/streaming/live/1", cfg.AccessCode, cfg.Host)
	return engine.NewStreamMux(func(ctx context.Context) (io.ReadCloser, error) {
		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-rtsp_transport", "tcp",
			"-i", rtspURL,
			"-c:v", "mjpeg",
			"-q:v", "5",
			"-r", "15",
			"-an",
			"-f", "mpjpeg",
			"-boundary_tag", "frame",
			"pipe:1",
		)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("failed to create ffmpeg stdout pipe: %w", err)
		}

		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("failed to start ffmpeg: %w", err)
		}

		slog.Info("started camera stream", "printer", m.serialToName[cfg.SerialNumber])

		// Return a wrapper that waits for cmd on close
		return &cmdReader{ReadCloser: stdout, cmd: cmd, name: m.serialToName[cfg.SerialNumber]}, nil
	})
}

type cmdReader struct {
	io.ReadCloser
	cmd  *exec.Cmd
	name string
}

func (c *cmdReader) Close() error {
	err := c.ReadCloser.Close()
	c.cmd.Wait()
	slog.Info("stopped camera stream", "printer", c.name)
	return err
}

func (m *Module) serveMJPEGStream(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")

	mux, ok := m.streams[serial]
	if !ok {
		http.Error(w, "Printer not found", http.StatusNotFound)
		return
	}

	ch := mux.Subscribe()
	if ch == nil {
		http.Error(w, "Failed to start camera stream", http.StatusInternalServerError)
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
	procs.Add(engine.Poll(time.Second*5, m.poll))
}

func (m *Module) poll(ctx context.Context) bool {
	slog.Info("starting to get Bambu printer status")
	start := time.Now()

	oldState := m.state.Load()

	var state []PrinterStatus
	for _, printer := range m.printers {
		name := m.serialToName[printer.GetSerial()]
		if err := printer.Connect(); err != nil {
			slog.Warn("unable to connect to Bambu printer", "error", err, "printer", name)
			continue
		}
		data, err := printer.Data()
		if err != nil {
			slog.Warn("unable to get status from Bambu printer", "error", err, "printer", name)
			continue
		}

		s := PrinterStatus{
			PrinterName:  name,
			SerialNumber: printer.GetSerial(),
			ErrorCode:    data.PrintErrorCode,
			GcodeFile:    data.GcodeFile,
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

	// Detect state changes and update print jobs
	if oldState != nil && m.db != nil {
		m.detectStateChanges(ctx, *oldState, state)
	}
	// Check for stuck jobs
	if m.db != nil {
		m.checkStuckJobs(ctx)
	}

	slog.Info("finished getting Bambu printer status", "seconds", time.Since(start).Seconds())
	return false
}

// parseDiscordUserID extracts a Discord user ID from a filename.
// Supports both prefix and suffix formats. Discord user IDs are 17-19 digit snowflakes.
// Examples:
//   - "123456789012345678_benchy.gcode" -> "123456789012345678" (prefix)
//   - "benchy_123456789012345678.gcode" -> "123456789012345678" (suffix)
//   - "benchy.gcode" -> ""
func parseDiscordUserID(filename string) string {
	// Try prefix first
	if matches := discordUserIDPrefixPattern.FindStringSubmatch(filename); len(matches) >= 2 {
		return matches[1]
	}
	// Try suffix
	if matches := discordUserIDSuffixPattern.FindStringSubmatch(filename); len(matches) >= 2 {
		return matches[1]
	}
	return ""
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

// detectStateChanges compares old and new printer states to detect job transitions.
func (m *Module) detectStateChanges(ctx context.Context, oldState, newState []PrinterStatus) {
	for _, newPrinter := range newState {
		oldPrinter := findPrinterBySerial(oldState, newPrinter.SerialNumber)

		// New job started: printer had no job (no finish timestamp), now has one
		if (oldPrinter == nil || oldPrinter.JobFinishedTimestamp == nil) && newPrinter.JobFinishedTimestamp != nil && newPrinter.GcodeFile != "" {
			m.onJobStarted(ctx, newPrinter)
			continue
		}

		if oldPrinter == nil {
			continue
		}

		// Job failed: error code appeared
		if oldPrinter.ErrorCode == "" && newPrinter.ErrorCode != "" {
			m.onJobFailed(ctx, newPrinter)
			continue
		}

		// Job completed: had a job, now no job, no error
		if oldPrinter.JobFinishedTimestamp != nil && newPrinter.JobFinishedTimestamp == nil && newPrinter.ErrorCode == "" {
			m.onJobCompleted(ctx, oldPrinter.SerialNumber, oldPrinter.PrinterName)
		}
	}
}

// onJobStarted handles when a new print job starts.
func (m *Module) onJobStarted(ctx context.Context, printer PrinterStatus) {
	discordUserID := parseDiscordUserID(printer.GcodeFile)

	var estimatedFinish *int64
	if printer.JobFinishedTimestamp != nil {
		estimatedFinish = printer.JobFinishedTimestamp
	}

	_, err := m.db.ExecContext(ctx, `
		INSERT INTO print_jobs (printer_serial, printer_name, gcode_file, discord_user_id, started_at, estimated_finish_at, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'running')`,
		printer.SerialNumber, printer.PrinterName, printer.GcodeFile, nullString(discordUserID),
		time.Now().Unix(), estimatedFinish)
	if err != nil {
		slog.Error("failed to insert print job", "error", err, "printer", printer.PrinterName)
	} else {
		slog.Info("print job started", "printer", printer.PrinterName, "gcode", printer.GcodeFile, "discord_user_id", discordUserID)
	}
}

// onJobCompleted handles when a print job successfully completes.
func (m *Module) onJobCompleted(ctx context.Context, serial, printerName string) {
	// Find the running job for this printer
	var jobID int64
	var gcodeFile string
	var discordUserID sql.NullString
	err := m.db.QueryRowContext(ctx, `
		SELECT id, gcode_file, discord_user_id FROM print_jobs
		WHERE printer_serial = $1 AND status = 'running'
		ORDER BY created DESC LIMIT 1`, serial).Scan(&jobID, &gcodeFile, &discordUserID)
	if err != nil {
		if err != sql.ErrNoRows {
			slog.Error("failed to find running print job", "error", err, "printer", printerName)
		}
		return
	}

	// Update the job status
	_, err = m.db.ExecContext(ctx, `UPDATE print_jobs SET status = 'completed', completed_at = $1 WHERE id = $2`, time.Now().Unix(), jobID)
	if err != nil {
		slog.Error("failed to update print job status", "error", err, "job_id", jobID)
		return
	}

	slog.Info("print job completed", "printer", printerName, "gcode", gcodeFile)
	m.sendNotification(ctx, jobID, gcodeFile, printerName, discordUserID.String, "completed", "")
}

// onJobFailed handles when a print job fails.
func (m *Module) onJobFailed(ctx context.Context, printer PrinterStatus) {
	// Find the running job for this printer
	var jobID int64
	var gcodeFile string
	var discordUserID sql.NullString
	err := m.db.QueryRowContext(ctx, `
		SELECT id, gcode_file, discord_user_id FROM print_jobs
		WHERE printer_serial = $1 AND status = 'running'
		ORDER BY created DESC LIMIT 1`, printer.SerialNumber).Scan(&jobID, &gcodeFile, &discordUserID)
	if err != nil {
		if err != sql.ErrNoRows {
			slog.Error("failed to find running print job for failure", "error", err, "printer", printer.PrinterName)
		}
		return
	}

	// Update the job status
	_, err = m.db.ExecContext(ctx, `UPDATE print_jobs SET status = 'failed', completed_at = $1, error_code = $2 WHERE id = $3`,
		time.Now().Unix(), printer.ErrorCode, jobID)
	if err != nil {
		slog.Error("failed to update print job status", "error", err, "job_id", jobID)
		return
	}

	slog.Info("print job failed", "printer", printer.PrinterName, "gcode", gcodeFile, "error_code", printer.ErrorCode)
	m.sendNotification(ctx, jobID, gcodeFile, printer.PrinterName, discordUserID.String, "failed", printer.ErrorCode)
}

// checkStuckJobs checks for jobs that are past their estimated finish time.
func (m *Module) checkStuckJobs(ctx context.Context) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, gcode_file, printer_name, discord_user_id FROM print_jobs
		WHERE status = 'running' AND estimated_finish_at IS NOT NULL AND estimated_finish_at < $1 AND notification_sent = 0`,
		time.Now().Unix())
	if err != nil {
		slog.Error("failed to query stuck jobs", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var jobID int64
		var gcodeFile, printerName string
		var discordUserID sql.NullString
		if err := rows.Scan(&jobID, &gcodeFile, &printerName, &discordUserID); err != nil {
			slog.Error("failed to scan stuck job", "error", err)
			continue
		}

		// Update the job status to stuck
		_, err = m.db.ExecContext(ctx, `UPDATE print_jobs SET status = 'stuck' WHERE id = $1`, jobID)
		if err != nil {
			slog.Error("failed to mark job as stuck", "error", err, "job_id", jobID)
			continue
		}

		slog.Info("print job stuck", "printer", printerName, "gcode", gcodeFile)
		m.sendNotification(ctx, jobID, gcodeFile, printerName, discordUserID.String, "stuck", "")
	}
}

// sendNotification queues a Discord notification for a print job status change.
func (m *Module) sendNotification(ctx context.Context, jobID int64, gcodeFile, printerName, discordUserID, status, errorCode string) {
	if m.notifier == nil || m.notificationChannel == "" {
		return
	}

	// Mark notification as sent
	_, err := m.db.ExecContext(ctx, `UPDATE print_jobs SET notification_sent = 1 WHERE id = $1`, jobID)
	if err != nil {
		slog.Error("failed to mark notification as sent", "error", err, "job_id", jobID)
	}

	// Build message content
	var message string
	displayName := stripDiscordID(gcodeFile)

	switch status {
	case "completed":
		message = fmt.Sprintf("Your print '%s' on %s has completed successfully! ✅", displayName, printerName)
	case "failed":
		message = fmt.Sprintf("Your print '%s' on %s has failed. ❌ Error code: %s", displayName, printerName, errorCode)
	case "stuck":
		message = fmt.Sprintf("Your print '%s' on %s appears to be stuck (past estimated completion time). ⚠️", displayName, printerName)
	default:
		return
	}

	// Add Discord mention if user ID is available
	if discordUserID != "" {
		message = fmt.Sprintf("<@%s> %s", discordUserID, message)
	}

	// Build JSON payload
	payload := fmt.Sprintf(`{"content":%q,"username":"Conway Print Bot"}`, message)

	if err := m.notifier.QueueMessage(ctx, m.notificationChannel, payload); err != nil {
		slog.Error("failed to queue discord notification", "error", err, "job_id", jobID)
	}
}

// stripDiscordID removes the Discord user ID (prefix or suffix) from a filename for display.
func stripDiscordID(filename string) string {
	// Try prefix first
	if loc := discordUserIDPrefixPattern.FindStringIndex(filename); loc != nil {
		return filename[loc[1]:]
	}
	// Try suffix - need to preserve the extension
	if loc := discordUserIDSuffixPattern.FindStringIndex(filename); loc != nil {
		// loc[0] is start of "_ID.", we want everything before "_" and after "ID"
		// Extract extension (everything from the last .)
		ext := filename[loc[1]-1:] // includes the dot
		return filename[:loc[0]] + ext
	}
	return filename
}

// nullString converts an empty string to a sql.NullString with Valid=false.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
