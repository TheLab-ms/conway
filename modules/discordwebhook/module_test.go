package discordwebhook

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestModule_QueueMessage(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	webhookURLs := map[string]string{
		"3d-printing": "https://discord.com/api/webhooks/test",
	}

	// Create module with noop sender
	m := New(db, nil, webhookURLs)

	ctx := context.Background()

	// Queue a message
	err = m.QueueMessage(ctx, "3d-printing", `{"content":"test message"}`)
	if err != nil {
		t.Fatalf("QueueMessage failed: %v", err)
	}

	// Verify message is in queue
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM discord_webhook_queue").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query queue: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 message in queue, got %d", count)
	}

	// Verify message content
	var channelID, payload string
	err = db.QueryRow("SELECT channel_id, payload FROM discord_webhook_queue").Scan(&channelID, &payload)
	if err != nil {
		t.Fatalf("failed to query message: %v", err)
	}
	if channelID != "3d-printing" {
		t.Errorf("expected channel_id '3d-printing', got %q", channelID)
	}
	if payload != `{"content":"test message"}` {
		t.Errorf("unexpected payload: %s", payload)
	}
}

func TestModule_GetItem(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	webhookURLs := map[string]string{
		"test-channel": "https://discord.com/api/webhooks/test",
	}

	m := New(db, nil, webhookURLs)
	ctx := context.Background()

	// Queue a message
	err = m.QueueMessage(ctx, "test-channel", `{"content":"hello"}`)
	if err != nil {
		t.Fatalf("QueueMessage failed: %v", err)
	}

	// Get the item
	item, err := m.GetItem(ctx)
	if err != nil {
		t.Fatalf("GetItem failed: %v", err)
	}

	if item.ChannelID != "test-channel" {
		t.Errorf("expected channel_id 'test-channel', got %q", item.ChannelID)
	}
	if item.Payload != `{"content":"hello"}` {
		t.Errorf("unexpected payload: %s", item.Payload)
	}
}

func TestModule_ProcessItem_NoWebhookURL(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	// Empty webhook URLs
	webhookURLs := map[string]string{}

	m := New(db, nil, webhookURLs)
	ctx := context.Background()

	item := message{
		ID:        1,
		ChannelID: "unknown-channel",
		Payload:   `{"content":"test"}`,
		Created:   0,
	}

	err = m.ProcessItem(ctx, item)
	if err == nil {
		t.Error("expected error for unknown channel, got nil")
	}
}

func TestModule_UpdateItem_Success(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	m := New(db, nil, nil)
	ctx := context.Background()

	// Queue a message
	err = m.QueueMessage(ctx, "test", `{"content":"test"}`)
	if err != nil {
		t.Fatalf("QueueMessage failed: %v", err)
	}

	// Get the item
	item, err := m.GetItem(ctx)
	if err != nil {
		t.Fatalf("GetItem failed: %v", err)
	}

	// Update as success - should delete
	err = m.UpdateItem(ctx, item, true)
	if err != nil {
		t.Fatalf("UpdateItem failed: %v", err)
	}

	// Verify message is deleted
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM discord_webhook_queue").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query queue: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 messages after success, got %d", count)
	}
}
