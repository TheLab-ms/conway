package badgenotify

// Config is the local representation of badge-in notification settings.
// It is populated by translating fields from discord.Config.
type Config struct {
	Enabled   bool
	ChannelID string
}

func (c *Config) Validate() error {
	return nil
}
