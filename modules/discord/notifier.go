package discord

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/modules/discordwebhook"
)

// Notifier sends templated Discord webhook notifications.
// It loads the current Discord config (templates + webhook URLs) on each call,
// so changes to templates take effect immediately.
type Notifier struct {
	loader *config.Loader[Config]
	queuer discordwebhook.MessageQueuer
}

// NewNotifier creates a Notifier that reads templates from the discord config
// and queues messages via the given MessageQueuer.
func NewNotifier(store *config.Store, queuer discordwebhook.MessageQueuer) *Notifier {
	return &Notifier{
		loader: config.NewLoader[Config](store, "discord"),
		queuer: queuer,
	}
}

// NotifySignup sends a signup notification to the configured Discord webhook.
func (n *Notifier) NotifySignup(ctx context.Context, email string, memberID int64) {
	cfg, err := n.loader.Load(ctx)
	if err != nil {
		slog.Error("failed to load discord config for signup notification", "error", err)
		return
	}
	if cfg.SignupWebhookURL == "" {
		return
	}

	data := discordwebhook.SignupData{
		Email:    email,
		MemberID: memberID,
	}
	err = n.queuer.QueueTemplateMessage(ctx, cfg.SignupWebhookURL, cfg.SignupTemplate(), data, "Conway")
	if err != nil {
		slog.Error("failed to queue signup notification", "error", err, "email", email)
	}
}

// NotifyPrintCompleted sends a print-completed notification to the configured Discord webhook.
// discordUserID may be empty if the print owner could not be identified.
func (n *Notifier) NotifyPrintCompleted(ctx context.Context, discordUserID, printerName, fileName string) {
	cfg, err := n.loader.Load(ctx)
	if err != nil {
		slog.Error("failed to load discord config for print completed notification", "error", err)
		return
	}
	if cfg.PrintWebhookURL == "" {
		return
	}

	mention := ""
	if discordUserID != "" {
		mention = fmt.Sprintf("<@%s>", discordUserID)
	}

	data := discordwebhook.PrintCompletedData{
		Mention:     mention,
		PrinterName: printerName,
		FileName:    fileName,
	}
	err = n.queuer.QueueTemplateMessage(ctx, cfg.PrintWebhookURL, cfg.PrintCompletedTmpl(), data, "Conway Print Bot")
	if err != nil {
		slog.Error("failed to queue print completed notification", "error", err)
	}
}

// NotifyPrintFailed sends a print-failed notification to the configured Discord webhook.
// discordUserID may be empty if the print owner could not be identified.
func (n *Notifier) NotifyPrintFailed(ctx context.Context, discordUserID, printerName, fileName, errorCode string) {
	cfg, err := n.loader.Load(ctx)
	if err != nil {
		slog.Error("failed to load discord config for print failed notification", "error", err)
		return
	}
	if cfg.PrintWebhookURL == "" {
		return
	}

	mention := ""
	if discordUserID != "" {
		mention = fmt.Sprintf("<@%s>", discordUserID)
	}

	data := discordwebhook.PrintFailedData{
		Mention:     mention,
		PrinterName: printerName,
		FileName:    fileName,
		ErrorCode:   errorCode,
	}
	err = n.queuer.QueueTemplateMessage(ctx, cfg.PrintWebhookURL, cfg.PrintFailedTmpl(), data, "Conway Print Bot")
	if err != nil {
		slog.Error("failed to queue print failed notification", "error", err)
	}
}
