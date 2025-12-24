package machines

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine/db"
	_ "modernc.org/sqlite"
)

// discordWebhookMigration is the migration for the discord_webhook_queue table.
// This is needed because the trigger inserts into this table.
const discordWebhookMigration = `
CREATE TABLE IF NOT EXISTS discord_webhook_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    send_at INTEGER DEFAULT (strftime('%s', 'now')),
    channel_id TEXT NOT NULL,
    payload TEXT NOT NULL
) STRICT;
`

func TestTrigger_NotificationQueuedOnCompletion(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	// Apply the discord webhook migration first (trigger depends on this table)
	db.MustMigrate(database, discordWebhookMigration)
	// Apply the machines migration
	db.MustMigrate(database, migration)

	ctx := context.Background()

	// Insert a running job with notification_channel set
	_, err = database.ExecContext(ctx, `
		INSERT INTO print_jobs (printer_serial, printer_name, gcode_file, gcode_file_display, discord_user_id, notification_channel, started_at, status)
		VALUES ('ABC123', 'Printer1', '123456789012345678_benchy.gcode', 'benchy.gcode', '123456789012345678', 'test-channel', $1, 'running')`,
		time.Now().Unix())
	if err != nil {
		t.Fatalf("failed to insert print job: %v", err)
	}

	// Update the job to completed - trigger should fire
	_, err = database.ExecContext(ctx, `UPDATE print_jobs SET status = 'completed', completed_at = $1 WHERE status = 'running'`, time.Now().Unix())
	if err != nil {
		t.Fatalf("failed to update print job: %v", err)
	}

	// Verify notification was queued
	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM discord_webhook_queue").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query queue: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 notification in queue, got %d", count)
	}

	// Verify the notification content
	var channelID, payload string
	err = database.QueryRow("SELECT channel_id, payload FROM discord_webhook_queue").Scan(&channelID, &payload)
	if err != nil {
		t.Fatalf("failed to query notification: %v", err)
	}
	if channelID != "test-channel" {
		t.Errorf("expected channel_id 'test-channel', got %q", channelID)
	}
	// Check that the payload contains expected content
	if !contains(payload, "benchy.gcode") {
		t.Errorf("payload should contain filename, got: %s", payload)
	}
	if !contains(payload, "completed successfully") {
		t.Errorf("payload should contain 'completed successfully', got: %s", payload)
	}
	if !contains(payload, "<@123456789012345678>") {
		t.Errorf("payload should contain Discord mention, got: %s", payload)
	}

	// Verify notification_sent was set
	var notificationSent int
	err = database.QueryRow("SELECT notification_sent FROM print_jobs").Scan(&notificationSent)
	if err != nil {
		t.Fatalf("failed to query notification_sent: %v", err)
	}
	if notificationSent != 1 {
		t.Errorf("expected notification_sent = 1, got %d", notificationSent)
	}
}

func TestTrigger_NoNotificationWhenChannelNull(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	db.MustMigrate(database, discordWebhookMigration)
	db.MustMigrate(database, migration)

	ctx := context.Background()

	// Insert a running job WITHOUT notification_channel
	_, err = database.ExecContext(ctx, `
		INSERT INTO print_jobs (printer_serial, printer_name, gcode_file, started_at, status)
		VALUES ('ABC123', 'Printer1', 'benchy.gcode', $1, 'running')`,
		time.Now().Unix())
	if err != nil {
		t.Fatalf("failed to insert print job: %v", err)
	}

	// Update the job to completed
	_, err = database.ExecContext(ctx, `UPDATE print_jobs SET status = 'completed', completed_at = $1 WHERE status = 'running'`, time.Now().Unix())
	if err != nil {
		t.Fatalf("failed to update print job: %v", err)
	}

	// Verify NO notification was queued
	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM discord_webhook_queue").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query queue: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 notifications in queue (channel was null), got %d", count)
	}
}

func TestTrigger_Idempotent(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	db.MustMigrate(database, discordWebhookMigration)
	db.MustMigrate(database, migration)

	ctx := context.Background()

	// Insert a running job with notification_channel set
	_, err = database.ExecContext(ctx, `
		INSERT INTO print_jobs (printer_serial, printer_name, gcode_file, notification_channel, started_at, status)
		VALUES ('ABC123', 'Printer1', 'benchy.gcode', 'test-channel', $1, 'running')`,
		time.Now().Unix())
	if err != nil {
		t.Fatalf("failed to insert print job: %v", err)
	}

	// Update the job to completed
	_, err = database.ExecContext(ctx, `UPDATE print_jobs SET status = 'completed', completed_at = $1 WHERE status = 'running'`, time.Now().Unix())
	if err != nil {
		t.Fatalf("failed to update print job: %v", err)
	}

	// Try to update again (should be idempotent - no new notification)
	_, err = database.ExecContext(ctx, `UPDATE print_jobs SET status = 'completed' WHERE status = 'completed'`)
	if err != nil {
		t.Fatalf("failed to update print job again: %v", err)
	}

	// Verify only 1 notification was queued
	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM discord_webhook_queue").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query queue: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 notification (idempotent), got %d", count)
	}
}

