package machines

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/modules/machines/bambu"
)

const migration = `
CREATE TABLE IF NOT EXISTS bambu_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    printers_json TEXT NOT NULL DEFAULT '[]',
    poll_interval_seconds INTEGER NOT NULL DEFAULT 5
) STRICT;

CREATE TABLE IF NOT EXISTS bambu_printer_state (
    serial_number TEXT PRIMARY KEY,
    printer_name TEXT NOT NULL,
    gcode_file TEXT NOT NULL DEFAULT '',
    subtask_name TEXT NOT NULL DEFAULT '',
    gcode_state TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    remaining_print_time INTEGER NOT NULL DEFAULT 0,
    print_percent_done INTEGER NOT NULL DEFAULT 0,
    job_finished_timestamp INTEGER,
    stop_requested INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
) STRICT;

CREATE INDEX IF NOT EXISTS bambu_printer_state_updated_idx ON bambu_printer_state (updated_at);

/* Drop old hardcoded notification triggers (replaced by Go template-based notifications) */
DROP TRIGGER IF EXISTS bambu_print_completed;
DROP TRIGGER IF EXISTS bambu_print_failed;
`

var discordHandleRegex = regexp.MustCompile(`@([a-zA-Z0-9_.]+)`)

type PrinterStatus struct {
	bambu.PrinterData

	PrinterName          string `json:"printer_name"`
	SerialNumber         string `json:"serial_number"`
	JobFinishedTimestamp *int64 `json:"job_finished_timestamp"`
	ErrorCode            string `json:"error_code"`
	StopRequested        bool   `json:"stop_requested"`
}

func (p PrinterStatus) OwnerDiscordHandle() string {
	match := discordHandleRegex.FindStringSubmatch(p.SubtaskName)
	if len(match) >= 2 {
		return match[1]
	}
	return ""
}

type Module struct {
	db           *sql.DB
	eventLogger  *engine.EventLogger
	configLoader *config.Loader[Config]

	printers     []*bambu.Printer
	configs      []PrinterConfig
	serialToName map[string]string

	streams map[string]*engine.StreamMux

	// pollInterval is the interval between printer status polls (from config or default)
	pollInterval time.Duration

	// configVersion tracks the current loaded config version to detect changes
	configVersion int
}

func New(db *sql.DB, eventLogger *engine.EventLogger) *Module {
	if db != nil {
		engine.MustMigrate(db, migration)
		// Add stop_requested column if it doesn't exist (for existing databases)
		db.Exec(`ALTER TABLE bambu_printer_state ADD COLUMN stop_requested INTEGER NOT NULL DEFAULT 0`)
	}

	m := &Module{
		db:           db,
		eventLogger:  eventLogger,
		serialToName: map[string]string{},
		streams:      map[string]*engine.StreamMux{},
		pollInterval: time.Second * 5, // default
	}

	return m
}

// SetConfigLoader sets the typed config loader and performs the initial config load.
func (m *Module) SetConfigLoader(store *config.Store) {
	m.configLoader = config.NewLoader[Config](store, "bambu")
	m.reloadConfig(context.Background())
}

// savePrinterState upserts a printer's state to the database.
func (m *Module) savePrinterState(ctx context.Context, status PrinterStatus) error {
	if m.db == nil {
		return nil
	}

	_, err := m.db.ExecContext(ctx, `
		INSERT INTO bambu_printer_state (
			serial_number, printer_name, gcode_file, subtask_name, gcode_state,
			error_code, remaining_print_time, print_percent_done, job_finished_timestamp, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, strftime('%s', 'now'))
		ON CONFLICT (serial_number) DO UPDATE SET
			printer_name = excluded.printer_name,
			gcode_file = excluded.gcode_file,
			subtask_name = excluded.subtask_name,
			gcode_state = excluded.gcode_state,
			error_code = excluded.error_code,
			remaining_print_time = excluded.remaining_print_time,
			print_percent_done = excluded.print_percent_done,
			job_finished_timestamp = excluded.job_finished_timestamp,
			updated_at = strftime('%s', 'now')`,
		status.SerialNumber, status.PrinterName, status.GcodeFile, status.SubtaskName, status.GcodeState,
		status.ErrorCode, status.RemainingPrintTime, status.PrintPercentDone, status.JobFinishedTimestamp)
	return err
}

