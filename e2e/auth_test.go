package e2e

import (
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogin_EmailSubmission(t *testing.T) {
	clearTestData(t)

	page := newPage(t)
	loginPage := NewLoginPage(t, page)

	loginPage.Navigate()
	loginPage.FillEmail("test@example.com")
	loginPage.Submit()

	// Should redirect to /login/sent
	loginPage.ExpectSentPage()
	loginPage.ExpectEmailSentMessage()

	// Verify email was queued
	subject, _, found := getLastEmail(t, "test@example.com")
	assert.True(t, found, "email should be sent")
	assert.Equal(t, "Makerspace Login", subject)
}

func TestLogin_MagicLinkValid(t *testing.T) {
	clearTestData(t)

	// Create a member first
	memberID := seedMember(t, "valid@example.com", WithConfirmed())

	// Generate a valid magic link token
	token := generateMagicLinkToken(t, memberID)

	page := newPage(t)

	// Navigate to the magic link URL
	_, err := page.Goto(baseURL + "/login?t=" + url.QueryEscape(token) + "&n=" + url.QueryEscape("/"))
	require.NoError(t, err)

	// Should be redirected to the callback URL (root)
	err = page.WaitForURL("**/")
	require.NoError(t, err)

	// Verify we're logged in by checking the dashboard shows member content
	dashboard := NewMemberDashboardPage(t, page)
	// Since the member has no waiver, payment, or fob, we should see an alert
	dashboard.ExpectMissingWaiverAlert()
}

func TestLogin_MagicLinkExpired(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "expired@example.com", WithConfirmed())

	// Generate an expired magic link token
	token := generateExpiredMagicLinkToken(t, memberID)

	page := newPage(t)

	// Navigate to the magic link URL
	resp, err := page.Goto(baseURL + "/login?t=" + url.QueryEscape(token) + "&n=/")
	require.NoError(t, err)

	// Should return a 400 error
	assert.Equal(t, 400, resp.Status())

	// Should show error message
	locator := page.GetByText("invalid login link")
	expect(t).Locator(locator).ToBeVisible()
}

func TestLogin_MagicLinkInvalid(t *testing.T) {
	clearTestData(t)

	page := newPage(t)

	// Navigate with an invalid token
	resp, err := page.Goto(baseURL + "/login?t=invalid-token&n=/")
	require.NoError(t, err)

	// Should return a 400 error
	assert.Equal(t, 400, resp.Status())
}

func TestLogin_CallbackRedirect(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "callback@example.com", WithConfirmed())
	token := generateMagicLinkToken(t, memberID)

	page := newPage(t)

	// Navigate with a specific callback
	callbackURL := "/admin/members"
	_, err := page.Goto(baseURL + "/login?t=" + url.QueryEscape(token) + "&n=" + url.QueryEscape(callbackURL))
	require.NoError(t, err)

	// Should redirect to the callback URL
	err = page.WaitForURL("**" + callbackURL)
	require.NoError(t, err)
}

func TestLogout(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "logout@example.com", WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345))

	ctx := newContext(t)
	loginAs(t, ctx, memberID)

	page := newPageInContext(t, ctx)

	// Go to dashboard
	_, err := page.Goto(baseURL + "/")
	require.NoError(t, err)

	// Verify we're logged in
	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectActiveStatus()

	// Click logout
	dashboard.ClickLogout()

	// Should redirect to login page (or callback_uri if specified)
	// After logout, trying to access protected route should redirect to login
	_, err = page.Goto(baseURL + "/")
	require.NoError(t, err)

	// Should redirect to login page
	err = page.WaitForURL("**/login**")
	require.NoError(t, err)
}

func TestLogin_EmailCreatesNewMember(t *testing.T) {
	clearTestData(t)

	page := newPage(t)
	loginPage := NewLoginPage(t, page)

	newEmail := "newmember_" + strconv.FormatInt(int64(testDB.Stats().OpenConnections), 10) + "@example.com"

	loginPage.Navigate()
	loginPage.FillEmail(newEmail)
	loginPage.Submit()

	loginPage.ExpectSentPage()

	// Verify member was created
	var memberID int64
	err := testDB.QueryRow("SELECT id FROM members WHERE email = ?", newEmail).Scan(&memberID)
	require.NoError(t, err, "member should be created")
	assert.Greater(t, memberID, int64(0))
}
