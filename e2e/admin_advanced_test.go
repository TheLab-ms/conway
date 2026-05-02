package e2e

import (
	"database/sql"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doFormAs performs a POST x-www-form-urlencoded request, optionally with an auth cookie.
// Pass empty token for unauthenticated requests.
func doFormAs(t *testing.T, method, fullURL, token string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, fullURL, strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "token", Value: token})
	}
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	return resp
}

// doGetAs performs a GET request, optionally with an auth cookie.
func doGetAs(t *testing.T, fullURL, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", fullURL, nil)
	require.NoError(t, err)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "token", Value: token})
	}
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	return resp
}

// TestAdmin_StripeCustomerLink verifies that POSTing to the stripe-customer
// endpoint with a configured Stripe API key creates a customer and stores its ID
// on the member record.
//
// Note: The current handler always creates a NEW Stripe customer (it does not
// honor a stripe_customer_id form value). It also requires a real Stripe API
// key, so this test is skipped unless STRIPE_TEST_KEY is set.
func TestAdmin_StripeCustomerLink(t *testing.T) {
	t.Parallel()
	if !stripeTestEnabled() {
		t.Skip("STRIPE_TEST_KEY not set; skipping live Stripe API test")
	}
	env := NewTestEnv(t)
	seedStripeConfig(t, env, getEnvWithFallback("STRIPE_TEST_KEY"), "whsec_test")

	adminID := seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	memberID := seedMember(t, env, "target@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, adminID)

	form := url.Values{}
	form.Set("stripe_customer_id", "cus_ignored")
	resp := doFormAs(t, "POST", env.baseURL+"/admin/members/"+itoa(memberID)+"/stripe-customer", tok, form)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	var custID sql.NullString
	require.NoError(t, env.db.QueryRow(
		"SELECT stripe_customer_id FROM members WHERE id = ?", memberID).Scan(&custID))
	assert.True(t, custID.Valid && strings.HasPrefix(custID.String, "cus_"),
		"expected stripe_customer_id to start with 'cus_', got %q", custID.String)
}

// TestAdmin_StripeCustomerLinkUnauthn verifies the endpoint redirects to /login
// when no auth cookie is provided.
func TestAdmin_StripeCustomerLinkUnauthn(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "target@example.com", WithConfirmed())

	resp := doFormAs(t, "POST",
		env.baseURL+"/admin/members/"+itoa(memberID)+"/stripe-customer",
		"", url.Values{})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode, "expected redirect to /login")
	assert.Contains(t, resp.Header.Get("Location"), "/login")
}

// TestAdmin_StripeCustomerLinkRequiresLeadership verifies non-leadership members
// receive 403.
func TestAdmin_StripeCustomerLinkRequiresLeadership(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	plebID := seedMember(t, env, "pleb@example.com", WithConfirmed())
	memberID := seedMember(t, env, "target@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, plebID)

	resp := doFormAs(t, "POST",
		env.baseURL+"/admin/members/"+itoa(memberID)+"/stripe-customer",
		tok, url.Values{})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// TestAdmin_DBConsoleReadOnlyQuery verifies a SELECT returns result rows
// (rendered as HTML) without modifying any data.
func TestAdmin_DBConsoleReadOnlyQuery(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	seedMember(t, env, "alice@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, adminID)

	form := url.Values{}
	form.Set("query", "SELECT email FROM members ORDER BY id")
	resp := doFormAs(t, "POST", env.baseURL+"/admin/config/dev/db", tok, form)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	assert.Contains(t, html, "admin@example.com")
	assert.Contains(t, html, "alice@example.com")
}

// TestAdmin_DBConsoleWriteQuery verifies a non-SELECT (UPDATE/INSERT) executes
// successfully and the page reflects the rows-affected count.
func TestAdmin_DBConsoleWriteQuery(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	targetID := seedMember(t, env, "target@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, adminID)

	form := url.Values{}
	form.Set("query", "UPDATE members SET name = 'Updated' WHERE id = "+itoa(targetID))
	resp := doFormAs(t, "POST", env.baseURL+"/admin/config/dev/db", tok, form)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify the write actually happened.
	var name string
	require.NoError(t, env.db.QueryRow(
		"SELECT name FROM members WHERE id = ?", targetID).Scan(&name))
	assert.Equal(t, "Updated", name)
}

// TestAdmin_DBConsoleRequiresLeadership verifies non-leadership members are
// rejected with 403.
func TestAdmin_DBConsoleRequiresLeadership(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	plebID := seedMember(t, env, "pleb@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, plebID)

	form := url.Values{}
	form.Set("query", "SELECT 1")
	resp := doFormAs(t, "POST", env.baseURL+"/admin/config/dev/db", tok, form)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// GET path is also leadership-gated.
	resp2 := doGetAs(t, env.baseURL+"/admin/config/dev/db", tok)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp2.StatusCode)
}

// TestAdmin_DBConsoleUnauthn verifies an unauthenticated request redirects to /login.
func TestAdmin_DBConsoleUnauthn(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	resp := doGetAs(t, env.baseURL+"/admin/config/dev/db", "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "/login")

	resp2 := doFormAs(t, "POST", env.baseURL+"/admin/config/dev/db", "", url.Values{"query": {"SELECT 1"}})
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusFound, resp2.StatusCode)
	assert.Contains(t, resp2.Header.Get("Location"), "/login")
}
