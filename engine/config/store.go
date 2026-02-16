package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Store provides config persistence operations.
type Store struct {
	db       *sql.DB
	registry *Registry
}

// NewStore creates a new config store.
func NewStore(db *sql.DB, registry *Registry) *Store {
	return &Store{
		db:       db,
		registry: registry,
	}
}

// Load retrieves the current config for a module.
// Returns a pointer to the config struct with values populated, the version number,
// and any error. If no config exists, returns a zero value with defaults applied.
func (s *Store) Load(ctx context.Context, module string) (any, int, error) {
	spec, ok := s.registry.Get(module)
	if !ok {
		return nil, 0, fmt.Errorf("unknown module: %s", module)
	}

	// Create new instance of config type
	configType := reflect.TypeOf(spec.Type)
	if configType.Kind() == reflect.Ptr {
		configType = configType.Elem()
	}
	configPtr := reflect.New(configType)

	// Get column names first, before querying the row. This avoids a
	// deadlock when MaxOpenConns is 1: QueryRowContext holds the single
	// connection until Scan is called, so a nested query inside scanRow
	// would block forever waiting for a connection.
	tableName := spec.tableName()
	columns, err := s.getTableColumns(ctx, tableName)
	if err != nil {
		return nil, 0, err
	}

	// Query latest version
	row := s.db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT * FROM %s ORDER BY version DESC LIMIT 1", tableName))

	version, err := s.scanRowWithColumns(row, configPtr, columns, spec)
	if err == sql.ErrNoRows {
		// Return zero value with defaults applied
		applyDefaults(configPtr.Elem(), spec)
		return configPtr.Interface(), 0, nil
	}
	if err != nil {
		return nil, 0, err
	}

	return configPtr.Interface(), version, nil
}

// scanRowWithColumns scans a database row into the config struct using pre-fetched column names.
func (s *Store) scanRowWithColumns(row *sql.Row, configPtr reflect.Value, columns []string, spec *ParsedSpec) (int, error) {
	// Create scan destinations
	scanDests := make([]any, len(columns))
	columnValues := make(map[string]any)

	for i, col := range columns {
		var dest any
		scanDests[i] = &dest
		columnValues[col] = &dest
	}

	if err := row.Scan(scanDests...); err != nil {
		return 0, err
	}

	// Extract version
	var version int
	if v, ok := columnValues["version"]; ok {
		if vPtr, ok := v.(*any); ok {
			if vv, ok := (*vPtr).(int64); ok {
				version = int(vv)
			}
		}
	}

	// Map column values to struct fields
	configVal := configPtr.Elem()
	for _, section := range spec.Sections {
		for _, field := range section.Fields {
			colName := field.JSONName
			if v, ok := columnValues[colName]; ok {
				setFieldFromDB(configVal.FieldByName(field.Name), v, field)
			}
		}
	}

	// Handle array fields
	for _, af := range spec.ArrayFields {
		colName := af.JSONName + "_json"
		if v, ok := columnValues[colName]; ok {
			setArrayFieldFromDB(configVal.FieldByName(af.Name), v, af)
		} else if v, ok := columnValues[af.JSONName]; ok {
			setArrayFieldFromDB(configVal.FieldByName(af.Name), v, af)
		}
	}

	return version, nil
}