func TestTrigger_FailedStatus(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer database.Close()

	db.MustMigrate(database, discordWebhookMigration)
	db.MustMigrate(database, migration)

	ctx := context.Background()

	// Insert a running job
	_, err = database.ExecContext(ctx, `
		INSERT INTO print_jobs (printer_serial, printer_name, gcode_file, notification_channel, started_at, status)
		VALUES ('ABC123', 'Printer1', 'benchy.gcode', 'test-channel', $1, 'running')`,
		time.Now().Unix())
	if err != nil {
		t.Fatalf("failed to insert print job: %v", err)
	}

	// Update the job to failed with error code
	_, err = database.ExecContext(ctx, `UPDATE print_jobs SET status = 'failed', error_code = 'E001' WHERE status = 'running'`)
	if err != nil {
		t.Fatalf("failed to update print job: %v", err)
	}

	// Verify notification was queued with failure message
	var payload string
	err = database.QueryRow("SELECT payload FROM discord_webhook_queue").Scan(&payload)
	if err != nil {
		t.Fatalf("failed to query notification: %v", err)
	}
	if !contains(payload, "has failed") {
		t.Errorf("payload should contain 'has failed', got: %s", payload)
	}
	if !contains(payload, "E001") {
		t.Errorf("payload should contain error code 'E001', got: %s", payload)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestParseDiscordUserID(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		// Prefix format tests
		{
			name:     "prefix: valid 17-digit ID",
			filename: "12345678901234567_benchy.gcode",
			want:     "12345678901234567",
		},
		{
			name:     "prefix: valid 18-digit ID",
			filename: "123456789012345678_benchy.gcode",
			want:     "123456789012345678",
		},
		{
			name:     "prefix: valid 19-digit ID",
			filename: "1234567890123456789_benchy.gcode",
			want:     "1234567890123456789",
		},
		{
			name:     "prefix: 3mf extension",
			filename: "123456789012345678_phone-case.3mf",
			want:     "123456789012345678",
		},
		// Suffix format tests
		{
			name:     "suffix: valid 17-digit ID",
			filename: "benchy_12345678901234567.gcode",
			want:     "12345678901234567",
		},
		{
			name:     "suffix: valid 18-digit ID",
			filename: "benchy_123456789012345678.gcode",
			want:     "123456789012345678",
		},
		{
			name:     "suffix: valid 19-digit ID",
			filename: "benchy_1234567890123456789.gcode",
			want:     "1234567890123456789",
		},
		{
			name:     "suffix: 3mf extension",
			filename: "phone-case_123456789012345678.3mf",
			want:     "123456789012345678",
		},
		{
			name:     "suffix: with dashes in name",
			filename: "my-cool-print_123456789012345678.gcode",
			want:     "123456789012345678",
		},
		// No ID tests
		{
			name:     "no ID",
			filename: "benchy.gcode",
			want:     "",
		},
		{
			name:     "too short ID prefix (16 digits)",
			filename: "1234567890123456_benchy.gcode",
			want:     "",
		},
		{
			name:     "too short ID suffix (16 digits)",
			filename: "benchy_1234567890123456.gcode",
			want:     "",
		},
		{
			name:     "too long ID prefix (20 digits)",
			filename: "12345678901234567890_benchy.gcode",
			want:     "",
		},
		{
			name:     "no underscore separator",
			filename: "123456789012345678benchy.gcode",
			want:     "",
		},
		{
			name:     "empty filename",
			filename: "",
			want:     "",
		},
		// Prefix takes precedence when both could match
		{
			name:     "prefix takes precedence",
			filename: "123456789012345678_benchy_987654321012345678.gcode",
			want:     "123456789012345678",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDiscordUserID(tt.filename)
			if got != tt.want {
				t.Errorf("parseDiscordUserID(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestStripDiscordID(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		// Prefix format
		{
			name:     "prefix: with ID",
			filename: "123456789012345678_benchy.gcode",
			want:     "benchy.gcode",
		},
		// Suffix format
		{
			name:     "suffix: with ID",
			filename: "benchy_123456789012345678.gcode",
			want:     "benchy.gcode",
		},
		{
			name:     "suffix: with dashes in name",
			filename: "my-cool-print_123456789012345678.gcode",
			want:     "my-cool-print.gcode",
		},
		{
			name:     "suffix: 3mf extension",
			filename: "phone-case_123456789012345678.3mf",
			want:     "phone-case.3mf",
		},
		// No ID
		{
			name:     "without ID",
			filename: "benchy.gcode",
			want:     "benchy.gcode",
		},
		{
			name:     "empty string",
			filename: "",
			want:     "",
		},
		{
			name:     "underscore but no valid ID",
			filename: "short_benchy.gcode",
			want:     "short_benchy.gcode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripDiscordID(tt.filename)
			if got != tt.want {
				t.Errorf("stripDiscordID(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}
