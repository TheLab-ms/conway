package discordbot

import (
	"encoding/hex"
	"fmt"
)

// Config controls the Discord discount-approval bot.
//
// LeadershipChannelID is the ID of the Discord channel where a notification is
// posted when a member requests a membership discount. The message is sent via
// the Discord bot REST API (authenticated with the configured bot token) so it
// can carry an Approve button that any leader in the channel can click.
//
// ApplicationPublicKey is the hex-encoded Ed25519 public key shown on the
// Discord application's "General Information" page. Discord signs every
// inbound interaction (button click) with this key; we verify the signature
// before trusting the request body.
//
// To wire this up in Discord:
//
//  1. Create a bot for your application, give it permission to post in the
//     leadership channel, and copy its Bot Token into the Discord bot config.
//  2. Enable Developer Mode in Discord, right-click the leadership channel,
//     choose "Copy Channel ID", and paste it into LeadershipChannelID.
//  3. In the Discord Developer Portal, open your application and copy the
//     "Public Key" from General Information into ApplicationPublicKey.
//  4. Set "Interactions Endpoint URL" to
//     https://<your-conway-host>/discord/interactions. Discord will PING the
//     URL and refuse to save unless Conway responds correctly, so make sure
//     Enabled=true and the public key is correct first.
type Config struct {
	Enabled              bool   `json:"enabled" config:"label=Enabled,help=When on, a member requesting a discount posts a Discord message with an Approve button. Inbound interactions are still verified regardless."`
	LeadershipChannelID  string `json:"leadership_channel_id" config:"label=Leadership Channel ID,help=Right-click the leadership channel in Discord (Developer Mode on) and choose Copy Channel ID. Discount requests are posted here by the bot."`
	ApplicationPublicKey string `json:"application_public_key" config:"label=Application Public Key,help=Hex-encoded Ed25519 public key from your Discord application's General Information page. Used to verify inbound button interactions."`
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
