package machines

import (
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine/testutil"
)

func TestRenderMachines(t *testing.T) {
	// Create test time values
	now := time.Now()
	future := now.Add(2 * time.Hour)
	past := now.Add(-1 * time.Hour)

	timestampAt := func(t time.Time) *int64 {
		ts := t.Unix()
		return &ts
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
					Name:                 "Printer1",
					JobFinishedTimestamp: nil,
					ErrorCode:            "",
				},
				{
					Name:                 "Printer2",
					JobFinishedTimestamp: nil,
					ErrorCode:            "E001",
				},
			},
			fixtureName: "_available",
			description: "Available printers, some with error codes",
		},
		{
			name: "busy_printers",
			printers: []*printerStatus{
				{
					Name:                 "BusyPrinter1",
					JobFinishedTimestamp: timestampAt(future),
					ErrorCode:            "",
				},
				{
					Name:                 "BusyPrinter2",
					JobFinishedTimestamp: timestampAt(future.Add(30 * time.Minute)),
					ErrorCode:            "W001",
				},
			},
			fixtureName: "_busy",
			description: "Printers currently printing jobs",
		},
		{
			name: "mixed_status",
			printers: []*printerStatus{
				{
					Name:                 "AvailablePrinter",
					JobFinishedTimestamp: nil,
					ErrorCode:            "",
				},
				{
					Name:                 "BusyPrinter",
					JobFinishedTimestamp: timestampAt(future),
					ErrorCode:            "",
				},
				{
					Name:                 "ErrorPrinter",
					JobFinishedTimestamp: nil,
					ErrorCode:            "E502",
				},
				{
					Name:                 "BusyErrorPrinter",
					JobFinishedTimestamp: timestampAt(future.Add(1 * time.Hour)),
					ErrorCode:            "W100",
				},
			},
			fixtureName: "_mixed",
			description: "Mixed printer statuses - available, busy, with/without errors",
		},
		{
			name: "overdue_job",
			printers: []*printerStatus{
				{
					Name:                 "OverduePrinter",
					JobFinishedTimestamp: timestampAt(past),
					ErrorCode:            "",
				},
			},
			fixtureName: "_overdue",
			description: "Printer with overdue job completion time",
		},
		{
			name: "single_printer",
			printers: []*printerStatus{
				{
					Name:                 "SinglePrinter",
					JobFinishedTimestamp: timestampAt(future.Add(15 * time.Minute)),
					ErrorCode:            "I001",
				},
			},
			fixtureName: "_single",
			description: "Single printer with job and info code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderMachines(tt.printers)
			testutil.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}
