package waiver

import "github.com/TheLab-ms/conway/engine/config"

// AdminConfig holds the waiver content configuration for the admin UI.
type AdminConfig struct {
	Content string `json:"content" config:"label=Waiver Content (Markdown),multiline,required,rows=20,help=<strong>Syntax:</strong><ul class=\"mb-0 mt-1\"><li><code># Title</code> - Sets the page title (first one wins)</li><li>Regular text becomes paragraphs (separate with blank lines)</li><li><code>- [ ] Checkbox text</code> - Creates a required checkbox</li></ul>"`
}

// ConfigSpec returns the waiver configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:    "waiver",
		Title:     "Waiver",
		Type:      AdminConfig{},
		TableName: "waiver_content",
		Order:     1,
	}
}
