package e2e

import (
	"bufio"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

// newPage creates a new browser page for a test and registers cleanup.
func newPage(t *testing.T) playwright.Page {
	t.Helper()
	page, err := browser.NewPage()
	require.NoError(t, err, "could not create new page")
	t.Cleanup(func() {
		if err := page.Close(); err != nil {
			t.Logf("warning: could not close page: %v", err)
		}
	})
	return page
}

// newContext creates a new browser context for a test with cleanup.
func newContext(t *testing.T) playwright.BrowserContext {
	t.Helper()
	ctx, err := browser.NewContext()
	require.NoError(t, err, "could not create new context")
	t.Cleanup(func() {
		if err := ctx.Close(); err != nil {
			t.Logf("warning: could not close context: %v", err)
		}
	})
	return ctx
}

// newPageInContext creates a new page within a given browser context.
func newPageInContext(t *testing.T, ctx playwright.BrowserContext) playwright.Page {
	t.Helper()
	page, err := ctx.NewPage()
	require.NoError(t, err, "could not create new page in context")
	t.Cleanup(func() {
		if err := page.Close(); err != nil {
			t.Logf("warning: could not close page: %v", err)
		}
	})
	return page
}

// cookieDomain extracts the hostname from the env's baseURL for cookie setting.
func cookieDomain(env *TestEnv) string {
	u, err := url.Parse(env.baseURL)
	if err != nil {
		return "localhost"
	}
	return u.Hostname()
}

// loginAs authenticates a browser context by setting a valid JWT cookie.
func loginAs(t *testing.T, env *TestEnv, ctx playwright.BrowserContext, memberID int64) {
	t.Helper()
	token := generateAuthToken(t, env, memberID)
	err := ctx.AddCookies([]playwright.OptionalCookie{{
		Name:   "token",
		Value:  token,
		Domain: playwright.String(cookieDomain(env)),
		Path:   playwright.String("/"),
	}})
	require.NoError(t, err, "could not add auth cookie")
}

// loginPageAs authenticates a page by setting a valid JWT cookie on its context.
func loginPageAs(t *testing.T, env *TestEnv, page playwright.Page, memberID int64) {
	t.Helper()
	token := generateAuthToken(t, env, memberID)
	err := page.Context().AddCookies([]playwright.OptionalCookie{{
		Name:   "token",
		Value:  token,
		Domain: playwright.String(cookieDomain(env)),
		Path:   playwright.String("/"),
	}})
	require.NoError(t, err, "could not add auth cookie")
}

// generateAuthToken creates a valid JWT token for the given member ID.
func generateAuthToken(t *testing.T, env *TestEnv, memberID int64) string {
	t.Helper()
	exp := time.Now().Add(time.Hour * 24)
	token, err := env.authIssuer.Sign(&jwt.RegisteredClaims{
		Issuer:    "conway",
		Subject:   strconv.FormatInt(memberID, 10),
		Audience:  jwt.ClaimStrings{"conway"},
		ExpiresAt: &jwt.NumericDate{Time: exp},
	})
	require.NoError(t, err, "could not generate auth token")
	return token
}

// generateLoginToken creates a valid login token for testing.
func generateLoginToken(t *testing.T, env *TestEnv, memberID int64) string {
	t.Helper()
	token, err := env.authIssuer.Sign(&jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(memberID, 10),
		ExpiresAt: &jwt.NumericDate{Time: time.Now().Add(time.Minute * 5)},
	})
	require.NoError(t, err, "could not generate login token")
	return token
}

// MemberOption is a functional option for configuring a test member.
type MemberOption func(*memberConfig)

type memberConfig struct {
	email            string
	name             string
	nameOverride     string
	bio              string
	profilePicture   []byte
	confirmed        bool
	leadership       bool
	nonBillable      bool
	hasWaiver        bool
	fobID            int64
	fobLastSeen      int64
	stripeSubState   string
	stripeCustomerID string
	discountType     string
	discordUserID    string
	discordUsername  string
	discordAvatar    []byte
}

// WithConfirmed marks the member as email-confirmed.
func WithConfirmed() MemberOption {
	return func(c *memberConfig) { c.confirmed = true }
}

// WithLeadership marks the member as leadership.
func WithLeadership() MemberOption {
	return func(c *memberConfig) { c.leadership = true }
}

// WithNonBillable marks the member as non-billable.
func WithNonBillable() MemberOption {
	return func(c *memberConfig) { c.nonBillable = true }
}

// WithWaiver signs a waiver for the member.
func WithWaiver() MemberOption {
	return func(c *memberConfig) { c.hasWaiver = true }
}

