package discordbot

import (
	"encoding/hex"
	"fmt"

	"github.com/TheLab-ms/conway/engine/config"
)

// Config controls the Discord signup-notification bot.
//
// SignupChannelWebhookURL is the Discord webhook URL used to POST the
// announcement message when a new member joins Conway. The message contains a
// string-select component letting any channel member assign a discount type.
//
// ApplicationPublicKey is the hex-encoded Ed25519 public key shown on the
// Discord application's "General Information" page. Discord signs every
// inbound interaction (button/select click) with this key; we verify the
// signature before trusting the request body.
//
// To wire this up in Discord:
//
//  1. Create a webhook on the signup channel (Channel Settings → Integrations
//     → Webhooks → New Webhook), copy its URL into SignupChannelWebhookURL.
//  2. In the Discord Developer Portal, open your application and copy the
//     "Public Key" from General Information into ApplicationPublicKey.
//  3. Set "Interactions Endpoint URL" to
//     https://<your-conway-host>/discord/interactions. Discord will PING the
//     URL and refuse to save unless Conway responds correctly, so make sure
//     Enabled=true and the public key is correct first.
type Config struct {
	Enabled                 bool   `json:"enabled" config:"label=Enabled,help=When on, new member signups post a Discord message with a discount-type picker. Inbound interactions are still verified regardless."`
	SignupChannelWebhookURL string `json:"signup_channel_webhook_url" config:"label=Signup Channel Webhook URL,secret,help=Create a webhook on the desired channel (Channel Settings → Integrations → Webhooks → New Webhook) and paste its URL here."`
	ApplicationPublicKey    string `json:"application_public_key" config:"label=Application Public Key,help=Hex-encoded Ed25519 public key from your Discord application's General Information page. Used to verify inbound button/select interactions."`
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

// ConfigSpec returns the configuration page specification for the bot.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:   "discordbot",
		Title:    "Discord Signup Bot",
		Type:     Config{},
		Order:    11,
		Category: "Integrations",
	}
}
