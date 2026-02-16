package fobapi

import (
	"context"
	"log/slog"

	"github.com/TheLab-ms/conway/engine/config"
	"github.com/a-h/templ"
)

// ConfigSpec returns the Fob API configuration specification (read-only documentation).
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "fobapi",
		Title:       "Building Access Control API",
		ReadOnly:    true,
		Order:       40,
		Description: configDescription(),
		InfoContent: configInfoContent(m.self.String()),
		ExtraContent: func(ctx context.Context) templ.Component {
			clients, err := m.listClients(ctx)
			if err != nil {
				slog.Error("failed to list fob clients", "error", err)
				clients = nil
			}
			return renderControllersCard(clients)
		},
	}
}
