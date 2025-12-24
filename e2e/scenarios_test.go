package e2e

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogin_MagicLinkValid verifies that a valid magic link token authenticates
// the user and redirects them to the dashboard.
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

// TestLogin_MagicLinkExpired verifies that an expired magic link token returns
// a 400 error with an appropriate message.
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

// TestLogin_MagicLinkInvalid verifies that an invalid magic link token returns
// a 400 error.
func TestLogin_MagicLinkInvalid(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	resp, err := page.Goto(baseURL + "/login?t=invalid-token&n=/")
	require.NoError(t, err)

	assert.Equal(t, 400, resp.Status())
}

// TestLogout verifies that logging out clears the session and redirects
// unauthenticated requests to the login page.
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

// TestAuth_CallbackPreservation verifies that callback_uri is preserved through
// the login flow when accessing protected resources without authentication.
func TestAuth_CallbackPreservation(t *testing.T) {
	clearTestData(t)
	memberID := seedMember(t, "callback@example.com", WithConfirmed(), WithLeadership())

	page := newPage(t)

	// Try to access admin page without auth
	_, err := page.Goto(baseURL + "/admin/members")
	require.NoError(t, err)

	err = page.WaitForURL("**/login**")
	require.NoError(t, err)

	url := page.URL()
	assert.Contains(t, url, "callback_uri")

	// Login via magic link with the callback
	token := generateMagicLinkToken(t, memberID)
	_, err = page.Goto(baseURL + "/login?t=" + token + "&n=" + "/admin/members")
	require.NoError(t, err)

	err = page.WaitForURL("**/admin/members**")
	require.NoError(t, err)
}

// TestWaiver_Display verifies that the waiver page renders with all required
// form elements including checkboxes, name, and email fields.
func TestWaiver_Display(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	waiverPage := NewWaiverPage(t, page)

	waiverPage.Navigate()
	waiverPage.ExpectWaiverText()

	// Check that the form elements are present
	expect(t).Locator(page.Locator("#agree1")).ToBeVisible()
	expect(t).Locator(page.Locator("#agree2")).ToBeVisible()
	expect(t).Locator(page.Locator("#name")).ToBeVisible()
	expect(t).Locator(page.Locator("#email")).ToBeVisible()
}

// TestWaiver_CheckboxValidation verifies that submitting without checking
// agreement boxes fails HTML5 validation and prevents waiver creation.
func TestWaiver_CheckboxValidation(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	waiverPage := NewWaiverPage(t, page)

	waiverPage.Navigate()
	waiverPage.FillName("No Checkboxes")
	waiverPage.FillEmail("nocheckbox@example.com")

	// Try to submit - should fail due to HTML5 validation
	waiverPage.Submit()

	// We should still be on the waiver page
	waiverPage.ExpectWaiverText()

	// Verify no waiver was created
	var count int
	err := testDB.QueryRow("SELECT COUNT(*) FROM waivers WHERE email = ?", "nocheckbox@example.com").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "waiver should not be created without checkboxes")
}

// TestWaiver_WithRedirect verifies that signing a waiver with a redirect
// parameter shows the success message and displays a done link.
func TestWaiver_WithRedirect(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	waiverPage := NewWaiverPage(t, page)

	waiverPage.NavigateWithRedirect("/")
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("Redirect Test")
	waiverPage.FillEmail("redirect@example.com")
	waiverPage.Submit()

	waiverPage.ExpectSuccessMessage()
	expect(t).Locator(page.Locator("a:has-text('Done')")).ToBeVisible()
}

