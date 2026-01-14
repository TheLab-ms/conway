package discord

import (
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

	// Notifications
	PrintWebhookURL string `json:"print_webhook_url" config:"label=3D Print Notification Webhook URL,secret,section=webhooks,help=Webhook URL for 3D printer completion and failure notifications."`

	// Sync Settings
	SyncIntervalHours int `json:"sync_interval_hours" config:"label=Full Reconciliation Interval (hours),section=sync,default=24,min=1,max=168,help=How often to fully reconcile all Discord role assignments. Default: 24 hours."`
}

// Validate validates the Discord configuration.
func (c *Config) Validate() error {
	if c.SyncIntervalHours < 1 {
		return fmt.Errorf("sync interval must be at least 1 hour")
	}
	if c.SyncIntervalHours > 168 {
		return fmt.Errorf("sync interval cannot exceed 168 hours (1 week)")
	}
	return nil
}

// ConfigSpec returns the Discord configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "discord",
		Title:       "Discord Integration",
		Description: `<strong>How Discord Integration Works</strong>
<ul class="mb-0 mt-2">
	<li><strong>Account Linking:</strong> Members can link their Discord accounts via OAuth2. This stores their Discord user ID, email, and avatar.</li>
	<li><strong>Role Sync:</strong> A background worker automatically assigns/removes a Discord role based on payment status. Paying members get the role; when payment lapses, it's removed.</li>
	<li><strong>Notifications:</strong> Webhook messages (e.g., 3D printer completion) are sent to configured Discord channels.</li>
</ul>`,
		Type: Config{},
		Sections: []config.SectionDef{
			{
				Name:        "oauth",
				Title:       "OAuth2 Configuration",
				Description: `These settings enable members to link their Discord accounts. Create an OAuth2 application at the <a href="https://discord.com/developers/applications" target="_blank" rel="noopener">Discord Developer Portal</a>.`,
			},
			{
				Name:        "bot",
				Title:       "Bot Configuration",
				Description: `The bot enables Conway to manage Discord roles based on payment status. Create a bot in your Discord application and invite it to your server with the <code>Manage Roles</code> permission. The bot's role must be positioned <strong>above</strong> the member role in Discord's role hierarchy.`,
			},
			{
				Name:        "webhooks",
				Title:       "Notifications",
				Description: `Configure webhooks for Discord notifications. Create a webhook in Discord: Channel Settings > Integrations > Webhooks.`,
			},
			{
				Name:  "sync",
				Title: "Sync Settings",
			},
		},
		Order: 10,
	}
}
