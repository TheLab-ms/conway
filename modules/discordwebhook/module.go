package discordwebhook

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
)

const migration = `
CREATE TABLE IF NOT EXISTS discord_webhook_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    send_at INTEGER DEFAULT (strftime('%s', 'now')),
    webhook_url TEXT NOT NULL,
    payload TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS discord_webhook_queue_send_at_idx ON discord_webhook_queue (send_at);
`

const maxRPS = 5

type Sender func(ctx context.Context, webhookURL, payload string) error

// MessageQueuer allows modules to queue Discord webhook messages.
// Implemented by *Module.
type MessageQueuer interface {
	QueueMessage(ctx context.Context, webhookURL, payload string) error
}

type Module struct {
	db     *sql.DB
	sender Sender
}

func New(d *sql.DB, sender Sender) *Module {
	engine.MustMigrate(d, migration)
	m := &Module{db: d, sender: sender}
	if m.sender == nil {
		m.sender = newNoopSender()
	}
	return m
}

// logEvent logs a Discord webhook event to the shared discord_events table.
func (m *Module) logEvent(ctx context.Context, eventType string, success bool, details string) {
	successInt := 0
	if success {
		successInt = 1
	}
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO discord_events (event_type, success, details)
		 VALUES (?, ?, ?)`,
		eventType, successInt, details)
	if err != nil {
		slog.Error("failed to log discord webhook event", "error", err, "eventType", eventType)
	}
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "stale discord webhooks",
		"DELETE FROM discord_webhook_queue WHERE unixepoch() - created > 3600")))
	mgr.Add(engine.Poll(time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, maxRPS))))
}

func (m *Module) GetItem(ctx context.Context) (message, error) {
	var item message
	err := m.db.QueryRowContext(ctx, "SELECT id, webhook_url, payload, created FROM discord_webhook_queue WHERE unixepoch() >= send_at AND unixepoch() - created < 3600 ORDER BY send_at ASC LIMIT 1;").Scan(&item.ID, &item.WebhookURL, &item.Payload, &item.Created)
	return item, err
}

func (m *Module) ProcessItem(ctx context.Context, item message) error {
	slog.Info("sending discord webhook", "id", item.ID)
	err := m.sender(ctx, item.WebhookURL, item.Payload)
	if err != nil {
		m.logEvent(ctx, "WebhookError", false, fmt.Sprintf("id=%d: %s", item.ID, err.Error()))
	}
	return err
}

func (m *Module) UpdateItem(ctx context.Context, item message, success bool) (err error) {
	if success {
		m.logEvent(ctx, "WebhookSent", true, fmt.Sprintf("id=%d", item.ID))
		_, err = m.db.ExecContext(ctx, "DELETE FROM discord_webhook_queue WHERE id = $1;", item.ID)
	} else {
		_, err = m.db.ExecContext(ctx, "UPDATE discord_webhook_queue SET send_at = unixepoch() + ((send_at - created) * 2) WHERE id = $1;", item.ID)
	}
	return err
}

// QueueMessage adds a message to the webhook queue for delivery.
func (m *Module) QueueMessage(ctx context.Context, webhookURL, payload string) error {
	_, err := m.db.ExecContext(ctx, "INSERT INTO discord_webhook_queue (webhook_url, payload) VALUES ($1, $2);", webhookURL, payload)
	return err
}

type message struct {
	ID         int64
	WebhookURL string
	Payload    string
	Created    int64
}

func (m *message) String() string { return fmt.Sprintf("id=%d", m.ID) }
