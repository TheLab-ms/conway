// Package e2e contains end-to-end tests using playwright-go.
package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/TheLab-ms/conway/modules/machines"
	"github.com/playwright-community/playwright-go"
	"github.com/stripe/stripe-go/v78"
)

var (
	pw       *playwright.Playwright
	browser  playwright.Browser
	baseURL  string
	testDB   *sql.DB
	appCtx   context.Context
	cancelFn context.CancelFunc

	// authIssuer is used to generate auth/magic link tokens for tests
	authIssuer *engine.TokenIssuer

	// testKeyDir stores generated key files for tests
	testKeyDir string

	// testMachinesModule is the machines module for tests (allows updating printer state)
	testMachinesModule *machines.Module
)

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

	// 3. Launch browser
	headless := os.Getenv("HEADED") != "true"
	browser, err = pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(headless),
	})
	if err != nil {
		fmt.Printf("could not launch browser: %v\n", err)
		os.Exit(1)
	}

	// 4. Setup test database and start server
	if err := setupTestServer(); err != nil {
		fmt.Printf("could not setup test server: %v\n", err)
		os.Exit(1)
	}

	// 5. Run tests
	code := m.Run()

	// 6. Cleanup
	cancelFn()
	browser.Close()
	pw.Stop()
	os.Exit(code)
}

func setupTestServer() error {
	// Create temp directory for test database and key files
	tmpDir, err := os.MkdirTemp("", "conway-e2e-*")
	if err != nil {
		return fmt.Errorf("could not create temp dir: %w", err)
	}
	testKeyDir = tmpDir

	dbPath := filepath.Join(tmpDir, "test.db")
	testDB, err = engine.OpenDB(dbPath)
	if err != nil {
		return fmt.Errorf("could not open test database: %w", err)
	}

	// Configure Stripe test mode
	if key := os.Getenv("STRIPE_TEST_KEY"); key != "" {
		stripe.Key = key
	} else if key := os.Getenv("CONWAY_STRIPE_KEY"); key != "" {
		stripe.Key = key
	}

	// Create app with test config
	baseURL = "http://localhost:18080"
	self, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("could not parse base URL: %w", err)
	}

	app, err := createTestApp(testDB, self, tmpDir)
	if err != nil {
		return fmt.Errorf("could not create test app: %w", err)
	}

	// Start server in background
	appCtx, cancelFn = context.WithCancel(context.Background())
	go app.Run(appCtx)

	// Wait for server to be ready
	return waitForServer(baseURL + "/login")
}

func createTestApp(database *sql.DB, self *url.URL, keyDir string) (*engine.App, error) {
	router := engine.NewRouter()

	// Create token issuers in test directory
	authIssuer = engine.NewTokenIssuer(filepath.Join(keyDir, "auth.pem"))
	oauthIssuer := engine.NewTokenIssuer(filepath.Join(keyDir, "oauth2.pem"))
	fobIssuer := engine.NewTokenIssuer(filepath.Join(keyDir, "fobs.pem"))

	// Machines module with mock printer data for testing
	inUseTime := time.Now().Add(30 * time.Minute).Unix()
	testMachinesModule = machines.NewForTesting([]machines.PrinterStatus{
		{PrinterName: "Printer A", SerialNumber: "test-001"},
		{PrinterName: "Printer B", SerialNumber: "test-002", JobFinishedTimestamp: &inUseTime},
		{PrinterName: "Printer C", SerialNumber: "test-003", ErrorCode: "HMS_0300_0100_0001"},
	})

	a := engine.NewApp(":18080", router)

	// Create the auth module first and set it as the authenticator BEFORE registering other modules.
	// This ensures that when modules call router.WithAuthn(), they get the real authenticator
	// instead of the noopAuthenticator default.
	authModule := auth.New(database, self, nil, authIssuer)
	a.Router.Authenticator = authModule

	modules.Register(a, modules.Options{
		Database:         database,
		Self:             self,
		AuthIssuer:       authIssuer,
		OAuthIssuer:      oauthIssuer,
		FobIssuer:        fobIssuer,
		Turnstile:        nil, // No Turnstile for tests
		EmailSender:      nil, // Emails stored in outbound_mail table
		StripeWebhookKey: getEnvWithFallback("STRIPE_TEST_WEBHOOK_KEY", "CONWAY_STRIPE_WEBHOOK_KEY"),
		SpaceHost:        "localhost",
		MachinesModule:   testMachinesModule,
	})

	return a, nil
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
