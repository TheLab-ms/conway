package config

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Test config struct for regular fields
type TestConfig struct {
	APIKey    string `json:"api_key" config:"label=API Key,secret"`
	Endpoint  string `json:"endpoint" config:"label=Endpoint,placeholder=https://api.example.com"`
	Timeout   int    `json:"timeout" config:"label=Timeout (seconds),default=30,min=1,max=300,section=advanced"`
	Enabled   bool   `json:"enabled" config:"label=Enabled"`
	LogLevel  string `json:"log_level" config:"label=Log Level,options=debug|info|warn|error,section=advanced"`
	Notes     string `json:"notes" config:"label=Notes,multiline,rows=5"`
}

// Test config struct for array fields
type PrinterConfig struct {
	Name       string `json:"name" config:"label=Name,required"`
	Host       string `json:"host" config:"label=Host,required"`
	AccessCode string `json:"access_code" config:"label=Access Code,secret"`
	Serial     string `json:"serial" config:"label=Serial Number,required"`
}

type PrintersConfig struct {
	Printers         []PrinterConfig `json:"printers" config:"label=Printers,item=Printer,key=Serial"`
	PollIntervalSecs int             `json:"poll_interval_secs" config:"label=Poll Interval,default=5,min=1,max=60"`
}

func setupTestDB(t *testing.T) *sql.DB {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	// Create the test_config table for TestConfig
	_, err = db.Exec(`
		CREATE TABLE test_config (
			version INTEGER PRIMARY KEY AUTOINCREMENT,
			created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			api_key TEXT,
			endpoint TEXT,
			timeout INTEGER,
			enabled INTEGER,
			log_level TEXT,
			notes TEXT
		) STRICT
	`)
	if err != nil {
		t.Fatalf("failed to create test_config table: %v", err)
	}

	// Create the printers_config table for PrintersConfig
	_, err = db.Exec(`
		CREATE TABLE printers_config (
			version INTEGER PRIMARY KEY AUTOINCREMENT,
			created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			poll_interval_secs INTEGER,
			printers_json TEXT
		) STRICT
	`)
	if err != nil {
		t.Fatalf("failed to create printers_config table: %v", err)
	}

	return db
}

func TestParseSpec_RegularFields(t *testing.T) {
	spec := Spec{
		Module: "test",
		Title:  "Test Config",
		Type:   TestConfig{},
		Sections: []SectionDef{
			{Name: "advanced", Title: "Advanced Settings"},
		},
	}

	parsed, err := parseSpec(spec)
	if err != nil {
		t.Fatalf("parseSpec failed: %v", err)
	}

	// Check that we have sections
	if len(parsed.Sections) == 0 {
		t.Fatal("expected sections to be populated")
	}

	// Find fields by name
	fieldsByName := make(map[string]Field)
	for _, section := range parsed.Sections {
		for _, f := range section.Fields {
			fieldsByName[f.Name] = f
		}
	}

	// Check API Key field
	apiKey, ok := fieldsByName["APIKey"]
	if !ok {
		t.Error("APIKey field not found")
	} else {
		if apiKey.Label != "API Key" {
			t.Errorf("APIKey label = %q, want %q", apiKey.Label, "API Key")
		}
		if !apiKey.Secret {
			t.Error("APIKey should be secret")
		}
		if apiKey.Type != FieldTypePassword {
			t.Errorf("APIKey type = %q, want %q", apiKey.Type, FieldTypePassword)
		}
	}

	// Check Timeout field
	timeout, ok := fieldsByName["Timeout"]
	if !ok {
		t.Error("Timeout field not found")
	} else {
		if timeout.Default != "30" {
			t.Errorf("Timeout default = %q, want %q", timeout.Default, "30")
		}
		if timeout.Min == nil || *timeout.Min != 1 {
			t.Errorf("Timeout min = %v, want 1", timeout.Min)
		}
		if timeout.Max == nil || *timeout.Max != 300 {
			t.Errorf("Timeout max = %v, want 300", timeout.Max)
		}
		if timeout.Section != "advanced" {
			t.Errorf("Timeout section = %q, want %q", timeout.Section, "advanced")
		}
	}

	// Check LogLevel field (select)
	logLevel, ok := fieldsByName["LogLevel"]
	if !ok {
		t.Error("LogLevel field not found")
	} else {
		if len(logLevel.Options) != 4 {
			t.Errorf("LogLevel options count = %d, want 4", len(logLevel.Options))
		}
	}

	// Check Notes field (textarea)
	notes, ok := fieldsByName["Notes"]
	if !ok {
		t.Error("Notes field not found")
	} else {
		if notes.Type != FieldTypeTextarea {
			t.Errorf("Notes type = %q, want %q", notes.Type, FieldTypeTextarea)
		}
		if notes.Rows != 5 {
			t.Errorf("Notes rows = %d, want 5", notes.Rows)
		}
	}
}