// WithFobID sets the member's fob ID.
func WithFobID(fobID int64) MemberOption {
	return func(c *memberConfig) { c.fobID = fobID }
}

// WithActiveStripeSubscription marks the member as having an active Stripe subscription.
func WithActiveStripeSubscription() MemberOption {
	return func(c *memberConfig) {
		c.stripeSubState = "active"
		c.stripeCustomerID = "cus_test_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
}

// WithStripeCustomerID sets the member's Stripe customer ID.
func WithStripeCustomerID(id string) MemberOption {
	return func(c *memberConfig) { c.stripeCustomerID = id }
}

// WithDiscount sets the member's discount type.
func WithDiscount(discountType string) MemberOption {
	return func(c *memberConfig) { c.discountType = discountType }
}

// WithDiscord sets the member's Discord user ID.
func WithDiscord(userID string) MemberOption {
	return func(c *memberConfig) { c.discordUserID = userID }
}

// WithName sets the member's display name.
func WithName(name string) MemberOption {
	return func(c *memberConfig) { c.name = name }
}

// WithDiscordUsername sets the member's Discord username.
func WithDiscordUsername(username string) MemberOption {
	return func(c *memberConfig) { c.discordUsername = username }
}

// WithDiscordAvatar sets the member's Discord avatar (raw bytes).
func WithDiscordAvatar(avatar []byte) MemberOption {
	return func(c *memberConfig) { c.discordAvatar = avatar }
}

// WithBio sets the member's bio text.
func WithBio(bio string) MemberOption {
	return func(c *memberConfig) { c.bio = bio }
}

// WithNameOverride sets the member's custom display name override.
func WithNameOverride(nameOverride string) MemberOption {
	return func(c *memberConfig) { c.nameOverride = nameOverride }
}

// WithProfilePicture sets the member's profile picture (raw bytes).
func WithProfilePicture(picture []byte) MemberOption {
	return func(c *memberConfig) { c.profilePicture = picture }
}

// WithFobLastSeen sets the member's fob last seen timestamp.
func WithFobLastSeen(timestamp int64) MemberOption {
	return func(c *memberConfig) { c.fobLastSeen = timestamp }
}

// WithReadyAccess marks the member as having ready building access.
// This sets non_billable=true and assigns a random fob_id, which makes
// the generated access_status column evaluate to 'Ready'.
func WithReadyAccess() MemberOption {
	return func(c *memberConfig) {
		c.nonBillable = true
		if c.fobID == 0 {
			c.fobID = time.Now().UnixNano() % 1000000
		}
	}
}

// seedMember creates a test member and returns their ID.
func seedMember(t *testing.T, env *TestEnv, email string, opts ...MemberOption) int64 {
	t.Helper()

	cfg := &memberConfig{email: email}
	for _, opt := range opts {
		opt(cfg)
	}

	// Insert member
	result, err := env.db.Exec(`
		INSERT INTO members (email, name, name_override, bio, profile_picture, confirmed, leadership, non_billable, fob_id, fob_last_seen, stripe_subscription_state, stripe_customer_id, discount_type, discord_user_id, discord_username, discord_avatar)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cfg.email,
		cfg.name, // Empty string is fine, column has NOT NULL DEFAULT ''
		sql.NullString{String: cfg.nameOverride, Valid: cfg.nameOverride != ""},
		sql.NullString{String: cfg.bio, Valid: cfg.bio != ""},
		cfg.profilePicture,
		cfg.confirmed, cfg.leadership, cfg.nonBillable,
		sql.NullInt64{Int64: cfg.fobID, Valid: cfg.fobID != 0},
		sql.NullInt64{Int64: cfg.fobLastSeen, Valid: cfg.fobLastSeen != 0},
		sql.NullString{String: cfg.stripeSubState, Valid: cfg.stripeSubState != ""},
		sql.NullString{String: cfg.stripeCustomerID, Valid: cfg.stripeCustomerID != ""},
		sql.NullString{String: cfg.discountType, Valid: cfg.discountType != ""},
		sql.NullString{String: cfg.discordUserID, Valid: cfg.discordUserID != ""},
		sql.NullString{String: cfg.discordUsername, Valid: cfg.discordUsername != ""},
		cfg.discordAvatar,
	)
	require.NoError(t, err, "could not insert member")

	memberID, err := result.LastInsertId()
	require.NoError(t, err, "could not get member ID")

	// Optionally sign a waiver
	if cfg.hasWaiver {
		seedWaiver(t, env, cfg.email)
	}

	return memberID
}

// seedWaiver creates a signed waiver for the given email.
func seedWaiver(t *testing.T, env *TestEnv, email string) {
	t.Helper()
	_, err := env.db.Exec(`INSERT INTO waivers (version, name, email) VALUES (1, 'Test User', ?)`, email)
	require.NoError(t, err, "could not insert waiver")
}

// seedWaiverContent creates waiver content in the database.
func seedWaiverContent(t *testing.T, env *TestEnv, content string) int {
	t.Helper()
	result, err := env.db.Exec(`INSERT INTO waiver_content (content) VALUES (?)`, content)
	require.NoError(t, err, "could not insert waiver content")
	version, err := result.LastInsertId()
	require.NoError(t, err, "could not get waiver content version")
	return int(version)
}

// clearWaiverContent removes all waiver content from the database.
func clearWaiverContent(t *testing.T, env *TestEnv) {
	t.Helper()
	_, err := env.db.Exec(`DELETE FROM waiver_content`)
	if err != nil {
		t.Logf("warning: could not clear waiver_content: %v", err)
	}
}

// seedFobSwipes creates fob swipe history for a member.
func seedFobSwipes(t *testing.T, env *TestEnv, fobID int64, count int) {
	t.Helper()
	baseTime := time.Now().Unix()
	for i := 0; i < count; i++ {
		_, err := env.db.Exec(`INSERT INTO fob_swipes (uid, timestamp, fob_id) VALUES (?, ?, ?)`,
			fmt.Sprintf("swipe-%d-%d", fobID, i), baseTime-int64(i*60), fobID)
		require.NoError(t, err, "could not insert fob swipe")
	}
}

// seedMemberEvents creates member events for testing.
func seedMemberEvents(t *testing.T, env *TestEnv, memberID int64, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		_, err := env.db.Exec(`INSERT INTO member_events (member, event, details) VALUES (?, ?, ?)`,
			memberID, fmt.Sprintf("TestEvent%d", i), fmt.Sprintf("Test event details %d", i))
		require.NoError(t, err, "could not insert member event")
	}
}

// seedMetrics creates test metrics data.
func seedMetrics(t *testing.T, env *TestEnv, series string, count int) {
	t.Helper()
	baseTime := time.Now().Unix()
	for i := 0; i < count; i++ {
		_, err := env.db.Exec(`INSERT INTO metrics (timestamp, series, value) VALUES (?, ?, ?)`,
			float64(baseTime-int64(i*3600)), series, float64(i*10))
		require.NoError(t, err, "could not insert metric")
	}
}

// getLastEmail retrieves the most recent email sent to a recipient.
func getLastEmail(t *testing.T, env *TestEnv, recipient string) (subject, body string, found bool) {
	t.Helper()
	err := env.db.QueryRow(`SELECT subject, body FROM outbound_mail WHERE recipient = ? ORDER BY id DESC LIMIT 1`, recipient).Scan(&subject, &body)
	if err == sql.ErrNoRows {
		return "", "", false
	}
	require.NoError(t, err, "could not query outbound_mail")
	return subject, body, true
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// extractLoginCodeFromEmail parses the 5-digit login code from an email body.
func extractLoginCodeFromEmail(t *testing.T, body string) string {
	t.Helper()
	// Look for 5-digit code pattern in the styled box
	// The code is displayed in a div with letter-spacing
	re := regexp.MustCompile(`>\s*(\d{5})\s*<`)
	matches := re.FindStringSubmatch(body)
	if len(matches) >= 2 {
		return matches[1]
	}
	t.Fatal("could not extract login code from email body")
	return ""
}

// extractLoginCodeLinkFromEmail parses the login link URL from an email body.
func extractLoginCodeLinkFromEmail(t *testing.T, body string) string {
	t.Helper()
	// Look for /login/code?code={code} pattern
	re := regexp.MustCompile(`href="([^"]*\/login\/code\?code=\d{5})"`)
	matches := re.FindStringSubmatch(body)
	if len(matches) >= 2 {
		return matches[1]
	}
	t.Fatal("could not extract login code link from email body")
	return ""
}

