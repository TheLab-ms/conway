package bambu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/peering"
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
	cache        *cache
}

func New(client *peering.Client, config string) *Module {
	m := &Module{
		client:       client,
		serialToName: make(map[string]string),
		cache: &cache{
			state: make(map[string]*peering.Event),
		},
	}
	if len(config) == 0 {
		return m
	}
	client.RegisterEventHook(m.cache.Flush)

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
		if s.ErrorCode == "0" {
			s.ErrorCode = ""
		}
		if data.RemainingPrintTime <= 0 {
			s.JobFinishedTimestamp = nil
		} else {
			// Calculate the finished timestamp based on the remaining time
			finishedTimestamp := time.Now().Add(time.Duration(data.RemainingPrintTime) * time.Minute).Unix()
			s.JobFinishedTimestamp = &finishedTimestamp
		}
		m.cache.Add(s)
	}

	return false
}
