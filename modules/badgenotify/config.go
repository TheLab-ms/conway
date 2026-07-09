package badgenotify

import (
	"fmt"

	"github.com/TheLab-ms/conway/engine/config"
)

// Config controls the badge-in Discord notification feature.
type Config struct {
	Enabled   bool   `json:"enabled" config:"label=Enabled,section=general,help=When on, opted-in members trigger a Discord message when they badge into the makerspace."`
	ChannelID string `json:"channel_id" config:"label=Channel ID,section=general,help=Right-click a Discord channel (Developer Mode on) and choose Copy Channel ID. Badge-in notifications are posted here by the bot."`
}

func (c *Config) Validate() error {
	if c.Enabled && c.ChannelID == "" {
		return fmt.Errorf("channel ID is required when badge-in notifications are enabled")
	}
	return nil
}

func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "badgenotify",
		Title:       "Badge-In Notifications",
		Description: badgeNotifyConfigDescription(),
		Type:        Config{},
		Sections: []config.SectionDef{
			{
				Name:        "general",
				Title:       "General",
				Description: badgeNotifySectionDescription(),
			},
		},
		Order:    11,
		Category: "Integrations",
	}
}
