package metrics

import "github.com/TheLab-ms/conway/engine/config"

// ChartConfig holds configuration for a single metrics chart.
type ChartConfig struct {
	Title  string `json:"title" config:"label=Title,required,placeholder=e.g. Active Members"`
	Series string `json:"series" config:"label=Metric Series,required,placeholder=e.g. active-members,help=The metric series name to query. Must match a series in the metrics table."`
	Color  string `json:"color" config:"label=Color,placeholder=e.g. #0d6efd,help=Hex color for the chart line. Leave blank for default teal."`
}

// Config holds metrics dashboard configuration.
type Config struct {
	Charts []ChartConfig `json:"charts" config:"label=Charts,item=Chart,key=Title"`
}

// ConfigSpec returns the metrics configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module: "metrics",
		Title:  "Metrics",
		Type:   Config{},
		ArrayFields: []config.ArrayFieldDef{
			{
				FieldName: "Charts",
				Label:     "Charts",
				ItemLabel: "Chart",
				Help:      "Configure which metric series are displayed on the metrics dashboard and how they appear.",
				KeyField:  "Title",
			},
		},
		Order: 50,
	}
}