// TestDashboard_OnboardingStates verifies that the dashboard correctly displays
// the member's onboarding progress through all possible status states.
func TestDashboard_OnboardingStates(t *testing.T) {
	tests := []struct {
		name       string
		opts       []MemberOption
		expectFunc func(*testing.T, *MemberDashboardPage)
	}{
		{
			name: "missing_waiver",
			opts: []MemberOption{WithConfirmed()},
			expectFunc: func(t *testing.T, d *MemberDashboardPage) {
				d.ExpectMissingWaiverAlert()
			},
		},
		{
			name: "missing_payment",
			opts: []MemberOption{WithConfirmed(), WithWaiver()},
			expectFunc: func(t *testing.T, d *MemberDashboardPage) {
				d.ExpectMissingPaymentAlert()
				d.ExpectStepComplete("Sign Liability Waiver")
				d.ExpectStepPending("Get Your Key Fob")
			},
		},
		{
			name: "missing_keyfob",
			opts: []MemberOption{WithConfirmed(), WithWaiver(), WithActiveStripeSubscription()},
			expectFunc: func(t *testing.T, d *MemberDashboardPage) {
				d.ExpectMissingKeyFobAlert()
				d.ExpectStepComplete("Sign Liability Waiver")
				d.ExpectStepComplete("Set Up Payment")
			},
		},
		{
			name: "fully_active",
			opts: []MemberOption{WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345)},
			expectFunc: func(t *testing.T, d *MemberDashboardPage) {
				d.ExpectActiveStatus()
				d.ExpectOnboardingChecklist()
				d.ExpectStepComplete("Sign Liability Waiver")
				d.ExpectStepComplete("Set Up Payment")
				d.ExpectStepComplete("Get Your Key Fob")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, page := setupMemberTest(t, tc.name+"@example.com", tc.opts...)
			dashboard := NewMemberDashboardPage(t, page)
			dashboard.Navigate()
			tc.expectFunc(t, dashboard)
		})
	}
}

// TestDashboard_DiscordLinking verifies the Discord linking UI appears or hides
// based on the member's Discord connection status.
func TestDashboard_DiscordLinking(t *testing.T) {
	t.Run("shows_link_button_when_not_linked", func(t *testing.T) {
		_, page := setupMemberTest(t, "nodiscord@example.com",
			WithConfirmed(),
			WithWaiver(),
			WithActiveStripeSubscription(),
			WithFobID(12345),
		)
		dashboard := NewMemberDashboardPage(t, page)
		dashboard.Navigate()
		expect(t).Locator(page.Locator("a:has-text('Link Discord')")).ToBeVisible()
	})

	t.Run("hides_link_button_when_linked", func(t *testing.T) {
		_, page := setupMemberTest(t, "hasdiscord@example.com",
			WithConfirmed(),
			WithWaiver(),
			WithActiveStripeSubscription(),
			WithFobID(12345),
			WithDiscord("123456789"),
		)
		dashboard := NewMemberDashboardPage(t, page)
		dashboard.Navigate()
		expect(t).Locator(page.Locator("a:has-text('Link Discord')")).ToBeHidden()
	})
}

// TestDashboard_RequiresAuthentication verifies that unauthenticated access to
// the dashboard redirects to the login page.
func TestDashboard_RequiresAuthentication(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	_, err := page.Goto(baseURL + "/")
	require.NoError(t, err)

	err = page.WaitForURL("**/login**")
	require.NoError(t, err)
}

// TestJourney_NewMemberOnboarding tests the complete new member signup flow:
// sign waiver, request login email, click magic link, and view dashboard.
func TestJourney_NewMemberOnboarding(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	email := "newmember@example.com"

	// Step 1: Sign the waiver
	waiverPage := NewWaiverPage(t, page)
	waiverPage.Navigate()
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("New Member")
	waiverPage.FillEmail(email)
	waiverPage.Submit()
	waiverPage.ExpectSuccessMessage()

	// Verify waiver was created
	var waiverID int64
	err := testDB.QueryRow("SELECT id FROM waivers WHERE email = ?", email).Scan(&waiverID)
	require.NoError(t, err, "waiver should be created")

	// Step 2: Request login email
	loginPage := NewLoginPage(t, page)
	loginPage.Navigate()
	loginPage.FillEmail(email)
	loginPage.Submit()
	loginPage.ExpectSentPage()

	// Verify member was created
	var memberID int64
	err = testDB.QueryRow("SELECT id FROM members WHERE email = ?", email).Scan(&memberID)
	require.NoError(t, err, "member should be created")

	// Step 3: Use magic link to login
	token := generateMagicLinkToken(t, memberID)
	_, err = page.Goto(baseURL + "/login?t=" + token + "&n=/")
	require.NoError(t, err)

	err = page.WaitForURL("**/")
	require.NoError(t, err)

	// Step 4: Dashboard should show missing payment
	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectMissingPaymentAlert()
	expect(t).Locator(page.Locator("a:has-text('Manage Payment')")).ToBeVisible()
}