func TestParseSpec_ArrayFields(t *testing.T) {
	spec := Spec{
		Module: "printers",
		Title:  "Printers Config",
		Type:   PrintersConfig{},
		ArrayFields: []ArrayFieldDef{
			{
				FieldName: "Printers",
				Label:     "Printers",
				ItemLabel: "Printer",
				KeyField:  "Serial",
			},
		},
	}

	parsed, err := parseSpec(spec)
	if err != nil {
		t.Fatalf("parseSpec failed: %v", err)
	}

	if len(parsed.ArrayFields) != 1 {
		t.Fatalf("expected 1 array field, got %d", len(parsed.ArrayFields))
	}

	af := parsed.ArrayFields[0]
	if af.Name != "Printers" {
		t.Errorf("array field name = %q, want %q", af.Name, "Printers")
	}
	if af.Label != "Printers" {
		t.Errorf("array field label = %q, want %q", af.Label, "Printers")
	}
	if af.ItemLabel != "Printer" {
		t.Errorf("array field item label = %q, want %q", af.ItemLabel, "Printer")
	}
	if af.KeyField != "Serial" {
		t.Errorf("array field key field = %q, want %q", af.KeyField, "Serial")
	}
	if len(af.Fields) != 4 {
		t.Errorf("array field has %d fields, want 4", len(af.Fields))
	}

	// Check that AccessCode is marked as secret
	var accessCodeField *Field
	for _, f := range af.Fields {
		if f.Name == "AccessCode" {
			accessCodeField = &f
			break
		}
	}
	if accessCodeField == nil {
		t.Error("AccessCode field not found in array item")
	} else if !accessCodeField.Secret {
		t.Error("AccessCode should be secret")
	}
}

func TestRegistry(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	registry := NewRegistry(db)

	// Register first spec
	err := registry.Register(Spec{
		Module: "test1",
		Title:  "Test 1",
		Type:   TestConfig{},
		Order:  20,
	})
	if err != nil {
		t.Fatalf("Register test1 failed: %v", err)
	}

	// Register second spec
	err = registry.Register(Spec{
		Module: "test2",
		Title:  "Test 2",
		Type:   TestConfig{},
		Order:  10,
	})
	if err != nil {
		t.Fatalf("Register test2 failed: %v", err)
	}

	// Test Get
	spec, ok := registry.Get("test1")
	if !ok {
		t.Error("Get test1 failed")
	}
	if spec.Title != "Test 1" {
		t.Errorf("spec title = %q, want %q", spec.Title, "Test 1")
	}

	// Test List returns sorted by Order
	specs := registry.List()
	if len(specs) != 2 {
		t.Fatalf("List returned %d specs, want 2", len(specs))
	}
	if specs[0].Module != "test2" {
		t.Errorf("first spec module = %q, want %q (lower order)", specs[0].Module, "test2")
	}

	// Test duplicate registration fails
	err = registry.Register(Spec{
		Module: "test1",
		Title:  "Duplicate",
		Type:   TestConfig{},
	})
	if err == nil {
		t.Error("expected error for duplicate registration")
	}
}

func TestStore_SaveAndLoad(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	registry := NewRegistry(db)
	registry.MustRegister(Spec{
		Module: "test",
		Title:  "Test",
		Type:   TestConfig{},
		Sections: []SectionDef{
			{Name: "advanced", Title: "Advanced"},
		},
	})

	store := NewStore(db, registry)
	ctx := context.Background()

	// Save a config
	cfg := &TestConfig{
		APIKey:   "secret-key-123",
		Endpoint: "https://api.example.com",
		Timeout:  60,
		Enabled:  true,
		LogLevel: "info",
		Notes:    "Test notes",
	}

	err := store.Save(ctx, "test", cfg, false)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load it back
	loaded, version, err := store.Load(ctx, "test")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}

	loadedCfg, ok := loaded.(*TestConfig)
	if !ok {
		t.Fatalf("loaded config has wrong type: %T", loaded)
	}

	if loadedCfg.APIKey != "secret-key-123" {
		t.Errorf("APIKey = %q, want %q", loadedCfg.APIKey, "secret-key-123")
	}
	if loadedCfg.Endpoint != "https://api.example.com" {
		t.Errorf("Endpoint = %q, want %q", loadedCfg.Endpoint, "https://api.example.com")
	}
	if loadedCfg.Timeout != 60 {
		t.Errorf("Timeout = %d, want 60", loadedCfg.Timeout)
	}
	if !loadedCfg.Enabled {
		t.Error("Enabled = false, want true")
	}
	if loadedCfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want %q", loadedCfg.LogLevel, "info")
	}
}

