package bambu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
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

	lock      sync.Mutex
	state     map[string]*peering.PrinterEvent
	lastFlush time.Time
}

func New(client *peering.Client, config string) *Module {
	m := &Module{
		client:       client,
		serialToName: make(map[string]string),
		state:        make(map[string]*peering.PrinterEvent),
	}
	if len(config) == 0 {
		return m
	}
	client.RegisterEventHook(m.buildEvents)

	// Decode the printer configuration from JSON
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

		s := &peering.PrinterEvent{
			PrinterName: name,
			ErrorCode:   data.PrintErrorCode,
		}
		if data.RemainingPrintTime > 0 {
			t := int64(data.RemainingPrintTime)
			s.JobRemainingMinutes = &t
		}

		m.lock.Lock()
		m.state[s.PrinterName] = s
		m.lock.Unlock()
	}

	return false
}

func (m *Module) buildEvents() []*peering.Event {
	m.lock.Lock()
	defer m.lock.Unlock()

	if time.Since(m.lastFlush) < time.Second*10 {
		return nil // only send the events every 10 seconds
	}

	events := []*peering.Event{}
	for _, s := range m.state {
		events = append(events, &peering.Event{
			UID:          uuid.NewString(),
			Timestamp:    time.Now().Unix(),
			PrinterEvent: s,
		})
	}

	m.lastFlush = time.Now()
	return events
}