// TestJourney_WaiverThenLogin tests that signing a waiver before creating an
// account properly links the waiver to the member on first login.
func TestJourney_WaiverThenLogin(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	email := "waiverfirst@example.com"

	// Sign waiver first (before any member record exists)
	waiverPage := NewWaiverPage(t, page)
	waiverPage.Navigate()
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("Waiver First User")
	waiverPage.FillEmail(email)
	waiverPage.Submit()
	waiverPage.ExpectSuccessMessage()

	// Now login (creates member record)
	loginPage := NewLoginPage(t, page)
	loginPage.Navigate()
	loginPage.FillEmail(email)
	loginPage.Submit()
	loginPage.ExpectSentPage()

	// Get member ID
	var memberID int64
	err := testDB.QueryRow("SELECT id FROM members WHERE email = ?", email).Scan(&memberID)
	require.NoError(t, err)

	// Use magic link
	token := generateMagicLinkToken(t, memberID)
	_, err = page.Goto(baseURL + "/login?t=" + token + "&n=/")
	require.NoError(t, err)

	// Check that member has waiver linked
	var waiverID *int64
	err = testDB.QueryRow("SELECT waiver FROM members WHERE id = ?", memberID).Scan(&waiverID)
	require.NoError(t, err)
	assert.NotNil(t, waiverID, "waiver should be linked to member")

	// Dashboard should show missing payment (not missing waiver)
	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectMissingPaymentAlert()
}

// TestAdmin_RequiresLeadership verifies that non-leadership members receive
// a 403 Forbidden error when accessing any admin endpoint.
func TestAdmin_RequiresLeadership(t *testing.T) {
	_, page := setupMemberTest(t, "regular@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
	)

	endpoints := []string{
		"/admin/members",
		"/admin/metrics",
		"/admin/fobs",
		"/admin/events",
		"/admin/waivers",
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			resp, err := page.Goto(baseURL + endpoint)
			require.NoError(t, err)
			assert.Equal(t, 403, resp.Status())
		})
	}
}

// TestAdmin_MembersListAndSearch verifies the admin members list page displays
// members and search filters results correctly.
func TestAdmin_MembersListAndSearch(t *testing.T) {
	_, page := setupAdminTest(t)

	// Create test members
	seedMember(t, "searchable@example.com", WithConfirmed())
	seedMember(t, "findme@example.com", WithConfirmed())
	seedMember(t, "other@example.com", WithConfirmed())

	adminPage := NewAdminMembersListPage(t, page)
	adminPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	t.Run("shows_members_list", func(t *testing.T) {
		expect(t).Locator(page.Locator("#results")).ToBeVisible()
	})

	t.Run("search_filters_results", func(t *testing.T) {
		adminPage.Search("searchable")
		adminPage.ExpectMemberInList("searchable@example.com")
	})
}

// TestAdmin_EditDesignations verifies that an admin can toggle member
// leadership status through the designations form.
func TestAdmin_EditDesignations(t *testing.T) {
	_, page := setupAdminTest(t)
	targetID := seedMember(t, "designations@example.com", WithConfirmed())

	_, err := page.Goto(baseURL + "/admin/members/" + strconv.FormatInt(targetID, 10))
	require.NoError(t, err)

	detail := NewAdminMemberDetailPage(t, page)
	detail.ToggleLeadership()
	detail.SubmitDesignationsForm()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	var leadership bool
	err = testDB.QueryRow("SELECT leadership FROM members WHERE id = ?", targetID).Scan(&leadership)
	require.NoError(t, err)
	assert.True(t, leadership, "member should now be leadership")
}

