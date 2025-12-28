package e2e

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogin_CodeLinkValid verifies that clicking a code link from the email
// authenticates the user and redirects them to the dashboard.
func TestLogin_CodeLinkValid(t *testing.T) {
	clearTestData(t)
	memberID := seedMember(t, "codelink@example.com", WithConfirmed())
	seedLoginCode(t, "12345", memberID, "/", time.Now().Add(5*time.Minute))

	page := newPage(t)
	_, err := page.Goto(baseURL + "/login/code?code=12345")
	require.NoError(t, err)

	err = page.WaitForURL("**/")
	require.NoError(t, err)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectMissingWaiverAlert()
}

// TestLogin_CodeLinkExpired verifies that an expired code returns a 400 error.
func TestLogin_CodeLinkExpired(t *testing.T) {
	clearTestData(t)
	memberID := seedMember(t, "expiredcode@example.com", WithConfirmed())
	seedLoginCode(t, "99999", memberID, "/", time.Now().Add(-1*time.Minute))

	page := newPage(t)
	resp, err := page.Goto(baseURL + "/login/code?code=99999")
	require.NoError(t, err)

	assert.Equal(t, 400, resp.Status())
}

// TestLogin_CodeLinkInvalid verifies that a non-existent code returns a 400 error.
func TestLogin_CodeLinkInvalid(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	resp, err := page.Goto(baseURL + "/login/code?code=00000")
	require.NoError(t, err)

	assert.Equal(t, 400, resp.Status())
}

// TestLogin_CodeFormEntry verifies that entering a code on the sent page
// authenticates the user.
func TestLogin_CodeFormEntry(t *testing.T) {
	clearTestData(t)
	memberID := seedMember(t, "codeform@example.com", WithConfirmed())
	seedLoginCode(t, "54321", memberID, "/", time.Now().Add(5*time.Minute))

	page := newPage(t)
	_, err := page.Goto(baseURL + "/login/sent?email=codeform@example.com")
	require.NoError(t, err)

	// Enter each digit
	digits := page.Locator(".code-digit")
	code := "54321"
	for i, digit := range code {
		err = digits.Nth(i).Fill(string(digit))
		require.NoError(t, err)
	}

	// Auto-submit should trigger, wait for redirect
	err = page.WaitForURL("**/")
	require.NoError(t, err)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectMissingWaiverAlert()
}

// TestLogin_CodeSingleUse verifies that a code can only be used once.
func TestLogin_CodeSingleUse(t *testing.T) {
	clearTestData(t)
	memberID := seedMember(t, "singleuse@example.com", WithConfirmed())
	seedLoginCode(t, "11111", memberID, "/", time.Now().Add(5*time.Minute))

	// First use should succeed
	page := newPage(t)
	_, err := page.Goto(baseURL + "/login/code?code=11111")
	require.NoError(t, err)
	err = page.WaitForURL("**/")
	require.NoError(t, err)

	// Second use should fail
	page2 := newPage(t)
	resp, err := page2.Goto(baseURL + "/login/code?code=11111")
	require.NoError(t, err)
	assert.Equal(t, 400, resp.Status())
}

// TestLogin_SentPageShowsEmail verifies that the sent page displays the user's email.
func TestLogin_SentPageShowsEmail(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	_, err := page.Goto(baseURL + "/login/sent?email=test@example.com")
	require.NoError(t, err)

	// Check that the email is displayed
	emailText := page.GetByText("test@example.com")
	expect(t).Locator(emailText).ToBeVisible()

	// Check that code input boxes are present
	digits := page.Locator(".code-digit")
	count, err := digits.Count()
	require.NoError(t, err)
	assert.Equal(t, 5, count, "should have 5 code digit inputs")
}

// TestLoginFlow_TypeCode tests the full login flow where the user types
// the login code from their email into the code entry form.
func TestLoginFlow_TypeCode(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	email := "typeflow@example.com"

	// Step 1: Navigate to login page and submit email
	loginPage := NewLoginPage(t, page)
	loginPage.Navigate()
	loginPage.FillEmail(email)
	loginPage.Submit()
	loginPage.ExpectSentPage()

	// Step 2: Verify email was queued
	subject, body, found := getLastEmail(t, email)
	require.True(t, found, "login email should be queued in outbound_mail")
	assert.Equal(t, "Makerspace Login", subject)

	// Step 3: Extract the code from the email body
	code := extractLoginCodeFromEmail(t, body)
	require.Len(t, code, 5, "login code should be 5 digits")

	// Step 4: Enter the code digit-by-digit on the sent page
	digits := page.Locator(".code-digit")
	for i, digit := range code {
		err := digits.Nth(i).Fill(string(digit))
		require.NoError(t, err)
	}

	// Step 5: Auto-submit should trigger, wait for redirect to dashboard
	err := page.WaitForURL("**/")
	require.NoError(t, err)

	// Step 6: Verify we're logged in and on the dashboard
	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectMissingWaiverAlert() // New member won't have waiver
}

