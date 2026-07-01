package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWhoami_Authenticated verifies that /whoami returns a JSON dump of the
// current session's UserMetadata when called with a valid auth cookie.
func TestWhoami_Authenticated(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "whoami@example.com",
		WithConfirmed(), WithLeadership(), WithActiveStripeSubscription())

	req := authedRequest(t, env, memberID, http.MethodGet, "/whoami", nil)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var meta map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&meta))
	assert.Equal(t, "whoami@example.com", meta["Email"])
	assert.Equal(t, true, meta["Leadership"])
	assert.Equal(t, true, meta["ActiveMember"])
	// ID is a JSON number; just sanity check it matches.
	if idF, ok := meta["ID"].(float64); ok {
		assert.Equal(t, memberID, int64(idF))
	}
}

// TestWhoami_Unauthenticated verifies that /whoami without a cookie redirects
// to /login (auth middleware behavior).
func TestWhoami_Unauthenticated(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	resp, err := noRedirectClient().Get(env.baseURL + "/whoami")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "/login")
}

// TestWaiver_RedirectAfterSign verifies that POSTing a signed waiver with an
// `r` query/form param embeds the redirect URL in the success page (waiver uses
// JS-based redirect, not an HTTP 3xx).
func TestWaiver_RedirectAfterSign(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	form := url.Values{}
	form.Set("name", "Jane Doe")
	form.Set("email", "waiver-redir@example.com")
	form.Set("r", "/some/path")
	// Default waiver content has 2 checkboxes.
	form.Set("agree0", "on")
	form.Set("agree1", "on")

	req, err := http.NewRequest(http.MethodPost, env.baseURL+"/waiver",
		strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "/some/path",
		"signed waiver page should embed the configured redirect URL")

	// Confirm the row was actually written.
	var n int
	require.NoError(t, env.db.QueryRow(
		`SELECT COUNT(*) FROM waivers WHERE email = ?`, "waiver-redir@example.com").Scan(&n))
	assert.Equal(t, 1, n)
}