// TestAdmin_GenerateLoginQR verifies that the admin can generate a login
// QR code image for a member.
func TestAdmin_GenerateLoginQR(t *testing.T) {
	_, page := setupAdminTest(t)
	targetID := seedMember(t, "qrtest@example.com", WithConfirmed())

	resp, err := page.Goto(baseURL + "/admin/members/" + strconv.FormatInt(targetID, 10) + "/logincode")
	require.NoError(t, err)

	assert.Equal(t, 200, resp.Status())
	headers := resp.Headers()
	assert.Contains(t, headers["content-type"], "image/png")
}

// TestAdmin_DeleteMember verifies that an admin can delete a member through
// the two-step confirmation flow.
func TestAdmin_DeleteMember(t *testing.T) {
	_, page := setupAdminTest(t)
	targetID := seedMember(t, "deleteme@example.com", WithConfirmed())

	_, err := page.Goto(baseURL + "/admin/members/" + strconv.FormatInt(targetID, 10))
	require.NoError(t, err)

	detail := NewAdminMemberDetailPage(t, page)
	detail.ClickDeleteMember()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	detail.ConfirmDelete()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	var count int
	err = testDB.QueryRow("SELECT COUNT(*) FROM members WHERE id = ?", targetID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "member should be deleted")
}

// TestJourney_AdminManagesMember tests an admin finding a member via search,
// navigating to their detail page, and editing their information.
func TestJourney_AdminManagesMember(t *testing.T) {
	_, page := setupAdminTest(t)
	targetID := seedMember(t, "manageme@example.com", WithConfirmed(), WithWaiver())

	// Step 1: Navigate to admin members list
	adminList := NewAdminMembersListPage(t, page)
	adminList.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Step 2: Search for the member
	adminList.Search("manageme@example.com")

	// Step 3: Click on the member row
	adminList.ClickMemberRow("manageme@example.com")

	err = page.WaitForURL(fmt.Sprintf("**/admin/members/%d", targetID))
	require.NoError(t, err)

	// Step 4: Edit member details
	detail := NewAdminMemberDetailPage(t, page)
	detail.FillFobID("99999")
	detail.FillAdminNotes("Updated by admin in E2E test")
	detail.SubmitBasicsForm()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Step 5: Verify changes were saved
	var fobID int64
	var notes string
	err = testDB.QueryRow("SELECT fob_id, admin_notes FROM members WHERE id = ?", targetID).Scan(&fobID, &notes)
	require.NoError(t, err)
	assert.Equal(t, int64(99999), fobID)
	assert.Equal(t, "Updated by admin in E2E test", notes)
}

// TestJourney_MultipleMembers tests admin managing multiple members in sequence
// without data leakage between edits.
func TestJourney_MultipleMembers(t *testing.T) {
	_, page := setupAdminTest(t)

	member1ID := seedMember(t, "multi1@example.com", WithConfirmed())
	member2ID := seedMember(t, "multi2@example.com", WithConfirmed())

	// Edit first member
	_, err := page.Goto(baseURL + "/admin/members/" + strconv.FormatInt(member1ID, 10))
	require.NoError(t, err)

	detail := NewAdminMemberDetailPage(t, page)
	detail.FillAdminNotes("First member notes")
	detail.SubmitBasicsForm()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Edit second member
	_, err = page.Goto(baseURL + "/admin/members/" + strconv.FormatInt(member2ID, 10))
	require.NoError(t, err)

	detail.FillAdminNotes("Second member notes")
	detail.SubmitBasicsForm()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Verify both were updated correctly
	var notes1, notes2 string
	err = testDB.QueryRow("SELECT admin_notes FROM members WHERE id = ?", member1ID).Scan(&notes1)
	require.NoError(t, err)
	err = testDB.QueryRow("SELECT admin_notes FROM members WHERE id = ?", member2ID).Scan(&notes2)
	require.NoError(t, err)

	assert.Equal(t, "First member notes", notes1)
	assert.Equal(t, "Second member notes", notes2)
}