func TestStore_SecretPreservation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	registry := NewRegistry(db)
	registry.MustRegister(Spec{
		Module: "test",
		Title:  "Test",
		Type:   TestConfig{},
		Sections: []SectionDef{
			{Name: "advanced", Title: "Advanced"},
		},
	})

	store := NewStore(db, registry)
	ctx := context.Background()

	// Save initial config with secret
	cfg := &TestConfig{
		APIKey:   "original-secret",
		Endpoint: "https://api.example.com",
		Timeout:  30,
	}
	if err := store.Save(ctx, "test", cfg, false); err != nil {
		t.Fatalf("initial Save failed: %v", err)
	}

	// Save again with empty secret but preserveSecrets=true
	cfg2 := &TestConfig{
		APIKey:   "", // Empty - should be preserved
		Endpoint: "https://new-endpoint.com",
		Timeout:  45,
	}
	if err := store.Save(ctx, "test", cfg2, true); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}

	// Load and verify secret was preserved
	loaded, _, err := store.Load(ctx, "test")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	loadedCfg := loaded.(*TestConfig)
	if loadedCfg.APIKey != "original-secret" {
		t.Errorf("APIKey = %q, want %q (preserved)", loadedCfg.APIKey, "original-secret")
	}
	if loadedCfg.Endpoint != "https://new-endpoint.com" {
		t.Errorf("Endpoint = %q, want %q", loadedCfg.Endpoint, "https://new-endpoint.com")
	}
	if loadedCfg.Timeout != 45 {
		t.Errorf("Timeout = %d, want 45", loadedCfg.Timeout)
	}
}

func TestStore_ArrayFieldSaveAndLoad(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	registry := NewRegistry(db)
	registry.MustRegister(Spec{
		Module: "printers",
		Title:  "Printers",
		Type:   PrintersConfig{},
		ArrayFields: []ArrayFieldDef{
			{
				FieldName: "Printers",
				Label:     "Printers",
				ItemLabel: "Printer",
				KeyField:  "Serial",
			},
		},
	})

	store := NewStore(db, registry)
	ctx := context.Background()

	// Save config with array
	cfg := &PrintersConfig{
		PollIntervalSecs: 10,
		Printers: []PrinterConfig{
			{Name: "Printer 1", Host: "192.168.1.100", AccessCode: "secret1", Serial: "SN001"},
			{Name: "Printer 2", Host: "192.168.1.101", AccessCode: "secret2", Serial: "SN002"},
		},
	}

	if err := store.Save(ctx, "printers", cfg, false); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load and verify
	loaded, _, err := store.Load(ctx, "printers")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	loadedCfg := loaded.(*PrintersConfig)
	if loadedCfg.PollIntervalSecs != 10 {
		t.Errorf("PollIntervalSecs = %d, want 10", loadedCfg.PollIntervalSecs)
	}
	if len(loadedCfg.Printers) != 2 {
		t.Fatalf("Printers count = %d, want 2", len(loadedCfg.Printers))
	}
	if loadedCfg.Printers[0].Name != "Printer 1" {
		t.Errorf("Printers[0].Name = %q, want %q", loadedCfg.Printers[0].Name, "Printer 1")
	}
	if loadedCfg.Printers[0].AccessCode != "secret1" {
		t.Errorf("Printers[0].AccessCode = %q, want %q", loadedCfg.Printers[0].AccessCode, "secret1")
	}
}