// TestSignupConfirm_EmailFlow drives a brand-new login through the
// signup-confirmation page. POSTing /login for an unknown email renders the
// confirmation HTML; submitting that token to /login/confirm-signup creates
// the member and queues a login code email.
func TestSignupConfirm_EmailFlow(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	// Step 1: POST /login with a brand-new email -> renders signup confirm page.
	form := url.Values{}
	form.Set("email", "new-signup@example.com")
	req, err := http.NewRequest(http.MethodPost, env.baseURL+"/login",
		strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	require.Contains(t, bodyStr, `name="confirm_token"`,
		"signup confirm page should include a confirm_token input")

	// Extract the confirm_token JWT from the hidden input.
	re := regexp.MustCompile(`name="confirm_token"\s+value="([^"]+)"`)
	matches := re.FindStringSubmatch(bodyStr)
	require.Len(t, matches, 2, "could not extract confirm_token")
	confirmToken := matches[1]

	// Member should NOT exist yet.
	var preCount int
	require.NoError(t, env.db.QueryRow(
		`SELECT COUNT(*) FROM members WHERE email = ?`, "new-signup@example.com").Scan(&preCount))
	require.Equal(t, 0, preCount)

	// Step 2: POST /login/confirm-signup with the token.
	form2 := url.Values{}
	form2.Set("confirm_token", confirmToken)
	form2.Set("heard_about", "Friend or member")
	req2, err := http.NewRequest(http.MethodPost, env.baseURL+"/login/confirm-signup",
		strings.NewReader(form2.Encode()))
	require.NoError(t, err)
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp2, err := noRedirectClient().Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	// sendLoginCode redirects (303) to /login/sent on success.
	require.Equal(t, http.StatusSeeOther, resp2.StatusCode)
	assert.Contains(t, resp2.Header.Get("Location"), "/login/sent")

	// Member should now exist.
	var memberID int64
	require.NoError(t, env.db.QueryRow(
		`SELECT id FROM members WHERE email = ?`, "new-signup@example.com").Scan(&memberID))
	assert.Greater(t, memberID, int64(0))

	// A login code email should have been queued.
	subj, _, found := getLastEmail(t, env, "new-signup@example.com")
	require.True(t, found, "a login email should have been queued for the new member")
	assert.Contains(t, subj, "Login")
}

// TestAccessStatus_FamilyInactiveCascade verifies that a child member with a
// payment-inactive parent is reported as 'FamilyInactive'.
func TestAccessStatus_FamilyInactiveCascade(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	// Parent: confirmed but no payment of any kind -> payment_status NULL.
	parentID := seedMember(t, env, "parent@example.com", WithConfirmed())

	// Child: confirmed, non_billable (so individual payment gates pass), with
	// a fob, and root_family_member pointing at the parent. The members
	// trigger should set root_family_member_active = 0 because the parent
	// has no payment_status.
	res, err := env.db.Exec(`
		INSERT INTO members
		    (email, confirmed, non_billable, fob_id, root_family_member)
		VALUES (?, 1, 1, ?, ?)`,
		"child@example.com", int64(987654), parentID)
	require.NoError(t, err)
	childID, err := res.LastInsertId()
	require.NoError(t, err)

	var status string
	var rootActive *int64
	require.NoError(t, env.db.QueryRow(
		`SELECT access_status, root_family_member_active FROM members WHERE id = ?`,
		childID).Scan(&status, &rootActive))
	assert.Equal(t, "FamilyInactive", status,
		"child should inherit FamilyInactive from inactive parent")
	require.NotNil(t, rootActive)
	assert.Equal(t, int64(0), *rootActive)
}

// TestAccessStatus_NonBillableNoFob verifies that a non-billable member
// without a fob falls through to 'MissingKeyFob'.
func TestAccessStatus_NonBillableNoFob(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	id := seedMember(t, env, "nobill-nofob@example.com", WithNonBillable())

	var status string
	require.NoError(t, env.db.QueryRow(
		`SELECT access_status FROM members WHERE id = ?`, id).Scan(&status))
	assert.Equal(t, "MissingKeyFob", status)
}

// TestBillingPortal_RedirectsToStripe verifies that a member with an active
// subscription hitting GET /payment/checkout is sent through the Stripe
// billing portal flow. Without a real Stripe API key wired up, the handler
// either redirects to billing.stripe.com (if a real key is configured) or
// returns a 5xx after the API call fails. We tolerate either outcome.
func TestBillingPortal_RedirectsToStripe(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "billing-portal@example.com",
		WithConfirmed(), WithActiveStripeSubscription())

	req := authedRequest(t, env, memberID, http.MethodGet, "/payment/checkout", nil)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		loc := resp.Header.Get("Location")
		assert.Contains(t, loc, "stripe.com",
			"billing portal redirect should target a stripe.com URL, got %q", loc)
	case resp.StatusCode >= 500:
		// Acceptable: no real Stripe API key configured, so the upstream
		// Stripe call fails and we surface a 5xx error page.
	default:
		t.Fatalf("unexpected status from /payment/checkout: %d", resp.StatusCode)
	}
}

// TestDonations_CheckoutRedirect verifies that GET /donations/checkout for a
// configured donation item either redirects to checkout.stripe.com (when a
// real Stripe key is wired up) or returns a 5xx when the Stripe API call
// fails for lack of credentials. Without any donation_items configuration the
// handler returns 400 ("No donation item selected"); we test the configured
// path here.
func TestDonations_CheckoutRedirect(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	// Seed stripe_config with a single donation item but no real api key.
	donationItems := `[{"name":"Test Donation","price_id":"price_test_donation"}]`
	seedStripeConfigWithDonations(t, env, "sk_test_dummy", "whsec_dummy", donationItems)

	memberID := seedMember(t, env, "donor@example.com",
		WithConfirmed(), WithActiveStripeSubscription())

	req := authedRequest(t, env, memberID, http.MethodGet,
		"/donations/checkout?price_id=price_test_donation", nil)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		loc := resp.Header.Get("Location")
		assert.Contains(t, loc, "stripe.com",
			"donation checkout should redirect to stripe.com, got %q", loc)
	case resp.StatusCode >= 500:
		// Acceptable: dummy api key, Stripe API call failed -> SystemError 500.
	default:
		t.Fatalf("unexpected status from /donations/checkout: %d", resp.StatusCode)
	}
}
