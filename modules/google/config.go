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
		Title:       "Google Login",
		Description: `<strong>How Google Login Works</strong>
<ul class="mb-0 mt-2">
	<li><strong>OAuth2 Login:</strong> Members can sign in with their Google account instead of using email-based passwordless login.</li>
	<li><strong>Account Linking:</strong> On first login, the member's Google account is linked to their Conway member record by email address.</li>
</ul>`,
		Type: Config{},
		Sections: []config.SectionDef{
			{
				Name:        "oauth",
				Title:       "OAuth2 Configuration",
				Description: `Create an OAuth2 application at the <a href="https://console.cloud.google.com/apis/credentials" target="_blank" rel="noopener">Google Cloud Console</a>. Set the authorized redirect URI to <code>[your-domain]/login/google/callback</code>.`,
			},
		},
		Order: 11,
	}
}
