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

// NewHTTPSender creates a Sender that posts to Discord webhook URLs.
func NewHTTPSender() Sender {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(ctx context.Context, webhookURL, payload string) error {
		req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewBufferString(payload))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to send webhook: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("webhook returned status %d: %s", resp.StatusCode, string(body))
		}
		return nil
	}
}

func newNoopSender() Sender {
	return func(ctx context.Context, webhookURL, payload string) error {
		fmt.Fprintf(os.Stdout, "--- START DISCORD WEBHOOK TO %s ---\n%s\n--- END DISCORD WEBHOOK ---\n", webhookURL, payload)
		return nil
	}
}
