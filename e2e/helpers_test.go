package e2e

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
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

// loginAs authenticates a browser context by setting a valid JWT cookie.
func loginAs(t *testing.T, ctx playwright.BrowserContext, memberID int64) {
	t.Helper()
	token := generateAuthToken(t, memberID)
	err := ctx.AddCookies([]playwright.OptionalCookie{{
		Name:   "token",
		Value:  token,
		Domain: playwright.String("localhost"),
		Path:   playwright.String("/"),
	}})
	require.NoError(t, err, "could not add auth cookie")
}

// loginPageAs authenticates a page by setting a valid JWT cookie on its context.
func loginPageAs(t *testing.T, page playwright.Page, memberID int64) {
	t.Helper()
	token := generateAuthToken(t, memberID)
	err := page.Context().AddCookies([]playwright.OptionalCookie{{
		Name:   "token",
		Value:  token,
		Domain: playwright.String("localhost"),
		Path:   playwright.String("/"),
	}})
	require.NoError(t, err, "could not add auth cookie")
}

// generateAuthToken creates a valid JWT token for the given member ID.
func generateAuthToken(t *testing.T, memberID int64) string {
	t.Helper()
	exp := time.Now().Add(time.Hour * 24)
	token, err := authIssuer.Sign(&jwt.RegisteredClaims{
		Issuer:    "conway",
		Subject:   strconv.FormatInt(memberID, 10),
		Audience:  jwt.ClaimStrings{"conway"},
		ExpiresAt: &jwt.NumericDate{Time: exp},
	})
	require.NoError(t, err, "could not generate auth token")
	return token
}

