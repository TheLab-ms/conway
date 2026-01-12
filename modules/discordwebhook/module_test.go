package discordwebhook

import (
	"testing"

	"github.com/TheLab-ms/conway/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueueMessage(t *testing.T) {
	testDB := engine.OpenTestDB(t)

	m := New(testDB, nil)
	ctx := t.Context()

	err := m.QueueMessage(ctx, "https://discord.com/api/webhooks/test", `{"content":"test message"}`)
	require.NoError(t, err)

	var count int
	err = testDB.QueryRow("SELECT COUNT(*) FROM discord_webhook_queue").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var webhookURL, payload string
	err = testDB.QueryRow("SELECT webhook_url, payload FROM discord_webhook_queue").Scan(&webhookURL, &payload)
	require.NoError(t, err)
	assert.Equal(t, "https://discord.com/api/webhooks/test", webhookURL)
	assert.Equal(t, `{"content":"test message"}`, payload)
}

func TestGetItem(t *testing.T) {
	testDB := engine.OpenTestDB(t)

	m := New(testDB, nil)
	ctx := t.Context()

	err := m.QueueMessage(ctx, "https://discord.com/api/webhooks/test", `{"content":"hello"}`)
	require.NoError(t, err)

	item, err := m.GetItem(ctx)
	require.NoError(t, err)

	assert.Equal(t, "https://discord.com/api/webhooks/test", item.WebhookURL)
	assert.Equal(t, `{"content":"hello"}`, item.Payload)
}

func TestUpdateItemSuccess(t *testing.T) {
	testDB := engine.OpenTestDB(t)

	m := New(testDB, nil)
	ctx := t.Context()

	err := m.QueueMessage(ctx, "https://discord.com/api/webhooks/test", `{"content":"test"}`)
	require.NoError(t, err)

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

func TestUpdateItemFailure(t *testing.T) {
	testDB := engine.OpenTestDB(t)

	m := New(testDB, nil)
	ctx := t.Context()

	err := m.QueueMessage(ctx, "https://discord.com/api/webhooks/test", `{"content":"test"}`)
	require.NoError(t, err)

	item, err := m.GetItem(ctx)
	require.NoError(t, err)

	// Update as failure - should reschedule with backoff
	err = m.UpdateItem(ctx, item, false)
	require.NoError(t, err)

	// Verify message still exists
	var count int
	err = testDB.QueryRow("SELECT COUNT(*) FROM discord_webhook_queue").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}
