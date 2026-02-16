package machines

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/machines/bambu"
)

// mockPrintNotifier records calls to NotifyPrintCompleted and NotifyPrintFailed.
type mockPrintNotifier struct {
	mu        sync.Mutex
	completed []printCompletedCall
	failed    []printFailedCall
}

type printCompletedCall struct {
	discordUserID string
	printerName   string
	fileName      string
}

type printFailedCall struct {
	discordUserID string
	printerName   string
	fileName      string
	errorCode     string
}

func (m *mockPrintNotifier) NotifyPrintCompleted(ctx context.Context, discordUserID, printerName, fileName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completed = append(m.completed, printCompletedCall{discordUserID, printerName, fileName})
}

func (m *mockPrintNotifier) NotifyPrintFailed(ctx context.Context, discordUserID, printerName, fileName, errorCode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed = append(m.failed, printFailedCall{discordUserID, printerName, fileName, errorCode})
}

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

// setupTestDBWithMember sets up the test DB with a test member for discord user lookup
func setupTestDBWithMember(t *testing.T, discordUsername, discordUserID string) *sql.DB {
	t.Helper()
	db := createTestDB(t)

	if discordUsername != "" {
		_, err := db.Exec(`INSERT INTO members (email, discord_username, discord_user_id) VALUES (?, ?, ?)`,
			discordUsername+"@example.com", discordUsername, discordUserID)
		if err != nil {
			t.Fatalf("failed to insert test member: %v", err)
		}
	}

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

func TestNotify_JobCompleted(t *testing.T) {
	db := setupTestDBWithMember(t, "jordan", "123456789")
	notifier := &mockPrintNotifier{}
	m := &Module{
		db:            db,
		pollInterval:  time.Second * 5,
		printNotifier: notifier,
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Insert initial state with a running job
	_, err := db.Exec(`INSERT INTO bambu_printer_state
		(serial_number, printer_name, gcode_file, subtask_name, gcode_state, error_code, remaining_print_time, print_percent_done, job_finished_timestamp, updated_at)
		VALUES ('ABC123', 'Printer1', 'benchy.gcode', '@jordan', 'RUNNING', '', 60, 50, ?, strftime('%s', 'now'))`, finishTime)
	if err != nil {
		t.Fatalf("failed to insert initial state: %v", err)
	}

	// Update to completed (job_finished_timestamp becomes NULL, no error)
	status := PrinterStatus{
		PrinterData: bambu.PrinterData{
			GcodeFile:   "benchy.gcode",
			SubtaskName: "@jordan",
			GcodeState:  "FINISH",
		},
		SerialNumber:         "ABC123",
		PrinterName:          "Printer1",
		JobFinishedTimestamp: nil, // Job finished
		ErrorCode:            "",
	}

	err = m.savePrinterState(ctx, status)
	if err != nil {
		t.Fatalf("savePrinterState failed: %v", err)
	}

	// Check that the notifier was called with correct args
	if len(notifier.completed) != 1 {
		t.Fatalf("expected 1 completed notification, got %d", len(notifier.completed))
	}
	if notifier.completed[0].discordUserID != "123456789" {
		t.Errorf("expected discordUserID '123456789', got %q", notifier.completed[0].discordUserID)
	}
	if notifier.completed[0].printerName != "Printer1" {
		t.Errorf("expected printerName 'Printer1', got %q", notifier.completed[0].printerName)
	}
	if notifier.completed[0].fileName != "benchy.gcode" {
		t.Errorf("expected fileName 'benchy.gcode', got %q", notifier.completed[0].fileName)
	}
}

func TestNotify_JobFailed(t *testing.T) {
	db := setupTestDBWithMember(t, "testuser", "987654321")
	notifier := &mockPrintNotifier{}
	m := &Module{
		db:            db,
		pollInterval:  time.Second * 5,
		printNotifier: notifier,
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Insert initial state with a running job (no error)
	_, err := db.Exec(`INSERT INTO bambu_printer_state
		(serial_number, printer_name, gcode_file, subtask_name, gcode_state, error_code, remaining_print_time, print_percent_done, job_finished_timestamp, updated_at)
		VALUES ('ABC123', 'Printer1', 'benchy.gcode', '@testuser', 'RUNNING', '', 60, 50, ?, strftime('%s', 'now'))`, finishTime)
	if err != nil {
		t.Fatalf("failed to insert initial state: %v", err)
	}

	// Update with an error
	status := PrinterStatus{
		PrinterData: bambu.PrinterData{
			GcodeFile:   "benchy.gcode",
			SubtaskName: "@testuser",
			GcodeState:  "FAILED",
		},
		SerialNumber:         "ABC123",
		PrinterName:          "Printer1",
		JobFinishedTimestamp: &finishTime,
		ErrorCode:            "E001",
	}

	err = m.savePrinterState(ctx, status)
	if err != nil {
		t.Fatalf("savePrinterState failed: %v", err)
	}

	// Check that the notifier was called with correct args
	if len(notifier.failed) != 1 {
		t.Fatalf("expected 1 failure notification, got %d", len(notifier.failed))
	}
	if notifier.failed[0].discordUserID != "987654321" {
		t.Errorf("expected discordUserID '987654321', got %q", notifier.failed[0].discordUserID)
	}
	if notifier.failed[0].printerName != "Printer1" {
		t.Errorf("expected printerName 'Printer1', got %q", notifier.failed[0].printerName)
	}
	if notifier.failed[0].errorCode != "E001" {
		t.Errorf("expected errorCode 'E001', got %q", notifier.failed[0].errorCode)
	}
}

func TestNotify_NoNotificationWithoutNotifier(t *testing.T) {
	db := setupTestDBWithMember(t, "testuser", "555555555")
	m := &Module{
		db:           db,
		pollInterval: time.Second * 5,
		// printNotifier is nil — no notifications should be sent
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Insert initial state with a running job
	_, err := db.Exec(`INSERT INTO bambu_printer_state
		(serial_number, printer_name, gcode_file, subtask_name, gcode_state, error_code, remaining_print_time, print_percent_done, job_finished_timestamp, updated_at)
		VALUES ('ABC123', 'Printer1', 'benchy.gcode', '@testuser', 'RUNNING', '', 60, 50, ?, strftime('%s', 'now'))`, finishTime)
	if err != nil {
		t.Fatalf("failed to insert initial state: %v", err)
	}

	// Complete the job
	status := PrinterStatus{
		PrinterData:          bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@testuser", GcodeState: "FINISH"},
		SerialNumber:         "ABC123",
		PrinterName:          "Printer1",
		JobFinishedTimestamp: nil,
		ErrorCode:            "",
	}

	// Should not panic or error even without a notifier
	err = m.savePrinterState(ctx, status)
	if err != nil {
		t.Fatalf("savePrinterState failed: %v", err)
	}
}

func TestNotify_NoCompletionForUnknownUser(t *testing.T) {
	db := setupTestDBWithMember(t, "", "") // No user set up
	notifier := &mockPrintNotifier{}
	m := &Module{
		db:            db,
		pollInterval:  time.Second * 5,
		printNotifier: notifier,
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Insert initial state with a running job for an unknown user
	_, err := db.Exec(`INSERT INTO bambu_printer_state
		(serial_number, printer_name, gcode_file, subtask_name, gcode_state, error_code, remaining_print_time, print_percent_done, job_finished_timestamp, updated_at)
		VALUES ('ABC123', 'Printer1', 'benchy.gcode', '@unknownuser', 'RUNNING', '', 60, 50, ?, strftime('%s', 'now'))`, finishTime)
	if err != nil {
		t.Fatalf("failed to insert initial state: %v", err)
	}

	// Complete the job
	status := PrinterStatus{
		PrinterData:          bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@unknownuser", GcodeState: "FINISH"},
		SerialNumber:         "ABC123",
		PrinterName:          "Printer1",
		JobFinishedTimestamp: nil,
		ErrorCode:            "",
	}

	err = m.savePrinterState(ctx, status)
	if err != nil {
		t.Fatalf("savePrinterState failed: %v", err)
	}

	// No completion notification for unknown user (discordUserID is empty, so notify is skipped)
	if len(notifier.completed) != 0 {
		t.Errorf("expected 0 completed notifications for unknown user, got %d", len(notifier.completed))
	}
}

func TestNotify_FailureWithoutUser(t *testing.T) {
	db := setupTestDBWithMember(t, "", "") // No user set up
	notifier := &mockPrintNotifier{}
	m := &Module{
		db:            db,
		pollInterval:  time.Second * 5,
		printNotifier: notifier,
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Insert initial state with a running job for an unknown user
	_, err := db.Exec(`INSERT INTO bambu_printer_state
		(serial_number, printer_name, gcode_file, subtask_name, gcode_state, error_code, remaining_print_time, print_percent_done, job_finished_timestamp, updated_at)
		VALUES ('ABC123', 'Printer1', 'benchy.gcode', '@unknownuser', 'RUNNING', '', 60, 50, ?, strftime('%s', 'now'))`, finishTime)
	if err != nil {
		t.Fatalf("failed to insert initial state: %v", err)
	}

	// Fail the job
	status := PrinterStatus{
		PrinterData:          bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@unknownuser"},
		SerialNumber:         "ABC123",
		PrinterName:          "Printer1",
		JobFinishedTimestamp: &finishTime,
		ErrorCode:            "E002",
	}

	err = m.savePrinterState(ctx, status)
	if err != nil {
		t.Fatalf("savePrinterState failed: %v", err)
	}

	// Failure notification should still be sent (with empty discordUserID)
	if len(notifier.failed) != 1 {
		t.Fatalf("expected 1 failure notification even without known user, got %d", len(notifier.failed))
	}
	if notifier.failed[0].discordUserID != "" {
		t.Errorf("expected empty discordUserID for unknown user, got %q", notifier.failed[0].discordUserID)
	}
	if notifier.failed[0].errorCode != "E002" {
		t.Errorf("expected errorCode 'E002', got %q", notifier.failed[0].errorCode)
	}
}

func TestNotify_NoDuplicateNotifications(t *testing.T) {
	db := setupTestDBWithMember(t, "jordan", "123456789")
	notifier := &mockPrintNotifier{}
	m := &Module{
		db:            db,
		pollInterval:  time.Second * 5,
		printNotifier: notifier,
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Insert initial state
	_, err := db.Exec(`INSERT INTO bambu_printer_state
		(serial_number, printer_name, gcode_file, subtask_name, gcode_state, error_code, remaining_print_time, print_percent_done, job_finished_timestamp, updated_at)
		VALUES ('ABC123', 'Printer1', 'benchy.gcode', '@jordan', 'RUNNING', '', 60, 50, ?, strftime('%s', 'now'))`, finishTime)
	if err != nil {
		t.Fatalf("failed to insert initial state: %v", err)
	}

	// Complete the job
	status := PrinterStatus{
		PrinterData:          bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@jordan", GcodeState: "FINISH"},
		SerialNumber:         "ABC123",
		PrinterName:          "Printer1",
		JobFinishedTimestamp: nil,
		ErrorCode:            "",
	}
	m.savePrinterState(ctx, status)

	// Should have 1 notification
	if len(notifier.completed) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifier.completed))
	}

	// Save the same state again (simulating repeated polls)
	m.savePrinterState(ctx, status)
	m.savePrinterState(ctx, status)

	// Should still have only 1 notification (transition only fires once)
	if len(notifier.completed) != 1 {
		t.Errorf("expected still 1 notification after repeated saves, got %d", len(notifier.completed))
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
