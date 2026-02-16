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
		},
		Order: 10,
	}
}
