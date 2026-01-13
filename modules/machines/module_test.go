package machines

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/machines/bambu"
)

// createTestDB creates a test database with all required tables and triggers
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

	// Apply machines module migration (creates bambu_printer_state table and triggers)
	engine.MustMigrate(db, migration)

	// Create discord_config table (needed by triggers)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS discord_config (
		version INTEGER PRIMARY KEY,
		client_id TEXT NOT NULL DEFAULT '',
		client_secret TEXT NOT NULL DEFAULT '',
		bot_token TEXT NOT NULL DEFAULT '',
		guild_id TEXT NOT NULL DEFAULT '',
		role_id TEXT NOT NULL DEFAULT '',
		print_webhook_url TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("failed to create discord_config table: %v", err)
	}

	// Create discord_webhook_queue table (needed by triggers)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS discord_webhook_queue (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
		send_at INTEGER DEFAULT (strftime('%s', 'now')),
		webhook_url TEXT NOT NULL,
		payload TEXT NOT NULL
	) STRICT`)
	if err != nil {
		t.Fatalf("failed to create discord_webhook_queue table: %v", err)
	}

	return db
}

// setupTestDBWithWebhook sets up the test DB with webhook config and test member
func setupTestDBWithWebhook(t *testing.T, webhookURL, discordUsername, discordUserID string) *sql.DB {
	t.Helper()
	db := createTestDB(t)

	_, err := db.Exec(`INSERT INTO discord_config (version, print_webhook_url) VALUES (1, ?)`, webhookURL)
	if err != nil {
		t.Fatalf("failed to insert discord_config: %v", err)
	}

	if discordUsername != "" {
		_, err = db.Exec(`INSERT INTO members (email, discord_username, discord_user_id) VALUES (?, ?, ?)`,
			discordUsername+"@example.com", discordUsername, discordUserID)
		if err != nil {
			t.Fatalf("failed to insert test member: %v", err)
		}
	}

	return db
}

// getQueuedMessages retrieves all messages from the webhook queue
func getQueuedMessages(t *testing.T, db *sql.DB) []struct{ webhookURL, payload string } {
	t.Helper()
	rows, err := db.Query("SELECT webhook_url, payload FROM discord_webhook_queue ORDER BY id")
	if err != nil {
		t.Fatalf("failed to query webhook queue: %v", err)
	}
	defer rows.Close()

	var messages []struct{ webhookURL, payload string }
	for rows.Next() {
		var msg struct{ webhookURL, payload string }
		if err := rows.Scan(&msg.webhookURL, &msg.payload); err != nil {
			t.Fatalf("failed to scan webhook message: %v", err)
		}
		messages = append(messages, msg)
	}
	return messages
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

func TestTrigger_JobCompleted(t *testing.T) {
	db := setupTestDBWithWebhook(t, "https://discord.com/api/webhooks/test", "jordan", "123456789")
	m := &Module{
		db:           db,
		pollInterval: time.Second * 5,
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

	// Verify no messages queued yet
	messages := getQueuedMessages(t, db)
	if len(messages) != 0 {
		t.Errorf("expected 0 messages before completion, got %d", len(messages))
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

	// Check that a completion notification was queued by the trigger
	messages = getQueuedMessages(t, db)
	if len(messages) != 1 {
		t.Fatalf("expected 1 message after completion, got %d", len(messages))
	}

	if messages[0].webhookURL != "https://discord.com/api/webhooks/test" {
		t.Errorf("expected webhookURL 'https://discord.com/api/webhooks/test', got %q", messages[0].webhookURL)
	}
	if !strings.Contains(messages[0].payload, "completed successfully") {
		t.Errorf("payload should contain 'completed successfully', got: %s", messages[0].payload)
	}
	if !strings.Contains(messages[0].payload, "123456789") {
		t.Errorf("payload should contain Discord user ID '123456789', got: %s", messages[0].payload)
	}
	if !strings.Contains(messages[0].payload, "Printer1") {
		t.Errorf("payload should contain printer name 'Printer1', got: %s", messages[0].payload)
	}
}

func TestTrigger_JobFailed(t *testing.T) {
	db := setupTestDBWithWebhook(t, "https://discord.com/api/webhooks/test", "testuser", "987654321")
	m := &Module{
		db:           db,
		pollInterval: time.Second * 5,
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

	// Check that a failure notification was queued by the trigger
	messages := getQueuedMessages(t, db)
	if len(messages) != 1 {
		t.Fatalf("expected 1 message after failure, got %d", len(messages))
	}

	if !strings.Contains(messages[0].payload, "has failed") {
		t.Errorf("payload should contain 'has failed', got: %s", messages[0].payload)
	}
	if !strings.Contains(messages[0].payload, "E001") {
		t.Errorf("payload should contain error code 'E001', got: %s", messages[0].payload)
	}
	if !strings.Contains(messages[0].payload, "987654321") {
		t.Errorf("payload should contain Discord user ID '987654321', got: %s", messages[0].payload)
	}
}

func TestTrigger_NoNotificationWhenWebhookEmpty(t *testing.T) {
	db := setupTestDBWithWebhook(t, "", "testuser", "555555555") // Empty webhook URL
	m := &Module{
		db:           db,
		pollInterval: time.Second * 5,
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
		PrinterData:          bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@testuser"},
		SerialNumber:         "ABC123",
		PrinterName:          "Printer1",
		JobFinishedTimestamp: nil,
		ErrorCode:            "",
	}

	err = m.savePrinterState(ctx, status)
	if err != nil {
		t.Fatalf("savePrinterState failed: %v", err)
	}

	// No notification should be queued when webhook URL is empty
	messages := getQueuedMessages(t, db)
	if len(messages) != 0 {
		t.Errorf("expected 0 notifications when webhook URL is empty, got %d", len(messages))
	}
}

func TestTrigger_NoNotificationForUnknownUser(t *testing.T) {
	db := setupTestDBWithWebhook(t, "https://discord.com/api/webhooks/test", "", "") // No user set up
	m := &Module{
		db:           db,
		pollInterval: time.Second * 5,
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
		PrinterData:          bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@unknownuser"},
		SerialNumber:         "ABC123",
		PrinterName:          "Printer1",
		JobFinishedTimestamp: nil,
		ErrorCode:            "",
	}

	err = m.savePrinterState(ctx, status)
	if err != nil {
		t.Fatalf("savePrinterState failed: %v", err)
	}

	// No completion notification for unknown user (trigger only inserts if discord_user_id found)
	messages := getQueuedMessages(t, db)
	if len(messages) != 0 {
		t.Errorf("expected 0 notifications for unknown user on completion, got %d", len(messages))
	}
}

func TestTrigger_FailureNotificationWithoutUser(t *testing.T) {
	db := setupTestDBWithWebhook(t, "https://discord.com/api/webhooks/test", "", "") // No user set up
	m := &Module{
		db:           db,
		pollInterval: time.Second * 5,
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

	// Failure notification should still be sent (without user mention)
	messages := getQueuedMessages(t, db)
	if len(messages) != 1 {
		t.Fatalf("expected 1 failure notification even without known user, got %d", len(messages))
	}

	if !strings.Contains(messages[0].payload, "has failed") {
		t.Errorf("payload should contain 'has failed', got: %s", messages[0].payload)
	}
	if !strings.Contains(messages[0].payload, "E002") {
		t.Errorf("payload should contain error code 'E002', got: %s", messages[0].payload)
	}
	// Should NOT contain user mention since user is unknown
	if strings.Contains(messages[0].payload, "<@") {
		t.Errorf("payload should NOT contain user mention for unknown user, got: %s", messages[0].payload)
	}
}

func TestTrigger_NoDuplicateNotifications(t *testing.T) {
	db := setupTestDBWithWebhook(t, "https://discord.com/api/webhooks/test", "jordan", "123456789")
	m := &Module{
		db:           db,
		pollInterval: time.Second * 5,
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
		PrinterData:          bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@jordan"},
		SerialNumber:         "ABC123",
		PrinterName:          "Printer1",
		JobFinishedTimestamp: nil,
		ErrorCode:            "",
	}
	m.savePrinterState(ctx, status)

	// Should have 1 notification
	messages := getQueuedMessages(t, db)
	if len(messages) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(messages))
	}

	// Save the same state again (simulating repeated polls)
	m.savePrinterState(ctx, status)
	m.savePrinterState(ctx, status)

	// Should still have only 1 notification (trigger only fires on transition)
	messages = getQueuedMessages(t, db)
	if len(messages) != 1 {
		t.Errorf("expected still 1 notification after repeated saves, got %d", len(messages))
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

func TestNewForTesting(t *testing.T) {
	db := createTestDB(t)
	finishTime := int64(1234567890)

	printers := []PrinterStatus{
		{
			PrinterData:          bambu.PrinterData{GcodeFile: "test.gcode", SubtaskName: "@user"},
			SerialNumber:         "SN001",
			PrinterName:          "TestPrinter",
			JobFinishedTimestamp: &finishTime,
		},
	}

	m := NewForTesting(db, printers)

	// Verify state was stored in DB
	states, err := m.loadPrinterStates(context.Background())
	if err != nil {
		t.Fatalf("loadPrinterStates failed: %v", err)
	}

	if len(states) != 1 {
		t.Fatalf("expected 1 state, got %d", len(states))
	}

	if states[0].SerialNumber != "SN001" {
		t.Errorf("expected SerialNumber 'SN001', got %q", states[0].SerialNumber)
	}
}
