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

// OutboundMessage is one queued Discord message. Exactly one delivery target
// is set: ChannelID routes through the bot REST API (required for interactive
// components such as buttons), WebhookURL routes through a plain incoming
// webhook (legacy, components are silently dropped by Discord).
type OutboundMessage struct {
	WebhookURL string
	ChannelID  string
	Payload    string
}

type Sender func(ctx context.Context, msg OutboundMessage) error

// MessageQueuer allows modules to queue Discord messages. Implemented by
// *Module.
type MessageQueuer interface {
	// QueueMessage enqueues a raw JSON payload for delivery to a webhook URL.
	QueueMessage(ctx context.Context, webhookURL, payload string) error
	// QueueChannelMessage enqueues a raw JSON payload for delivery to a
	// channel via the bot REST API. Use this when the payload includes
	// interactive components (buttons/select menus); plain webhooks drop them.
	QueueChannelMessage(ctx context.Context, channelID, payload string) error
	QueueTemplateMessage(ctx context.Context, webhookURL, tmpl string, replacements map[string]string) error
}

type Module struct {
	db     *sql.DB
	sender Sender
	events *engine.EventLogger
}

func New(d *sql.DB, events *engine.EventLogger, sender Sender) *Module {
	engine.MustMigrate(d, migration)
	// channel_id generalizes the queue to bot-API channel delivery. ALTER
	// TABLE ADD COLUMN can't use IF NOT EXISTS, so run it best-effort and
	// ignore the "duplicate column" error on subsequent boots.
	d.Exec("ALTER TABLE discord_webhook_queue ADD COLUMN channel_id TEXT NOT NULL DEFAULT ''")
	m := &Module{db: d, sender: sender, events: events}
	if m.sender == nil {
		m.sender = newNoopSender()
	}
	return m
}

// logEvent records a Discord delivery event to the shared module_events table.
func (m *Module) logEvent(ctx context.Context, eventType string, success bool, details string) {
	if m.events == nil {
		return
	}
	m.events.LogEvent(ctx, 0, eventType, "", "", success, details)
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "stale discord webhooks",
		"DELETE FROM discord_webhook_queue WHERE unixepoch() - created > 3600")))
	mgr.Add(engine.Poll(time.Second, engine.PollWorkqueue(engine.WithRateLimiting(m, maxRPS))))
}

func (m *Module) GetItem(ctx context.Context) (message, error) {
	var item message
	err := m.db.QueryRowContext(ctx, "SELECT id, webhook_url, channel_id, payload, created FROM discord_webhook_queue WHERE unixepoch() >= send_at AND unixepoch() - created < 3600 ORDER BY send_at ASC LIMIT 1;").Scan(&item.ID, &item.WebhookURL, &item.ChannelID, &item.Payload, &item.Created)
	return item, err
}

func (m *Module) ProcessItem(ctx context.Context, item message) error {
	slog.Info("sending discord message", "id", item.ID)
	err := m.sender(ctx, OutboundMessage{WebhookURL: item.WebhookURL, ChannelID: item.ChannelID, Payload: item.Payload})
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

// QueueMessage adds a webhook message to the queue for delivery.
func (m *Module) QueueMessage(ctx context.Context, webhookURL, payload string) error {
	_, err := m.db.ExecContext(ctx, "INSERT INTO discord_webhook_queue (webhook_url, payload) VALUES ($1, $2);", webhookURL, payload)
	return err
}

// QueueChannelMessage adds a message to the queue for delivery to a Discord
// channel via the bot REST API. This is the path that supports interactive
// components (buttons). The bot must be a member of the channel's guild and
// have permission to post there.
func (m *Module) QueueChannelMessage(ctx context.Context, channelID, payload string) error {
	_, err := m.db.ExecContext(ctx, "INSERT INTO discord_webhook_queue (webhook_url, channel_id, payload) VALUES ('', $1, $2);", channelID, payload)
	return err
}

// QueueTemplateMessage renders a message template with placeholder substitution and queues the result.
func (m *Module) QueueTemplateMessage(ctx context.Context, webhookURL, tmpl string, replacements map[string]string) error {
	payload, err := RenderMessage(tmpl, replacements)
	if err != nil {
		return fmt.Errorf("rendering template: %w", err)
	}
	return m.QueueMessage(ctx, webhookURL, payload)
}

type message struct {
	ID         int64
	WebhookURL string
	ChannelID  string
	Payload    string
	Created    int64
}

func (m *message) String() string { return fmt.Sprintf("id=%d", m.ID) }