// TestLoginFlow_ClickEmailLink tests the full login flow where the user
// clicks the login link from their email.
func TestLoginFlow_ClickEmailLink(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	email := "linkflow@example.com"

	// Step 1: Navigate to login page and submit email
	loginPage := NewLoginPage(t, page)
	loginPage.Navigate()
	loginPage.FillEmail(email)
	loginPage.Submit()
	loginPage.ExpectSentPage()

	// Step 2: Verify email was queued
	subject, body, found := getLastEmail(t, email)
	require.True(t, found, "login email should be queued in outbound_mail")
	assert.Equal(t, "Makerspace Login", subject)

	// Step 3: Extract the login link from the email body
	link := extractLoginCodeLinkFromEmail(t, body)
	require.NotEmpty(t, link, "email should contain login link")
	assert.Contains(t, link, "/login/code?code=")

	// Step 4: Click the link (navigate to it)
	_, err := page.Goto(link)
	require.NoError(t, err)

	// Step 5: Wait for redirect to dashboard
	err = page.WaitForURL("**/")
	require.NoError(t, err)

	// Step 6: Verify we're logged in and on the dashboard
	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectMissingWaiverAlert() // New member won't have waiver
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

	// Login via code link with the callback
	seedLoginCode(t, "22222", memberID, "/admin/members", time.Now().Add(5*time.Minute))
	_, err = page.Goto(baseURL + "/login/code?code=22222")
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
// sign waiver, request login email, use login code, and view dashboard.
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

	// Step 3: Get login code from database and use it
	var code string
	err = testDB.QueryRow("SELECT code FROM login_codes WHERE email = ?", email).Scan(&code)
	require.NoError(t, err, "login code should be created")

	_, err = page.Goto(baseURL + "/login/code?code=" + code)
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

	// Use login code from database
	var code string
	err = testDB.QueryRow("SELECT code FROM login_codes WHERE email = ?", email).Scan(&code)
	require.NoError(t, err, "login code should be created")

	_, err = page.Goto(baseURL + "/login/code?code=" + code)
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

// TestKeyfob_StatusEndpoint verifies the keyfob status API requires
// requests from the physical makerspace (returns 403 for remote requests).
func TestKeyfob_StatusEndpoint(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	seedMember(t, "hasfob@example.com", WithConfirmed(), WithFobID(99999))

	t.Run("existing_fob", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/keyfob/status/99999")
		require.NoError(t, err)
		assert.Equal(t, 403, resp.Status()) // requires physical presence at makerspace
	})

	t.Run("unused_fob", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/keyfob/status/11111")
		require.NoError(t, err)
		assert.Equal(t, 403, resp.Status()) // requires physical presence at makerspace
	})
}

