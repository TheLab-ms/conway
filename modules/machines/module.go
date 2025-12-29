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
	"sync"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/discordwebhook"
	"github.com/TheLab-ms/conway/modules/machines/bambu"
)

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

// PrinterStatus contains the full printer status including UI-related fields.
// It embeds PrinterData for the raw printer data and adds computed/enriched fields
// that the machines module populates for display purposes.
type PrinterStatus struct {
	bambu.PrinterData

	PrinterName          string `json:"printer_name"`
	SerialNumber         string `json:"serial_number"`
	JobFinishedTimestamp *int64 `json:"job_finished_timestamp"`
	ErrorCode            string `json:"error_code"`
	GcodeFileDisplay     string `json:"gcode_file_display"`
	DiscordUserID        string `json:"discord_user_id"`
	OwnerDiscordUsername string `json:"owner_discord_username"`
}

type Module struct {
	state atomic.Pointer[[]PrinterStatus]

	db                  *sql.DB // Only used for member username lookups
	notificationChannel string
	messageQueuer       discordwebhook.MessageQueuer

	// lastNotifiedState tracks the last state that triggered a notification per printer.
	// Key: serial number, Value: notifiedState
	lastNotifiedState sync.Map

	printers     []*bambu.Printer
	configs      []*printerConfig
	serialToName map[string]string

	streams map[string]*engine.StreamMux

	testMode bool // When true, skip polling and use injected state
}

// notifiedState tracks what state we last notified about for a printer.
type notifiedState struct {
	hadJob           bool
	errorCode        string
	gcodeFile        string
	subtaskName      string // User-editable plate name from Bambu Studio
	gcodeFileDisplay string
	discordUserID    string
	printerName      string
}

