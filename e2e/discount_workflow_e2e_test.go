package e2e

import (
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getDiscount returns discount columns for a member (any may be NULL).
func getDiscount(t *testing.T, env *TestEnv, memberID int64) (discountType, status, requestID *string, root *int64) {
	t.Helper()
	var dt, st, rid sql.NullString
	var rt sql.NullInt64
	err := env.db.QueryRow(
		`SELECT discount_type, discount_status, discount_request_id, root_family_member FROM members WHERE id = ?`,
		memberID).Scan(&dt, &st, &rid, &rt)
	require.NoError(t, err)
	if dt.Valid {
		discountType = &dt.String
	}
	if st.Valid {
		status = &st.String
	}
	if rid.Valid {
		requestID = &rid.String
	}
	if rt.Valid {
		root = &rt.Int64
	}
	return discountType, status, requestID, root
}

// requestQueueCount returns how many rows are queued in the discordbot
// discount-request notification queue for the given member.
func requestQueueCount(t *testing.T, env *TestEnv, memberID int64) int {
	t.Helper()
	var n int
	err := env.db.QueryRow(
		`SELECT COUNT(*) FROM discordbot_discount_request_queue WHERE member_id = ?`,
		memberID).Scan(&n)
	require.NoError(t, err, "discordbot_discount_request_queue should exist in the e2e schema")
	return n
}

// adminApproveDiscount POSTs the leadership approval form as the given admin.
func adminApproveDiscount(t *testing.T, env *TestEnv, adminID, memberID int64, familyEmail string) *http.Response {
	t.Helper()
	form := url.Values{}
	form.Set("family_email", familyEmail)
	path := "/admin/members/" + strconv.FormatInt(memberID, 10) + "/updates/discount-approve"
	req := authedRequest(t, env, adminID, http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	return resp
}

// TestDiscountWorkflow_RequestHidesPayButton drives the member-facing request
// flow end to end: a confirmed, waiver-signed member with no discount sees the
// "Set Up Payment" button, requests a discount, and the button disappears while
// the request is pending. The request is persisted and enqueued for the
// leadership Discord notification.
func TestDiscountWorkflow_RequestHidesPayButton(t *testing.T) {
	t.Parallel()
	env, memberID, page := setupMemberTest(t, "discount-request@example.com",
		WithConfirmed(), WithWaiver())

	_, err := page.Goto(env.baseURL + "/")
	require.NoError(t, err)

	// Initial state: pay button + request affordance both present.
	payBtn := page.Locator("a:has-text('Set Up Payment')")
	expect(t).Locator(payBtn).ToBeVisible()
	expect(t).Locator(page.Locator("#discount-request-toggle")).ToBeVisible()

	// Reveal and submit the request form for the "student" discount.
	require.NoError(t, page.Locator("#discount-request-toggle").Click())
	_, err = page.Locator("#discount-request-select").SelectOption(
		playwright.SelectOptionValues{Values: &[]string{"student"}})
	require.NoError(t, err)
	require.NoError(t, page.Locator("#discount-request-form button[type='submit']").Click())

	// Back on the dashboard: pending notice shown, pay button gone.
	expect(t).Locator(page.Locator("#discount-pending")).ToBeVisible()
	expect(t).Locator(page.Locator("a:has-text('Set Up Payment')")).ToBeHidden()

	// Persistence + enqueue.
	dt, st, requestID, _ := getDiscount(t, env, memberID)
	require.NotNil(t, dt)
	assert.Equal(t, "student", *dt)
	require.NotNil(t, st)
	assert.Equal(t, "requested", *st)
	require.NotNil(t, requestID)
	assertValidDiscountRequestID(t, *requestID)
	expect(t).Locator(page.Locator("#discount-request-id")).ToHaveText(*requestID)
	assert.Equal(t, 1, requestQueueCount(t, env, memberID),
		"requesting a discount should enqueue exactly one leadership notification")
}

func assertValidDiscountRequestID(t *testing.T, requestID string) {
	t.Helper()
	parts := strings.Split(requestID, "-")
	require.Len(t, parts, 3)
	for _, part := range parts {
		assert.NotEmpty(t, part)
	}
}

// TestDiscountWorkflow_NoEnqueueOnSignupOrStatusChange verifies leadership is
// NOT notified when members sign up or when unrelated columns change. Only a
// transition into discount_status='requested' should enqueue.
func TestDiscountWorkflow_NoEnqueueOnSignupOrStatusChange(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "discount-nonotify@example.com",
		WithConfirmed(), WithWaiver())

	// Fresh signup: nothing queued.
	assert.Equal(t, 0, requestQueueCount(t, env, memberID),
		"signing up should not notify leadership")

	// Unrelated state change (assign a fob): still nothing queued.
	_, err := env.db.Exec(`UPDATE members SET fob_id = ? WHERE id = ?`, 424242, memberID)
	require.NoError(t, err)
	assert.Equal(t, 0, requestQueueCount(t, env, memberID),
		"unrelated status changes should not notify leadership")
}

// TestDiscountWorkflow_AdminApprove verifies leadership approval via the admin
// web form flips the status to approved and re-enables the pay button.
func TestDiscountWorkflow_AdminApprove(t *testing.T) {
	t.Parallel()
	env, memberID, page := setupMemberTest(t, "discount-approve@example.com",
		WithConfirmed(), WithWaiver(), WithDiscount("student"), WithDiscountStatus("requested"))
	adminID := seedMember(t, env, "approver@example.com", WithConfirmed(), WithLeadership())

	// While pending the member sees no pay button.
	_, err := page.Goto(env.baseURL + "/")
	require.NoError(t, err)
	expect(t).Locator(page.Locator("#discount-pending")).ToBeVisible()
	expect(t).Locator(page.Locator("a:has-text('Set Up Payment')")).ToBeHidden()

	// Leadership approves.
	resp := adminApproveDiscount(t, env, adminID, memberID, "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	_, st, _, _ := getDiscount(t, env, memberID)
	require.NotNil(t, st)
	assert.Equal(t, "approved", *st)

	// Member dashboard now shows the approved badge and the pay button returns.
	_, err = page.Goto(env.baseURL + "/")
	require.NoError(t, err)
	expect(t).Locator(page.Locator("#discount-approved")).ToBeVisible()
	expect(t).Locator(page.Locator("a:has-text('Set Up Payment')")).ToBeVisible()
}

// TestDiscountWorkflow_RemovePending verifies a member can cancel a pending
// request at any time, which clears the discount and restores the pay button.
func TestDiscountWorkflow_RemovePending(t *testing.T) {
	t.Parallel()
	env, memberID, page := setupMemberTest(t, "discount-remove-pending@example.com",
		WithConfirmed(), WithWaiver(), WithDiscount("student"), WithDiscountStatus("requested"))

	_, err := page.Goto(env.baseURL + "/")
	require.NoError(t, err)
	expect(t).Locator(page.Locator("#discount-pending")).ToBeVisible()

	// Cancel the pending request.
	require.NoError(t, page.Locator("button:has-text('Cancel request')").Click())

	expect(t).Locator(page.Locator("a:has-text('Set Up Payment')")).ToBeVisible()
	dt, st, requestID, _ := getDiscount(t, env, memberID)
	assert.Nil(t, dt, "discount_type should be cleared")
	assert.Nil(t, st, "discount_status should be cleared")
	assert.Nil(t, requestID, "discount_request_id should be cleared")
}

// TestDiscountWorkflow_RemoveApproved verifies the same removal semantics apply
// once a discount has been approved.
func TestDiscountWorkflow_RemoveApproved(t *testing.T) {
	t.Parallel()
	env, memberID, page := setupMemberTest(t, "discount-remove-approved@example.com",
		WithConfirmed(), WithWaiver(), WithDiscount("student"), WithDiscountStatus("approved"))

	_, err := page.Goto(env.baseURL + "/")
	require.NoError(t, err)
	expect(t).Locator(page.Locator("#discount-approved")).ToBeVisible()
	expect(t).Locator(page.Locator("a:has-text('Set Up Payment')")).ToBeVisible()

	// Remove the approved discount.
	require.NoError(t, page.Locator("button:has-text('Remove discount')").Click())

	expect(t).Locator(page.Locator("#discount-approved")).ToBeHidden()
	dt, st, requestID, _ := getDiscount(t, env, memberID)
	assert.Nil(t, dt, "discount_type should be cleared")
	assert.Nil(t, st, "discount_status should be cleared")
	assert.Nil(t, requestID, "discount_request_id should be cleared")
}

// TestDiscountWorkflow_FamilyLinkage verifies that approving a family discount
// with a provided family email links the member to the root family account.
func TestDiscountWorkflow_FamilyLinkage(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	rootID := seedMember(t, env, "family-root@example.com", WithConfirmed())
	memberID := seedMember(t, env, "family-child@example.com",
		WithConfirmed(), WithWaiver(), WithDiscount("family"), WithDiscountStatus("requested"))
	adminID := seedMember(t, env, "family-approver@example.com", WithConfirmed(), WithLeadership())

	resp := adminApproveDiscount(t, env, adminID, memberID, "family-root@example.com")
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	_, st, _, root := getDiscount(t, env, memberID)
	require.NotNil(t, st)
	assert.Equal(t, "approved", *st)
	require.NotNil(t, root, "approving a family discount should link the root family member")
	assert.Equal(t, rootID, *root)
}

func TestDiscountWorkflow_AdminShowsRequestID(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "discount-admin-id@example.com",
		WithConfirmed(), WithWaiver(), WithDiscount("student"), WithDiscountStatus("requested"), WithDiscountRequestID("lathe-solder-circuit"))
	adminID := seedMember(t, env, "discount-admin-viewer@example.com", WithConfirmed(), WithLeadership())
	page := newPage(t)
	loginPageAs(t, env, page, adminID)

	_, err := page.Goto(env.baseURL + "/admin/members/" + strconv.FormatInt(memberID, 10))
	require.NoError(t, err)
	expect(t).Locator(page.Locator("#discount-request-pending")).ToBeVisible()
	expect(t).Locator(page.Locator("#discount-request-id")).ToHaveText("lathe-solder-circuit")
}

// TestDiscountWorkflow_CheckoutNotBlockedWhilePending confirms the decision to
// HIDE (not block) the pay button: the GET /payment/checkout endpoint is still
// reachable while a request is pending. Without a real Stripe key the handler
// either redirects to stripe.com or returns a 5xx; both prove it wasn't blocked
// by the discount status (a block would surface a 4xx/redirect to "/").
func TestDiscountWorkflow_CheckoutNotBlockedWhilePending(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "discount-checkout@example.com",
		WithConfirmed(), WithWaiver(), WithDiscount("student"), WithDiscountStatus("requested"))

	req := authedRequest(t, env, memberID, http.MethodGet, "/payment/checkout", nil)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		assert.Contains(t, resp.Header.Get("Location"), "stripe.com",
			"checkout should head to Stripe, not be bounced back to the dashboard")
	case resp.StatusCode >= 500:
		// Acceptable: no real Stripe key, upstream call fails.
	default:
		t.Fatalf("unexpected status from /payment/checkout while pending: %d", resp.StatusCode)
	}
}