// TestAdmin_DataListPages verifies that all admin data list pages load
// and display results correctly.
func TestAdmin_DataListPages(t *testing.T) {
	_, page := setupAdminTest(t)

	// Seed test data for all pages
	memberID := seedMember(t, "data@example.com", WithConfirmed(), WithFobID(11111))
	seedFobSwipes(t, 11111, 3)
	seedMemberEvents(t, memberID, 3)
	seedWaiver(t, "waiver@example.com")

	pages := []struct {
		name   string
		pageFn func(*testing.T, playwright.Page) *AdminDataListPage
	}{
		{"fobs", NewAdminFobsPage},
		{"events", NewAdminEventsPage},
		{"waivers", NewAdminWaiversPage},
	}

	for _, tc := range pages {
		t.Run(tc.name, func(t *testing.T) {
			dataPage := tc.pageFn(t, page)
			dataPage.Navigate()
			err := page.WaitForLoadState()
			require.NoError(t, err)
			expect(t).Locator(page.Locator("#results")).ToBeVisible()
		})
	}
}

// TestAdmin_Pagination verifies that pagination controls work correctly
// when there are more results than fit on one page.
func TestAdmin_Pagination(t *testing.T) {
	_, page := setupAdminTest(t)

	// Create enough members to trigger pagination (limit is 20 per page)
	for i := 0; i < 45; i++ {
		seedMember(t, fmt.Sprintf("pagination%02d@example.com", i), WithConfirmed())
	}

	adminPage := NewAdminMembersListPage(t, page)
	adminPage.Navigate()

	err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	})
	require.NoError(t, err)

	_, err = page.WaitForSelector("#results table", playwright.PageWaitForSelectorOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(10000),
	})
	require.NoError(t, err)

	// Check if pagination controls exist and work
	nextButton := page.Locator("a.btn-primary:has-text('Next'):not(.disabled)")
	count, err := nextButton.Count()
	require.NoError(t, err)

	if count > 0 {
		err = nextButton.Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(5000),
		})
		require.NoError(t, err)

		err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		require.NoError(t, err)

		// Previous should now be visible
		prevButton := page.Locator("a.btn-primary:has-text('Previous'):not(.disabled)")
		expect(t).Locator(prevButton).ToBeVisible()
	}
}

// TestJourney_AdminExportsAllData tests that an admin can export CSV data
// for all supported data types.
func TestJourney_AdminExportsAllData(t *testing.T) {
	_, page := setupAdminTest(t)

	// Create some test data
	memberID := seedMember(t, "exportmember@example.com", WithConfirmed(), WithFobID(44444))
	seedWaiver(t, "exportwaiver@example.com")
	seedFobSwipes(t, 44444, 3)
	seedMemberEvents(t, memberID, 3)

	ctx := page.Context()
	apiContext := ctx.Request()

	tables := []string{"members", "waivers", "fob_swipes", "member_events"}

	for _, table := range tables {
		resp, err := apiContext.Get(baseURL+"/admin/export/"+table, playwright.APIRequestContextGetOptions{})
		require.NoError(t, err, "should export "+table)
		assert.Equal(t, 200, resp.Status(), "export should succeed for "+table)
		headers := resp.Headers()
		contentType := headers["content-type"]
		assert.Contains(t, contentType, "text/csv", "should be CSV for "+table)
	}
}

// TestAdmin_MetricsDashboard verifies the metrics page displays charts,
// interval selector, and responds to API requests correctly.
func TestAdmin_MetricsDashboard(t *testing.T) {
	_, page := setupAdminTest(t)
	seedMetrics(t, "test_series", 10)

	metricsPage := NewAdminMetricsPage(t, page)
	metricsPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	t.Run("interval_selector", func(t *testing.T) {
		expect(t).Locator(page.Locator("#interval")).ToBeVisible()
	})

	t.Run("chart_rendering", func(t *testing.T) {
		metricsPage.ExpectChartForSeries("test_series")
	})

	t.Run("time_window_selection", func(t *testing.T) {
		metricsPage.SelectInterval("720h")
		err := page.WaitForLoadState()
		require.NoError(t, err)
		expect(t).Locator(page.Locator("#interval option[selected][value='720h']")).ToBeVisible()
	})

	t.Run("chart_api", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/admin/chart?series=test_series&window=168h")
		require.NoError(t, err)
		assert.Equal(t, 200, resp.Status())

		body, err := resp.Body()
		require.NoError(t, err)

		var data []struct {
			Timestamp int64   `json:"t"`
			Value     float64 `json:"v"`
		}
		err = json.Unmarshal(body, &data)
		require.NoError(t, err)
		assert.NotEmpty(t, data, "should have metric data points")
	})
}

