package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// parseSpec parses a Spec's Type struct and returns a ParsedSpec with all
// field metadata extracted from struct tags.
func parseSpec(spec Spec) (*ParsedSpec, error) {
	t := reflect.TypeOf(spec.Type)
	if t == nil {
		// ReadOnly/info-only specs may have no Type; return a ParsedSpec with no fields.
		return &ParsedSpec{Spec: spec}, nil
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("spec.Type must be a struct, got %s", t.Kind())
	}

	parsed := &ParsedSpec{
		Spec: spec,
	}

	// Parse all fields
	allFields := make([]Field, 0, t.NumField())
	arrayFields := make(map[string]*ArrayField)

	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}

		field := parseField(sf)

		// Check if this is an array field
		if sf.Type.Kind() == reflect.Slice && sf.Type.Elem().Kind() == reflect.Struct {
			af := parseArrayField(sf, spec.ArrayFields)
			arrayFields[sf.Name] = af
			parsed.ArrayFields = append(parsed.ArrayFields, *af)
		} else {
			allFields = append(allFields, field)
		}
	}

	// Group fields into sections
	parsed.Sections = groupFieldsIntoSections(allFields, spec.Sections)

	return parsed, nil
}

// parseField extracts field metadata from struct tags.
func parseField(sf reflect.StructField) Field {
	field := Field{
		Name:   sf.Name,
		GoType: sf.Type,
	}

	// Get JSON name
	jsonTag := sf.Tag.Get("json")
	if jsonTag != "" && jsonTag != "-" {
		parts := strings.Split(jsonTag, ",")
		field.JSONName = parts[0]
	}
	if field.JSONName == "" {
		field.JSONName = strings.ToLower(sf.Name)
	}

	// Default field type based on Go type
	switch sf.Type.Kind() {
	case reflect.String:
		field.Type = FieldTypeText
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		field.Type = FieldTypeNumber
	case reflect.Bool:
		field.Type = FieldTypeBool
	default:
		field.Type = FieldTypeText
	}

	// Parse config tag
	configTag := sf.Tag.Get("config")
	if configTag != "" {
		parseConfigTag(configTag, &field)
	}

	// Default label from field name if not set
	if field.Label == "" {
		field.Label = splitCamelCase(sf.Name)
	}

	return field
}

// parseConfigTag parses the config:"..." struct tag.
func parseConfigTag(tag string, field *Field) {
	parts := splitConfigTag(tag)

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Handle boolean flags (no =)
		if !strings.Contains(part, "=") {
			switch part {
			case "secret":
				field.Secret = true
				field.Type = FieldTypePassword
			case "required":
				field.Required = true
			case "multiline":
				field.Type = FieldTypeTextarea
			}
			continue
		}

		// Handle key=value pairs
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key, value := kv[0], kv[1]

		switch key {
		case "label":
			field.Label = value
		case "help":
			field.Help = value
		case "type":
			switch value {
			case "text":
				field.Type = FieldTypeText
			case "password":
				field.Type = FieldTypePassword
			case "number":
				field.Type = FieldTypeNumber
			case "textarea":
				field.Type = FieldTypeTextarea
			case "select":
				field.Type = FieldTypeSelect
			case "bool":
				field.Type = FieldTypeBool
			}
		case "default":
			field.Default = value
		case "min":
			if v, err := strconv.Atoi(value); err == nil {
				field.Min = &v
			}
		case "max":
			if v, err := strconv.Atoi(value); err == nil {
				field.Max = &v
			}
		case "placeholder":
			field.Placeholder = value
		case "rows":
			if v, err := strconv.Atoi(value); err == nil {
				field.Rows = v
			}
		case "options":
			// Parse pipe-separated options: "hourly|daily|weekly"
			opts := strings.Split(value, "|")
			for _, opt := range opts {
				field.Options = append(field.Options, Option{Value: opt, Label: opt})
			}
		case "section":
			field.Section = value
		}
	}
}

