// Package e2e contains end-to-end tests using playwright-go.
package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/TheLab-ms/conway/modules"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/playwright-community/playwright-go"
	"github.com/stripe/stripe-go/v78"
)

var (
	pw      *playwright.Playwright
	browser playwright.Browser
)

// TestEnv holds an isolated test environment with its own database, server, and auth.
// Each test gets its own TestEnv, enabling full parallel execution.
type TestEnv struct {
	baseURL    string
	db         *sql.DB
	authIssuer *engine.TokenIssuer
	cancel     context.CancelFunc
}

func TestMain(m *testing.M) {
	// 1. Install Playwright browsers if needed
	if err := playwright.Install(&playwright.RunOptions{
		Browsers: []string{"chromium"},
	}); err != nil {
		fmt.Printf("could not install playwright: %v\n", err)
		os.Exit(1)
	}

	// 2. Start Playwright
	var err error
	pw, err = playwright.Run()
	if err != nil {
		fmt.Printf("could not start playwright: %v\n", err)
		os.Exit(1)
	}

	// 3. Launch browser (shared across all tests - Playwright browser is thread-safe)
	headless := os.Getenv("HEADED") != "true"
	browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(headless),
	})
	if err != nil {
		fmt.Printf("could not launch browser: %v\n", err)
		os.Exit(1)
	}

	// 4. Configure Stripe test mode (global, but read-only after set)
	if key := os.Getenv("STRIPE_TEST_KEY"); key != "" {
		stripe.Key = key
	}

	// 5. Run tests
	code := m.Run()

	// 6. Cleanup
	browser.Close()
	pw.Stop()
	os.Exit(code)
}

// NewTestEnv creates a fully isolated test environment with its own database,
// HTTP server on an ephemeral port, and token issuers. The environment is
// automatically cleaned up when the test completes.
func NewTestEnv(t *testing.T) *TestEnv {
	t.Helper()

	// Create temp directory for this test's database and key files
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := engine.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("could not open test database: %v", err)
	}

	// Create token issuers with unique key files
	authIssuer := engine.NewTokenIssuer(filepath.Join(tmpDir, "auth.pem"))
	oauthIssuer := engine.NewTokenIssuer(filepath.Join(tmpDir, "oauth2.pem"))
	fobIssuer := engine.NewTokenIssuer(filepath.Join(tmpDir, "fobs.pem"))

	// Listen on an ephemeral port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not listen on ephemeral port: %v", err)
	}
	addr := listener.Addr().String()
	baseURL := "http://" + addr

	self, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("could not parse base URL: %v", err)
	}

	// Create app
	router := engine.NewRouter()
	a := engine.NewApp(addr, router, db)

	authModule := auth.New(db, self, nil, authIssuer)
	a.Router.Authenticator = authModule

	modules.Register(a, modules.Options{
		Database:    db,
		Self:        self,
		AuthIssuer:  authIssuer,
		OAuthIssuer: oauthIssuer,
		FobIssuer:   fobIssuer,
		Turnstile:   nil,
		EmailSender: nil,
		SpaceHost:   "127.0.0.1",
	})

	// Seed printer state
	seedPrinterState(db)

	// Start server on the listener (not using app.Run since we need the listener)
	ctx, cancel := context.WithCancel(context.Background())
	svr := &http.Server{Handler: router}
	go func() {
		svr.Serve(listener)
	}()
	go func() {
		<-ctx.Done()
		svr.Shutdown(context.Background())
	}()

	// Start any background workers from modules (config watchers, etc.)
	// We run the app's ProcMgr minus the HTTP server (which we handle manually).
	// Since we used NewApp which already added Serve, and we're serving separately,
	// we skip ProcMgr.Run to avoid double-binding.

	env := &TestEnv{
		baseURL:    baseURL,
		db:         db,
		authIssuer: authIssuer,
		cancel:     cancel,
	}

	// Wait for server to be ready
	if err := waitForServer(baseURL + "/login"); err != nil {
		t.Fatalf("test server did not become ready: %v", err)
	}

	// Register cleanup
	t.Cleanup(func() {
		cancel()
		db.Close()
	})

	// Seed default waiver content for this environment
	seedDefaultWaiverContent(t, env)

	return env
}

// seedPrinterState inserts mock printer data for e2e tests.
func seedPrinterState(db *sql.DB) {
	inUseTime := time.Now().Add(30 * time.Minute).Unix()

	// Insert test printer states directly into the database
	db.Exec(`INSERT INTO bambu_printer_state
		(serial_number, printer_name, gcode_file, subtask_name, gcode_state,
		 error_code, remaining_print_time, print_percent_done, job_finished_timestamp, stop_requested, updated_at)
		VALUES
		('test-001', 'Printer A', '', '', '', '', 0, 0, NULL, 0, strftime('%s', 'now')),
		('test-002', 'Printer B', 'test.gcode', '@testuser', 'RUNNING', '', 30, 50, $1, 0, strftime('%s', 'now')),
		('test-003', 'Printer C', 'failed.gcode', '', 'FAILED', 'HMS_0300_0100_0001', 0, 0, NULL, 0, strftime('%s', 'now'))`,
		inUseTime)
}

func waitForServer(url string) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("server did not become ready at %s", url)
}

// getEnvWithFallback returns the value of the first env var that is set.
func getEnvWithFallback(keys ...string) string {
	for _, key := range keys {
		if val := os.Getenv(key); val != "" {
			return val
		}
	}
	return ""
}

// NewTestEnvForStripe creates a test environment specifically for Stripe tests.
// It uses a fixed port (18080) because the Stripe CLI forwards webhooks to a fixed URL.
// Stripe tests must not run in parallel with each other.
func NewTestEnvForStripe(t *testing.T) *TestEnv {
	t.Helper()

	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := engine.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("could not open test database: %v", err)
	}

	authIssuer := engine.NewTokenIssuer(filepath.Join(tmpDir, "auth.pem"))
	oauthIssuer := engine.NewTokenIssuer(filepath.Join(tmpDir, "oauth2.pem"))
	fobIssuer := engine.NewTokenIssuer(filepath.Join(tmpDir, "fobs.pem"))

	baseURL := "http://localhost:18080"
	self, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("could not parse base URL: %v", err)
	}

	router := engine.NewRouter()

	// For Stripe tests we need the config registry to be functional
	reg := config.NewRegistry(db)
	_ = reg

	a := engine.NewApp(":18080", router, db)

	authModule := auth.New(db, self, nil, authIssuer)
	a.Router.Authenticator = authModule

	modules.Register(a, modules.Options{
		Database:    db,
		Self:        self,
		AuthIssuer:  authIssuer,
		OAuthIssuer: oauthIssuer,
		FobIssuer:   fobIssuer,
		Turnstile:   nil,
		EmailSender: nil,
		SpaceHost:   "localhost",
	})

	seedPrinterState(db)

	ctx, cancel := context.WithCancel(context.Background())
	go a.Run(ctx)

	env := &TestEnv{
		baseURL:    baseURL,
		db:         db,
		authIssuer: authIssuer,
		cancel:     cancel,
	}

	if err := waitForServer(baseURL + "/login"); err != nil {
		t.Fatalf("test server did not become ready: %v", err)
	}

	t.Cleanup(func() {
		cancel()
		db.Close()
	})

	seedDefaultWaiverContent(t, env)

	return env
}
