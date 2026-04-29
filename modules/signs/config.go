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

// FieldDef describes a user-facing form field that the template expects.
// Templates declare their own fields, and the sign form renders them
// dynamically. Field values are passed to the Go template body as
// {{.FieldName}}.
type FieldDef struct {
	// Name is the template variable name (e.g. "MachineName"). Must be a
	// valid Go template identifier (letters/digits/underscores, starts
	// with a letter).
	Name string `json:"name"`

	// Label is the human-readable label shown on the form.
	Label string `json:"label"`

	// Placeholder is optional hint text inside the input.
	Placeholder string `json:"placeholder,omitempty"`

	// Required marks the field as mandatory. The submit handler rejects
	// empty values for required fields.
	Required bool `json:"required,omitempty"`

	// Multiline renders the field as a <textarea> instead of a single-line
	// <input>.
	Multiline bool `json:"multiline,omitempty"`
}

// Template describes a single printable sign template.
//
// Templates use Go text/template syntax over a markdown body.
// The following variables are always available:
//   - {{.DiscordHandle}}: Discord username of the user who initiated the print
//   - {{.Date}}: human-readable date the print was initiated
//
// Additional variables are provided by the template's Fields definitions.
type Template struct {
	Slug        string `json:"slug" config:"label=Slug,required,placeholder=e.g. maintenance,help=URL-safe identifier."`
	Name        string `json:"name" config:"label=Name,required,placeholder=e.g. Out of Service"`
	Description string `json:"description" config:"label=Short Description,placeholder=Shown on the picker page."`
	Orientation string `json:"orientation" config:"label=Orientation,options=portrait|landscape,default=portrait"`
	Body        string `json:"body" config:"label=Body (Markdown + Go template),required,multiline,rows=14,help=Markdown body. Use {{.DiscordHandle}} and {{.Date}} (always available) plus any field names defined below (e.g. {{.MachineName}})."`
	FieldsJSON  string `json:"fields_json,omitempty" config:"label=Fields (JSON),multiline,rows=8,help=JSON array defining form fields. Each object: {\"name\": \"MachineName\"&#44; \"label\": \"Machine name\"&#44; \"placeholder\": \"e.g. Drill Press\"&#44; \"required\": true&#44; \"multiline\": false}. Field names become template variables."`
}

// ParsedFields returns the FieldDef list parsed from the FieldsJSON string.
// Returns nil on empty or malformed JSON.
func (t Template) ParsedFields() []FieldDef {
	if t.FieldsJSON == "" {
		return nil
	}
	var fields []FieldDef
	if err := json.Unmarshal([]byte(t.FieldsJSON), &fields); err != nil {
		return nil
	}
	return fields
}

// mustMarshalFields JSON-encodes a []FieldDef into a string. Panics on error.
func mustMarshalFields(fields []FieldDef) string {
	b, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return string(b)
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
				Help:      "Sign templates members can fill in and print. Body is markdown with Go template syntax. Available variables: {{.DiscordHandle}}, {{.Date}}, plus any custom field names you define in the Fields JSON below.",
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
	FieldsJSON: mustMarshalFields([]FieldDef{
		{
			Name:        "MachineName",
			Label:       "Machine / equipment name",
			Placeholder: "e.g. Bambu Lab Printer 2",
			Required:    true,
		},
		{
			Name:        "Issue",
			Label:       "What's wrong? (1-2 sentences)",
			Placeholder: "Describe the issue clearly so the next person knows what's broken.",
			Required:    true,
			Multiline:   true,
		},
	}),
}
