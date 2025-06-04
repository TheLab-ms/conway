package templates

import (
	"context"
	"html/template"
	"io"
	"path/filepath"
)

// Renderer provides template rendering functionality using Go's html/template
type Renderer struct {
	templates map[string]*template.Template
}

// Component represents a template component that can be rendered
type Component interface {
	Render(ctx context.Context, w io.Writer) error
}

// TemplateComponent implements Component interface for html/template rendering
type TemplateComponent struct {
	Template *template.Template
	Data     interface{}
}

// Render renders the template component to the writer
func (tc *TemplateComponent) Render(ctx context.Context, w io.Writer) error {
	return tc.Template.Execute(w, tc.Data)
}

// NewRenderer creates a new template renderer
func NewRenderer() *Renderer {
	return &Renderer{
		templates: make(map[string]*template.Template),
	}
}

// LoadTemplate loads a template from the given path
func (r *Renderer) LoadTemplate(name, path string) error {
	tmpl, err := template.ParseFiles(path)
	if err != nil {
		return err
	}
	r.templates[name] = tmpl
	return nil
}

// LoadTemplateFromString loads a template from a string
func (r *Renderer) LoadTemplateFromString(name, content string) error {
	tmpl, err := template.New(name).Parse(content)
	if err != nil {
		return err
	}
	r.templates[name] = tmpl
	return nil
}

// Execute creates a component that will render the named template with the given data
func (r *Renderer) Execute(name string, data interface{}) Component {
	tmpl := r.templates[name]
	if tmpl == nil {
		// Return a component that will error when rendered
		return &TemplateComponent{
			Template: template.Must(template.New("error").Parse("Template not found: {{.}}")),
			Data:     name,
		}
	}
	return &TemplateComponent{
		Template: tmpl,
		Data:     data,
	}
}

// Global renderer instance
var globalRenderer = NewRenderer()

// LoadGlobalTemplate loads a template into the global renderer
func LoadGlobalTemplate(name, path string) error {
	return globalRenderer.LoadTemplate(name, path)
}

// LoadGlobalTemplateFromString loads a template from string into the global renderer
func LoadGlobalTemplateFromString(name, content string) error {
	return globalRenderer.LoadTemplateFromString(name, content)
}

// Execute creates a component using the global renderer
func Execute(name string, data interface{}) Component {
	return globalRenderer.Execute(name, data)
}

// Helper function to create template components from string templates
func ComponentFromString(tmplStr string, data interface{}) Component {
	tmpl := template.Must(template.New("inline").Parse(tmplStr))
	return &TemplateComponent{
		Template: tmpl,
		Data:     data,
	}
}

// Helper function to load templates from a directory
func LoadTemplatesFromDir(dir string) error {
	pattern := filepath.Join(dir, "*.html")
	templates, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	
	for _, tmplPath := range templates {
		name := filepath.Base(tmplPath)
		name = name[:len(name)-len(filepath.Ext(name))] // remove .html extension
		if err := LoadGlobalTemplate(name, tmplPath); err != nil {
			return err
		}
	}
	
	return nil
}