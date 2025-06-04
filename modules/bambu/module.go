package bambu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/peering"
	"github.com/google/uuid"
	"github.com/torbenconto/bambulabs_api"
)

type printerConfig struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	AccessCode   string `json:"access_code"`
	SerialNumber string `json:"serial_number"`
}

type Module struct {
	printers     []*bambulabs_api.Printer
	client       *peering.Client
	serialToName map[string]string
	state        map[string]*PrinterState
}

func New(client *peering.Client, config string) *Module {
	m := &Module{
		client:       client,
		serialToName: make(map[string]string),
		state:        make(map[string]*PrinterState),
	}
	if len(config) == 0 {
		return m
	}

	configs := []*printerConfig{}
	err := json.Unmarshal([]byte(config), &configs)
	if err != nil {
		panic(fmt.Sprintf("failed to parse Bambu printer config: %v", err))
	}

	for _, cfg := range configs {
		m.serialToName[cfg.SerialNumber] = cfg.Name
		m.printers = append(m.printers, bambulabs_api.NewPrinter(&bambulabs_api.PrinterConfig{
			Host:         cfg.Host,
			AccessCode:   cfg.AccessCode,
			SerialNumber: cfg.SerialNumber,
		}))
	}

	return m
}

func (m *Module) AttachWorkers(procs *engine.ProcMgr) {
	procs.Add(engine.Poll(time.Second*10, m.poll))
}

func (m *Module) poll(ctx context.Context) bool {
	for _, printer := range m.printers {
		name := m.serialToName[printer.GetSerial()]

		if err := printer.Connect(); err != nil {
			slog.Error("error while connecting to Bambu printer", "error", err, "printer", name)
			continue
		}

		data, err := printer.Data()
		if err != nil {
			slog.Error("error while getting data from Bambu printer", "error", err, "printer", name)
			continue
		}

		s := &PrinterState{
			Name:      name,
			ErrorCode: data.PrintErrorCode,
		}
		if minutes := data.RemainingPrintTime; minutes > 0 {
			t := time.Now().Add(time.Duration(minutes) * time.Minute)
			s.JobFinisedAt = &t
		} else {
			// Job is completed or not running, ensure JobFinisedAt is nil
			s.JobFinisedAt = nil
		}
		current := m.state[name]
		m.state[name] = s

		if s.Equal(current) {
			continue // no change
		}

		slog.Info("printer state changed", "printer", s.Name, "error_code", s.ErrorCode, "job_finished_at", s.JobFinisedAt)
		m.client.BufferEvent(&peering.Event{
			UID:       uuid.NewString(),
			Timestamp: time.Now().Unix(),
			PrinterEvent: &peering.PrinterEvent{
				PrinterName:  s.Name,
				JobFinisedAt: s.JobFinisedAt,
				ErrorCode:    s.ErrorCode,
			},
		})
	}

	return false
}

type PrinterState struct {
	Name         string
	JobFinisedAt *time.Time
	ErrorCode    string
}

func (p *PrinterState) Equal(other *PrinterState) bool {
	if (p == nil) != (other == nil) {
		return false
	}

	// Finish times are set, return false if they aren't within 5 min of each other
	if p.JobFinisedAt != nil &&
		other.JobFinisedAt != nil &&
		!p.JobFinisedAt.Round(time.Minute*5).Equal(other.JobFinisedAt.Round(time.Minute*5)) {
		return false
	}

	return p.Name == other.Name &&
		other.ErrorCode == p.ErrorCode &&
		(p.JobFinisedAt == nil) == (other.JobFinisedAt == nil)
}
