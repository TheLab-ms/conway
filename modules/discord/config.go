package discord

import (
	"encoding/hex"
	"fmt"

	"github.com/TheLab-ms/conway/engine/config"
)

// Config holds Discord-related configuration.
type Config struct {
	// OAuth2 Configuration
	ClientID     string `json:"client_id" config:"label=Client ID,section=oauth,help=The Application ID from Discord Developer Portal."`
	ClientSecret string `json:"client_secret" config:"label=Client Secret,secret,section=oauth,help=Keep this confidential."`

	// Bot Configuration
	BotToken string `json:"bot_token" config:"label=Bot Token,secret,section=bot"`
	GuildID  string `json:"guild_id" config:"label=Server (Guild) ID,section=bot,help=Right-click your server name in Discord (with Developer Mode enabled) and select \"Copy Server ID\"."`
	RoleID   string `json:"role_id" config:"label=Member Role ID,section=bot,help=The role to assign to paying members. Right-click the role in Server Settings > Roles and select \"Copy Role ID\"."`

	// Sync Settings
	SyncIntervalHours int `json:"sync_interval_hours" config:"label=Full Reconciliation Interval (hours),section=sync,default=24,min=1,max=168,help=How often to fully reconcile all Discord role assignments. Default: 24 hours."`

	// Discount Approval Bot
	ApprovalBotEnabled   bool   `json:"approval_bot_enabled" config:"label=Enabled,section=approvalbot,help=When on, a member requesting a discount posts a Discord message with an Approve button. Inbound interactions are still verified regardless."`
	LeadershipChannelID  string `json:"leadership_channel_id" config:"label=Leadership Channel ID,section=approvalbot,help=Right-click the leadership channel in Discord (Developer Mode on) and choose Copy Channel ID. Discount requests are posted here by the bot. Requires the Bot Token above."`
	ApplicationPublicKey string `json:"application_public_key" config:"label=Application Public Key,section=approvalbot,help=Hex-encoded Ed25519 public key from your Discord application's General Information page. Used to verify inbound button interactions."`

	// Badge-In Notifications
	BadgeNotifyEnabled   bool   `json:"badge_notify_enabled" config:"label=Enabled,section=badgenotify,help=When on, opted-in members trigger a Discord message when they badge into the makerspace."`
	BadgeNotifyChannelID string `json:"badge_notify_channel_id" config:"label=Channel ID,section=badgenotify,help=Right-click a Discord channel (Developer Mode on) and choose Copy Channel ID. Badge-in notifications are posted here by the bot."`
}

// Validate validates the Discord configuration.
func (c *Config) Validate() error {
	if c.SyncIntervalHours < 1 {
		return fmt.Errorf("sync interval must be at least 1 hour")
	}
	if c.SyncIntervalHours > 168 {
		return fmt.Errorf("sync interval cannot exceed 168 hours (1 week)")
	}
	if c.ApplicationPublicKey != "" {
		key, err := hex.DecodeString(c.ApplicationPublicKey)
		if err != nil {
			return fmt.Errorf("application public key must be hex-encoded: %w", err)
		}
		if len(key) != 32 {
			return fmt.Errorf("application public key must decode to 32 bytes (got %d)", len(key))
		}
	}
	if c.BadgeNotifyEnabled && c.BadgeNotifyChannelID == "" {
		return fmt.Errorf("channel ID is required when badge-in notifications are enabled")
	}
	return nil
}

// ConfigSpec returns the Discord configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "discord",
		Title:       "Discord",
		Description: configDescription(),
		Type:        Config{},
		Sections: []config.SectionDef{
			{
				Name:        "oauth",
				Title:       "OAuth2 Configuration",
				Description: oauthSectionDescription(m.self.String()),
			},
			{
				Name:        "bot",
				Title:       "Bot Configuration",
				Description: botSectionDescription(),
			},
			{
				Name:  "sync",
				Title: "Sync Settings",
			},
			{
				Name:        "approvalbot",
				Title:       "Discount Approval Bot",
				Description: approvalBotSectionDescription(m.self.String()),
			},
			{
				Name:        "badgenotify",
				Title:       "Badge-In Notifications",
				Description: badgeNotifySectionDescription(),
			},
		},
		Order:    10,
		Category: "Integrations",
	}
}
