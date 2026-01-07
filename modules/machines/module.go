package machines

import (
	"context"
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

var discordHandleRegex = regexp.MustCompile(`@([a-zA-Z0-9_.]+)`)

// extractDiscordHandle extracts a Discord handle from a string.
// It looks for patterns like @username and returns the username without the @ prefix.
// Returns empty string if no Discord handle is found.
func extractDiscordHandle(s string) string {
	match := discordHandleRegex.FindStringSubmatch(s)
	if len(match) >= 2 {
		return match[1]
	}
	return ""
}

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
	OwnerDiscordUsername string `json:"owner_discord_username"`
}

type Module struct {
	state atomic.Pointer[[]PrinterStatus]

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
	hadJob               bool
	errorCode            string
	gcodeFile            string
	subtaskName          string // User-editable plate name from Bambu Studio (contains Discord username)
	ownerDiscordUsername string
	printerName          string
}

func New(config string, notificationChannel string, messageQueuer discordwebhook.MessageQueuer) *Module {
	configs := []*printerConfig{}
	err := json.Unmarshal([]byte(config), &configs)
	if err != nil {
		panic(fmt.Sprintf("failed to parse Bambu printer config: %v", err))
	}

	m := &Module{
		notificationChannel: notificationChannel,
		messageQueuer:       messageQueuer,
		configs:             configs,
		serialToName:        map[string]string{},
		streams:             map[string]*engine.StreamMux{},
	}
	for _, cfg := range configs {
		m.serialToName[cfg.SerialNumber] = cfg.Name
		printer := bambu.NewPrinter(&bambu.PrinterConfig{
			Host:         cfg.Host,
			AccessCode:   cfg.AccessCode,
			SerialNumber: cfg.SerialNumber,
		})
		m.printers = append(m.printers, printer)
		m.streams[cfg.SerialNumber] = engine.NewStreamMux(func(ctx context.Context) (io.ReadCloser, error) {
			return printer.CameraStream(ctx)
		})
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
	procs.Add(engine.Poll(time.Second*5, m.poll))
}

func (m *Module) poll(ctx context.Context) bool {
	slog.Info("starting to get Bambu printer status")
	start := time.Now()

	oldState := m.state.Load()

	var state []PrinterStatus
	for _, printer := range m.printers {
		name := m.serialToName[printer.GetSerial()]
		data, err := printer.GetState()
		if err != nil {
			slog.Warn("unable to get status from Bambu printer", "error", err, "printer", name)
			continue
		}

		s := PrinterStatus{
			PrinterData:          data,
			PrinterName:          name,
			SerialNumber:         printer.GetSerial(),
			ErrorCode:            data.PrintErrorCode,
			OwnerDiscordUsername: extractDiscordHandle(data.SubtaskName),
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
			m.sendCompletedNotificationFromState(ctx, lastNotified)
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
				ownerDiscordUsername: newPrinter.OwnerDiscordUsername,
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
				ownerDiscordUsername: newPrinter.OwnerDiscordUsername,
				printerName:          newPrinter.PrinterName,
			})
			slog.Info("print job started", "printer", newPrinter.PrinterName, "gcode", newPrinter.GcodeFile, "owner", newPrinter.OwnerDiscordUsername)
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

	owner := ""
	if state.ownerDiscordUsername != "" {
		owner = fmt.Sprintf("%s: ", state.ownerDiscordUsername)
	}

	payload := fmt.Sprintf(`{"content":"%sYour print on %s has completed successfully.","username":"Conway Print Bot"}`,
		owner, state.printerName)

	if err := m.messageQueuer.QueueMessage(ctx, m.notificationChannel, payload); err != nil {
		slog.Error("failed to queue completion notification", "error", err, "printer", state.printerName)
	}
}

// sendFailedNotification queues a Discord notification for a failed print.
func (m *Module) sendFailedNotification(ctx context.Context, printer PrinterStatus) {
	if m.notificationChannel == "" || m.messageQueuer == nil {
		return
	}

	owner := ""
	if printer.OwnerDiscordUsername != "" {
		owner = fmt.Sprintf("%s: ", printer.OwnerDiscordUsername)
	}

	errorCode := printer.ErrorCode
	if errorCode == "" {
		errorCode = "unknown"
	}

	payload := fmt.Sprintf(`{"content":"%sYour print on %s has failed. Error code: %s","username":"Conway Print Bot"}`,
		owner, printer.PrinterName, errorCode)

	if err := m.messageQueuer.QueueMessage(ctx, m.notificationChannel, payload); err != nil {
		slog.Error("failed to queue failure notification", "error", err, "printer", printer.PrinterName)
	}
}
