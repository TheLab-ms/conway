package templates

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"io"
	"path/filepath"
	"reflect"
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

// LoadTemplatesFromEmbeddedFS loads templates from an embedded filesystem
func LoadTemplatesFromEmbeddedFS(fs embed.FS, dir string) (*template.Template, error) {
	return template.ParseFS(fs, filepath.Join(dir, "*.html"))
}

// RenderToHTML renders a component to HTML and returns it as template.HTML
func RenderToHTML(component Component) (template.HTML, error) {
	var buf bytes.Buffer
	if err := component.Render(nil, &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

// DBQueryHelper provides generic database query functionality
type DBQueryHelper struct {
	db *sql.DB
}

// NewDBQueryHelper creates a new database query helper
func NewDBQueryHelper(db *sql.DB) *DBQueryHelper {
	return &DBQueryHelper{db: db}
}

// QueryRow executes a query that returns a single row and scans it into the provided destination
func (h *DBQueryHelper) QueryRow(ctx context.Context, query string, dest interface{}, args ...interface{}) error {
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	if !rows.Next() {
		return sql.ErrNoRows
	}

	return h.scanRow(rows, dest)
}

// QueryRows executes a query that returns multiple rows and scans them into a slice
func (h *DBQueryHelper) QueryRows(ctx context.Context, query string, dest interface{}, args ...interface{}) error {
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	return h.scanRows(rows, dest)
}

// scanRow scans a single row into a struct using reflection
func (h *DBQueryHelper) scanRow(rows *sql.Rows, dest interface{}) error {
	columns, err := rows.Columns()
	if err != nil {
		return err
	}

	// Get the value and type of the destination
	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("dest must be a pointer to a struct")
	}

	structValue := v.Elem()
	structType := structValue.Type()

	// Create scan destinations
	scanDest := make([]interface{}, len(columns))
	for i, col := range columns {
		field := h.findFieldByDBTag(structType, col)
		if field != nil {
			scanDest[i] = structValue.FieldByName(field.Name).Addr().Interface()
		} else {
			// If no matching field, scan into a dummy variable
			var dummy interface{}
			scanDest[i] = &dummy
		}
	}

	return rows.Scan(scanDest...)
}

// scanRows scans multiple rows into a slice of structs
func (h *DBQueryHelper) scanRows(rows *sql.Rows, dest interface{}) error {
	// dest should be a pointer to a slice
	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Slice {
		return fmt.Errorf("dest must be a pointer to a slice")
	}

	sliceValue := v.Elem()
	elementType := sliceValue.Type().Elem()

	// If element type is pointer, get the underlying type
	if elementType.Kind() == reflect.Ptr {
		elementType = elementType.Elem()
	}

	if elementType.Kind() != reflect.Struct {
		return fmt.Errorf("slice elements must be structs or pointers to structs")
	}

	for rows.Next() {
		// Create a new instance of the element type
		newElement := reflect.New(elementType)
		
		if err := h.scanRow(rows, newElement.Interface()); err != nil {
			return err
		}

		// Add to slice
		if sliceValue.Type().Elem().Kind() == reflect.Ptr {
			sliceValue.Set(reflect.Append(sliceValue, newElement))
		} else {
			sliceValue.Set(reflect.Append(sliceValue, newElement.Elem()))
		}
	}

	return rows.Err()
}

// findFieldByDBTag finds a struct field by its db tag or field name
func (h *DBQueryHelper) findFieldByDBTag(structType reflect.Type, columnName string) *reflect.StructField {
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		
		// Check db tag first
		if dbTag := field.Tag.Get("db"); dbTag != "" {
			if dbTag == columnName {
				return &field
			}
		}
		
		// Fallback to field name (case insensitive)
		if field.Name == columnName {
			return &field
		}
	}
	return nil
}