func TestStore_ArraySecretPreservation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	registry := NewRegistry(db)
	registry.MustRegister(Spec{
		Module: "printers",
		Title:  "Printers",
		Type:   PrintersConfig{},
		ArrayFields: []ArrayFieldDef{
			{
				FieldName: "Printers",
				Label:     "Printers",
				ItemLabel: "Printer",
				KeyField:  "Serial",
			},
		},
	})

	store := NewStore(db, registry)
	ctx := context.Background()

	// Save initial config with secrets
	cfg := &PrintersConfig{
		PollIntervalSecs: 10,
		Printers: []PrinterConfig{
			{Name: "Printer 1", Host: "192.168.1.100", AccessCode: "secret1", Serial: "SN001"},
			{Name: "Printer 2", Host: "192.168.1.101", AccessCode: "secret2", Serial: "SN002"},
		},
	}
	if err := store.Save(ctx, "printers", cfg, false); err != nil {
		t.Fatalf("initial Save failed: %v", err)
	}

	// Save again with empty secrets but same serials (should preserve)
	cfg2 := &PrintersConfig{
		PollIntervalSecs: 15,
		Printers: []PrinterConfig{
			{Name: "Printer 1 Updated", Host: "192.168.1.100", AccessCode: "", Serial: "SN001"}, // Empty secret
			{Name: "Printer 2", Host: "192.168.1.101", AccessCode: "", Serial: "SN002"},         // Empty secret
		},
	}
	if err := store.Save(ctx, "printers", cfg2, true); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}

	// Load and verify secrets were preserved
	loaded, _, err := store.Load(ctx, "printers")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	loadedCfg := loaded.(*PrintersConfig)
	if loadedCfg.Printers[0].AccessCode != "secret1" {
		t.Errorf("Printers[0].AccessCode = %q, want %q (preserved)", loadedCfg.Printers[0].AccessCode, "secret1")
	}
	if loadedCfg.Printers[0].Name != "Printer 1 Updated" {
		t.Errorf("Printers[0].Name = %q, want %q", loadedCfg.Printers[0].Name, "Printer 1 Updated")
	}
	if loadedCfg.Printers[1].AccessCode != "secret2" {
		t.Errorf("Printers[1].AccessCode = %q, want %q (preserved)", loadedCfg.Printers[1].AccessCode, "secret2")
	}
}

func TestStore_ParseFormIntoConfig(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	registry := NewRegistry(db)
	registry.MustRegister(Spec{
		Module: "test",
		Title:  "Test",
		Type:   TestConfig{},
		Sections: []SectionDef{
			{Name: "advanced", Title: "Advanced"},
		},
	})

	store := NewStore(db, registry)

	// Create a mock HTTP request with form data
	formData := url.Values{
		"api_key":   {"my-api-key"},
		"endpoint":  {"https://api.test.com"},
		"timeout":   {"120"},
		"enabled":   {"on"},
		"log_level": {"debug"},
		"notes":     {"Some notes here"},
	}

	req, err := http.NewRequest("POST", "/config", strings.NewReader(formData.Encode()))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	cfg, err := store.ParseFormIntoConfig(req, "test")
	if err != nil {
		t.Fatalf("ParseFormIntoConfig failed: %v", err)
	}

	parsedCfg := cfg.(*TestConfig)
	if parsedCfg.APIKey != "my-api-key" {
		t.Errorf("APIKey = %q, want %q", parsedCfg.APIKey, "my-api-key")
	}
	if parsedCfg.Endpoint != "https://api.test.com" {
		t.Errorf("Endpoint = %q, want %q", parsedCfg.Endpoint, "https://api.test.com")
	}
	if parsedCfg.Timeout != 120 {
		t.Errorf("Timeout = %d, want 120", parsedCfg.Timeout)
	}
	if !parsedCfg.Enabled {
		t.Error("Enabled = false, want true")
	}
	if parsedCfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", parsedCfg.LogLevel, "debug")
	}
}

