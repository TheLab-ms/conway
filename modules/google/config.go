package google

import "github.com/TheLab-ms/conway/engine/config"

// Config holds Google OAuth-related configuration.
type Config struct {
	ClientID     string `json:"client_id" config:"label=Client ID,section=oauth,help=The Client ID from Google Cloud Console."`
	ClientSecret string `json:"client_secret" config:"label=Client Secret,secret,section=oauth,help=Keep this confidential."`
}

// ConfigSpec returns the Google configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "google",
		Title:       "Google",
		Description: configDescription(),
		Type:        Config{},
		Sections: []config.SectionDef{
			{
				Name:        "oauth",
				Title:       "OAuth2 Configuration",
				Description: oauthSectionDescription(m.self.String()),
			},
		},
		Order:    11,
		Category: "Integrations",
	}
}
