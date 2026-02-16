package machines

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/machines/bambu"
)

// createTestDB creates a test database with all required tables
func createTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := engine.OpenTestDB(t)

	// Create members table (minimal version needed for tests)
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS members (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
		email TEXT NOT NULL DEFAULT '',
		discord_username TEXT,
		discord_user_id TEXT
	) STRICT`)
	if err != nil {
		t.Fatalf("failed to create members table: %v", err)
	}

	// Apply machines module migration (creates bambu_printer_state table)
	engine.MustMigrate(db, migration)

	return db
}

func TestSaveAndLoadPrinterState(t *testing.T) {
	db := createTestDB(t)
	m := &Module{
		db:           db,
		pollInterval: time.Second * 5,
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Save a printer state
	status := PrinterStatus{
		PrinterData: bambu.PrinterData{
			GcodeFile:          "benchy.gcode",
			SubtaskName:        "@jordan",
			GcodeState:         "RUNNING",
			RemainingPrintTime: 60,
			PrintPercentDone:   50,
		},
		SerialNumber:         "ABC123",
		PrinterName:          "Printer1",
		JobFinishedTimestamp: &finishTime,
		ErrorCode:            "",
	}

	err := m.savePrinterState(ctx, status)
	if err != nil {
		t.Fatalf("savePrinterState failed: %v", err)
	}

	// Load printer states
	states, err := m.loadPrinterStates(ctx)
	if err != nil {
		t.Fatalf("loadPrinterStates failed: %v", err)
	}

	if len(states) != 1 {
		t.Fatalf("expected 1 state, got %d", len(states))
	}

	s := states[0]
	if s.SerialNumber != "ABC123" {
		t.Errorf("expected SerialNumber 'ABC123', got %q", s.SerialNumber)
	}
	if s.PrinterName != "Printer1" {
		t.Errorf("expected PrinterName 'Printer1', got %q", s.PrinterName)
	}
	if s.GcodeFile != "benchy.gcode" {
		t.Errorf("expected GcodeFile 'benchy.gcode', got %q", s.GcodeFile)
	}
	if s.SubtaskName != "@jordan" {
		t.Errorf("expected SubtaskName '@jordan', got %q", s.SubtaskName)
	}
	if s.JobFinishedTimestamp == nil || *s.JobFinishedTimestamp != finishTime {
		t.Errorf("expected JobFinishedTimestamp %d, got %v", finishTime, s.JobFinishedTimestamp)
	}
}

func TestOwnerDiscordHandle(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"@jordan", "jordan"},
		{"Print job for @jordan", "jordan"},
		{"@user_123", "user_123"},
		{"@user.name", "user.name"},
		{"no handle here", ""},
		{"", ""},
		{"just text without at sign", ""},
		{"email@example.com", "example.com"}, // @ in email will match (acceptable edge case)
		{"@first @second", "first"},          // returns first match
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			p := PrinterStatus{PrinterData: bambu.PrinterData{SubtaskName: tc.input}}
			result := p.OwnerDiscordHandle()
			if result != tc.expected {
				t.Errorf("OwnerDiscordHandle() with SubtaskName=%q = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

func TestStopRequested(t *testing.T) {
	db := createTestDB(t)
	m := &Module{
		db:           db,
		pollInterval: time.Second * 5,
	}

	ctx := context.Background()

	// Insert a printer state
	_, err := db.Exec(`INSERT INTO bambu_printer_state
		(serial_number, printer_name, gcode_file, subtask_name, gcode_state,
		 error_code, remaining_print_time, print_percent_done, job_finished_timestamp, stop_requested, updated_at)
		VALUES ('ABC123', 'Printer1', 'benchy.gcode', '@jordan', 'RUNNING', '', 60, 50, NULL, 0, strftime('%s', 'now'))`)
	if err != nil {
		t.Fatalf("failed to insert initial state: %v", err)
	}

	// Verify stop is not requested initially
	if m.isStopRequested(ctx, "ABC123") {
		t.Error("stop should not be requested initially")
	}

	// Set stop_requested flag
	_, err = db.Exec(`UPDATE bambu_printer_state SET stop_requested = 1 WHERE serial_number = 'ABC123'`)
	if err != nil {
		t.Fatalf("failed to set stop_requested: %v", err)
	}

	// Verify stop is now requested
	if !m.isStopRequested(ctx, "ABC123") {
		t.Error("stop should be requested after update")
	}

	// Clear the stop request
	m.clearStopRequest(ctx, "ABC123")

	// Verify stop is no longer requested
	if m.isStopRequested(ctx, "ABC123") {
		t.Error("stop should not be requested after clear")
	}

	// Verify state includes stop_requested field
	states, err := m.loadPrinterStates(ctx)
	if err != nil {
		t.Fatalf("loadPrinterStates failed: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("expected 1 state, got %d", len(states))
	}
	if states[0].StopRequested {
		t.Error("StopRequested should be false after clear")
	}
}