// seedLoginCode creates a login code in the database for testing.
func seedLoginCode(t *testing.T, env *TestEnv, code string, memberID int64, callback string, expiresAt time.Time) {
	t.Helper()
	token := generateLoginToken(t, env, memberID)
	var email string
	err := env.db.QueryRow("SELECT email FROM members WHERE id = ?", memberID).Scan(&email)
	require.NoError(t, err, "could not get member email")

	_, err = env.db.Exec(
		"INSERT INTO login_codes (code, token, email, callback, expires_at) VALUES (?, ?, ?, ?, ?)",
		code, token, email, callback, expiresAt.Unix())
	require.NoError(t, err, "could not insert login code")
}

// seedDefaultWaiverContent inserts the default waiver content for tests.
func seedDefaultWaiverContent(t *testing.T, env *TestEnv) {
	t.Helper()
	_, err := env.db.Exec(`INSERT INTO waiver_content (content) VALUES ('# Liability Waiver

This is a sample liability waiver for testing.

1. I acknowledge that participation in activities may involve inherent risks.

2. I understand that I am personally responsible for my safety and actions.

3. I affirm that I am at least 18 years of age.

- [ ] I consent to the use of my electronic signature.
- [ ] I agree to be bound by this waiver.')`)
	if err != nil {
		t.Logf("warning: could not seed default waiver content: %v", err)
	}
}

