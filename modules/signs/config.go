package signs

import (
	"encoding/json"

	"github.com/TheLab-ms/conway/engine/config"
)

// mustMarshalTemplate JSON-encodes a template. Panics on failure (templates
// are constants we control).
func mustMarshalTemplate(t Template) string {
	b, err := json.Marshal(t)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Template describes a single printable sign template.
//
// Templates use Go text/template syntax over a markdown body.
// The following variables are always available:
//   - {{.DiscordHandle}}: Discord username of the user who initiated the print
//   - {{.Date}}: human-readable date the print was initiated
//   - {{.MachineName}}: machine/equipment name (optional, from form)
//   - {{.Issue}}: free-form description (required, from form)
type Template struct {
	Slug        string `json:"slug" config:"label=Slug,required,placeholder=e.g. maintenance,help=URL-safe identifier."`
	Name        string `json:"name" config:"label=Name,required,placeholder=e.g. Out of Service"`
	Description string `json:"description" config:"label=Short Description,placeholder=Shown on the picker page."`
	Orientation string `json:"orientation" config:"label=Orientation,options=portrait|landscape,default=portrait"`
	Body        string `json:"body" config:"label=Body (Markdown + Go template),required,multiline,rows=14,help=Markdown body. Use {{.DiscordHandle}}, {{.Date}}, {{.MachineName}}, {{.Issue}}."`
}

// Config holds the signs module configuration.
type Config struct {
	PrinterHost  string     `json:"printer_host" config:"label=Printer Host/IP,placeholder=e.g. 192.168.1.50,help=LAN-only — IPP traffic is unauthenticated.,section=printer"`
	PrinterPort  int        `json:"printer_port" config:"label=Printer Port,default=631,min=1,max=65535,section=printer"`
	PrinterQueue string     `json:"printer_queue" config:"label=Printer Queue Name,placeholder=e.g. ipp/print,help=The IPP queue path (without leading slash).,section=printer"`
	Templates    []Template `json:"templates" config:"key=Slug"`
}

// ConfigSpec returns the signs module configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:   "signs",
		Title:    "Signs",
		Type:     Config{},
		Order:    40,
		Category: "Integrations",
		ArrayFields: []config.ArrayFieldDef{
			{
				FieldName: "Templates",
				Label:     "Sign Templates",
				ItemLabel: "Template",
				Help:      "Sign templates members can fill in and print. Body is markdown with Go template syntax. Available variables: {{.DiscordHandle}}, {{.Date}}, {{.MachineName}}, {{.Issue}}.",
				KeyField:  "Slug",
			},
		},
		Sections: []config.SectionDef{
			{Name: "printer", Title: "Printer (IPP)"},
		},
	}
}

// DefaultMaintenanceTemplate is the seed template installed on first run.
var DefaultMaintenanceTemplate = Template{
	Slug:        "maintenance",
	Name:        "Out of Service",
	Description: "Mark a machine or piece of equipment as out of service.",
	Orientation: "portrait",
	Body: `# OUT OF SERVICE
{{if .MachineName}}
## {{.MachineName}}
{{end}}
{{.Issue}}

---

Reported by **@{{.DiscordHandle}}**

{{.Date}}
`,
}
