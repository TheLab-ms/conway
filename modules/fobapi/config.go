package fobapi

import "github.com/TheLab-ms/conway/engine/config"

// ConfigSpec returns the Fob API configuration specification (read-only documentation).
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "fobapi",
		Title:       "Fob API (Access Controllers)",
		ReadOnly:    true,
		Order:       40,
		Description: configDescription(),
		InfoContent: configInfoContent(m.self.String()),
	}
}
