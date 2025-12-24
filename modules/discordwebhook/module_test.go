package discordwebhook

import (
	"testing"

	"github.com/TheLab-ms/conway/engine/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueueMessage(t *testing.T) {
	testDB := db.OpenTest(t)

	webhookURLs := map[string]string{
		"3d-printing": "https://discord.com/api/webhooks/test",
	}

	// Create module with noop sender
	m := New(testDB, nil, webhookURLs)

	ctx := t.Context()

	// Queue a message
	err := m.QueueMessage(ctx, "3d-printing", `{"content":"test message"}`)
	require.NoError(t, err)

	// Verify message is in queue
	var count int
	err = testDB.QueryRow("SELECT COUNT(*) FROM discord_webhook_queue").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Verify message content
	var channelID, payload string
	err = testDB.QueryRow("SELECT channel_id, payload FROM discord_webhook_queue").Scan(&channelID, &payload)
	require.NoError(t, err)
	assert.Equal(t, "3d-printing", channelID)
	assert.Equal(t, `{"content":"test message"}`, payload)
}

func TestGetItem(t *testing.T) {
	testDB := db.OpenTest(t)

	webhookURLs := map[string]string{
		"test-channel": "https://discord.com/api/webhooks/test",
	}

	m := New(testDB, nil, webhookURLs)
	ctx := t.Context()

	// Queue a message
	err := m.QueueMessage(ctx, "test-channel", `{"content":"hello"}`)
	require.NoError(t, err)

	// Get the item
	item, err := m.GetItem(ctx)
	require.NoError(t, err)

	assert.Equal(t, "test-channel", item.ChannelID)
	assert.Equal(t, `{"content":"hello"}`, item.Payload)
}

func TestProcessItemNoWebhookURL(t *testing.T) {
	testDB := db.OpenTest(t)

	// Empty webhook URLs
	webhookURLs := map[string]string{}

	m := New(testDB, nil, webhookURLs)
	ctx := t.Context()

	item := message{
		ID:        1,
		ChannelID: "unknown-channel",
		Payload:   `{"content":"test"}`,
		Created:   0,
	}

	err := m.ProcessItem(ctx, item)
	assert.Error(t, err)
}

func TestUpdateItemSuccess(t *testing.T) {
	testDB := db.OpenTest(t)

	m := New(testDB, nil, nil)
	ctx := t.Context()

	// Queue a message
	err := m.QueueMessage(ctx, "test", `{"content":"test"}`)
	require.NoError(t, err)

	// Get the item
	item, err := m.GetItem(ctx)
	require.NoError(t, err)

	// Update as success - should delete
	err = m.UpdateItem(ctx, item, true)
	require.NoError(t, err)

	// Verify message is deleted
	var count int
	err = testDB.QueryRow("SELECT COUNT(*) FROM discord_webhook_queue").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}