// loadPrinterStates loads all non-stale printer states from the database.
// States older than 3x the poll interval are considered stale and excluded.
func (m *Module) loadPrinterStates(ctx context.Context) ([]PrinterStatus, error) {
	if m.db == nil {
		return nil, nil
	}
	ttlSeconds := int64(m.pollInterval.Seconds()) * 3
	rows, err := m.db.QueryContext(ctx, `
		SELECT serial_number, printer_name, gcode_file, subtask_name, gcode_state,
		       error_code, remaining_print_time, print_percent_done, job_finished_timestamp, stop_requested
		FROM bambu_printer_state
		WHERE updated_at > unixepoch() - $1
		ORDER BY printer_name`, ttlSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []PrinterStatus
	for rows.Next() {
		var s PrinterStatus
		err := rows.Scan(
			&s.SerialNumber, &s.PrinterName, &s.GcodeFile, &s.SubtaskName, &s.GcodeState,
			&s.ErrorCode, &s.RemainingPrintTime, &s.PrintPercentDone, &s.JobFinishedTimestamp, &s.StopRequested)
		if err != nil {
			return nil, err
		}
		states = append(states, s)
	}
	return states, rows.Err()
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /machines", router.WithAuthn(m.renderView))
	router.HandleFunc("GET /machines/stream/{serial}", router.WithAuthn(m.serveMJPEGStream))
	router.HandleFunc("POST /machines/{serial}/stop", router.WithAuthn(m.stopPrint))
}

func (m *Module) stopPrint(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")

	// Set stop_requested flag in database
	result, err := m.db.ExecContext(r.Context(),
		`UPDATE bambu_printer_state SET stop_requested = 1 WHERE serial_number = $1`,
		serial)
	if err != nil {
		slog.Error("failed to set stop_requested", "error", err, "serial", serial)
		engine.ClientError(w, "Stop Failed", "Failed to request stop", http.StatusInternalServerError)
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		engine.ClientError(w, "Not Found", "Printer not found", http.StatusNotFound)
		return
	}

	slog.Info("stop requested", "serial", serial, "printer", m.serialToName[serial])

	// Redirect back to machines page
	http.Redirect(w, r, "/machines", http.StatusSeeOther)
}

func (m *Module) renderView(w http.ResponseWriter, r *http.Request) {
	states, err := m.loadPrinterStates(r.Context())
	if err != nil {
		slog.Error("failed to load printer states", "error", err)
		states = []PrinterStatus{}
	}
	w.Header().Set("Content-Type", "text/html")
	renderMachines(states).Render(r.Context(), w)
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
	procs.Add(engine.DynamicPoll(func() time.Duration { return m.pollInterval }, m.poll))
	// Cleanup old printer states hourly (24 hour TTL for printers removed from config)
	procs.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "old printer states",
		"DELETE FROM bambu_printer_state WHERE updated_at < unixepoch() - 86400")))
}

func (m *Module) poll(ctx context.Context) bool {
	// Check if config has changed and reload if needed
	if m.configChanged(ctx) {
		slog.Info("Bambu config changed, reloading")
		m.reloadConfig(ctx)
	}

	if len(m.printers) == 0 {
		return false
	}

	start := time.Now()

	for _, printer := range m.printers {
		name := m.serialToName[printer.GetSerial()]
		serial := printer.GetSerial()

		// Check if stop is requested and execute it
		if m.isStopRequested(ctx, serial) {
			if err := printer.StopPrint(); err != nil {
				slog.Error("failed to stop print", "error", err, "printer", name)
				m.eventLogger.LogEvent(ctx, 0, "StopError", serial, name, false, err.Error())
			} else {
				slog.Info("print stopped", "printer", name)
				m.eventLogger.LogEvent(ctx, 0, "Stop", serial, name, true, "")
			}
			// Clear the stop_requested flag regardless of success
			m.clearStopRequest(ctx, serial)
		}

		data, err := printer.GetState()
		if err != nil {
			slog.Warn("unable to get status from Bambu printer", "error", err, "printer", name)
			m.eventLogger.LogEvent(ctx, 0, "PollError", serial, name, false, err.Error())
			continue
		}

		m.eventLogger.LogEvent(ctx, 0, "Poll", serial, name, true, fmt.Sprintf("state=%s", data.GcodeState))

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

		// Save state to DB - triggers will handle notifications on state transitions
		if err := m.savePrinterState(ctx, s); err != nil {
			slog.Error("failed to save printer state", "error", err, "printer", name)
		}
	}

	slog.Info("finished getting Bambu printer status", "seconds", time.Since(start).Seconds())
	return false
}