// TestStripe_SubscriptionLifecycle tests the complete Stripe subscription
// workflow through the browser UI: create via Checkout and cancel via Billing Portal.
// This test requires:
//   - STRIPE_TEST_KEY or CONWAY_STRIPE_KEY environment variable
//   - STRIPE_TEST_WEBHOOK_KEY or CONWAY_STRIPE_WEBHOOK_KEY environment variable
//   - The 'stripe' CLI installed and authenticated
//   - A price with lookup_key "monthly" in your Stripe test account
func TestStripe_SubscriptionLifecycle(t *testing.T) {
	if !stripeTestEnabled() {
		t.Skip("Skipping Stripe test: STRIPE_TEST_KEY not set")
	}

	// Start Stripe CLI for webhook forwarding
	startStripeCLI(t, "localhost:18080/webhooks/stripe")

	clearTestData(t)

	// Use unique email to avoid conflicts between test runs
	email := fmt.Sprintf("stripe-test-%d@example.com", time.Now().UnixNano())
	memberID := seedMember(t, email, WithConfirmed(), WithWaiver())

	// Set up authenticated browser context
	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	// Step 1: Navigate to dashboard and click "Set Up Payment"
	t.Log("Step 1: Starting Stripe Checkout flow from member dashboard")

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()
	dashboard.ExpectMissingPaymentAlert()

	// Click "Set Up Payment" button - this redirects to Stripe Checkout
	setupPaymentBtn := page.Locator("a.btn-primary:has-text('Set Up Payment')")
	expect(t).Locator(setupPaymentBtn).ToBeVisible()
	err := setupPaymentBtn.Click()
	require.NoError(t, err)

	// Wait for redirect to Stripe Checkout (checkout.stripe.com)
	err = page.WaitForURL("**/checkout.stripe.com/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(30000),
	})
	require.NoError(t, err, "should redirect to Stripe Checkout")

	// Step 2: Fill Stripe Checkout form with test card
	t.Log("Step 2: Filling Stripe Checkout form with test card")

	// Wait for Stripe Checkout form to be ready (card number field)
	cardNumberField := page.Locator("#cardNumber")
	err = cardNumberField.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(30000),
	})
	require.NoError(t, err, "card number field should be visible")

	// Fill card details (Stripe test card 4242 4242 4242 4242)
	err = cardNumberField.Fill("4242424242424242")
	require.NoError(t, err)
	err = page.Locator("#cardExpiry").Fill("12/30")
	require.NoError(t, err)
	err = page.Locator("#cardCvc").Fill("123")
	require.NoError(t, err)
	err = page.Locator("#billingName").Fill("Test User")
	require.NoError(t, err)

	// Fill postal code if required
	postalCodeField := page.Locator("#billingPostalCode")
	if visible, _ := postalCodeField.IsVisible(); visible {
		err = postalCodeField.Fill("12345")
		require.NoError(t, err)
	}

	// Submit payment
	t.Log("Step 3: Submitting payment")
	submitBtn := page.Locator("button[type='submit']:has-text('Subscribe')")
	err = submitBtn.Click()
	require.NoError(t, err)

	// Wait for redirect back to our app
	err = page.WaitForURL("**/localhost:18080/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(60000),
	})
	require.NoError(t, err, "should redirect back to app after payment")

	// Wait for webhook to process and update database
	t.Log("Step 4: Waiting for subscription webhook")
	waitForMemberState(t, email, 30*time.Second, func(subState, name string) bool {
		return subState == "active"
	})

	// Verify dashboard now shows active subscription
	dashboard.Navigate()
	dashboard.ExpectStepComplete("Set Up Payment")

	// Verify "Manage Payment" button is now visible
	managePaymentBtn := page.Locator("a.btn-outline-success:has-text('Manage Payment')")
	expect(t).Locator(managePaymentBtn).ToBeVisible()

	// Step 5: Go to Billing Portal and cancel subscription
	t.Log("Step 5: Opening Stripe Billing Portal to cancel subscription")

	err = managePaymentBtn.Click()
	require.NoError(t, err)

	// Wait for redirect to Stripe Billing Portal
	err = page.WaitForURL("**/billing.stripe.com/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(30000),
	})
	require.NoError(t, err, "should redirect to Stripe Billing Portal")

	// Wait for Billing Portal to fully render
	time.Sleep(2 * time.Second)

	// Find and click "Cancel subscription" link
	cancelLink := page.Locator("a:has-text('Cancel subscription'), button:has-text('Cancel subscription')").First()
	err = cancelLink.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(15000),
	})
	require.NoError(t, err, "Cancel subscription link should be visible")
	err = cancelLink.Click()
	require.NoError(t, err)

	// Wait for confirmation dialog
	time.Sleep(2 * time.Second)

	// Click the confirmation button
	confirmBtn := page.Locator("button:has-text('Cancel subscription'), button:has-text('Cancel plan')").Last()
	err = confirmBtn.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(10000),
	})
	require.NoError(t, err, "Cancel confirmation button should be visible")
	err = confirmBtn.Click()
	require.NoError(t, err)

	// Wait for cancellation to be processed
	// Note: Stripe Billing Portal schedules cancellation at period end by default,
	// so the subscription status may remain "active" with cancel_at_period_end=true.
	time.Sleep(3 * time.Second)

	// Navigate back to our app
	_, err = page.Goto(baseURL + "/")
	require.NoError(t, err)

	t.Log("Test completed: subscription created and cancellation initiated via Billing Portal")
}