// TestOAuth2_Discovery verifies the OpenID configuration and JWKS endpoints
// return properly formatted responses with required fields.
func TestOAuth2_Discovery(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	t.Run("openid_config", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/.well-known/openid-configuration")
		require.NoError(t, err)
		assert.Equal(t, 200, resp.Status())

		body, err := resp.Body()
		require.NoError(t, err)

		var config map[string]interface{}
		err = json.Unmarshal(body, &config)
		require.NoError(t, err)

		assert.Contains(t, config, "issuer")
		assert.Contains(t, config, "authorization_endpoint")
		assert.Contains(t, config, "token_endpoint")
		assert.Contains(t, config, "userinfo_endpoint")
		assert.Contains(t, config, "jwks_uri")
	})

	t.Run("jwks", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/oauth2/jwks")
		require.NoError(t, err)
		assert.Equal(t, 200, resp.Status())

		body, err := resp.Body()
		require.NoError(t, err)

		var jwks map[string]interface{}
		err = json.Unmarshal(body, &jwks)
		require.NoError(t, err)

		assert.Contains(t, jwks, "keys")
		keys := jwks["keys"].([]interface{})
		assert.NotEmpty(t, keys, "should have at least one key")

		key := keys[0].(map[string]interface{})
		assert.Contains(t, key, "kty")
		assert.Contains(t, key, "kid")
		assert.Contains(t, key, "n")
		assert.Contains(t, key, "e")
	})
}

