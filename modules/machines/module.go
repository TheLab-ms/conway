package machines

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/torbenconto/bambulabs_api"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

type printerConfig struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	AccessCode   string `json:"access_code"`
	SerialNumber string `json:"serial_number"`
}

type printerStatus struct {
	PrinterName          string `json:"printer_name"`
	JobFinishedTimestamp *int64 `json:"job_finished_timestamp"`
	ErrorCode            string `json:"error_code"`
}

type Module struct {
	state atomic.Pointer[[]printerStatus]

	printers     []*bambulabs_api.Printer
	serialToName map[string]string
}

func New(config string) *Module {
	configs := []*printerConfig{}
	err := json.Unmarshal([]byte(config), &configs)
	if err != nil {
		panic(fmt.Sprintf("failed to parse Bambu printer config: %v", err))
	}

	m := &Module{serialToName: map[string]string{}}
	for _, cfg := range configs {
		m.serialToName[cfg.SerialNumber] = cfg.Name
		m.printers = append(m.printers, bambulabs_api.NewPrinter(&bambulabs_api.PrinterConfig{
			Host:         cfg.Host,
			AccessCode:   cfg.AccessCode,
			SerialNumber: cfg.SerialNumber,
		}))
	}
	zero := []printerStatus{}
	m.state.Store(&zero)
	return m
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /machines", router.WithAuthn(m.renderView))
}

func (m *Module) renderView(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	renderMachines(*m.state.Load()).Render(r.Context(), w)
}

func (m *Module) AttachWorkers(procs *engine.ProcMgr) {
	procs.Add(engine.Poll(time.Second*30, m.poll))
}

func (m *Module) poll(ctx context.Context) bool {
	slog.Info("starting to get Bambu printer status")
	start := time.Now()

	var state []printerStatus
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

		s := printerStatus{
			PrinterName: name,
			ErrorCode:   data.PrintErrorCode,
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

		err = printer.Disconnect()
		if err != nil {
			slog.Warn("unable to disconnect from Bambu printer", "error", err, "printer", name)
			continue
		}
	}
	m.state.Store(&state)

	slog.Info("finished getting Bambu printer status", "seconds", time.Since(start).Seconds())
	return false
}
