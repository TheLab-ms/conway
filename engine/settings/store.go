package settings

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
)

// FieldType represents the type of a settings field.
type FieldType int

const (
	FieldTypeText FieldType = iota
	FieldTypePassword
	FieldTypeTextArea
)

// Field describes a single settings field for the admin UI.
type Field struct {
	Key         string
	Label       string
	Description string
	Sensitive   bool
	Type        FieldType
}

// Section describes a group of settings fields for the admin UI.
type Section struct {
	Title  string
	Fields []Field
}

// Store manages application settings with change notification.
type Store struct {
	db        *sql.DB
	mu        sync.RWMutex
	callbacks map[string][]func(string)
	sections  []Section
}

// New creates a settings store.
func New(db *sql.DB) *Store {
	return &Store{
		db:        db,
		callbacks: make(map[string][]func(string)),
	}
}

// Get retrieves a setting value. Returns empty string if not found.
func (s *Store) Get(ctx context.Context, key string) string {
	var value string
	s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	return value
}

// Set updates a setting and notifies all registered callbacks.
func (s *Store) Set(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated) VALUES (?, ?, unixepoch())
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated = excluded.updated
	`, key, value)
	if err != nil {
		return err
	}

	s.mu.RLock()
	cbs := s.callbacks[key]
	s.mu.RUnlock()

	for _, cb := range cbs {
		cb(value)
	}

	slog.Info("setting updated", "key", key)
	return nil
}

// Watch registers a callback for when a setting changes.
// The callback is also invoked immediately with the current value.
func (s *Store) Watch(ctx context.Context, key string, cb func(string)) {
	s.mu.Lock()
	s.callbacks[key] = append(s.callbacks[key], cb)
	s.mu.Unlock()

	// Invoke with current value
	cb(s.Get(ctx, key))
}

// RegisterSection registers a settings section for the admin UI.
// Modules call this to define their settings fields.
func (s *Store) RegisterSection(section Section) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sections = append(s.sections, section)
}

// Sections returns all registered settings sections.
func (s *Store) Sections() []Section {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sections
}

// SectionValues returns all registered sections with current values populated.
func (s *Store) SectionValues(ctx context.Context) []SectionWithValues {
	s.mu.RLock()
	sections := s.sections
	s.mu.RUnlock()

	var result []SectionWithValues
	for _, section := range sections {
		swv := SectionWithValues{Title: section.Title}
		for _, field := range section.Fields {
			fwv := FieldWithValue{
				Field: field,
			}
			value := s.Get(ctx, field.Key)
			if field.Sensitive {
				fwv.IsSet = value != ""
			} else {
				fwv.Value = value
			}
			swv.Fields = append(swv.Fields, fwv)
		}
		result = append(result, swv)
	}
	return result
}

// SectionWithValues is a Section with current values.
type SectionWithValues struct {
	Title  string
	Fields []FieldWithValue
}

// FieldWithValue is a Field with its current value.
type FieldWithValue struct {
	Field
	Value string
	IsSet bool // For sensitive fields: true if value exists
}

// Setting represents a configuration setting for display.
type Setting struct {
	Key         string
	Value       string
	Module      string
	Sensitive   bool
	Description string
	IsSet       bool // For sensitive fields: true if value exists
}

// ModuleGroup represents settings grouped by module.
type ModuleGroup struct {
	Name     string
	Title    string
	Settings []Setting
}

var moduleOrder = []string{"core", "payment", "discord", "email", "auth", "machines", "access_controller", "kiosk"}

var moduleTitles = map[string]string{
	"core":              "Core Settings",
	"payment":           "Stripe",
	"discord":           "Discord",
	"email":             "Email",
	"auth":              "Cloudflare Turnstile",
	"machines":          "Bambu Lab",
	"access_controller": "Generic Access Controller",
	"kiosk":             "Kiosk",
}

// GetAllGrouped returns all settings grouped by module for the admin UI.
func (s *Store) GetAllGrouped(ctx context.Context) ([]ModuleGroup, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT key, value, module, sensitive, description FROM settings ORDER BY module, key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byModule := make(map[string][]Setting)
	for rows.Next() {
		var set Setting
		var sensitive int
		if err := rows.Scan(&set.Key, &set.Value, &set.Module, &sensitive, &set.Description); err != nil {
			return nil, err
		}
		set.Sensitive = sensitive == 1

		// Mask sensitive values
		if set.Sensitive && set.Value != "" {
			set.IsSet = true
			set.Value = ""
		}

		byModule[set.Module] = append(byModule[set.Module], set)
	}

	var groups []ModuleGroup
	for _, module := range moduleOrder {
		if settings, ok := byModule[module]; ok {
			title := moduleTitles[module]
			if title == "" {
				title = module
			}
			groups = append(groups, ModuleGroup{
				Name:     module,
				Title:    title,
				Settings: settings,
			})
		}
	}

	return groups, nil
}

// IsSensitive checks if a setting key is marked as sensitive.
func (s *Store) IsSensitive(ctx context.Context, key string) bool {
	var sensitive int
	s.db.QueryRowContext(ctx, "SELECT sensitive FROM settings WHERE key = ?", key).Scan(&sensitive)
	return sensitive == 1
}
