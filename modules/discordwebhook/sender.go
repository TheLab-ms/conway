package discordwebhook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// apiBase is the Discord REST API base used for bot-authenticated channel
// posts. It is a package var so tests can point it at a fake server.
var apiBase = "https://discord.com/api/v10"

// TokenProvider returns the Discord bot token to authenticate channel posts.
// It is called per send so the latest configured token is always used.
type TokenProvider func(ctx context.Context) (string, error)

// NewHTTPSender creates a Sender that delivers queued Discord messages.
//
// A message either targets a channel (ChannelID set) or a webhook URL
// (WebhookURL set). Channel delivery goes through the Discord REST API
// authenticated with the bot token, which is the only way to post interactive
// components (buttons/select menus). Webhook delivery is the legacy path used
// by SQL-trigger notifications that only need plain content/embeds.
//
// botToken may be nil if no channel delivery is ever needed; a channel message
// encountered with a nil/empty token returns an error so the queue retries.
func NewHTTPSender(botToken TokenProvider) Sender {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(ctx context.Context, msg OutboundMessage) error {
		var (
			url   string
			token string
		)
		if msg.ChannelID != "" {
			if botToken == nil {
				return fmt.Errorf("channel message for %q requires a bot token, but none is configured", msg.ChannelID)
			}
			t, err := botToken(ctx)
			if err != nil {
				return fmt.Errorf("loading bot token: %w", err)
			}
			if t == "" {
				return fmt.Errorf("channel message for %q requires a bot token, but none is configured", msg.ChannelID)
			}
			token = t
			url = fmt.Sprintf("%s/channels/%s/messages", apiBase, msg.ChannelID)
		} else {
			url = msg.WebhookURL
		}

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBufferString(msg.Payload))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bot "+token)
		}

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to send: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("discord returned status %d: %s", resp.StatusCode, string(body))
		}
		return nil
	}
}

func newNoopSender() Sender {
	return func(ctx context.Context, msg OutboundMessage) error {
		dest := msg.WebhookURL
		if msg.ChannelID != "" {
			dest = "channel:" + msg.ChannelID
		}
		fmt.Fprintf(os.Stdout, "--- START DISCORD MESSAGE TO %s ---\n%s\n--- END DISCORD MESSAGE ---\n", dest, msg.Payload)
		return nil
	}
}
