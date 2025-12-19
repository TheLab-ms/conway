package e2e

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogin_MagicLinkValid(t *testing.T) {
	clearTestData(t)
	memberID := seedMember(t, "valid@example.com", WithConfirmed())
	token := generateMagicLinkToken(t, memberID)

	page := newPage(t)
	_, err := page.Goto(baseURL + "/login?t=" + url.QueryEscape(token) + "&n=" + url.QueryEscape("/"))
	require.NoError(t, err)

	err = page.WaitForURL("**/")
	require.NoError(t, err)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectMissingWaiverAlert()
}

func TestLogin_MagicLinkExpired(t *testing.T) {
	clearTestData(t)
	memberID := seedMember(t, "expired@example.com", WithConfirmed())
	token := generateExpiredMagicLinkToken(t, memberID)

	page := newPage(t)
	resp, err := page.Goto(baseURL + "/login?t=" + url.QueryEscape(token) + "&n=/")
	require.NoError(t, err)

	assert.Equal(t, 400, resp.Status())
	locator := page.GetByText("invalid login link")
	expect(t).Locator(locator).ToBeVisible()
}

func TestLogin_MagicLinkInvalid(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	resp, err := page.Goto(baseURL + "/login?t=invalid-token&n=/")
	require.NoError(t, err)

	assert.Equal(t, 400, resp.Status())
}

func TestLogout(t *testing.T) {
	memberID, page := setupMemberTest(t, "logout@example.com",
		WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345))
	_ = memberID

	_, err := page.Goto(baseURL + "/")
	require.NoError(t, err)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectActiveStatus()
	dashboard.ClickLogout()

	// After logout, accessing protected route should redirect to login
	_, err = page.Goto(baseURL + "/")
	require.NoError(t, err)

	err = page.WaitForURL("**/login**")
	require.NoError(t, err)
}