// generateLoginToken creates a valid login token for testing.
func generateLoginToken(t *testing.T, memberID int64) string {
	t.Helper()
	token, err := authIssuer.Sign(&jwt.RegisteredClaims{
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
	confirmed        bool
	leadership       bool
	nonBillable      bool
	hasWaiver        bool
	fobID            int64
	stripeSubState   string
	stripeCustomerID string
	discountType     string
	discordUserID    string
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

// seedMember creates a test member and returns their ID.
func seedMember(t *testing.T, email string, opts ...MemberOption) int64 {
	t.Helper()

	cfg := &memberConfig{email: email}
	for _, opt := range opts {
		opt(cfg)
	}

	// Insert member
	result, err := testDB.Exec(`
		INSERT INTO members (email, confirmed, leadership, non_billable, fob_id, stripe_subscription_state, stripe_customer_id, discount_type, discord_user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cfg.email, cfg.confirmed, cfg.leadership, cfg.nonBillable,
		sql.NullInt64{Int64: cfg.fobID, Valid: cfg.fobID != 0},
		sql.NullString{String: cfg.stripeSubState, Valid: cfg.stripeSubState != ""},
		sql.NullString{String: cfg.stripeCustomerID, Valid: cfg.stripeCustomerID != ""},
		sql.NullString{String: cfg.discountType, Valid: cfg.discountType != ""},
		sql.NullString{String: cfg.discordUserID, Valid: cfg.discordUserID != ""},
	)
	require.NoError(t, err, "could not insert member")

	memberID, err := result.LastInsertId()
	require.NoError(t, err, "could not get member ID")

	// Optionally sign a waiver
	if cfg.hasWaiver {
		seedWaiver(t, cfg.email)
	}

	return memberID
}

// seedWaiver creates a signed waiver for the given email.
func seedWaiver(t *testing.T, email string) {
	t.Helper()
	_, err := testDB.Exec(`INSERT INTO waivers (version, name, email) VALUES (1, 'Test User', ?)`, email)
	require.NoError(t, err, "could not insert waiver")
}

// seedFobSwipes creates fob swipe history for a member.
func seedFobSwipes(t *testing.T, fobID int64, count int) {
	t.Helper()
	baseTime := time.Now().Unix()
	for i := 0; i < count; i++ {
		_, err := testDB.Exec(`INSERT INTO fob_swipes (uid, timestamp, fob_id) VALUES (?, ?, ?)`,
			fmt.Sprintf("swipe-%d-%d", fobID, i), baseTime-int64(i*60), fobID)
		require.NoError(t, err, "could not insert fob swipe")
	}
}

// seedMemberEvents creates member events for testing.
func seedMemberEvents(t *testing.T, memberID int64, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		_, err := testDB.Exec(`INSERT INTO member_events (member, event, details) VALUES (?, ?, ?)`,
			memberID, fmt.Sprintf("TestEvent%d", i), fmt.Sprintf("Test event details %d", i))
		require.NoError(t, err, "could not insert member event")
	}
}

// seedMetrics creates test metrics data.
func seedMetrics(t *testing.T, series string, count int) {
	t.Helper()
	baseTime := time.Now().Unix()
	for i := 0; i < count; i++ {
		_, err := testDB.Exec(`INSERT INTO metrics (timestamp, series, value) VALUES (?, ?, ?)`,
			float64(baseTime-int64(i*3600)), series, float64(i*10))
		require.NoError(t, err, "could not insert metric")
	}
}

// getLastEmail retrieves the most recent email sent to a recipient.
func getLastEmail(t *testing.T, recipient string) (subject, body string, found bool) {
	t.Helper()
	err := testDB.QueryRow(`SELECT subject, body FROM outbound_mail WHERE recipient = ? ORDER BY id DESC LIMIT 1`, recipient).Scan(&subject, &body)
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

// extractLoginCodeLinkFromEmail parses the short link URL from an email body.
func extractLoginCodeLinkFromEmail(t *testing.T, body string) string {
	t.Helper()
	// Look for /login/code/{code} pattern
	re := regexp.MustCompile(`href="([^"]*\/login\/code\/\d{5})"`)
	matches := re.FindStringSubmatch(body)
	if len(matches) >= 2 {
		return matches[1]
	}
	t.Fatal("could not extract login code link from email body")
	return ""
}

// seedLoginCode creates a login code in the database for testing.
func seedLoginCode(t *testing.T, code string, memberID int64, callback string, expiresAt time.Time) {
	t.Helper()
	token := generateLoginToken(t, memberID)
	var email string
	err := testDB.QueryRow("SELECT email FROM members WHERE id = ?", memberID).Scan(&email)
	require.NoError(t, err, "could not get member email")

	_, err = testDB.Exec(
		"INSERT INTO login_codes (code, token, email, callback, expires_at) VALUES (?, ?, ?, ?, ?)",
		code, token, email, callback, expiresAt.Unix())
	require.NoError(t, err, "could not insert login code")
}

// clearTestData removes all test data from the database between tests.
func clearTestData(t *testing.T) {
	t.Helper()
	tables := []string{"members", "waivers", "fob_swipes", "member_events", "outbound_mail", "metrics", "login_codes"}
	for _, table := range tables {
		_, err := testDB.Exec(fmt.Sprintf("DELETE FROM %s", table))
		if err != nil {
			t.Logf("warning: could not clear table %s: %v", table, err)
		}
	}
}

// expect returns a new PlaywrightAssertions instance for making assertions.
func expect(t *testing.T) playwright.PlaywrightAssertions {
	t.Helper()
	return playwright.NewPlaywrightAssertions()
}

// setupAdminTest creates an admin, logs in, and returns the admin ID and page.
func setupAdminTest(t *testing.T) (adminID int64, page playwright.Page) {
	t.Helper()
	clearTestData(t)
	adminID = seedMember(t, "admin@example.com", WithConfirmed(), WithLeadership())
	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page = newPageInContext(t, ctx)
	return adminID, page
}

// setupMemberTest creates a member with given options, logs in, and returns the member ID and page.
func setupMemberTest(t *testing.T, email string, opts ...MemberOption) (memberID int64, page playwright.Page) {
	t.Helper()
	clearTestData(t)
	memberID = seedMember(t, email, opts...)
	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page = newPageInContext(t, ctx)
	return memberID, page
}

// setupUnauthenticatedTest clears data and returns an unauthenticated page.
func setupUnauthenticatedTest(t *testing.T) playwright.Page {
	t.Helper()
	clearTestData(t)
	return newPage(t)
}
