package settings

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
)

// envMappings maps environment variable names to setting keys.
var envMappings = map[string]string{
	"CONWAY_StripeKey":            "stripe.key",
	"CONWAY_StripeWebhookKey":     "stripe.webhook_key",
	"CONWAY_DiscordClientID":      "discord.client_id",
	"CONWAY_DiscordClientSecret":  "discord.client_secret",
	"CONWAY_DiscordBotToken":      "discord.bot_token",
	"CONWAY_DiscordGuildID":       "discord.guild_id",
	"CONWAY_DiscordRoleID":        "discord.role_id",
	"CONWAY_EmailFrom":            "email.from",
	"CONWAY_TurnstileSiteKey":     "turnstile.site_key",
	"CONWAY_TurnstileSecret":      "turnstile.secret",
	"CONWAY_BambuPrinters":        "bambu.printers",
	"CONWAY_AccessControllerHost": "access_controller.host",
	"CONWAY_SpaceHost":            "kiosk.space_host",
	"SELF_URL":                    "core.self_url",
}

// MigrateFromEnv copies environment variables to settings table if settings are empty.
// This is a one-time migration that preserves existing env var configuration.
func MigrateFromEnv(ctx context.Context, db *sql.DB) error {
	for envKey, settingKey := range envMappings {
		envValue := os.Getenv(envKey)
		if envValue == "" {
			continue
		}

		// Only migrate if the setting is currently empty
		var currentValue string
		err := db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", settingKey).Scan(&currentValue)
		if err != nil && err != sql.ErrNoRows {
			return err
		}

		if currentValue == "" {
			_, err = db.ExecContext(ctx, `
				UPDATE settings SET value = ?, updated = unixepoch() WHERE key = ?
			`, envValue, settingKey)
			if err != nil {
				return err
			}
			slog.Info("migrated setting from environment variable", "setting", settingKey, "env", envKey)
		}
	}

	return nil
}
