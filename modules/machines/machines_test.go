package machines

import (
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	snaptest "github.com/TheLab-ms/conway/internal/testing"
)

func TestRenderMachines(t *testing.T) {
	// Create test time values
	now := time.Now()
	future := now.Add(2 * time.Hour)
	past := now.Add(-1 * time.Hour)

	tests := []struct {
		name        string
		printers    []*printerStatus
		fixtureName string
		description string
	}{
		{
			name:        "empty_printers",
			printers:    []*printerStatus{},
			fixtureName: "_empty",
			description: "No printers available",
		},
		{
			name: "available_printers",
			printers: []*printerStatus{
				{
					Name:          "Printer1",
					JobFinishedAt: nil,
					ErrorCode:     "",
				},
				{
					Name:          "Printer2",
					JobFinishedAt: nil,
					ErrorCode:     "E001",
				},
			},
			fixtureName: "_available",
			description: "Available printers, some with error codes",
		},
		{
			name: "busy_printers",
			printers: []*printerStatus{
				{
					Name:          "BusyPrinter1",
					JobFinishedAt: &engine.LocalTime{Time: future},
					ErrorCode:     "",
				},
				{
					Name:          "BusyPrinter2",
					JobFinishedAt: &engine.LocalTime{Time: future.Add(30 * time.Minute)},
					ErrorCode:     "W001",
				},
			},
			fixtureName: "_busy",
			description: "Printers currently printing jobs",
		},
		{
			name: "mixed_status",
			printers: []*printerStatus{
				{
					Name:          "AvailablePrinter",
					JobFinishedAt: nil,
					ErrorCode:     "",
				},
				{
					Name:          "BusyPrinter",
					JobFinishedAt: &engine.LocalTime{Time: future},
					ErrorCode:     "",
				},
				{
					Name:          "ErrorPrinter",
					JobFinishedAt: nil,
					ErrorCode:     "E502",
				},
				{
					Name:          "BusyErrorPrinter",
					JobFinishedAt: &engine.LocalTime{Time: future.Add(1 * time.Hour)},
					ErrorCode:     "W100",
				},
			},
			fixtureName: "_mixed",
			description: "Mixed printer statuses - available, busy, with/without errors",
		},
		{
			name: "overdue_job",
			printers: []*printerStatus{
				{
					Name:          "OverduePrinter",
					JobFinishedAt: &engine.LocalTime{Time: past},
					ErrorCode:     "",
				},
			},
			fixtureName: "_overdue",
			description: "Printer with overdue job completion time",
		},
		{
			name: "single_printer",
			printers: []*printerStatus{
				{
					Name:          "SinglePrinter",
					JobFinishedAt: &engine.LocalTime{Time: future.Add(15 * time.Minute)},
					ErrorCode:     "I001",
				},
			},
			fixtureName: "_single",
			description: "Single printer with job and info code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderMachines(tt.printers)
			snaptest.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}