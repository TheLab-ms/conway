package auth

import "github.com/TheLab-ms/conway/engine/config"

// OAuthConfig holds Google and Discord OAuth credentials for login.
type OAuthConfig struct {
	GoogleClientID     string `json:"google_client_id" config:"label=Client ID,section=google"`
	GoogleClientSecret string `json:"google_client_secret" config:"label=Client Secret,secret,section=google"`

	DiscordClientID     string `json:"discord_client_id" config:"label=Client ID,section=discord"`
	DiscordClientSecret string `json:"discord_client_secret" config:"label=Client Secret,secret,section=discord"`
}

func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "auth",
		Title:       "Login / OAuth",
		Description: "Configure Google and Discord OAuth for member login.",
		Type:        OAuthConfig{},
		Sections: []config.SectionDef{
			{Name: "google", Title: "Google OAuth", Description: "Create credentials at https://console.cloud.google.com/apis/credentials. Set the authorized redirect URI to {your-domain}/login/google/callback."},
			{Name: "discord", Title: "Discord OAuth (Login)", Description: "Create an application at https://discord.com/developers/applications. Set the redirect URI to {your-domain}/login/discord/callback. This is separate from the Discord bot integration used for role sync."},
		},
		Order: 1,
	}
}