// expect returns a new PlaywrightAssertions instance for making assertions.
func expect(t *testing.T) playwright.PlaywrightAssertions {
	t.Helper()
	return playwright.NewPlaywrightAssertions()
}

// stripeTestEnabled returns true if Stripe test credentials are configured.
func stripeTestEnabled() bool {
	return os.Getenv("STRIPE_TEST_KEY") != ""
}

// seedStripeConfig inserts Stripe configuration into the database for testing.
func seedStripeConfig(t *testing.T, env *TestEnv, apiKey, webhookKey string) {
	t.Helper()
	_, err := env.db.Exec(`INSERT INTO stripe_config (api_key, webhook_key) VALUES (?, ?)`, apiKey, webhookKey)
	require.NoError(t, err, "could not insert stripe config")
}

// getStripeConfigVersion returns the current version of the stripe config.
func getStripeConfigVersion(t *testing.T, env *TestEnv) int {
	t.Helper()
	var version int
	err := env.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM stripe_config`).Scan(&version)
	require.NoError(t, err, "could not get stripe config version")
	return version
}

// startStripeCLI spawns the Stripe CLI for webhook forwarding and returns when ready.
// It registers a cleanup function to kill the process when the test ends.
// The forwardURL should be the full URL to forward webhooks to (e.g., "localhost:18080/webhooks/stripe").
func startStripeCLI(t *testing.T, forwardURL string) {
	t.Helper()

	apiKey := os.Getenv("STRIPE_TEST_KEY")
	if apiKey == "" {
		t.Fatal("stripe API key not configured - set STRIPE_TEST_KEY")
	}

	cmd := exec.Command("stripe", "listen", "--forward-to", forwardURL, "--api-key", apiKey)

	// Capture stdout to detect when the CLI is ready
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err, "could not create stdout pipe for stripe CLI")

	// Capture stderr for debugging (Stripe CLI outputs to stderr too)
	stderr, err := cmd.StderrPipe()
	require.NoError(t, err, "could not create stderr pipe for stripe CLI")

	err = cmd.Start()
	require.NoError(t, err, "could not start stripe CLI - ensure 'stripe' is installed and authenticated")

	// Register cleanup to kill the process
	t.Cleanup(func() {
		if cmd.Process != nil {
			t.Log("Stopping Stripe CLI webhook forwarding")
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	// Wait for the CLI to be ready by looking for "Ready!" in the output
	ready := make(chan struct{})
	errChan := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			t.Logf("stripe stdout: %s", line)
			if strings.Contains(line, "Ready!") {
				close(ready)
				return
			}
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			t.Logf("stripe stderr: %s", line)
			if strings.Contains(line, "Ready!") {
				close(ready)
				return
			}
		}
	}()

	// Also check for early exit
	go func() {
		err := cmd.Wait()
		if err != nil {
			select {
			case errChan <- fmt.Errorf("stripe CLI exited unexpectedly: %w", err):
			default:
			}
		}
	}()

	// Wait for ready signal or timeout
	select {
	case <-ready:
		t.Log("Stripe CLI webhook forwarding is ready")
	case err := <-errChan:
		t.Fatalf("stripe CLI failed: %v", err)
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timeout waiting for stripe CLI to become ready")
	}
}

// waitForMemberState polls the database until the member's fields match the expected values or times out.
// The check function receives the current stripe_subscription_state and name, and returns true if the condition is met.
func waitForMemberState(t *testing.T, env *TestEnv, email string, timeout time.Duration, check func(subState, name string) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var subState, name sql.NullString
		err := env.db.QueryRow("SELECT stripe_subscription_state, name FROM members WHERE email = ?", email).Scan(&subState, &name)
		if err == nil && check(subState.String, name.String) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for member state: email=%s", email)
}

// setupAdminTest creates an isolated test environment, seeds an admin, logs in,
// and returns the environment, admin ID, and page.
func setupAdminTest(t *testing.T) (env *TestEnv, adminID int64, page playwright.Page) {
	t.Helper()
	env = NewTestEnv(t)
	adminID = seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	ctx := newContext(t)
	loginAs(t, env, ctx, adminID)
	page = newPageInContext(t, ctx)
	return env, adminID, page
}

// setupMemberTest creates an isolated test environment, seeds a member with
// given options, logs in, and returns the environment, member ID, and page.
func setupMemberTest(t *testing.T, email string, opts ...MemberOption) (env *TestEnv, memberID int64, page playwright.Page) {
	t.Helper()
	env = NewTestEnv(t)
	memberID = seedMember(t, env, email, opts...)
	ctx := newContext(t)
	loginAs(t, env, ctx, memberID)
	page = newPageInContext(t, ctx)
	return env, memberID, page
}

// setupUnauthenticatedTest creates an isolated test environment and returns
// the environment and an unauthenticated page.
func setupUnauthenticatedTest(t *testing.T) (env *TestEnv, page playwright.Page) {
	t.Helper()
	env = NewTestEnv(t)
	page = newPage(t)
	return env, page
}

// seedBambuConfig inserts Bambu printer configuration into the database for testing.
func seedBambuConfig(t *testing.T, env *TestEnv, printersJSON string, pollIntervalSecs int) {
	t.Helper()
	_, err := env.db.Exec(`INSERT INTO bambu_config (printers_json, poll_interval_seconds) VALUES (?, ?)`, printersJSON, pollIntervalSecs)
	require.NoError(t, err, "could not insert bambu config")
}

// clearBambuConfig removes all Bambu configuration from the database.
func clearBambuConfig(t *testing.T, env *TestEnv) {
	t.Helper()
	_, err := env.db.Exec(`DELETE FROM bambu_config`)
	if err != nil {
		t.Logf("warning: could not clear bambu_config: %v", err)
	}
}

// getBambuConfigVersion returns the current version of the bambu config.
func getBambuConfigVersion(t *testing.T, env *TestEnv) int {
	t.Helper()
	var version int
	err := env.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM bambu_config`).Scan(&version)
	require.NoError(t, err, "could not get bambu config version")
	return version
}

