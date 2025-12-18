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
	"github.com/TheLab-ms/conway/engine/db"
	"github.com/TheLab-ms/conway/modules/admin"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/TheLab-ms/conway/modules/email"
	"github.com/TheLab-ms/conway/modules/fobapi"
	"github.com/TheLab-ms/conway/modules/kiosk"
	"github.com/TheLab-ms/conway/modules/machines"
	"github.com/TheLab-ms/conway/modules/members"
	"github.com/TheLab-ms/conway/modules/metrics"
	"github.com/TheLab-ms/conway/modules/oauth2"
	"github.com/TheLab-ms/conway/modules/payment"
	"github.com/TheLab-ms/conway/modules/pruning"
	"github.com/TheLab-ms/conway/modules/waiver"
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

	// tokenIssuer is used to generate auth tokens for tests
	tokenIssuer *engine.TokenIssuer
	// linksIssuer is used to generate magic link tokens
	linksIssuer *engine.TokenIssuer

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
	testDB, err = db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("could not open test database: %w", err)
	}

	// Configure Stripe test mode
	if key := os.Getenv("STRIPE_TEST_KEY"); key != "" {
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
	tokenIssuer = engine.NewTokenIssuer(filepath.Join(keyDir, "auth.pem"))
	linksIssuer = engine.NewTokenIssuer(filepath.Join(keyDir, "links.pem"))
	oauthIssuer := engine.NewTokenIssuer(filepath.Join(keyDir, "oauth2.pem"))
	fobIssuer := engine.NewTokenIssuer(filepath.Join(keyDir, "fobs.pem"))

	a := engine.NewApp(":18080", router)

	// Auth module (no Turnstile for tests)
	authModule := auth.New(database, self, nil, linksIssuer, tokenIssuer)
	a.Add(authModule)
	a.Router.Authenticator = authModule

	// Email module with no-op sender (emails stored in outbound_mail table)
	a.Add(email.New(database, nil))

	// OAuth2 provider
	a.Add(oauth2.New(database, self, oauthIssuer))

	// Payment module (no webhook key for tests, use Stripe test mode)
	webhookKey := os.Getenv("STRIPE_TEST_WEBHOOK_KEY")
	a.Add(payment.New(database, webhookKey, self))

	// Admin module
	a.Add(admin.New(database, self, linksIssuer))

	// Members module
	a.Add(members.New(database))

	// Waiver module
	a.Add(waiver.New(database))

	// Kiosk module - use "localhost" as SpaceHost for testing
	a.Add(kiosk.New(database, self, fobIssuer, "localhost"))

	// Metrics module
	a.Add(metrics.New(database))

	// Pruning module
	a.Add(pruning.New(database))

	// Fob API module
	a.Add(fobapi.New(database))

	// Machines module with mock printer data for testing
	inUseTime := time.Now().Add(30 * time.Minute).Unix()
	testMachinesModule = machines.NewForTesting([]machines.PrinterStatus{
		{PrinterName: "Printer A", SerialNumber: "test-001"},
		{PrinterName: "Printer B", SerialNumber: "test-002", JobFinishedTimestamp: &inUseTime},
		{PrinterName: "Printer C", SerialNumber: "test-003", ErrorCode: "HMS_0300_0100_0001"},
	})
	a.Add(testMachinesModule)

	// Apply database migrations
	db.MustMigrate(database, db.BaseMigration)

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
