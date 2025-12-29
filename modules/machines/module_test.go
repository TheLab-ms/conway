package machines

import (
	"context"
	"sync"
	"testing"

	"github.com/TheLab-ms/conway/modules/machines/bambu"
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
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Simulate job starting
	oldState := []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil},
	}
	newState := []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime, GcodeFile: "123456789012345678_benchy.gcode", GcodeFileDisplay: "benchy.gcode", DiscordUserID: "123456789012345678"},
	}

	m.detectStateChanges(ctx, oldState, newState)

	// No notification on start
	if len(mock.getMessages()) != 0 {
		t.Errorf("expected no notifications on job start, got %d", len(mock.getMessages()))
	}

	// Now simulate job completing
	oldState = newState
	newState = []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil, GcodeFile: "", GcodeFileDisplay: "", DiscordUserID: ""},
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
	if !contains(messages[0].payload, "benchy.gcode") {
		t.Errorf("payload should contain filename, got: %s", messages[0].payload)
	}
	if !contains(messages[0].payload, "completed successfully") {
		t.Errorf("payload should contain 'completed successfully', got: %s", messages[0].payload)
	}
	if !contains(messages[0].payload, "<@123456789012345678>") {
		t.Errorf("payload should contain Discord mention, got: %s", messages[0].payload)
	}
}

func TestStateTransition_JobFailed(t *testing.T) {
	mock := &mockMessageQueuer{}
	m := &Module{
		notificationChannel: "test-channel",
		messageQueuer:       mock,
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Simulate job running then failing
	oldState := []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime, GcodeFile: "benchy.gcode", GcodeFileDisplay: "benchy.gcode", ErrorCode: ""},
	}
	// Set hadJob state with job metadata
	m.updateLastNotifiedState("ABC123", notifiedState{
		hadJob:           true,
		gcodeFile:        "benchy.gcode",
		gcodeFileDisplay: "benchy.gcode",
		printerName:      "Printer1",
	})

	newState := []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime, GcodeFile: "benchy.gcode", GcodeFileDisplay: "benchy.gcode", ErrorCode: "E001"},
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
}

func TestStateTransition_NoDuplicateNotifications(t *testing.T) {
	mock := &mockMessageQueuer{}
	m := &Module{
		notificationChannel: "test-channel",
		messageQueuer:       mock,
	}

	ctx := context.Background()
	finishTime := int64(1234567890)

	// Start a job
	oldState := []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil},
	}
	newState := []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime, GcodeFile: "benchy.gcode", GcodeFileDisplay: "benchy.gcode"},
	}
	m.detectStateChanges(ctx, oldState, newState)

	// Complete the job
	oldState = newState
	newState = []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil},
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
		hadJob:           true,
		gcodeFile:        "benchy.gcode",
		gcodeFileDisplay: "benchy.gcode",
		printerName:      "Printer1",
	})
	oldState := []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime, GcodeFile: "benchy.gcode"},
	}
	newState := []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil},
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
		hadJob:           true,
		gcodeFile:        "benchy.gcode",
		gcodeFileDisplay: "benchy.gcode",
		printerName:      "Printer1",
	})
	oldState := []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: &finishTime, GcodeFile: "benchy.gcode"},
	}
	newState := []bambu.PrinterData{
		{SerialNumber: "ABC123", PrinterName: "Printer1", JobFinishedTimestamp: nil},
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