// getTableColumns returns the column names for a table.
func (s *Store) getTableColumns(ctx context.Context, tableName string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf("SELECT name FROM pragma_table_info('%s')", tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

// setFieldFromDB sets a struct field from a database value.
func setFieldFromDB(field reflect.Value, dbVal any, fieldMeta Field) {
	if !field.IsValid() || !field.CanSet() {
		return
	}

	// Unwrap pointer
	if ptr, ok := dbVal.(*any); ok {
		if *ptr == nil {
			return
		}
		dbVal = *ptr
	}

	switch field.Kind() {
	case reflect.String:
		if s, ok := dbVal.(string); ok {
			field.SetString(s)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		switch v := dbVal.(type) {
		case int64:
			field.SetInt(v)
		case int:
			field.SetInt(int64(v))
		case float64:
			field.SetInt(int64(v))
		}
	case reflect.Bool:
		switch v := dbVal.(type) {
		case bool:
			field.SetBool(v)
		case int64:
			field.SetBool(v != 0)
		}
	}
}

// setArrayFieldFromDB sets an array field from JSON stored in the database.
func setArrayFieldFromDB(field reflect.Value, dbVal any, af ArrayField) {
	if !field.IsValid() || !field.CanSet() {
		return
	}

	// Unwrap pointer
	if ptr, ok := dbVal.(*any); ok {
		if *ptr == nil {
			return
		}
		dbVal = *ptr
	}

	jsonStr, ok := dbVal.(string)
	if !ok {
		return
	}

	// Create a new slice of the appropriate type
	sliceType := field.Type()
	newSlice := reflect.New(sliceType)

	if err := json.Unmarshal([]byte(jsonStr), newSlice.Interface()); err != nil {
		return
	}

	field.Set(newSlice.Elem())
}

// applyDefaults applies default values from field metadata.
func applyDefaults(configVal reflect.Value, spec *ParsedSpec) {
	for _, section := range spec.Sections {
		for _, field := range section.Fields {
			if field.Default == "" {
				continue
			}
			f := configVal.FieldByName(field.Name)
			if !f.IsValid() || !f.CanSet() || !f.IsZero() {
				continue
			}
			switch f.Kind() {
			case reflect.String:
				f.SetString(field.Default)
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				if v, err := strconv.ParseInt(field.Default, 10, 64); err == nil {
					f.SetInt(v)
				}
			case reflect.Bool:
				f.SetBool(field.Default == "true" || field.Default == "1")
			}
		}
	}
}

// Save stores the config, replacing any existing row.
// If preserveSecrets is true, empty secret fields will keep their existing values.
func (s *Store) Save(ctx context.Context, module string, config any, preserveSecrets bool) error {
	spec, ok := s.registry.Get(module)
	if !ok {
		return fmt.Errorf("unknown module: %s", module)
	}

	configVal := reflect.ValueOf(config)
	if configVal.Kind() == reflect.Ptr {
		configVal = configVal.Elem()
	}

	// If preserving secrets, load existing and merge
	if preserveSecrets {
		existing, _, err := s.Load(ctx, module)
		if err == nil && existing != nil {
			existingVal := reflect.ValueOf(existing)
			if existingVal.Kind() == reflect.Ptr {
				existingVal = existingVal.Elem()
			}
			mergeSecrets(configVal, existingVal, spec)
			filterNewArrayItemsWithoutSecrets(configVal, existingVal, spec)
		} else {
			// No existing config — filter new items with empty secrets
			filterNewArrayItemsWithoutSecrets(configVal, reflect.Value{}, spec)
		}
	}

	// Validate if the config implements Validatable
	if v, ok := config.(Validatable); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("validation failed: %w", err)
		}
	}

	// Build INSERT statement
	columns, values := s.buildInsertParams(configVal, spec)
	tableName := spec.tableName()

	placeholders := make([]string, len(columns))
	for i := range placeholders {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	// Delete the existing row and insert the new one inside a transaction
	// so the config is never missing between the two operations. The version
	// column auto-increments, so each save gets a new version number even
	// though only one row is ever retained. This allows consumers (e.g.
	// machines module) to detect config changes by comparing version numbers.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s", tableName))
	if err != nil {
		return fmt.Errorf("clearing old config: %w", err)
	}

	query := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		tableName,
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "),
	)

	_, err = tx.ExecContext(ctx, query, values...)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// buildInsertParams builds column names and values for INSERT.
func (s *Store) buildInsertParams(configVal reflect.Value, spec *ParsedSpec) ([]string, []any) {
	var columns []string
	var values []any

	// Add regular fields
	for _, section := range spec.Sections {
		for _, field := range section.Fields {
			columns = append(columns, field.JSONName)
			f := configVal.FieldByName(field.Name)
			if f.IsValid() {
				values = append(values, f.Interface())
			} else {
				values = append(values, nil)
			}
		}
	}

	// Add array fields as JSON
	for _, af := range spec.ArrayFields {
		columns = append(columns, af.JSONName+"_json")
		f := configVal.FieldByName(af.Name)
		if f.IsValid() {
			jsonBytes, _ := json.Marshal(f.Interface())
			values = append(values, string(jsonBytes))
		} else {
			values = append(values, "[]")
		}
	}

	return columns, values
}

// mergeSecrets preserves secret field values from existing config when new values are empty.
func mergeSecrets(newVal, existingVal reflect.Value, spec *ParsedSpec) {
	for _, section := range spec.Sections {
		for _, field := range section.Fields {
			if !field.Secret {
				continue
			}
			newField := newVal.FieldByName(field.Name)
			existingField := existingVal.FieldByName(field.Name)
			if !newField.IsValid() || !existingField.IsValid() || !newField.CanSet() {
				continue
			}
			// If new value is empty, preserve existing
			if newField.IsZero() && !existingField.IsZero() {
				newField.Set(existingField)
			}
		}
	}

	// Handle array fields with secrets
	for _, af := range spec.ArrayFields {
		if af.KeyField == "" {
			continue
		}
		preserveArraySecrets(
			newVal.FieldByName(af.Name),
			existingVal.FieldByName(af.Name),
			af,
		)
	}
}

// preserveArraySecrets preserves secrets in array items by matching on KeyField.
func preserveArraySecrets(newSlice, oldSlice reflect.Value, af ArrayField) {
	if !newSlice.IsValid() || !oldSlice.IsValid() {
		return
	}
	if newSlice.Kind() != reflect.Slice || oldSlice.Kind() != reflect.Slice {
		return
	}

	// Build map of old items by key
	oldByKey := make(map[string]reflect.Value)
	for i := 0; i < oldSlice.Len(); i++ {
		item := oldSlice.Index(i)
		keyField := item.FieldByName(af.KeyField)
		if keyField.IsValid() {
			key := fmt.Sprintf("%v", keyField.Interface())
			oldByKey[key] = item
		}
	}

	// For each new item, preserve secrets if key matches
	for i := 0; i < newSlice.Len(); i++ {
		newItem := newSlice.Index(i)
		keyField := newItem.FieldByName(af.KeyField)
		if !keyField.IsValid() {
			continue
		}
		key := fmt.Sprintf("%v", keyField.Interface())
		oldItem, ok := oldByKey[key]
		if !ok {
			continue
		}

		// Preserve secret fields
		for _, field := range af.Fields {
			if !field.Secret {
				continue
			}
			newField := newItem.FieldByName(field.Name)
			oldField := oldItem.FieldByName(field.Name)
			if newField.IsValid() && oldField.IsValid() && newField.CanSet() {
				if newField.IsZero() && !oldField.IsZero() {
					newField.Set(oldField)
				}
			}
		}
	}
}

// filterNewArrayItemsWithoutSecrets removes new array items that have empty secret fields.
// A "new" item is one whose KeyField value doesn't match any existing item.
// This prevents saving incomplete array items (e.g., printers without access codes).
func filterNewArrayItemsWithoutSecrets(newVal, existingVal reflect.Value, spec *ParsedSpec) {
	for _, af := range spec.ArrayFields {
		if af.KeyField == "" {
			continue
		}
		// Check if any fields are secret
		hasSecrets := false
		for _, field := range af.Fields {
			if field.Secret {
				hasSecrets = true
				break
			}
		}
		if !hasSecrets {
			continue
		}

		newSlice := newVal.FieldByName(af.Name)
		if !newSlice.IsValid() || newSlice.Kind() != reflect.Slice {
			continue
		}

		// Build set of existing keys
		existingKeys := make(map[string]bool)
		if existingVal.IsValid() {
			oldSlice := existingVal.FieldByName(af.Name)
			if oldSlice.IsValid() && oldSlice.Kind() == reflect.Slice {
				for i := 0; i < oldSlice.Len(); i++ {
					keyField := oldSlice.Index(i).FieldByName(af.KeyField)
					if keyField.IsValid() {
						existingKeys[fmt.Sprintf("%v", keyField.Interface())] = true
					}
				}
			}
		}

		// Filter: keep items that are existing OR have non-empty secrets
		filtered := reflect.MakeSlice(newSlice.Type(), 0, newSlice.Len())
		for i := 0; i < newSlice.Len(); i++ {
			item := newSlice.Index(i)
			keyField := item.FieldByName(af.KeyField)
			key := ""
			if keyField.IsValid() {
				key = fmt.Sprintf("%v", keyField.Interface())
			}

			// Keep existing items (they already have secrets preserved)
			if existingKeys[key] {
				filtered = reflect.Append(filtered, item)
				continue
			}

			// For new items, check that all secret fields are non-empty
			allSecretsSet := true
			for _, field := range af.Fields {
				if !field.Secret {
					continue
				}
				secretField := item.FieldByName(field.Name)
				if secretField.IsValid() && secretField.IsZero() {
					allSecretsSet = false
					break
				}
			}
			if allSecretsSet {
				filtered = reflect.Append(filtered, item)
			}
		}

		newSlice.Set(filtered)
	}
}

// ParseFormIntoConfig parses an HTTP form into a config struct.
func (s *Store) ParseFormIntoConfig(r *http.Request, module string) (any, error) {
	spec, ok := s.registry.Get(module)
	if !ok {
		return nil, fmt.Errorf("unknown module: %s", module)
	}

	if err := r.ParseForm(); err != nil {
		return nil, err
	}

	// Create new instance
	configType := reflect.TypeOf(spec.Type)
	if configType.Kind() == reflect.Ptr {
		configType = configType.Elem()
	}
	configPtr := reflect.New(configType)
	configVal := configPtr.Elem()

	// Parse regular fields
	for _, section := range spec.Sections {
		for _, field := range section.Fields {
			formValue := r.FormValue(field.JSONName)
			setFieldFromForm(configVal.FieldByName(field.Name), formValue, field)
		}
	}

	// Parse array fields
	for _, af := range spec.ArrayFields {
		parseArrayFieldFromForm(r, configVal.FieldByName(af.Name), af)
	}

	return configPtr.Interface(), nil
}

// setFieldFromForm sets a struct field from a form value.
func setFieldFromForm(field reflect.Value, formValue string, fieldMeta Field) {
	if !field.IsValid() || !field.CanSet() {
		return
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(formValue)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if formValue == "" {
			// Use default if provided
			if fieldMeta.Default != "" {
				if v, err := strconv.ParseInt(fieldMeta.Default, 10, 64); err == nil {
					field.SetInt(v)
				}
			}
		} else if v, err := strconv.ParseInt(formValue, 10, 64); err == nil {
			field.SetInt(v)
		}
	case reflect.Bool:
		field.SetBool(formValue == "on" || formValue == "true" || formValue == "1")
	}
}

// parseArrayFieldFromForm parses an array field from form data.
// Form names are indexed: printer[0][name], printer[0][host], etc.
func parseArrayFieldFromForm(r *http.Request, sliceVal reflect.Value, af ArrayField) {
	if !sliceVal.IsValid() || !sliceVal.CanSet() {
		return
	}

	// Find all indices present in form
	indices := findFormIndices(r.Form, af.JSONName)
	if len(indices) == 0 {
		sliceVal.Set(reflect.MakeSlice(sliceVal.Type(), 0, 0))
		return
	}

	elemType := sliceVal.Type().Elem()
	newSlice := reflect.MakeSlice(sliceVal.Type(), 0, len(indices))

	for _, idx := range indices {
		elem := reflect.New(elemType).Elem()
		prefix := fmt.Sprintf("%s[%d]", af.JSONName, idx)

		for _, field := range af.Fields {
			formKey := fmt.Sprintf("%s[%s]", prefix, field.JSONName)
			formValue := r.FormValue(formKey)
			setFieldFromForm(elem.FieldByName(field.Name), formValue, field)
		}

		newSlice = reflect.Append(newSlice, elem)
	}

	sliceVal.Set(newSlice)
}

// findFormIndices finds all indices used in form data for an array field.
// e.g., for "printer[0][name]" and "printer[2][name]", returns [0, 2]
func findFormIndices(form map[string][]string, prefix string) []int {
	pattern := regexp.MustCompile(fmt.Sprintf(`^%s\[(\d+)\]`, regexp.QuoteMeta(prefix)))
	indexSet := make(map[int]bool)

	for key := range form {
		matches := pattern.FindStringSubmatch(key)
		if len(matches) >= 2 {
			if idx, err := strconv.Atoi(matches[1]); err == nil {
				indexSet[idx] = true
			}
		}
	}

	indices := make([]int, 0, len(indexSet))
	for idx := range indexSet {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	return indices
}

// Loader provides typed config loading for modules.
type Loader[T any] struct {
	store  *Store
	module string
}

// NewLoader creates a typed config loader for a module.
func NewLoader[T any](store *Store, module string) *Loader[T] {
	return &Loader[T]{
		store:  store,
		module: module,
	}
}

// Load retrieves the current config.
func (l *Loader[T]) Load(ctx context.Context) (*T, error) {
	cfg, _, err := l.store.Load(ctx, l.module)
	if err != nil {
		return nil, err
	}
	if result, ok := cfg.(*T); ok {
		return result, nil
	}
	return nil, fmt.Errorf("config type mismatch: expected *%T", new(T))
}

// LoadWithVersion retrieves the current config and its version.
func (l *Loader[T]) LoadWithVersion(ctx context.Context) (*T, int, error) {
	cfg, version, err := l.store.Load(ctx, l.module)
	if err != nil {
		return nil, 0, err
	}
	if result, ok := cfg.(*T); ok {
		return result, version, nil
	}
	return nil, 0, fmt.Errorf("config type mismatch: expected *%T", new(T))
}