// getBambuPrintersJSON returns the printers_json from the latest bambu config.
func getBambuPrintersJSON(t *testing.T, env *TestEnv) string {
	t.Helper()
	var printersJSON string
	err := env.db.QueryRow(`SELECT printers_json FROM bambu_config ORDER BY version DESC LIMIT 1`).Scan(&printersJSON)
	if err == sql.ErrNoRows {
		return "[]"
	}
	require.NoError(t, err, "could not get bambu printers json")
	return printersJSON
}

// getBambuPollInterval returns the poll_interval_seconds from the latest bambu config.
func getBambuPollInterval(t *testing.T, env *TestEnv) int {
	t.Helper()
	var pollInterval int
	err := env.db.QueryRow(`SELECT poll_interval_seconds FROM bambu_config ORDER BY version DESC LIMIT 1`).Scan(&pollInterval)
	if err == sql.ErrNoRows {
		return 5 // default
	}
	require.NoError(t, err, "could not get bambu poll interval")
	return pollInterval
}

// seedDiscordConfig inserts Discord configuration into the database for testing.
func seedDiscordConfig(t *testing.T, env *TestEnv, clientID, clientSecret, botToken, guildID, roleID, printWebhookURL string, syncIntervalHours int) {
	t.Helper()
	_, err := env.db.Exec(`INSERT INTO discord_config (client_id, client_secret, bot_token, guild_id, role_id, print_webhook_url, sync_interval_hours) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		clientID, clientSecret, botToken, guildID, roleID, printWebhookURL, syncIntervalHours)
	require.NoError(t, err, "could not insert discord config")
}

// seedGoogleConfig inserts Google configuration into the database for testing.
func seedGoogleConfig(t *testing.T, env *TestEnv, clientID, clientSecret string) {
	t.Helper()
	_, err := env.db.Exec(`INSERT INTO google_config (client_id, client_secret) VALUES (?, ?)`, clientID, clientSecret)
	require.NoError(t, err, "could not insert google config")
}

// refreshPrinterStateTimestamps updates the updated_at timestamp on all printer states
// to ensure they don't expire during long test runs. The machines module only shows
// printers where updated_at > now - (pollInterval * 3).
func refreshPrinterStateTimestamps(t *testing.T, env *TestEnv) {
	t.Helper()
	_, err := env.db.Exec(`UPDATE bambu_printer_state SET updated_at = strftime('%s', 'now')`)
	if err != nil {
		t.Logf("warning: could not refresh printer state timestamps: %v", err)
	}
}
