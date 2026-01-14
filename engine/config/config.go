// Package config provides a declarative configuration system for modules.
// Modules define their configuration needs using Go structs with tags,
// and the admin UI automatically generates forms for editing them.
package config

import (
	"reflect"
	"time"
)

// FieldType defines the UI input type for a config field.
type FieldType string

const (
	FieldTypeText     FieldType = "text"
	FieldTypePassword FieldType = "password" // Masked input, secret preservation
	FieldTypeNumber   FieldType = "number"
	FieldTypeTextarea FieldType = "textarea"
	FieldTypeSelect   FieldType = "select"
	FieldTypeBool     FieldType = "bool"
)

// Field represents a single configuration field with metadata.
type Field struct {
	Name        string       // Go struct field name
	JSONName    string       // JSON/form field name (from json tag or lowercased)
	Label       string       // Display label
	Help        string       // Help text shown below field
	Type        FieldType    // Input type
	Secret      bool         // If true, value is masked and preserved on empty submit
	Required    bool         // If true, field cannot be empty
	Default     string       // Default value (as string)
	Min         *int         // For numbers: minimum value
	Max         *int         // For numbers: maximum value
	Options     []Option     // For select fields: available options
	Placeholder string       // Input placeholder text
	Rows        int          // For textarea: number of rows
	Section     string       // Section name this field belongs to
	GoType      reflect.Type // The underlying Go type
}

// Option represents a select field option.
type Option struct {
	Value string
	Label string
}

// Section groups related fields together in the UI.
type Section struct {
	Name        string  // Section identifier
	Title       string  // Display title
	Description string  // Section description/help text
	Fields      []Field // Fields in this section
}

// SectionDef defines a section in the Spec.
type SectionDef struct {
	Name        string   // Section identifier
	Title       string   // Display title
	Description string   // Help text for the section
	Fields      []string // Optional: explicit field names (in order) belonging to this section
}

// ArrayFieldDef defines a dynamic array field.
type ArrayFieldDef struct {
	FieldName string // The struct field name
	Label     string // Display label (e.g., "Printers")
	ItemLabel string // Item label (e.g., "Printer")
	Help      string // Help text
	KeyField  string // Field name used for secret preservation matching
	MinItems  int
	MaxItems  int
}

// Spec defines a module's configuration specification.
type Spec struct {
	// Module identifier (used in database table naming and URL paths)
	Module string

	// Display title for the admin UI
	Title string

	// Description/help text shown at the top of the config page
	Description string

	// Type is a zero value of the config struct.
	// Must be a struct or pointer to struct.
	Type any

	// Sections defines how fields are grouped.
	// If empty, fields are assigned to sections based on their `section` tag.
	Sections []SectionDef

	// ArrayFields defines fields that are dynamic arrays.
	// These require special form handling (add/remove items).
	ArrayFields []ArrayFieldDef

	// InfoContent is an optional HTML string for read-only informational content.
	// Used for pages like the Fob API documentation.
	InfoContent string

	// ReadOnly means this config page is informational only (no form).
	ReadOnly bool

	// Order controls display order in the config sidebar (lower = first).
	Order int
}

// ParsedSpec is a Spec with all fields parsed from struct tags.
type ParsedSpec struct {
	Spec
	Sections    []Section    // Parsed sections with fields populated
	ArrayFields []ArrayField // Parsed array field definitions
}

// ArrayField represents a parsed dynamic array field.
type ArrayField struct {
	Name      string    // Go struct field name
	JSONName  string    // JSON field name
	Label     string    // Display label (e.g., "Printers")
	ItemLabel string    // Label for each item (e.g., "Printer")
	Help      string    // Help text
	KeyField  string    // Field name used for secret preservation matching
	MinItems  int       // Minimum required items
	MaxItems  int       // Maximum allowed items (0 = unlimited)
	Fields    []Field   // Fields within each array item
	GoType    reflect.Type
}

// Event represents an event log entry for display.
type Event struct {
	ID         int64
	Created    time.Time
	Module     string
	MemberID   *int64
	EventType  string
	EntityID   string
	EntityName string
	Success    bool
	Details    string
}

// Validatable is implemented by config structs that need custom validation.
type Validatable interface {
	Validate() error
}
