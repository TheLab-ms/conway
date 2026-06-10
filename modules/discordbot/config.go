package discordbot

import (
	"encoding/hex"
	"fmt"
)

// Config controls the Discord discount-approval bot.
//
// LeadershipChannelWebhookURL is the Discord webhook URL used to POST a
// notification when a member requests a membership discount. The message
// contains an Approve button that any leader in the channel can click to
// approve the request.
//
// ApplicationPublicKey is the hex-encoded Ed25519 public key shown on the
// Discord application's "General Information" page. Discord signs every
// inbound interaction (button click) with this key; we verify the signature
// before trusting the request body.
//
// To wire this up in Discord:
//
//  1. Create a webhook on the leadership channel (Channel Settings →
//     Integrations → Webhooks → New Webhook), copy its URL into
//     LeadershipChannelWebhookURL.
//  2. In the Discord Developer Portal, open your application and copy the
//     "Public Key" from General Information into ApplicationPublicKey.
//  3. Set "Interactions Endpoint URL" to
//     https://<your-conway-host>/discord/interactions. Discord will PING the
//     URL and refuse to save unless Conway responds correctly, so make sure
//     Enabled=true and the public key is correct first.
type Config struct {
	Enabled                     bool   `json:"enabled" config:"label=Enabled,help=When on, a member requesting a discount posts a Discord message with an Approve button. Inbound interactions are still verified regardless."`
	LeadershipChannelWebhookURL string `json:"leadership_channel_webhook_url" config:"label=Leadership Channel Webhook URL,secret,help=Create a webhook on the leadership channel (Channel Settings → Integrations → Webhooks → New Webhook) and paste its URL here. Discount requests are posted here for approval."`
	ApplicationPublicKey        string `json:"application_public_key" config:"label=Application Public Key,help=Hex-encoded Ed25519 public key from your Discord application's General Information page. Used to verify inbound button interactions."`
}

// Validate ensures the public key is a valid Ed25519 key when set. Other
// fields may be empty when the bot is being configured; the worker and
// interaction handler check Enabled themselves.
func (c *Config) Validate() error {
	if c.ApplicationPublicKey == "" {
		return nil
	}
	key, err := hex.DecodeString(c.ApplicationPublicKey)
	if err != nil {
		return fmt.Errorf("application public key must be hex-encoded: %w", err)
	}
	if len(key) != 32 {
		return fmt.Errorf("application public key must decode to 32 bytes (got %d)", len(key))
	}
	return nil
}
