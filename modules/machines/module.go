package machines

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/settings"
	"github.com/torbenconto/bambulabs_api"
)

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
}

type Module struct {
	settings *settings.Store
	state    atomic.Pointer[[]PrinterStatus]

	mu           sync.Mutex
	printers     []*bambulabs_api.Printer
	configs      []*printerConfig
	serialToName map[string]string
	streams      map[string]*engine.StreamMux

	testMode bool // When true, skip polling and use injected state
}

func New(settingsStore *settings.Store) *Module {
	m := &Module{
		settings:     settingsStore,
		serialToName: map[string]string{},
		streams:      map[string]*engine.StreamMux{},
	}
	zero := []PrinterStatus{}
	m.state.Store(&zero)
	return m
}

// NewForTesting creates a Module with mock printer data for e2e tests.
// The printers slice defines what the UI will render - no real connections are made.
func NewForTesting(printers []PrinterStatus) *Module {
	m := &Module{
		streams:      map[string]*engine.StreamMux{},
		serialToName: map[string]string{},
		testMode:     true,
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

func (m *Module) AttachWorkers(procs *engine.ProcMgr) {
	if m.testMode {
		return // Skip polling in test mode - use injected state
	}

	ctx := context.Background()

	// Watch for bambu.printers changes
	m.settings.Watch(ctx, "bambu.printers", func(configJSON string) {
		m.updatePrinterConfig(configJSON)
	})

	procs.Add(engine.Poll(time.Second*5, m.poll))
}

func (m *Module) updatePrinterConfig(configJSON string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Close existing streams
	for _, stream := range m.streams {
		stream.Close()
	}

	// Clear existing config
	m.printers = nil
	m.configs = nil
	m.serialToName = map[string]string{}
	m.streams = map[string]*engine.StreamMux{}

	if configJSON == "" {
		slog.Info("bambu printers disabled (no configuration)")
		return
	}

	configs := []*printerConfig{}
	if err := json.Unmarshal([]byte(configJSON), &configs); err != nil {
		slog.Error("failed to parse Bambu printer config", "error", err)
		return
	}

	m.configs = configs
	for _, cfg := range configs {
		m.serialToName[cfg.SerialNumber] = cfg.Name
		m.printers = append(m.printers, bambulabs_api.NewPrinter(&bambulabs_api.PrinterConfig{
			Host:         cfg.Host,
			AccessCode:   cfg.AccessCode,
			SerialNumber: cfg.SerialNumber,
		}))
		m.streams[cfg.SerialNumber] = m.newStreamMux(cfg)
	}

	slog.Info("bambu printers configured", "count", len(configs))
}

func (m *Module) stopPrint(w http.ResponseWriter, r *http.Request) {
	serial := r.PathValue("serial")

	m.mu.Lock()
	var printer *bambulabs_api.Printer
	for _, p := range m.printers {
		if p.GetSerial() == serial {
			printer = p
			break
		}
	}
	printerName := m.serialToName[serial]
	m.mu.Unlock()

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

	slog.Info("print stopped successfully", "serial", serial, "printer", printerName)

	// Redirect back to machines page
	http.Redirect(w, r, "/machines", http.StatusSeeOther)
}

func (m *Module) renderView(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	renderMachines(*m.state.Load()).Render(r.Context(), w)
}

func (m *Module) newStreamMux(cfg *printerConfig) *engine.StreamMux {
	rtspURL := fmt.Sprintf("rtsps://bblp:%s@%s:322/streaming/live/1", cfg.AccessCode, cfg.Host)
	printerName := cfg.Name
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

		slog.Info("started camera stream", "printer", printerName)

		// Return a wrapper that waits for cmd on close
		return &cmdReader{ReadCloser: stdout, cmd: cmd, name: printerName}, nil
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

	m.mu.Lock()
	mux, ok := m.streams[serial]
	m.mu.Unlock()

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

func (m *Module) poll(ctx context.Context) bool {
	m.mu.Lock()
	printers := m.printers
	serialToName := m.serialToName
	m.mu.Unlock()

	if len(printers) == 0 {
		return false // Not configured
	}

	slog.Info("starting to get Bambu printer status")
	start := time.Now()

	var state []PrinterStatus
	for _, printer := range printers {
		name := serialToName[printer.GetSerial()]
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

	slog.Info("finished getting Bambu printer status", "seconds", time.Since(start).Seconds())
	return false
}