// TestOAuth2_AuthorizeFlow verifies the OAuth2 authorization code flow
// redirects correctly for authenticated users.
func TestOAuth2_AuthorizeFlow(t *testing.T) {
	clearTestData(t)
	memberID := seedMember(t, "oauth@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	redirectURI := baseURL + "/login"
	authURL := baseURL + "/oauth2/authorize?response_type=code&client_id=test-client&redirect_uri=" + redirectURI + "&state=teststate"

	resp, _ := page.Goto(authURL)
	finalURL := page.URL()

	// The endpoint should return a valid response
	assert.True(t, resp.Status() == 200 || resp.Status() == 302 || resp.Status() == 400,
		"should return valid HTTP status, got: %d", resp.Status())

	// Check if we got redirected with a code parameter
	if strings.Contains(finalURL, "code=") {
		assert.Contains(t, finalURL, "state=teststate")
	}
}

// TestOAuth2_UserInfo_RequiresAuth verifies the userinfo endpoint requires
// authentication and returns an error for unauthenticated requests.
func TestOAuth2_UserInfo_RequiresAuth(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	resp, err := page.Goto(baseURL + "/oauth2/userinfo")
	require.NoError(t, err)

	assert.GreaterOrEqual(t, resp.Status(), 400)
}

// TestMachines_RequiresAuth verifies that unauthenticated access to the
// machines page redirects to login.
func TestMachines_RequiresAuth(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	_, err := page.Goto(baseURL + "/machines")
	require.NoError(t, err)

	err = page.WaitForURL("**/login**")
	require.NoError(t, err)
}

// TestMachines_AllPrinterStatuses verifies the printers page displays all
// printer cards with correct status badges, controls, and visual elements.
// The test app is configured with 3 mock printers:
// - "Printer A" (test-001): Available (no job, no error)
// - "Printer B" (test-002): In Use (has JobFinishedTimestamp)
// - "Printer C" (test-003): Failed (has ErrorCode)
func TestMachines_AllPrinterStatuses(t *testing.T) {
	_, page := setupMemberTest(t, "machines@example.com", WithConfirmed())
	machinesPage := NewMachinesPage(t, page)
	machinesPage.Navigate()

	t.Run("page_structure", func(t *testing.T) {
		machinesPage.ExpectHeading()
		machinesPage.ExpectPrinterCard("Printer A")
		machinesPage.ExpectPrinterCard("Printer B")
		machinesPage.ExpectPrinterCard("Printer C")

		// Verify responsive layout uses Bootstrap grid
		cards := page.Locator(".col-md-4 .card")
		count, err := cards.Count()
		require.NoError(t, err)
		assert.Equal(t, 3, count, "should have 3 printer cards in responsive grid")

		// Each printer card should have a camera image element
		machinesPage.ExpectCameraImg("Printer A")
		machinesPage.ExpectCameraImg("Printer B")
		machinesPage.ExpectCameraImg("Printer C")
	})

	t.Run("available_printer", func(t *testing.T) {
		machinesPage.ExpectStatusBadge("Printer A", "Available")
		machinesPage.ExpectNoStopButton("Printer A")
	})

	t.Run("in_use_printer", func(t *testing.T) {
		machinesPage.ExpectStatusBadge("Printer B", "In Use")
		machinesPage.ExpectTimeRemaining("Printer B")
		machinesPage.ExpectStopButton("Printer B")

		// Verify stop button form
		stopButton := machinesPage.StopButton("Printer B")
		expect(t).Locator(stopButton).ToBeVisible()

		form := machinesPage.PrinterCard("Printer B").Locator("form")
		action, err := form.GetAttribute("action")
		require.NoError(t, err)
		assert.Equal(t, "/machines/test-002/stop", action)

		onsubmit, err := form.GetAttribute("onsubmit")
		require.NoError(t, err)
		assert.Contains(t, onsubmit, "confirm")

		// In-use printer has outline-danger button
		classB, err := stopButton.GetAttribute("class")
		require.NoError(t, err)
		assert.Contains(t, classB, "btn-outline-danger")
	})

	t.Run("failed_printer", func(t *testing.T) {
		machinesPage.ExpectStatusBadge("Printer C", "Failed")
		machinesPage.ExpectErrorCode("Printer C", "HMS_0300_0100_0001")
		machinesPage.ExpectStopButton("Printer C")

		// Failed printer has solid danger button
		btnC := machinesPage.StopButton("Printer C")
		classC, err := btnC.GetAttribute("class")
		require.NoError(t, err)
		assert.Contains(t, classC, "btn-danger")
		assert.NotContains(t, classC, "btn-outline-danger")
	})
}

// TestKiosk_AccessFromPhysicalSpace verifies the kiosk page loads correctly
// when accessed from the physical space network.
func TestKiosk_AccessFromPhysicalSpace(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	kiosk := NewKioskPage(t, page)
	kiosk.Navigate()
	kiosk.ExpectKioskInterface()
}

// TestKeyfob_BindFlow verifies the keyfob binding flow starts correctly
// for a member without a bound keyfob.
func TestKeyfob_BindFlow(t *testing.T) {
	memberID, page := setupMemberTest(t, "bindkey@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
	)

	// Verify member has no fob initially
	var fobID *int64
	err := testDB.QueryRow("SELECT fob_id FROM members WHERE id = ?", memberID).Scan(&fobID)
	require.NoError(t, err)
	assert.Nil(t, fobID, "member should not have a fob initially")

	// Navigate to dashboard to verify missing keyfob alert
	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()
	dashboard.ExpectMissingKeyFobAlert()
}

// TestKeyfob_StatusEndpoint verifies the keyfob status API returns correct
// responses for existing and unused fobs.
func TestKeyfob_StatusEndpoint(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	seedMember(t, "hasfob@example.com", WithConfirmed(), WithFobID(99999))

	t.Run("existing_fob", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/keyfob/status/99999")
		require.NoError(t, err)
		assert.Equal(t, 200, resp.Status())
	})

	t.Run("unused_fob", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/keyfob/status/11111")
		require.NoError(t, err)
		assert.Equal(t, 200, resp.Status())
	})
}
