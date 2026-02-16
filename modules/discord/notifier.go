package discord

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/TheLab-ms/conway/modules/discordwebhook"
)

// webhookConfig represents a single row from the discord_webhooks table.
type webhookConfig struct {
	WebhookURL      string
	MessageTemplate string
	Username        string
}

// Notifier sends Discord webhook notifications for application-level events
// (Signup, PrintCompleted, PrintFailed) that aren't handled by SQLite triggers.
// It queries the discord_webhooks table to find matching webhook configs.
type Notifier struct {
	db     *sql.DB
	queuer discordwebhook.MessageQueuer
}

// NewNotifier creates a Notifier that queries webhook configs from the database
// and queues messages via the given MessageQueuer.
func NewNotifier(db *sql.DB, queuer discordwebhook.MessageQueuer) *Notifier {
	return &Notifier{
		db:     db,
		queuer: queuer,
	}
}

// loadWebhooks returns all enabled webhook configs for the given trigger event.
func (n *Notifier) loadWebhooks(ctx context.Context, triggerEvent string) ([]webhookConfig, error) {
	rows, err := n.db.QueryContext(ctx,
		`SELECT webhook_url, message_template, username
		 FROM discord_webhooks
		 WHERE trigger_event = ? AND enabled = 1`, triggerEvent)
	if err != nil {
		return nil, fmt.Errorf("querying webhooks for %s: %w", triggerEvent, err)
	}
	defer rows.Close()

	var configs []webhookConfig
	for rows.Next() {
		var wc webhookConfig
		if err := rows.Scan(&wc.WebhookURL, &wc.MessageTemplate, &wc.Username); err != nil {
			return nil, fmt.Errorf("scanning webhook config: %w", err)
		}
		configs = append(configs, wc)
	}
	return configs, rows.Err()
}

// dispatchAll renders and queues a message for each matching webhook config.
func (n *Notifier) dispatchAll(ctx context.Context, triggerEvent string, replacements map[string]string) {
	webhooks, err := n.loadWebhooks(ctx, triggerEvent)
	if err != nil {
		slog.Error("failed to load webhook configs", "error", err, "trigger", triggerEvent)
		return
	}

	for _, wc := range webhooks {
		payload, err := discordwebhook.RenderMessage(wc.MessageTemplate, replacements, wc.Username)
		if err != nil {
			slog.Error("failed to render webhook message", "error", err, "trigger", triggerEvent)
			continue
		}
		if err := n.queuer.QueueMessage(ctx, wc.WebhookURL, payload); err != nil {
			slog.Error("failed to queue webhook message", "error", err, "trigger", triggerEvent)
		}
	}
}

// NotifySignup sends a signup notification to all webhooks configured for the "Signup" trigger.
func (n *Notifier) NotifySignup(ctx context.Context, email string, memberID int64) {
	n.dispatchAll(ctx, "Signup", map[string]string{
		"event":     "Signup",
		"email":     email,
		"member_id": fmt.Sprintf("%d", memberID),
		"details":   fmt.Sprintf("New member signed up: %s", email),
	})
}

// NotifyPrintCompleted sends a print-completed notification to all webhooks
// configured for the "PrintCompleted" trigger.
// discordUserID may be empty if the print owner could not be identified.
func (n *Notifier) NotifyPrintCompleted(ctx context.Context, discordUserID, printerName, fileName string) {
	mention := ""
	if discordUserID != "" {
		mention = fmt.Sprintf("<@%s>", discordUserID)
	}

	n.dispatchAll(ctx, "PrintCompleted", map[string]string{
		"event":        "PrintCompleted",
		"mention":      mention,
		"printer_name": printerName,
		"file_name":    fileName,
	})
}

// NotifyPrintFailed sends a print-failed notification to all webhooks
// configured for the "PrintFailed" trigger.
// discordUserID may be empty if the print owner could not be identified.
func (n *Notifier) NotifyPrintFailed(ctx context.Context, discordUserID, printerName, fileName, errorCode string) {
	mention := ""
	if discordUserID != "" {
		mention = fmt.Sprintf("<@%s>", discordUserID)
	}

	n.dispatchAll(ctx, "PrintFailed", map[string]string{
		"event":        "PrintFailed",
		"mention":      mention,
		"printer_name": printerName,
		"file_name":    fileName,
		"error_code":   errorCode,
	})
}