// isStopRequested checks if a stop has been requested for the given printer.
func (m *Module) isStopRequested(ctx context.Context, serial string) bool {
	if m.db == nil {
		return false
	}
	var requested bool
	m.db.QueryRowContext(ctx,
		`SELECT stop_requested FROM bambu_printer_state WHERE serial_number = $1`,
		serial).Scan(&requested)
	return requested
}

// clearStopRequest clears the stop_requested flag for the given printer.
func (m *Module) clearStopRequest(ctx context.Context, serial string) {
	if m.db == nil {
		return
	}
	m.db.ExecContext(ctx,
		`UPDATE bambu_printer_state SET stop_requested = 0 WHERE serial_number = $1`,
		serial)
}

// configChanged checks if the database config version differs from the loaded version.
func (m *Module) configChanged(ctx context.Context) bool {
	if m.configLoader == nil {
		return false
	}
	_, version, err := m.configLoader.LoadWithVersion(ctx)
	if err != nil {
		return false
	}
	return version != m.configVersion
}

// reloadConfig loads the configuration and rebuilds printer connections.
func (m *Module) reloadConfig(ctx context.Context) {
	if m.configLoader == nil {
		return
	}

	cfg, version, err := m.configLoader.LoadWithVersion(ctx)
	if err != nil {
		slog.Error("failed to load Bambu config", "error", err)
		return
	}

	m.configVersion = version

	// Apply poll interval
	if cfg.PollIntervalSeconds >= 1 {
		m.pollInterval = time.Duration(cfg.PollIntervalSeconds) * time.Second
	}

	// Disconnect old printer MQTT connections before rebuilding
	for _, printer := range m.printers {
		printer.Disconnect()
	}

	// Build set of new serial numbers to detect removed printers
	newSerials := make(map[string]struct{}, len(cfg.Printers))
	for _, p := range cfg.Printers {
		newSerials[p.SerialNumber] = struct{}{}
	}

	// Remove StreamMux instances for printers no longer in config
	for serial, mux := range m.streams {
		if _, exists := newSerials[serial]; !exists {
			mux.Stop()
			delete(m.streams, serial)
		}
	}

	// Rebuild printer connections
	m.configs = cfg.Printers
	m.serialToName = make(map[string]string)
	m.printers = nil

	for _, p := range cfg.Printers {
		m.serialToName[p.SerialNumber] = p.Name
		printer := bambu.NewPrinter(&bambu.PrinterConfig{
			Host:         p.Host,
			AccessCode:   p.AccessCode,
			SerialNumber: p.SerialNumber,
		})
		m.printers = append(m.printers, printer)
		// Only create stream mux if it doesn't exist
		if _, exists := m.streams[p.SerialNumber]; !exists {
			m.streams[p.SerialNumber] = engine.NewStreamMux(func(ctx context.Context) (io.ReadCloser, error) {
				return printer.CameraStream(ctx)
			})
		}
	}

	slog.Info("loaded Bambu config", "printers", len(cfg.Printers), "pollInterval", m.pollInterval)
}

// GetConfiguredPrinterCount returns the number of configured printers.
func (m *Module) GetConfiguredPrinterCount() int {
	return len(m.configs)
}