func New(config string, database *sql.DB, notificationChannel string, messageQueuer discordwebhook.MessageQueuer) *Module {
	configs := []*printerConfig{}
	err := json.Unmarshal([]byte(config), &configs)
	if err != nil {
		panic(fmt.Sprintf("failed to parse Bambu printer config: %v", err))
	}

	m := &Module{
		db:                  database,
		notificationChannel: notificationChannel,
		messageQueuer:       messageQueuer,
		configs:             configs,
		serialToName:        map[string]string{},
		streams:             map[string]*engine.StreamMux{},
	}
	for _, cfg := range configs {
		m.serialToName[cfg.SerialNumber] = cfg.Name
		m.printers = append(m.printers, bambu.NewPrinter(&bambu.PrinterConfig{
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

	// Connect and stop the print
	if err := printer.Connect(); err != nil {
		slog.Error("failed to connect to printer for stop", "error", err, "serial", serial)
		engine.ClientError(w, "Connection Failed", "Failed to connect to printer", http.StatusInternalServerError)
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
			PrinterData:  data,
			PrinterName:  name,
			SerialNumber: printer.GetSerial(),
			ErrorCode:    data.PrintErrorCode,
			DiscordUserID: parseDiscordUserID(data.GcodeFile),
		}
		// Prefer SubtaskName (plate name) for display, fall back to stripped gcode filename
		if s.SubtaskName != "" {
			s.GcodeFileDisplay = s.SubtaskName
		} else {
			s.GcodeFileDisplay = stripDiscordID(data.GcodeFile)
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

	// Populate owner usernames from database
	if m.db != nil {
		m.populateOwnerUsernames(ctx, state)
	}

	m.state.Store(&state)

	// Detect state changes and send notifications
	if oldState != nil {
		m.detectStateChanges(ctx, *oldState, state)
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
			m.sendCompletedNotificationFromState(ctx, lastNotified)
			m.updateLastNotifiedState(newPrinter.SerialNumber, notifiedState{hadJob: false})
			slog.Info("print job completed", "printer", newPrinter.PrinterName)
			continue
		}

		// Job failed: error appeared (compare with previous poll, not last notified)
		if oldPrinter != nil && oldPrinter.ErrorCode == "" && hasError {
			m.sendFailedNotification(ctx, newPrinter)
			m.updateLastNotifiedState(newPrinter.SerialNumber, notifiedState{
				hadJob:           hasJob,
				errorCode:        newPrinter.ErrorCode,
				gcodeFile:        newPrinter.GcodeFile,
				subtaskName:      newPrinter.SubtaskName,
				gcodeFileDisplay: newPrinter.GcodeFileDisplay,
				discordUserID:    newPrinter.DiscordUserID,
				printerName:      newPrinter.PrinterName,
			})
			slog.Info("print job failed", "printer", newPrinter.PrinterName, "error_code", newPrinter.ErrorCode)
			continue
		}

		// Job started: update tracking with job metadata (no notification needed for start)
		if (oldPrinter == nil || oldPrinter.JobFinishedTimestamp == nil) && hasJob && newPrinter.GcodeFile != "" {
			m.updateLastNotifiedState(newPrinter.SerialNumber, notifiedState{
				hadJob:           true,
				gcodeFile:        newPrinter.GcodeFile,
				subtaskName:      newPrinter.SubtaskName,
				gcodeFileDisplay: newPrinter.GcodeFileDisplay,
				discordUserID:    newPrinter.DiscordUserID,
				printerName:      newPrinter.PrinterName,
			})
			slog.Info("print job started", "printer", newPrinter.PrinterName, "gcode", newPrinter.GcodeFile, "discord_user_id", newPrinter.DiscordUserID)
		}
	}
}

// getLastNotifiedState retrieves the last notified state for a printer.
func (m *Module) getLastNotifiedState(serial string) notifiedState {
	if v, ok := m.lastNotifiedState.Load(serial); ok {
		return v.(notifiedState)
	}
	return notifiedState{}
}

// updateLastNotifiedState updates the last notified state for a printer.
func (m *Module) updateLastNotifiedState(serial string, state notifiedState) {
	m.lastNotifiedState.Store(serial, state)
}

// sendCompletedNotificationFromState queues a Discord notification for a completed print using stored state.
func (m *Module) sendCompletedNotificationFromState(ctx context.Context, state notifiedState) {
	if m.notificationChannel == "" || m.messageQueuer == nil {
		return
	}

	mention := ""
	if state.discordUserID != "" {
		mention = fmt.Sprintf("<@%s> ", state.discordUserID)
	}

	displayName := state.gcodeFileDisplay
	if displayName == "" {
		displayName = state.gcodeFile
	}

	payload := fmt.Sprintf(`{"content":"%sYour print '%s' on %s has completed successfully.","username":"Conway Print Bot"}`,
		mention, displayName, state.printerName)

	if err := m.messageQueuer.QueueMessage(ctx, m.notificationChannel, payload); err != nil {
		slog.Error("failed to queue completion notification", "error", err, "printer", state.printerName)
	}
}

// sendFailedNotification queues a Discord notification for a failed print.
func (m *Module) sendFailedNotification(ctx context.Context, printer PrinterStatus) {
	if m.notificationChannel == "" || m.messageQueuer == nil {
		return
	}

	mention := ""
	if printer.DiscordUserID != "" {
		mention = fmt.Sprintf("<@%s> ", printer.DiscordUserID)
	}

	displayName := printer.GcodeFileDisplay
	if displayName == "" {
		displayName = printer.GcodeFile
	}

	errorCode := printer.ErrorCode
	if errorCode == "" {
		errorCode = "unknown"
	}

	payload := fmt.Sprintf(`{"content":"%sYour print '%s' on %s has failed. Error code: %s","username":"Conway Print Bot"}`,
		mention, displayName, printer.PrinterName, errorCode)

	if err := m.messageQueuer.QueueMessage(ctx, m.notificationChannel, payload); err != nil {
		slog.Error("failed to queue failure notification", "error", err, "printer", printer.PrinterName)
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

// populateOwnerUsernames looks up Discord usernames for printers with active jobs.
func (m *Module) populateOwnerUsernames(ctx context.Context, state []PrinterStatus) {
	for i := range state {
		if state[i].JobFinishedTimestamp == nil && state[i].ErrorCode == "" {
			continue // No active job
		}
		if state[i].DiscordUserID == "" {
			continue // No Discord ID in filename
		}
		var username sql.NullString
		err := m.db.QueryRowContext(ctx, `
			SELECT discord_username FROM members WHERE discord_user_id = $1 LIMIT 1`,
			state[i].DiscordUserID).Scan(&username)
		if err != nil && err != sql.ErrNoRows {
			slog.Warn("failed to query owner username", "error", err, "printer", state[i].PrinterName)
			continue
		}
		if username.Valid {
			state[i].OwnerDiscordUsername = username.String
		}
	}
}
