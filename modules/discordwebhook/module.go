package discordwebhook

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/db"
)

const migration = `
CREATE TABLE IF NOT EXISTS discord_webhook_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    send_at INTEGER DEFAULT (strftime('%s', 'now')),
    channel_id TEXT NOT NULL,
    payload TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS discord_webhook_queue_send_at_idx ON discord_webhook_queue (send_at);
`

const maxRPS = 5

type Sender func(ctx context.Context, webhookURL, payload string) error

// MessageQueuer allows modules to queue Discord webhook messages.
// Implemented by *Module.
type MessageQueuer interface {
	QueueMessage(ctx context.Context, channelID, payload string) error
}

type Module struct {
	db          *sql.DB
	sender      Sender
	webhookURLs map[string]string // channel_id -> webhook URL
}

func New(d *sql.DB, sender Sender, webhookURLs map[string]string) *Module {
	db.MustMigrate(d, migration)
	m := &Module{db: d, sender: sender, webhookURLs: webhookURLs}
	if m.sender == nil {
		m.sender = newNoopSender()
	}
	return m
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "stale discord webhooks",
		"DELETE FROM discord_webhook_queue WHERE unixepoch() - created > 3600")))
	if len(m.webhookURLs) == 0 {
		slog.Warn("disabling discord webhook processing because no webhook URLs were configured")
		return
	}
	mgr.Add(engine.Poll(time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, maxRPS))))
}

func (m *Module) GetItem(ctx context.Context) (message, error) {
	var item message
	err := m.db.QueryRowContext(ctx, "SELECT id, channel_id, payload, created FROM discord_webhook_queue WHERE unixepoch() >= send_at AND unixepoch() - created < 3600 ORDER BY send_at ASC LIMIT 1;").Scan(&item.ID, &item.ChannelID, &item.Payload, &item.Created)
	return item, err
}

func (m *Module) ProcessItem(ctx context.Context, item message) error {
	webhookURL, ok := m.webhookURLs[item.ChannelID]
	if !ok {
		slog.Warn("no webhook URL configured for channel", "channel_id", item.ChannelID)
		return fmt.Errorf("no webhook URL for channel %q", item.ChannelID)
	}
	slog.Info("sending discord webhook", "id", item.ID, "channel_id", item.ChannelID)
	return m.sender(ctx, webhookURL, item.Payload)
}

func (m *Module) UpdateItem(ctx context.Context, item message, success bool) (err error) {
	if success {
		_, err = m.db.Exec("DELETE FROM discord_webhook_queue WHERE id = $1;", item.ID)
	} else {
		_, err = m.db.Exec("UPDATE discord_webhook_queue SET send_at = unixepoch() + ((send_at - created) * 2) WHERE id = $1;", item.ID)
	}
	return err
}

// QueueMessage adds a message to the webhook queue for delivery.
func (m *Module) QueueMessage(ctx context.Context, channelID, payload string) error {
	_, err := m.db.ExecContext(ctx, "INSERT INTO discord_webhook_queue (channel_id, payload) VALUES ($1, $2);", channelID, payload)
	return err
}

type message struct {
	ID        int64
	ChannelID string
	Payload   string
	Created   int64
}

func (m *message) String() string { return fmt.Sprintf("id=%d", m.ID) }