func TestStore_ParseFormIntoConfig_ArrayFields(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	registry := NewRegistry(db)
	registry.MustRegister(Spec{
		Module: "printers",
		Title:  "Printers",
		Type:   PrintersConfig{},
		ArrayFields: []ArrayFieldDef{
			{
				FieldName: "Printers",
				Label:     "Printers",
				ItemLabel: "Printer",
				KeyField:  "Serial",
			},
		},
	})

	store := NewStore(db, registry)

	// Create a mock HTTP request with array form data
	formData := url.Values{
		"poll_interval_secs": {"15"},
		"printers[0][name]":        {"First Printer"},
		"printers[0][host]":        {"192.168.1.10"},
		"printers[0][access_code]": {"code1"},
		"printers[0][serial]":      {"SERIAL001"},
		"printers[1][name]":        {"Second Printer"},
		"printers[1][host]":        {"192.168.1.11"},
		"printers[1][access_code]": {"code2"},
		"printers[1][serial]":      {"SERIAL002"},
	}

	req, err := http.NewRequest("POST", "/config", strings.NewReader(formData.Encode()))
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	cfg, err := store.ParseFormIntoConfig(req, "printers")
	if err != nil {
		t.Fatalf("ParseFormIntoConfig failed: %v", err)
	}

	parsedCfg := cfg.(*PrintersConfig)
	if parsedCfg.PollIntervalSecs != 15 {
		t.Errorf("PollIntervalSecs = %d, want 15", parsedCfg.PollIntervalSecs)
	}
	if len(parsedCfg.Printers) != 2 {
		t.Fatalf("Printers count = %d, want 2", len(parsedCfg.Printers))
	}
	if parsedCfg.Printers[0].Name != "First Printer" {
		t.Errorf("Printers[0].Name = %q, want %q", parsedCfg.Printers[0].Name, "First Printer")
	}
	if parsedCfg.Printers[0].Serial != "SERIAL001" {
		t.Errorf("Printers[0].Serial = %q, want %q", parsedCfg.Printers[0].Serial, "SERIAL001")
	}
	if parsedCfg.Printers[1].Name != "Second Printer" {
		t.Errorf("Printers[1].Name = %q, want %q", parsedCfg.Printers[1].Name, "Second Printer")
	}
}

func TestGetStringValue(t *testing.T) {
	cfg := &TestConfig{
		APIKey:   "test-key",
		Endpoint: "https://example.com",
		Timeout:  42,
		Enabled:  true,
	}

	tests := []struct {
		fieldName string
		want      string
	}{
		{"APIKey", "test-key"},
		{"Endpoint", "https://example.com"},
		{"Timeout", "42"},
		{"Enabled", "true"},
		{"NonExistent", ""},
	}

	for _, tt := range tests {
		got := GetStringValue(cfg, tt.fieldName)
		if got != tt.want {
			t.Errorf("GetStringValue(%q) = %q, want %q", tt.fieldName, got, tt.want)
		}
	}
}

func TestHasValue(t *testing.T) {
	cfg := &TestConfig{
		APIKey:   "test-key",
		Endpoint: "",
		Timeout:  42,
		Enabled:  false,
	}

	tests := []struct {
		fieldName string
		want      bool
	}{
		{"APIKey", true},
		{"Endpoint", false},
		{"Timeout", true},
		{"Enabled", false},
		{"NonExistent", false},
	}

	for _, tt := range tests {
		got := HasValue(cfg, tt.fieldName)
		if got != tt.want {
			t.Errorf("HasValue(%q) = %v, want %v", tt.fieldName, got, tt.want)
		}
	}
}

func TestSplitCamelCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"APIKey", "A P I Key"},         // Splits on every capital
		{"SyncIntervalHours", "Sync Interval Hours"},
		{"Name", "Name"},
		{"HTTPSEnabled", "H T T P S Enabled"}, // Splits on every capital
	}

	for _, tt := range tests {
		got := splitCamelCase(tt.input)
		if got != tt.want {
			t.Errorf("splitCamelCase(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLoader(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	registry := NewRegistry(db)
	registry.MustRegister(Spec{
		Module: "test",
		Title:  "Test",
		Type:   TestConfig{},
		Sections: []SectionDef{
			{Name: "advanced", Title: "Advanced"},
		},
	})

	store := NewStore(db, registry)
	ctx := context.Background()

	// Save a config first
	cfg := &TestConfig{
		APIKey:   "loader-test-key",
		Endpoint: "https://loader.example.com",
		Timeout:  90,
	}
	if err := store.Save(ctx, "test", cfg, false); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Use the typed loader
	loader := NewLoader[TestConfig](store, "test")

	loadedCfg, err := loader.Load(ctx)
	if err != nil {
		t.Fatalf("Loader.Load failed: %v", err)
	}

	if loadedCfg.APIKey != "loader-test-key" {
		t.Errorf("APIKey = %q, want %q", loadedCfg.APIKey, "loader-test-key")
	}

	// Test LoadWithVersion
	loadedCfg2, version, err := loader.LoadWithVersion(ctx)
	if err != nil {
		t.Fatalf("Loader.LoadWithVersion failed: %v", err)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}
	if loadedCfg2.Timeout != 90 {
		t.Errorf("Timeout = %d, want 90", loadedCfg2.Timeout)
	}
}
