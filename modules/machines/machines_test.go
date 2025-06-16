package machines

import (
	"testing"
	"time"

	snaptest "github.com/TheLab-ms/conway/internal/testing"
)

func TestRenderMachines(t *testing.T) {
	// Create test time values
	now := time.Now()
	future := now.Add(2 * time.Hour)
	past := now.Add(-1 * time.Hour)

	minutesUntil := func(t time.Time) *int {
		m := int(t.Sub(now).Minutes())
		return &m
	}

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
					Name:                "Printer1",
					JobRemainingMinutes: nil,
					ErrorCode:           "",
				},
				{
					Name:                "Printer2",
					JobRemainingMinutes: nil,
					ErrorCode:           "E001",
				},
			},
			fixtureName: "_available",
			description: "Available printers, some with error codes",
		},
		{
			name: "busy_printers",
			printers: []*printerStatus{
				{
					Name:                "BusyPrinter1",
					JobRemainingMinutes: minutesUntil(future),
					ErrorCode:           "",
				},
				{
					Name:                "BusyPrinter2",
					JobRemainingMinutes: minutesUntil(future.Add(30 * time.Minute)),
					ErrorCode:           "W001",
				},
			},
			fixtureName: "_busy",
			description: "Printers currently printing jobs",
		},
		{
			name: "mixed_status",
			printers: []*printerStatus{
				{
					Name:                "AvailablePrinter",
					JobRemainingMinutes: nil,
					ErrorCode:           "",
				},
				{
					Name:                "BusyPrinter",
					JobRemainingMinutes: minutesUntil(future),
					ErrorCode:           "",
				},
				{
					Name:                "ErrorPrinter",
					JobRemainingMinutes: nil,
					ErrorCode:           "E502",
				},
				{
					Name:                "BusyErrorPrinter",
					JobRemainingMinutes: minutesUntil(future.Add(1 * time.Hour)),
					ErrorCode:           "W100",
				},
			},
			fixtureName: "_mixed",
			description: "Mixed printer statuses - available, busy, with/without errors",
		},
		{
			name: "overdue_job",
			printers: []*printerStatus{
				{
					Name:                "OverduePrinter",
					JobRemainingMinutes: minutesUntil(past),
					ErrorCode:           "",
				},
			},
			fixtureName: "_overdue",
			description: "Printer with overdue job completion time",
		},
		{
			name: "single_printer",
			printers: []*printerStatus{
				{
					Name:                "SinglePrinter",
					JobRemainingMinutes: minutesUntil(future.Add(15 * time.Minute)),
					ErrorCode:           "I001",
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