// splitConfigTag splits a config tag respecting commas inside values.
// For example: "label=Hello, World,secret" -> ["label=Hello, World", "secret"]
// This is tricky because we need to handle values that might contain commas.
// We use a simple heuristic: if a part contains = and the next part doesn't, they're separate.
func splitConfigTag(tag string) []string {
	var parts []string
	var current strings.Builder
	depth := 0

	for i := 0; i < len(tag); i++ {
		c := tag[i]
		switch c {
		case '(':
			depth++
			current.WriteByte(c)
		case ')':
			depth--
			current.WriteByte(c)
		case ',':
			if depth == 0 {
				parts = append(parts, current.String())
				current.Reset()
			} else {
				current.WriteByte(c)
			}
		default:
			current.WriteByte(c)
		}
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// parseArrayField parses an array field and its item struct.
func parseArrayField(sf reflect.StructField, defs []ArrayFieldDef) *ArrayField {
	af := &ArrayField{
		Name:      sf.Name,
		GoType:    sf.Type,
		ItemLabel: "Item",
	}

	// Get JSON name
	jsonTag := sf.Tag.Get("json")
	if jsonTag != "" && jsonTag != "-" {
		parts := strings.Split(jsonTag, ",")
		af.JSONName = parts[0]
	}
	if af.JSONName == "" {
		af.JSONName = strings.ToLower(sf.Name)
	}

	// Parse config tag for array-level metadata
	configTag := sf.Tag.Get("config")
	if configTag != "" {
		parts := splitConfigTag(configTag)
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if !strings.Contains(part, "=") {
				continue
			}
			kv := strings.SplitN(part, "=", 2)
			if len(kv) != 2 {
				continue
			}
			switch kv[0] {
			case "label":
				af.Label = kv[1]
			case "item":
				af.ItemLabel = kv[1]
			case "help":
				af.Help = kv[1]
			case "key":
				af.KeyField = kv[1]
			case "min":
				if v, err := strconv.Atoi(kv[1]); err == nil {
					af.MinItems = v
				}
			case "max":
				if v, err := strconv.Atoi(kv[1]); err == nil {
					af.MaxItems = v
				}
			}
		}
	}

	// Check for explicit definition in Spec.ArrayFields
	for _, def := range defs {
		if def.FieldName == sf.Name {
			if def.Label != "" {
				af.Label = def.Label
			}
			if def.ItemLabel != "" {
				af.ItemLabel = def.ItemLabel
			}
			if def.Help != "" {
				af.Help = def.Help
			}
			if def.KeyField != "" {
				af.KeyField = def.KeyField
			}
			af.MinItems = def.MinItems
			af.MaxItems = def.MaxItems
			break
		}
	}

	// Default label
	if af.Label == "" {
		af.Label = splitCamelCase(sf.Name)
	}

	// Parse item struct fields
	elemType := sf.Type.Elem()
	for i := 0; i < elemType.NumField(); i++ {
		itemField := elemType.Field(i)
		if !itemField.IsExported() {
			continue
		}
		af.Fields = append(af.Fields, parseField(itemField))
	}

	return af
}

// groupFieldsIntoSections organizes fields into sections based on their section tag
// and the SectionDef order specified in the Spec.
func groupFieldsIntoSections(fields []Field, sectionDefs []SectionDef) []Section {
	// Build a map of section name to section definition
	sectionDefMap := make(map[string]SectionDef)
	for _, def := range sectionDefs {
		sectionDefMap[def.Name] = def
	}

	// Group fields by section tag
	fieldsBySection := make(map[string][]Field)
	var unsectionedFields []Field

	for _, field := range fields {
		sectionName := getFieldSection(field)
		if sectionName == "" {
			unsectionedFields = append(unsectionedFields, field)
		} else {
			fieldsBySection[sectionName] = append(fieldsBySection[sectionName], field)
		}
	}

	// Build sections in order
	var sections []Section

	// First, add sections in the order defined in SectionDefs
	for _, def := range sectionDefs {
		section := Section{
			Name:        def.Name,
			Title:       def.Title,
			Description: def.Description,
		}

		// If explicit field list provided, use it
		if len(def.Fields) > 0 {
			for _, fieldName := range def.Fields {
				for _, f := range fields {
					if f.Name == fieldName {
						section.Fields = append(section.Fields, f)
						break
					}
				}
			}
		} else {
			// Otherwise, use fields that have this section tag
			section.Fields = fieldsBySection[def.Name]
		}

		if len(section.Fields) > 0 {
			sections = append(sections, section)
		}
	}

	// Add any unsectioned fields at the beginning
	if len(unsectionedFields) > 0 {
		// If there are section definitions, prepend unsectioned fields
		if len(sections) > 0 {
			sections = append([]Section{{Fields: unsectionedFields}}, sections...)
		} else {
			// No sections defined, all fields go into one unnamed section
			sections = []Section{{Fields: unsectionedFields}}
		}
	}

	// If no sections at all, put all fields in one section
	if len(sections) == 0 && len(fields) > 0 {
		sections = []Section{{Fields: fields}}
	}

	return sections
}

// getFieldSection returns the section name from the field's Section field.
func getFieldSection(field Field) string {
	return field.Section
}

// splitCamelCase converts "SyncIntervalHours" to "Sync Interval Hours".
func splitCamelCase(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteByte(' ')
		}
		result.WriteRune(r)
	}
	return result.String()
}

// GetFieldValue uses reflection to get a field value from a config struct.
func GetFieldValue(cfg any, fieldName string) any {
	v := reflect.ValueOf(cfg)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	f := v.FieldByName(fieldName)
	if !f.IsValid() {
		return nil
	}
	return f.Interface()
}

// GetStringValue returns a field's value as a string.
func GetStringValue(cfg any, fieldName string) string {
	v := GetFieldValue(cfg, fieldName)
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", val)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", val)
	case float32, float64:
		return fmt.Sprintf("%v", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", val)
	}
}

// HasValue returns true if the field has a non-zero value.
func HasValue(cfg any, fieldName string) bool {
	v := GetFieldValue(cfg, fieldName)
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case string:
		return val != ""
	case int, int8, int16, int32, int64:
		return val != 0
	case uint, uint8, uint16, uint32, uint64:
		return val != 0
	case bool:
		return val
	default:
		return !reflect.ValueOf(v).IsZero()
	}
}
