package oauth2

import "github.com/TheLab-ms/conway/engine/config"

// ConfigSpec returns the OAuth2 provider configuration specification (read-only documentation).
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "oauth2",
		Title:       "OAuth2 Provider",
		ReadOnly:    true,
		Order:       50,
		Description: configDescription(),
		InfoContent: configInfoContent(m.self.String()),
	}
}
