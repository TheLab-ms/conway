package machines

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/TheLab-ms/conway/modules/machines/bambu"
	"github.com/TheLab-ms/conway/modules/members"
)

// mockMessageQueuer is a test implementation of discordwebhook.MessageQueuer
type mockMessageQueuer struct {
	mu       sync.Mutex
	messages []queuedMessage
}

type queuedMessage struct {
	channelID string
	payload   string
}

func (m *mockMessageQueuer) QueueMessage(ctx context.Context, channelID, payload string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, queuedMessage{channelID: channelID, payload: payload})
	return nil
}

func (m *mockMessageQueuer) getMessages() []queuedMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]queuedMessage{}, m.messages...)
}

func (m *mockMessageQueuer) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = nil
}

func TestStateTransition_JobCompleted(t *testing.T) {
	mock := &mockMessageQueuer{}
	m := &Module{
		notificationChannel: "test-channel",
		messageQueuer:       mock,
		db:                  createTestDB(t, "jordan", "123456789"),
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Simulate job starting
	oldState := []PrinterStatus{
		{PrinterData: bambu.PrinterData{}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil},
	}
	newState := []PrinterStatus{
		{PrinterData: bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@jordan"}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime},
	}

	m.detectStateChanges(ctx, oldState, newState)

	// No notification on start
	if len(mock.getMessages()) != 0 {
		t.Errorf("expected no notifications on job start, got %d", len(mock.getMessages()))
	}

	// Now simulate job completing (keep the gcode/subtask data from the running job)
	oldState = newState
	newState = []PrinterStatus{
		{PrinterData: bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@jordan"}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil},
	}

	m.detectStateChanges(ctx, oldState, newState)

	// Should have 1 completion notification
	messages := mock.getMessages()
	if len(messages) != 1 {
		t.Fatalf("expected 1 notification on job completion, got %d", len(messages))
	}

	if messages[0].channelID != "test-channel" {
		t.Errorf("expected channel_id 'test-channel', got %q", messages[0].channelID)
	}
	if !contains(messages[0].payload, "completed successfully") {
		t.Errorf("payload should contain 'completed successfully', got: %s", messages[0].payload)
	}
	if !contains(messages[0].payload, "123456789") {
		t.Errorf("payload should contain Discord user ID '123456789', got: %s", messages[0].payload)
	}
}

func TestStateTransition_JobFailed(t *testing.T) {
	mock := &mockMessageQueuer{}
	m := &Module{
		notificationChannel: "test-channel",
		messageQueuer:       mock,
		db:                  createTestDB(t, "testuser", "987654321"),
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Simulate job running then failing
	oldState := []PrinterStatus{
		{PrinterData: bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@testuser"}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime, ErrorCode: ""},
	}
	// Set hadJob state with job metadata
	m.updateLastNotifiedState("ABC123", notifiedState{
		hadJob:               true,
		gcodeFile:            "benchy.gcode",
		ownerDiscordUsername: "testuser",
		printerName:          "Printer1",
	})

	newState := []PrinterStatus{
		{PrinterData: bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@testuser"}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime, ErrorCode: "E001"},
	}

	m.detectStateChanges(ctx, oldState, newState)

	// Should have 1 failure notification
	messages := mock.getMessages()
	if len(messages) != 1 {
		t.Fatalf("expected 1 notification on job failure, got %d", len(messages))
	}

	if !contains(messages[0].payload, "has failed") {
		t.Errorf("payload should contain 'has failed', got: %s", messages[0].payload)
	}
	if !contains(messages[0].payload, "E001") {
		t.Errorf("payload should contain error code 'E001', got: %s", messages[0].payload)
	}
	if !contains(messages[0].payload, "987654321") {
		t.Errorf("payload should contain Discord user ID '987654321', got: %s", messages[0].payload)
	}
}

func TestStateTransition_NoDuplicateNotifications(t *testing.T) {
	mock := &mockMessageQueuer{}
	m := &Module{
		notificationChannel: "test-channel",
		messageQueuer:       mock,
		db:                  createTestDB(t, "testuser", "555555555"),
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Start a job
	oldState := []PrinterStatus{
		{PrinterData: bambu.PrinterData{}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil},
	}
	newState := []PrinterStatus{
		{PrinterData: bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@testuser"}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime},
	}
	m.detectStateChanges(ctx, oldState, newState)

	// Complete the job (keep subtask data so owner can be notified)
	oldState = newState
	newState = []PrinterStatus{
		{PrinterData: bambu.PrinterData{GcodeFile: "benchy.gcode", SubtaskName: "@testuser"}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil},
	}
	m.detectStateChanges(ctx, oldState, newState)

	// Should have exactly 1 notification
	if len(mock.getMessages()) != 1 {
		t.Fatalf("expected exactly 1 notification, got %d", len(mock.getMessages()))
	}

	// Call detectStateChanges again with same state (simulating repeated polls)
	m.detectStateChanges(ctx, newState, newState)
	m.detectStateChanges(ctx, newState, newState)
	m.detectStateChanges(ctx, newState, newState)

	// Should still have exactly 1 notification (idempotent)
	if len(mock.getMessages()) != 1 {
		t.Errorf("expected exactly 1 notification after repeated polls, got %d", len(mock.getMessages()))
	}
}

func TestStateTransition_NoNotificationWhenChannelEmpty(t *testing.T) {
	mock := &mockMessageQueuer{}
	m := &Module{
		notificationChannel: "", // No channel configured
		messageQueuer:       mock,
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Start and complete a job
	m.updateLastNotifiedState("ABC123", notifiedState{
		hadJob:      true,
		gcodeFile:   "benchy.gcode",
		printerName: "Printer1",
	})
	oldState := []PrinterStatus{
		{PrinterData: bambu.PrinterData{GcodeFile: "benchy.gcode"}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime},
	}
	newState := []PrinterStatus{
		{PrinterData: bambu.PrinterData{}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil},
	}
	m.detectStateChanges(ctx, oldState, newState)

	// Should have no notifications (channel not configured)
	if len(mock.getMessages()) != 0 {
		t.Errorf("expected 0 notifications when channel is empty, got %d", len(mock.getMessages()))
	}
}

func TestStateTransition_NoNotificationWhenQueuerNil(t *testing.T) {
	m := &Module{
		notificationChannel: "test-channel",
		messageQueuer:       nil, // No queuer
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Start and complete a job - should not panic
	m.updateLastNotifiedState("ABC123", notifiedState{
		hadJob:      true,
		gcodeFile:   "benchy.gcode",
		printerName: "Printer1",
	})
	oldState := []PrinterStatus{
		{PrinterData: bambu.PrinterData{GcodeFile: "benchy.gcode"}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime},
	}
	newState := []PrinterStatus{
		{PrinterData: bambu.PrinterData{}, SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil},
	}
	m.detectStateChanges(ctx, oldState, newState)
	// Should complete without panic
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

// createTestDB creates a test database with the members schema and a single member for testing.
func createTestDB(t *testing.T, discordUsername, discordUserID string) *sql.DB {
	t.Helper()
	db := members.NewTestDB(t)
	_, err := db.Exec(`INSERT INTO members (email, discord_username, discord_user_id) VALUES (?, ?, ?)`,
		discordUsername+"@example.com", discordUsername, discordUserID)
	if err != nil {
		t.Fatalf("failed to insert test member: %v", err)
	}
	return db
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
