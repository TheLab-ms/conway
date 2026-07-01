package elections

import (
	"context"

	"github.com/TheLab-ms/conway/engine/config"
	"github.com/a-h/templ"
)

func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "elections",
		Title:       "Elections",
		Description: templ.Raw("Create private election links and review tamper-evident vote logs."),
		ReadOnly:    true,
		InfoContent: renderElectionsSettingsIntro(),
		ExtraContent: func(ctx context.Context) templ.Component {
			elections, err := m.listElections(ctx)
			if err != nil {
				return renderElectionsSettingsError(err.Error())
			}
			return renderElectionsSettingsPanel(elections)
		},
		Order: 80,
	}
}
