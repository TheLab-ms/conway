package settings

import (
	"context"
	"database/sql"
)

// Definition defines a setting for registration.
type Definition struct {
	Key         string
	Module      string
	Description string
	Sensitive   bool
}

// Definitions contains all known settings.
var Definitions = []Definition{
	// Core
	{Key: "core.self_url", Module: "core", Description: "Public URL of this server", Sensitive: false},

	// Payment (Stripe)
	{Key: "stripe.key", Module: "payment", Description: "Stripe API key", Sensitive: true},
	{Key: "stripe.webhook_key", Module: "payment", Description: "Stripe webhook signing secret", Sensitive: true},

	// Discord
	{Key: "discord.client_id", Module: "discord", Description: "Discord OAuth2 client ID", Sensitive: false},
	{Key: "discord.client_secret", Module: "discord", Description: "Discord OAuth2 client secret", Sensitive: true},
	{Key: "discord.bot_token", Module: "discord", Description: "Discord bot token", Sensitive: true},
	{Key: "discord.guild_id", Module: "discord", Description: "Discord server (guild) ID", Sensitive: false},
	{Key: "discord.role_id", Module: "discord", Description: "Discord role ID for active members", Sensitive: false},

	// Email
	{Key: "email.from", Module: "email", Description: "Sender email address (e.g., noreply@example.com)", Sensitive: false},
	{Key: "email.google_service_account", Module: "email", Description: "Google service account JSON for sending emails via Gmail API", Sensitive: true},

	// Auth (Turnstile)
	{Key: "turnstile.site_key", Module: "auth", Description: "Cloudflare Turnstile site key", Sensitive: false},
	{Key: "turnstile.secret", Module: "auth", Description: "Cloudflare Turnstile secret key", Sensitive: true},

	// Machines (Bambu)
	{Key: "bambu.printers", Module: "machines", Description: "Bambu printer configuration (JSON array)", Sensitive: false},

	// Access Controller
	{Key: "access_controller.host", Module: "access_controller", Description: "Access controller API host URL", Sensitive: false},

	// Kiosk
	{Key: "kiosk.space_host", Module: "kiosk", Description: "Hostname resolving to makerspace public IP", Sensitive: false},
}

// EnsureDefaults inserts all defined settings with empty values if they don't exist.
func EnsureDefaults(ctx context.Context, db *sql.DB) error {
	for _, def := range Definitions {
		_, err := db.ExecContext(ctx, `
			INSERT INTO settings (key, module, sensitive, description, value)
			VALUES (?, ?, ?, ?, '')
			ON CONFLICT(key) DO UPDATE SET
				module = excluded.module,
				sensitive = excluded.sensitive,
				description = excluded.description
		`, def.Key, def.Module, boolToInt(def.Sensitive), def.Description)
		if err != nil {
			return err
		}
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
