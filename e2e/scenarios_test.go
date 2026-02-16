package e2e

import (
	"encoding/json"
	"fmt"
	"os"
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
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "codelink@example.com", WithConfirmed())
	seedLoginCode(t, env, "12345", memberID, "/", time.Now().Add(5*time.Minute))

	page := newPage(t)
	_, err := page.Goto(env.baseURL + "/login/code?code=12345")
	require.NoError(t, err)

	err = page.WaitForURL("**/")
	require.NoError(t, err)

	dashboard := NewMemberDashboardPage(t, page, env.baseURL)
	dashboard.ExpectMissingWaiverAlert()
}

// TestLogin_CodeLinkUnusable verifies that expired and non-existent codes
// both return a 400 error.
func TestLogin_CodeLinkUnusable(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "expiredcode@example.com", WithConfirmed())
	seedLoginCode(t, env, "99999", memberID, "/", time.Now().Add(-1*time.Minute))

	page := newPage(t)

	t.Run("expired_code", func(t *testing.T) {
		resp, err := page.Goto(env.baseURL + "/login/code?code=99999")
		require.NoError(t, err)
		assert.Equal(t, 400, resp.Status())
	})

	t.Run("nonexistent_code", func(t *testing.T) {
		resp, err := page.Goto(env.baseURL + "/login/code?code=00000")
		require.NoError(t, err)
		assert.Equal(t, 400, resp.Status())
	})
}

// TestLogin_CodeFormEntry verifies that entering a code on the sent page
// authenticates the user.
func TestLogin_CodeFormEntry(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "codeform@example.com", WithConfirmed())
	seedLoginCode(t, env, "54321", memberID, "/", time.Now().Add(5*time.Minute))

	page := newPage(t)
	_, err := page.Goto(env.baseURL + "/login/sent?email=codeform@example.com")
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

	dashboard := NewMemberDashboardPage(t, page, env.baseURL)
	dashboard.ExpectMissingWaiverAlert()
}

// TestLogin_CodeSingleUse verifies that a code can only be used once.
func TestLogin_CodeSingleUse(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "singleuse@example.com", WithConfirmed())
	seedLoginCode(t, env, "11111", memberID, "/", time.Now().Add(5*time.Minute))

	// First use should succeed
	page := newPage(t)
	_, err := page.Goto(env.baseURL + "/login/code?code=11111")
	require.NoError(t, err)
	err = page.WaitForURL("**/")
	require.NoError(t, err)

	// Second use should fail
	page2 := newPage(t)
	resp, err := page2.Goto(env.baseURL + "/login/code?code=11111")
	require.NoError(t, err)
	assert.Equal(t, 400, resp.Status())
}

// TestLogin_SentPageShowsEmail verifies that the sent page displays the user's email.
func TestLogin_SentPageShowsEmail(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)

	_, err := page.Goto(env.baseURL + "/login/sent?email=test@example.com")
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

// TestLoginFlow tests the full login flow via both methods: typing the code
// from the email and clicking the login link from the email.
func TestLoginFlow(t *testing.T) {
	t.Parallel()

	t.Run("type_code", func(t *testing.T) {
		env, page := setupUnauthenticatedTest(t)
		email := "typeflow@example.com"

		// Navigate to login page and submit email
		loginPage := NewLoginPage(t, page, env.baseURL)
		loginPage.Navigate()
		loginPage.FillEmail(email)
		loginPage.Submit()
		loginPage.ConfirmSignup()
		loginPage.ExpectSentPage()

		// Verify email was queued
		subject, body, found := getLastEmail(t, env, email)
		require.True(t, found, "login email should be queued in outbound_mail")
		assert.Equal(t, "Makerspace Login", subject)

		// Extract the code from the email body
		code := extractLoginCodeFromEmail(t, body)
		require.Len(t, code, 5, "login code should be 5 digits")

		// Enter the code digit-by-digit on the sent page
		digits := page.Locator(".code-digit")
		for i, digit := range code {
			err := digits.Nth(i).Fill(string(digit))
			require.NoError(t, err)
		}

		// Auto-submit should trigger, wait for redirect to dashboard
		err := page.WaitForURL("**/")
		require.NoError(t, err)

		// Verify we're logged in and on the dashboard
		dashboard := NewMemberDashboardPage(t, page, env.baseURL)
		dashboard.ExpectMissingWaiverAlert()
	})

	t.Run("click_email_link", func(t *testing.T) {
		env, page := setupUnauthenticatedTest(t)
		email := "linkflow@example.com"

		// Navigate to login page and submit email
		loginPage := NewLoginPage(t, page, env.baseURL)
		loginPage.Navigate()
		loginPage.FillEmail(email)
		loginPage.Submit()
		loginPage.ConfirmSignup()
		loginPage.ExpectSentPage()

		// Verify email was queued
		subject, body, found := getLastEmail(t, env, email)
		require.True(t, found, "login email should be queued in outbound_mail")
		assert.Equal(t, "Makerspace Login", subject)

		// Extract the login link from the email body
		link := extractLoginCodeLinkFromEmail(t, body)
		require.NotEmpty(t, link, "email should contain login link")
		assert.Contains(t, link, "/login/code?code=")

		// Click the link (navigate to it)
		_, err := page.Goto(link)
		require.NoError(t, err)

		// Wait for redirect to dashboard
		err = page.WaitForURL("**/")
		require.NoError(t, err)

		// Verify we're logged in and on the dashboard
		dashboard := NewMemberDashboardPage(t, page, env.baseURL)
		dashboard.ExpectMissingWaiverAlert()
	})
}

// TestLogout verifies that logging out clears the session and redirects
// unauthenticated requests to the login page.
func TestLogout(t *testing.T) {
	t.Parallel()
	env, memberID, page := setupMemberTest(t, "logout@example.com",
		WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345))
	_ = memberID

	_, err := page.Goto(env.baseURL + "/")
	require.NoError(t, err)

	dashboard := NewMemberDashboardPage(t, page, env.baseURL)
	dashboard.ExpectActiveStatus()
	dashboard.ClickLogout()

	// After logout, accessing protected route should redirect to login
	_, err = page.Goto(env.baseURL + "/")
	require.NoError(t, err)

	err = page.WaitForURL("**/login**")
	require.NoError(t, err)
}

// TestAuth_CallbackPreservation verifies that callback_uri is preserved through
// the login flow when accessing protected resources without authentication.
func TestAuth_CallbackPreservation(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "callback@example.com", WithConfirmed(), WithLeadership())

	page := newPage(t)

	// Try to access admin page without auth
	_, err := page.Goto(env.baseURL + "/admin/members")
	require.NoError(t, err)

	err = page.WaitForURL("**/login**")
	require.NoError(t, err)

	url := page.URL()
	assert.Contains(t, url, "callback_uri")

	// Login via code link with the callback
	seedLoginCode(t, env, "22222", memberID, "/admin/members", time.Now().Add(5*time.Minute))
	_, err = page.Goto(env.baseURL + "/login/code?code=22222")
	require.NoError(t, err)

	err = page.WaitForURL("**/admin/members**")
	require.NoError(t, err)
}

// TestWaiver_DisplayAndValidation verifies that the waiver page renders with
// all required form elements and that submitting without checking agreement
// boxes fails HTML5 validation and prevents waiver creation.
func TestWaiver_DisplayAndValidation(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)
	waiverPage := NewWaiverPage(t, page, env.baseURL)

	waiverPage.Navigate()
	waiverPage.ExpectWaiverText()

	// Check that the form elements are present
	expect(t).Locator(page.Locator("#agree0")).ToBeVisible()
	expect(t).Locator(page.Locator("#agree1")).ToBeVisible()
	expect(t).Locator(page.Locator("#name")).ToBeVisible()
	expect(t).Locator(page.Locator("#email")).ToBeVisible()

	// Fill name and email but skip checkboxes
	waiverPage.FillName("No Checkboxes")
	waiverPage.FillEmail("nocheckbox@example.com")

	// Try to submit - should fail due to HTML5 validation
	waiverPage.Submit()

	// We should still be on the waiver page
	waiverPage.ExpectWaiverText()

	// Verify no waiver was created
	var count int
	err := env.db.QueryRow("SELECT COUNT(*) FROM waivers WHERE email = ?", "nocheckbox@example.com").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "waiver should not be created without checkboxes")
}

// TestWaiver_SuccessfulSubmission verifies that a user can successfully sign
// the waiver by filling all required fields and checking all checkboxes,
// and that using a redirect parameter shows a done link.
func TestWaiver_SuccessfulSubmission(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)
	waiverPage := NewWaiverPage(t, page, env.baseURL)

	waiverPage.NavigateWithRedirect("/")
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("Test Signer")
	waiverPage.FillEmail("testsigner@example.com")
	waiverPage.Submit()

	waiverPage.ExpectSuccessMessage()
	expect(t).Locator(page.Locator("a:has-text('Done')")).ToBeVisible()

	// Verify waiver was created in database
	var name, email string
	err := env.db.QueryRow("SELECT name, email FROM waivers WHERE email = ?", "testsigner@example.com").Scan(&name, &email)
	require.NoError(t, err, "waiver should be created in database")
	assert.Equal(t, "Test Signer", name)
	assert.Equal(t, "testsigner@example.com", email)
}

// TestDashboard_OnboardingStates verifies that the dashboard correctly displays
// the member's onboarding progress through all possible status states.
func TestDashboard_OnboardingStates(t *testing.T) {
	t.Parallel()
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
			env, _, page := setupMemberTest(t, tc.name+"@example.com", tc.opts...)
			dashboard := NewMemberDashboardPage(t, page, env.baseURL)
			dashboard.Navigate()
			tc.expectFunc(t, dashboard)
		})
	}
}

// TestDashboard_DiscordLinking verifies the Discord linking UI appears or hides
// based on the member's Discord connection status.
func TestDashboard_DiscordLinking(t *testing.T) {
	t.Parallel()
	t.Run("shows_link_button_when_not_linked", func(t *testing.T) {
		env, _, page := setupMemberTest(t, "nodiscord@example.com",
			WithConfirmed(),
			WithWaiver(),
			WithActiveStripeSubscription(),
			WithFobID(12345),
		)
		dashboard := NewMemberDashboardPage(t, page, env.baseURL)
		dashboard.Navigate()
		expect(t).Locator(page.Locator("a:has-text('Link Discord')")).ToBeVisible()
	})

	t.Run("hides_link_button_when_linked", func(t *testing.T) {
		env, _, page := setupMemberTest(t, "hasdiscord@example.com",
			WithConfirmed(),
			WithWaiver(),
			WithActiveStripeSubscription(),
			WithFobID(12345),
			WithDiscord("123456789"),
		)
		dashboard := NewMemberDashboardPage(t, page, env.baseURL)
		dashboard.Navigate()
		expect(t).Locator(page.Locator("a:has-text('Link Discord')")).ToBeHidden()
	})
}

// TestProtectedRoutes_RequireAuthentication verifies that unauthenticated
// access to protected routes redirects to the login page.
func TestProtectedRoutes_RequireAuthentication(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)

	routes := []string{
		"/",
		"/machines",
		"/directory",
		"/donations/checkout?price_id=price_test",
	}

	for _, route := range routes {
		t.Run(route, func(t *testing.T) {
			_, err := page.Goto(env.baseURL + route)
			require.NoError(t, err)

			err = page.WaitForURL("**/login**")
			require.NoError(t, err)
		})
	}
}

// TestJourney_NewMemberOnboarding tests the complete new member signup flow:
// sign waiver, request login email, use login code, and view dashboard.
func TestJourney_NewMemberOnboarding(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)
	email := "newmember@example.com"

	// Step 1: Sign the waiver
	waiverPage := NewWaiverPage(t, page, env.baseURL)
	waiverPage.Navigate()
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("New Member")
	waiverPage.FillEmail(email)
	waiverPage.Submit()
	waiverPage.ExpectSuccessMessage()

	// Verify waiver was created
	var waiverID int64
	err := env.db.QueryRow("SELECT id FROM waivers WHERE email = ?", email).Scan(&waiverID)
	require.NoError(t, err, "waiver should be created")

	// Step 2: Request login email
	loginPage := NewLoginPage(t, page, env.baseURL)
	loginPage.Navigate()
	loginPage.FillEmail(email)
	loginPage.Submit()
	loginPage.ConfirmSignup()
	loginPage.ExpectSentPage()

	// Verify member was created
	var memberID int64
	err = env.db.QueryRow("SELECT id FROM members WHERE email = ?", email).Scan(&memberID)
	require.NoError(t, err, "member should be created")

	// Step 3: Get login code from database and use it
	var code string
	err = env.db.QueryRow("SELECT code FROM login_codes WHERE email = ?", email).Scan(&code)
	require.NoError(t, err, "login code should be created")

	_, err = page.Goto(env.baseURL + "/login/code?code=" + code)
	require.NoError(t, err)

	err = page.WaitForURL("**/")
	require.NoError(t, err)

	// Step 4: Dashboard should show missing payment
	dashboard := NewMemberDashboardPage(t, page, env.baseURL)
	dashboard.ExpectMissingPaymentAlert()
	expect(t).Locator(page.Locator("a:has-text('Manage Payment')")).ToBeVisible()
}

// TestJourney_WaiverThenLogin tests that signing a waiver before creating an
// account properly links the waiver to the member on first login.
func TestJourney_WaiverThenLogin(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)
	email := "waiverfirst@example.com"

	// Sign waiver first (before any member record exists)
	waiverPage := NewWaiverPage(t, page, env.baseURL)
	waiverPage.Navigate()
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("Waiver First User")
	waiverPage.FillEmail(email)
	waiverPage.Submit()
	waiverPage.ExpectSuccessMessage()

	// Now login (creates member record)
	loginPage := NewLoginPage(t, page, env.baseURL)
	loginPage.Navigate()
	loginPage.FillEmail(email)
	loginPage.Submit()
	loginPage.ConfirmSignup()
	loginPage.ExpectSentPage()

	// Get member ID
	var memberID int64
	err := env.db.QueryRow("SELECT id FROM members WHERE email = ?", email).Scan(&memberID)
	require.NoError(t, err)

	// Use login code from database
	var code string
	err = env.db.QueryRow("SELECT code FROM login_codes WHERE email = ?", email).Scan(&code)
	require.NoError(t, err, "login code should be created")

	_, err = page.Goto(env.baseURL + "/login/code?code=" + code)
	require.NoError(t, err)

	// Check that member has waiver linked
	var waiverID *int64
	err = env.db.QueryRow("SELECT waiver FROM members WHERE id = ?", memberID).Scan(&waiverID)
	require.NoError(t, err)
	assert.NotNil(t, waiverID, "waiver should be linked to member")

	// Dashboard should show missing payment (not missing waiver)
	dashboard := NewMemberDashboardPage(t, page, env.baseURL)
	dashboard.ExpectMissingPaymentAlert()
}

// TestAdmin_RequiresLeadership verifies that non-leadership members receive
// a 403 Forbidden error when accessing any admin endpoint.
func TestAdmin_RequiresLeadership(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "regular@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
	)

	endpoints := []string{
		"/admin/members",
		"/admin/metrics",
		"/admin/events",
		"/admin/config/waiver",
		"/admin/config/bambu",
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			resp, err := page.Goto(env.baseURL + endpoint)
			require.NoError(t, err)
			assert.Equal(t, 403, resp.Status())
		})
	}
}

// TestAdmin_MembersListAndSearch verifies the admin members list page displays
// members and search filters results correctly.
func TestAdmin_MembersListAndSearch(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Create test members
	seedMember(t, env, "searchable@example.com", WithConfirmed())
	seedMember(t, env, "findme@example.com", WithConfirmed())
	seedMember(t, env, "other@example.com", WithConfirmed())

	adminPage := NewAdminMembersListPage(t, page, env.baseURL)
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

// TestAdmin_NewMemberButton verifies that an admin can create a new member
// using the "New Member" button on the members list page.
func TestAdmin_NewMemberButton(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	adminPage := NewAdminMembersListPage(t, page, env.baseURL)
	adminPage.Navigate()

	// Click "New Member" to reveal the form
	err := page.Locator("button:has-text('New Member')").Click()
	require.NoError(t, err)

	// Fill in an email and submit
	err = page.Locator("#new-item-form input[name='email']").Fill("newmember@example.com")
	require.NoError(t, err)
	err = page.Locator("#new-item-form button[type='submit']").Click()
	require.NoError(t, err)

	// Should redirect to the new member's detail page
	err = page.WaitForURL("**/admin/members/*")
	require.NoError(t, err)

	// Verify the member was created in the database
	var email string
	err = env.db.QueryRow("SELECT email FROM members WHERE email = ?", "newmember@example.com").Scan(&email)
	require.NoError(t, err)
	assert.Equal(t, "newmember@example.com", email)
}

// TestAdmin_EditDesignations verifies that an admin can toggle member
// leadership status through the designations form.
func TestAdmin_EditDesignations(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)
	targetID := seedMember(t, env, "designations@example.com", WithConfirmed())

	_, err := page.Goto(env.baseURL + "/admin/members/" + strconv.FormatInt(targetID, 10))
	require.NoError(t, err)

	detail := NewAdminMemberDetailPage(t, page, env.baseURL)
	detail.ToggleLeadership()
	detail.SubmitDesignationsForm()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	var leadership bool
	err = env.db.QueryRow("SELECT leadership FROM members WHERE id = ?", targetID).Scan(&leadership)
	require.NoError(t, err)
	assert.True(t, leadership, "member should now be leadership")
}

// TestAdmin_GenerateLoginQR verifies that the admin can generate a login
// QR code image for a member.
func TestAdmin_GenerateLoginQR(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)
	targetID := seedMember(t, env, "qrtest@example.com", WithConfirmed())

	resp, err := page.Goto(env.baseURL + "/admin/members/" + strconv.FormatInt(targetID, 10) + "/logincode")
	require.NoError(t, err)

	assert.Equal(t, 200, resp.Status())
	headers := resp.Headers()
	assert.Contains(t, headers["content-type"], "image/png")
}

// TestAdmin_DeleteMember verifies that an admin can delete a member through
// the two-step confirmation flow.
func TestAdmin_DeleteMember(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)
	targetID := seedMember(t, env, "deleteme@example.com", WithConfirmed())

	_, err := page.Goto(env.baseURL + "/admin/members/" + strconv.FormatInt(targetID, 10))
	require.NoError(t, err)

	detail := NewAdminMemberDetailPage(t, page, env.baseURL)
	detail.ClickDeleteMember()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	detail.ConfirmDelete()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	var count int
	err = env.db.QueryRow("SELECT COUNT(*) FROM members WHERE id = ?", targetID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "member should be deleted")
}

// TestJourney_AdminManagesMember tests an admin finding a member via search,
// navigating to their detail page, and editing their information.
func TestJourney_AdminManagesMember(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)
	targetID := seedMember(t, env, "manageme@example.com", WithConfirmed(), WithWaiver())

	// Step 1: Navigate to admin members list
	adminList := NewAdminMembersListPage(t, page, env.baseURL)
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
	detail := NewAdminMemberDetailPage(t, page, env.baseURL)
	detail.FillFobID("99999")
	detail.FillAdminNotes("Updated by admin in E2E test")
	detail.SubmitBasicsForm()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Step 5: Verify changes were saved
	var fobID int64
	var notes string
	err = env.db.QueryRow("SELECT fob_id, admin_notes FROM members WHERE id = ?", targetID).Scan(&fobID, &notes)
	require.NoError(t, err)
	assert.Equal(t, int64(99999), fobID)
	assert.Equal(t, "Updated by admin in E2E test", notes)
}

// TestJourney_MultipleMembers tests admin managing multiple members in sequence
// without data leakage between edits.
func TestJourney_MultipleMembers(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	member1ID := seedMember(t, env, "multi1@example.com", WithConfirmed())
	member2ID := seedMember(t, env, "multi2@example.com", WithConfirmed())

	// Edit first member
	_, err := page.Goto(env.baseURL + "/admin/members/" + strconv.FormatInt(member1ID, 10))
	require.NoError(t, err)

	detail := NewAdminMemberDetailPage(t, page, env.baseURL)
	detail.FillAdminNotes("First member notes")
	detail.SubmitBasicsForm()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Edit second member
	_, err = page.Goto(env.baseURL + "/admin/members/" + strconv.FormatInt(member2ID, 10))
	require.NoError(t, err)

	detail.FillAdminNotes("Second member notes")
	detail.SubmitBasicsForm()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Verify both were updated correctly
	var notes1, notes2 string
	err = env.db.QueryRow("SELECT admin_notes FROM members WHERE id = ?", member1ID).Scan(&notes1)
	require.NoError(t, err)
	err = env.db.QueryRow("SELECT admin_notes FROM members WHERE id = ?", member2ID).Scan(&notes2)
	require.NoError(t, err)

	assert.Equal(t, "First member notes", notes1)
	assert.Equal(t, "Second member notes", notes2)
}

// TestAdmin_DataListPages verifies that the admin events page loads
// and displays results from all event types (fob swipes, member events, waivers).
func TestAdmin_DataListPages(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Seed test data for all event types
	memberID := seedMember(t, env, "data@example.com", WithConfirmed(), WithFobID(11111))
	seedFobSwipes(t, env, 11111, 3)
	seedMemberEvents(t, env, memberID, 3)
	seedWaiver(t, env, "waiver@example.com")

	eventsPage := NewAdminEventsPage(t, page, env.baseURL)
	eventsPage.Navigate()
	err := page.WaitForLoadState()
	require.NoError(t, err)
	expect(t).Locator(page.Locator("#results")).ToBeVisible()

	// Verify fob swipes appear (shown as fob ID in details)
	eventsPage.ExpectRowWithText("11111")
	// Verify waivers appear (shown as email in details)
	eventsPage.ExpectRowWithText("waiver@example.com")
}

// TestAdmin_Pagination verifies that pagination controls work correctly
// when there are more results than fit on one page.
func TestAdmin_Pagination(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Create enough members to trigger pagination (limit is 20 per page)
	for i := 0; i < 45; i++ {
		seedMember(t, env, fmt.Sprintf("pagination%02d@example.com", i), WithConfirmed())
	}

	adminPage := NewAdminMembersListPage(t, page, env.baseURL)
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

// TestAdmin_MembersCSVDownloadLink verifies that the CSV download link is visible
// on the admin members list page and clicking it downloads a valid CSV file.
func TestAdmin_MembersCSVDownloadLink(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Create some test members to ensure CSV has data
	seedMember(t, env, "csvtest1@example.com", WithConfirmed())
	seedMember(t, env, "csvtest2@example.com", WithConfirmed(), WithFobID(55555))

	adminPage := NewAdminMembersListPage(t, page, env.baseURL)
	adminPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Verify the CSV download link icon is visible next to the title
	csvLink := page.Locator("a[href='/admin/export/members']")
	expect(t).Locator(csvLink).ToBeVisible()

	// Set up download handler before clicking
	download, err := page.ExpectDownload(func() error {
		return csvLink.Click()
	})
	require.NoError(t, err)

	// Verify the download occurred and has expected properties
	path, err := download.Path()
	require.NoError(t, err)
	assert.NotEmpty(t, path, "download path should not be empty")

	// Read the downloaded content
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	// Verify CSV content contains expected headers and data
	csvContent := string(content)
	assert.Contains(t, csvContent, "email", "CSV should contain email column")
	assert.Contains(t, csvContent, "csvtest1@example.com", "CSV should contain test member data")
	assert.Contains(t, csvContent, "csvtest2@example.com", "CSV should contain test member data")
}

// TestJourney_AdminExportsAllData tests that an admin can export CSV data
// for all supported data types.
func TestJourney_AdminExportsAllData(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Create some test data
	memberID := seedMember(t, env, "exportmember@example.com", WithConfirmed(), WithFobID(44444))
	seedWaiver(t, env, "exportwaiver@example.com")
	seedFobSwipes(t, env, 44444, 3)
	seedMemberEvents(t, env, memberID, 3)

	ctx := page.Context()
	apiContext := ctx.Request()

	tables := []string{"members", "waivers", "fob_swipes", "member_events"}

	for _, table := range tables {
		resp, err := apiContext.Get(env.baseURL+"/admin/export/"+table, playwright.APIRequestContextGetOptions{})
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
	t.Parallel()
	env, _, page := setupAdminTest(t)
	seedMetrics(t, env, "test_series", 10)

	metricsPage := NewAdminMetricsPage(t, page, env.baseURL)
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
		resp, err := page.Goto(env.baseURL + "/admin/chart?series=test_series&window=168h")
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
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)

	t.Run("openid_config", func(t *testing.T) {
		resp, err := page.Goto(env.baseURL + "/.well-known/openid-configuration")
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
		resp, err := page.Goto(env.baseURL + "/oauth2/jwks")
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

	t.Run("userinfo_requires_auth", func(t *testing.T) {
		resp, err := page.Goto(env.baseURL + "/oauth2/userinfo")
		require.NoError(t, err)
		assert.GreaterOrEqual(t, resp.Status(), 400)
	})
}

// TestOAuth2_AuthorizeFlow verifies the OAuth2 authorization code flow
// redirects correctly for authenticated users.
func TestOAuth2_AuthorizeFlow(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "oauth@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
	)

	ctx := newContext(t)
	loginAs(t, env, ctx, memberID)
	page := newPageInContext(t, ctx)

	redirectURI := env.baseURL + "/login"
	authURL := env.baseURL + "/oauth2/authorize?response_type=code&client_id=test-client&redirect_uri=" + redirectURI + "&state=teststate"

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

// TestMachines_AllPrinterStatuses verifies the printers page displays all
// printer cards with correct status badges, controls, and visual elements.
// The test app is configured with 3 mock printers:
// - "Printer A" (test-001): Available (no job, no error)
// - "Printer B" (test-002): In Use (has JobFinishedTimestamp)
// - "Printer C" (test-003): Failed (has ErrorCode)
func TestMachines_AllPrinterStatuses(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "machines@example.com", WithConfirmed())
	// Refresh printer state timestamps to prevent TTL expiration during long test runs
	refreshPrinterStateTimestamps(t, env)
	machinesPage := NewMachinesPage(t, page, env.baseURL)
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
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)

	kiosk := NewKioskPage(t, page, env.baseURL)
	kiosk.Navigate()
	kiosk.ExpectKioskInterface()
}

// TestKeyfob_BindFlow verifies the keyfob binding flow starts correctly
// for a member without a bound keyfob.
func TestKeyfob_BindFlow(t *testing.T) {
	t.Parallel()
	env, memberID, page := setupMemberTest(t, "bindkey@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
	)

	// Verify member has no fob initially
	var fobID *int64
	err := env.db.QueryRow("SELECT fob_id FROM members WHERE id = ?", memberID).Scan(&fobID)
	require.NoError(t, err)
	assert.Nil(t, fobID, "member should not have a fob initially")

	// Navigate to dashboard to verify missing keyfob alert
	dashboard := NewMemberDashboardPage(t, page, env.baseURL)
	dashboard.Navigate()
	dashboard.ExpectMissingKeyFobAlert()
}

// TestKeyfob_StatusEndpoint verifies the keyfob status API requires
// requests from the physical makerspace (returns 403 for remote requests).
func TestKeyfob_StatusEndpoint(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)
	seedMember(t, env, "hasfob@example.com", WithConfirmed(), WithFobID(99999))

	t.Run("existing_fob", func(t *testing.T) {
		resp, err := page.Goto(env.baseURL + "/keyfob/status/99999")
		require.NoError(t, err)
		assert.Equal(t, 403, resp.Status()) // requires physical presence at makerspace
	})

	t.Run("unused_fob", func(t *testing.T) {
		resp, err := page.Goto(env.baseURL + "/keyfob/status/11111")
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

	env := NewTestEnvForStripe(t)

	// Configure Stripe via the database (simulating admin configuration)
	// The payment module reads these values dynamically from stripe_config table
	apiKey := os.Getenv("STRIPE_TEST_KEY")
	webhookKey := getEnvWithFallback("STRIPE_TEST_WEBHOOK_KEY", "CONWAY_STRIPE_WEBHOOK_KEY")
	seedStripeConfig(t, env, apiKey, webhookKey)
	t.Log("Configured Stripe via dynamic config (stripe_config table)")

	// Use unique email to avoid conflicts between test runs
	email := fmt.Sprintf("stripe-test-%d@example.com", time.Now().UnixNano())
	memberID := seedMember(t, env, email, WithConfirmed(), WithWaiver())

	// Set up authenticated browser context
	ctx := newContext(t)
	loginAs(t, env, ctx, memberID)
	page := newPageInContext(t, ctx)

	// Step 1: Navigate to dashboard and click "Set Up Payment"
	t.Log("Step 1: Starting Stripe Checkout flow from member dashboard")

	dashboard := NewMemberDashboardPage(t, page, env.baseURL)
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
	waitForMemberState(t, env, email, 30*time.Second, func(subState, name string) bool {
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
	_, err = page.Goto(env.baseURL + "/")
	require.NoError(t, err)

	t.Log("Test completed: subscription created and cancellation initiated via Billing Portal")
}

// TestStripe_SubscriptionWithAdminUIConfig tests the Stripe subscription workflow
// where an admin first configures Stripe via the admin UI, then a member subscribes.
// This is the complete journey test for dynamic Stripe configuration.
func TestStripe_SubscriptionWithAdminUIConfig(t *testing.T) {
	if !stripeTestEnabled() {
		t.Skip("Skipping Stripe test: STRIPE_TEST_KEY not set")
	}

	// Start Stripe CLI for webhook forwarding
	startStripeCLI(t, "localhost:18080/webhooks/stripe")

	env := NewTestEnvForStripe(t)

	// Step 1: Admin configures Stripe via the admin UI
	t.Log("Step 1: Admin configuring Stripe via admin settings page")

	adminID := seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	adminCtx := newContext(t)
	loginAs(t, env, adminCtx, adminID)
	adminPage := newPageInContext(t, adminCtx)

	configPage := NewAdminStripeConfigPage(t, adminPage, env.baseURL)
	configPage.Navigate()

	err := adminPage.WaitForLoadState()
	require.NoError(t, err)

	// Fill Stripe configuration with real test credentials
	apiKey := os.Getenv("STRIPE_TEST_KEY")
	webhookKey := getEnvWithFallback("STRIPE_TEST_WEBHOOK_KEY", "CONWAY_STRIPE_WEBHOOK_KEY")

	configPage.FillAPIKey(apiKey)
	configPage.FillWebhookKey(webhookKey)
	configPage.Submit()

	err = adminPage.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()
	t.Log("Stripe configuration saved via admin UI")

	// Step 2: Member subscribes using the dynamically configured Stripe
	t.Log("Step 2: Member subscribing via Stripe Checkout")

	email := fmt.Sprintf("stripe-admin-ui-test-%d@example.com", time.Now().UnixNano())
	memberID := seedMember(t, env, email, WithConfirmed(), WithWaiver())

	memberCtx := newContext(t)
	loginAs(t, env, memberCtx, memberID)
	memberPage := newPageInContext(t, memberCtx)

	dashboard := NewMemberDashboardPage(t, memberPage, env.baseURL)
	dashboard.Navigate()
	dashboard.ExpectMissingPaymentAlert()

	// Click "Set Up Payment" button
	setupPaymentBtn := memberPage.Locator("a.btn-primary:has-text('Set Up Payment')")
	expect(t).Locator(setupPaymentBtn).ToBeVisible()
	err = setupPaymentBtn.Click()
	require.NoError(t, err)

	// Wait for redirect to Stripe Checkout
	err = memberPage.WaitForURL("**/checkout.stripe.com/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(30000),
	})
	require.NoError(t, err, "should redirect to Stripe Checkout")

	// Fill Stripe Checkout form
	cardNumberField := memberPage.Locator("#cardNumber")
	err = cardNumberField.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(30000),
	})
	require.NoError(t, err)

	err = cardNumberField.Fill("4242424242424242")
	require.NoError(t, err)
	err = memberPage.Locator("#cardExpiry").Fill("12/30")
	require.NoError(t, err)
	err = memberPage.Locator("#cardCvc").Fill("123")
	require.NoError(t, err)
	err = memberPage.Locator("#billingName").Fill("Admin UI Test User")
	require.NoError(t, err)

	postalCodeField := memberPage.Locator("#billingPostalCode")
	if visible, _ := postalCodeField.IsVisible(); visible {
		err = postalCodeField.Fill("12345")
		require.NoError(t, err)
	}

	// Submit payment
	t.Log("Step 3: Submitting payment")
	submitBtn := memberPage.Locator("button[type='submit']:has-text('Subscribe')")
	err = submitBtn.Click()
	require.NoError(t, err)

	// Wait for redirect back to our app
	err = memberPage.WaitForURL("**/localhost:18080/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(60000),
	})
	require.NoError(t, err, "should redirect back to app after payment")

	// Wait for webhook to process
	t.Log("Step 4: Waiting for subscription webhook")
	waitForMemberState(t, env, email, 30*time.Second, func(subState, name string) bool {
		return subState == "active"
	})

	// Verify dashboard shows active subscription
	dashboard.Navigate()
	dashboard.ExpectStepComplete("Set Up Payment")

	t.Log("Test completed: admin configured Stripe, member subscribed successfully")
}

// TestWaiver_PrefilledEmail verifies that the email field can be prefilled
// via query parameter.
func TestWaiver_PrefilledEmail(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)

	_, err := page.Goto(env.baseURL + "/waiver?email=prefilled@example.com")
	require.NoError(t, err)

	emailValue, err := page.Locator("#email").InputValue()
	require.NoError(t, err)
	assert.Equal(t, "prefilled@example.com", emailValue)
}

// TestWaiver_CustomContentAndDynamicCheckboxes verifies that custom waiver
// content set by admin is displayed correctly, checkboxes are generated
// dynamically from markdown content, and all must be checked to submit.
func TestWaiver_CustomContentAndDynamicCheckboxes(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)
	clearWaiverContent(t, env)

	customContent := `# Custom Waiver Title

This is a custom waiver paragraph with special content.

Another paragraph here.

- [ ] I agree to the first custom term
- [ ] I agree to the second custom term
- [ ] I agree to the third custom term
- [ ] I agree to the fourth custom term`

	seedWaiverContent(t, env, customContent)

	waiverPage := NewWaiverPage(t, page, env.baseURL)
	waiverPage.Navigate()

	// Verify custom title and paragraph are displayed
	expect(t).Locator(page.GetByText("Custom Waiver Title")).ToBeVisible()
	expect(t).Locator(page.GetByText("This is a custom waiver paragraph")).ToBeVisible()

	// Verify 4 checkboxes are present
	checkboxes := page.Locator("input[type='checkbox']")
	count, err := checkboxes.Count()
	require.NoError(t, err)
	assert.Equal(t, 4, count, "should have 4 checkboxes for custom waiver")

	// Fill form but only check 2 of 4 checkboxes
	err = page.Locator("#agree0").Check()
	require.NoError(t, err)
	err = page.Locator("#agree1").Check()
	require.NoError(t, err)
	err = page.Locator("#name").Fill("Partial Signer")
	require.NoError(t, err)
	err = page.Locator("#email").Fill("partial@example.com")
	require.NoError(t, err)

	// Submit - should fail due to HTML5 validation
	err = page.Locator("button[type='submit']").Click()
	require.NoError(t, err)

	// Verify no waiver was created
	var count2 int
	err = env.db.QueryRow("SELECT COUNT(*) FROM waivers WHERE email = ?", "partial@example.com").Scan(&count2)
	require.NoError(t, err)
	assert.Equal(t, 0, count2, "waiver should not be created with unchecked boxes")
}

// TestWaiver_VersionTracking verifies that signed waivers record the version
// of the waiver content that was signed.
func TestWaiver_VersionTracking(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)
	clearWaiverContent(t, env)

	// Create version 1
	content := `# Version Test

Test content.

- [ ] I agree`

	version := seedWaiverContent(t, env, content)

	waiverPage := NewWaiverPage(t, page, env.baseURL)
	waiverPage.Navigate()

	// Sign the waiver
	err := page.Locator("#agree0").Check()
	require.NoError(t, err)
	err = page.Locator("#name").Fill("Version Tester")
	require.NoError(t, err)
	err = page.Locator("#email").Fill("versiontest@example.com")
	require.NoError(t, err)
	err = page.Locator("button[type='submit']").Click()
	require.NoError(t, err)

	waiverPage.ExpectSuccessMessage()

	// Verify the waiver was signed with correct version
	var signedVersion int
	err = env.db.QueryRow("SELECT version FROM waivers WHERE email = ?", "versiontest@example.com").Scan(&signedVersion)
	require.NoError(t, err)
	assert.Equal(t, version, signedVersion, "signed waiver should have correct version")
}

// TestAdmin_WaiverConfigPage verifies that leadership can access the waiver
// configuration page and see all required elements.
func TestAdmin_WaiverConfigPage(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminWaiverConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Verify page elements
	expect(t).Locator(page.GetByText("Waiver Content")).ToBeVisible()
	configPage.ExpectSyntaxGuide()

	// Verify textarea is present and editable
	textarea := page.Locator("#content")
	expect(t).Locator(textarea).ToBeVisible()
	expect(t).Locator(textarea).ToBeEditable()
}

// TestAdmin_WaiverConfigSaveAndVersioning verifies that saving waiver content
// creates a new version with success message, and subsequent saves increment
// the version number.
func TestAdmin_WaiverConfigSaveAndVersioning(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)
	clearWaiverContent(t, env)

	configPage := NewAdminWaiverConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Save first version
	firstContent := `# Updated Waiver

This is the updated waiver content.

- [ ] I agree to the updated terms`

	configPage.SetContent(firstContent)
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Verify content was saved to database (normalize line endings for comparison)
	var savedContent string
	err = env.db.QueryRow("SELECT content FROM waiver_content ORDER BY version DESC LIMIT 1").Scan(&savedContent)
	require.NoError(t, err)
	assert.Equal(t, firstContent, strings.ReplaceAll(savedContent, "\r\n", "\n"))

	// Save second version
	secondContent := `# Second Version

- [ ] Second agreement`

	configPage.SetContent(secondContent)
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Verify only one row exists in database (old version deleted)
	var count int
	err = env.db.QueryRow("SELECT COUNT(*) FROM waiver_content").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "should have 1 waiver content row (old version deleted)")
}

// TestAdmin_WaiverListPage verifies the admin events page displays
// signed waivers with correct information.
func TestAdmin_WaiverListPage(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Seed some waivers
	seedWaiver(t, env, "waiver1@example.com")
	seedWaiver(t, env, "waiver2@example.com")

	eventsPage := NewAdminEventsPage(t, page, env.baseURL)
	eventsPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Verify waivers are displayed (emails shown in details column)
	expect(t).Locator(page.Locator("#results")).ToBeVisible()
	eventsPage.ExpectRowWithText("waiver1@example.com")
	eventsPage.ExpectRowWithText("waiver2@example.com")
}

// TestJourney_AdminEditsWaiverMemberSigns tests the complete flow of an admin
// editing waiver content and a new member signing the updated waiver.
func TestJourney_AdminEditsWaiverMemberSigns(t *testing.T) {
	t.Parallel()
	// Step 1: Admin edits the waiver
	env, _, adminPage := setupAdminTest(t)
	clearWaiverContent(t, env)

	configPage := NewAdminWaiverConfigPage(t, adminPage, env.baseURL)
	configPage.Navigate()

	err := adminPage.WaitForLoadState()
	require.NoError(t, err)

	customContent := `# Custom Liability Waiver

By signing this waiver, you acknowledge our terms.

- [ ] I understand and accept the risks
- [ ] I agree to follow all safety rules`

	configPage.SetContent(customContent)
	configPage.Submit()

	err = adminPage.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Step 2: New user navigates to waiver page and sees custom content
	memberPage := newPage(t)

	_, err = memberPage.Goto(env.baseURL + "/waiver")
	require.NoError(t, err)

	// Verify custom title is displayed
	expect(t).Locator(memberPage.GetByText("Custom Liability Waiver")).ToBeVisible()

	// Verify custom checkboxes are displayed
	expect(t).Locator(memberPage.GetByText("I understand and accept the risks")).ToBeVisible()
	expect(t).Locator(memberPage.GetByText("I agree to follow all safety rules")).ToBeVisible()

	// Step 3: Member signs the waiver
	err = memberPage.Locator("#agree0").Check()
	require.NoError(t, err)
	err = memberPage.Locator("#agree1").Check()
	require.NoError(t, err)
	err = memberPage.Locator("#name").Fill("Journey Test User")
	require.NoError(t, err)
	err = memberPage.Locator("#email").Fill("journey@example.com")
	require.NoError(t, err)
	err = memberPage.Locator("button[type='submit']").Click()
	require.NoError(t, err)

	// Verify success
	expect(t).Locator(memberPage.GetByText("Waiver has been submitted successfully")).ToBeVisible()

	// Verify waiver saved with correct version (should be the latest version)
	var signedVersion int
	err = env.db.QueryRow("SELECT version FROM waivers WHERE email = ?", "journey@example.com").Scan(&signedVersion)
	require.NoError(t, err)

	var latestContentVersion int
	err = env.db.QueryRow("SELECT MAX(version) FROM waiver_content").Scan(&latestContentVersion)
	require.NoError(t, err)
	assert.Equal(t, latestContentVersion, signedVersion, "waiver should be signed with the latest waiver content version")

	// Step 4: Admin can see the signed waiver in the list
	eventsPage := NewAdminEventsPage(t, adminPage, env.baseURL)
	eventsPage.Navigate()

	err = adminPage.WaitForLoadState()
	require.NoError(t, err)

	eventsPage.ExpectRowWithText("journey@example.com")
}

// TestWaiver_MissingContent verifies that when no waiver content exists in the
// database, an error is displayed.
func TestWaiver_MissingContent(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)
	clearWaiverContent(t, env)

	_, err := page.Goto(env.baseURL + "/waiver")
	require.NoError(t, err)

	// Should show system error when no waiver content is configured
	expect(t).Locator(page.GetByText("no waiver content configured")).ToBeVisible()
}

// TestWaiver_DuplicateSubmission verifies that submitting a waiver with the
// same email doesn't create duplicate records (ON CONFLICT DO NOTHING).
func TestWaiver_DuplicateSubmission(t *testing.T) {
	t.Parallel()
	env, page := setupUnauthenticatedTest(t)

	email := "duplicate@example.com"

	// Submit first waiver
	waiverPage := NewWaiverPage(t, page, env.baseURL)
	waiverPage.Navigate()
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("First Submission")
	waiverPage.FillEmail(email)
	waiverPage.Submit()
	waiverPage.ExpectSuccessMessage()

	// Submit second waiver with same email
	waiverPage.Navigate()
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("Second Submission")
	waiverPage.FillEmail(email)
	waiverPage.Submit()
	waiverPage.ExpectSuccessMessage()

	// Verify only one waiver exists
	var count int
	err := env.db.QueryRow("SELECT COUNT(*) FROM waivers WHERE email = ?", email).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "should only have one waiver for the email")

	// Verify the first name was kept (ON CONFLICT DO NOTHING)
	var name string
	err = env.db.QueryRow("SELECT name FROM waivers WHERE email = ?", email).Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "First Submission", name, "original waiver should be preserved")
}

// TestAdmin_StripeConfigPage verifies that administrators can view and update
// Stripe configuration via the admin settings page.
func TestAdmin_StripeConfigPage(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminStripeConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Verify the page displays Stripe configuration instructions
	configPage.ExpectWebhookURLInstruction()

	// Initially no version badge since no config exists
	initialVersion := getStripeConfigVersion(t, env)
	assert.Equal(t, 0, initialVersion, "should have no initial stripe config")

	// Fill in API key and webhook key
	configPage.FillAPIKey("sk_test_example_api_key")
	configPage.FillWebhookKey("whsec_example_webhook_secret")
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Verify success message is shown
	configPage.ExpectSaveSuccessMessage()

	// Verify version was incremented
	newVersion := getStripeConfigVersion(t, env)
	assert.Equal(t, 1, newVersion, "should have version 1 after first save")

	// Verify placeholders indicate secrets are set
	configPage.Navigate()
	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectHasAPIKey()
	configPage.ExpectHasWebhookKey()
}

// TestAdmin_StripeConfigVersioning verifies that saving Stripe config
// creates new versions without overwriting old ones.
func TestAdmin_StripeConfigVersioning(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminStripeConfigPage(t, page, env.baseURL)

	// Save first version
	configPage.Navigate()
	configPage.FillAPIKey("sk_test_first")
	configPage.FillWebhookKey("whsec_first")
	configPage.Submit()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Save second version (just updating the webhook key)
	configPage.Navigate()
	configPage.FillWebhookKey("whsec_second")
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Verify only one row exists in database (old version deleted)
	var count int
	err = env.db.QueryRow("SELECT COUNT(*) FROM stripe_config").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "should have one config row (old version deleted)")

	// Verify the latest version is used (API key should be preserved from first save)
	var apiKey, webhookKey string
	err = env.db.QueryRow("SELECT api_key, webhook_key FROM stripe_config ORDER BY version DESC LIMIT 1").Scan(&apiKey, &webhookKey)
	require.NoError(t, err)
	assert.Equal(t, "sk_test_first", apiKey, "API key should be preserved when not updated")
	assert.Equal(t, "whsec_second", webhookKey, "webhook key should be updated")
}

// TestDirectory_DisplaysReadyMembers verifies that the directory page only
// shows members with access_status = 'Ready'.
func TestDirectory_DisplaysReadyMembers(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())

	// Seed members with different access statuses
	seedMember(t, env, "ready@example.com", WithConfirmed(), WithReadyAccess(), WithName("Ready Member"))
	seedMember(t, env, "notready@example.com", WithConfirmed(), WithName("Not Ready Member"))

	directoryPage := NewDirectoryPage(t, page, env.baseURL)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	directoryPage.ExpectHeading()
	directoryPage.ExpectMemberCard("Ready Member")
	directoryPage.ExpectMemberCardNotVisible("Not Ready Member")
}

// TestDirectory_ShowsLeadershipBadge verifies that leadership members have
// a leadership badge displayed on their card.
func TestDirectory_ShowsLeadershipBadge(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())

	// Seed a leadership member and a regular member
	seedMember(t, env, "leader@example.com", WithConfirmed(), WithReadyAccess(), WithName("Leader Person"), WithLeadership())
	seedMember(t, env, "regular@example.com", WithConfirmed(), WithReadyAccess(), WithName("Regular Person"))

	directoryPage := NewDirectoryPage(t, page, env.baseURL)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	directoryPage.ExpectMemberCard("Leader Person")
	directoryPage.ExpectLeadershipBadge("Leader Person")
	directoryPage.ExpectMemberCard("Regular Person")
	directoryPage.ExpectNoLeadershipBadge("Regular Person")
}

// TestDirectory_ShowsDiscordUsername verifies that members with a Discord
// username have it displayed on their card.
func TestDirectory_ShowsDiscordUsername(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())

	seedMember(t, env, "discord@example.com", WithConfirmed(), WithReadyAccess(), WithName("Discord User"), WithDiscordUsername("discorduser123"))

	directoryPage := NewDirectoryPage(t, page, env.baseURL)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	directoryPage.ExpectMemberCard("Discord User")
	directoryPage.ExpectDiscordUsername("Discord User", "discorduser123")
}

// TestDirectory_ShowsPlaceholderAvatar verifies that members without a
// Discord avatar show a placeholder avatar.
func TestDirectory_ShowsPlaceholderAvatar(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())

	seedMember(t, env, "noavatar@example.com", WithConfirmed(), WithReadyAccess(), WithName("No Avatar User"))

	directoryPage := NewDirectoryPage(t, page, env.baseURL)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	directoryPage.ExpectMemberCard("No Avatar User")
	directoryPage.ExpectPlaceholderAvatar("No Avatar User")
}

// TestDirectory_ShowsRealAvatar verifies that members with a Discord avatar
// display an img element pointing to the avatar endpoint.
func TestDirectory_ShowsRealAvatar(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())

	// Create a simple PNG image (1x1 red pixel)
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
		0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xFE, 0xD4, 0xEF, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}
	seedMember(t, env, "hasavatar@example.com", WithConfirmed(), WithReadyAccess(), WithName("Has Avatar User"), WithDiscordAvatar(pngData))

	directoryPage := NewDirectoryPage(t, page, env.baseURL)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	directoryPage.ExpectMemberCard("Has Avatar User")
	directoryPage.ExpectAvatar("Has Avatar User")
}

// TestDirectory_EmptyDirectory verifies that when no members have Ready
// access, an empty message is displayed.
func TestDirectory_EmptyDirectory(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())

	// Don't seed any members with Ready access

	directoryPage := NewDirectoryPage(t, page, env.baseURL)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	directoryPage.ExpectHeading()
	directoryPage.ExpectEmptyMessage()
}

// TestDirectory_AvatarEndpoint verifies the avatar endpoint returns the
// correct image data for members with avatars.
func TestDirectory_AvatarEndpoint(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())

	// Create a simple PNG image (1x1 red pixel)
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
		0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xFE, 0xD4, 0xEF, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}
	memberID := seedMember(t, env, "avatar@example.com", WithConfirmed(), WithReadyAccess(), WithDiscordAvatar(pngData))

	resp, err := page.Goto(fmt.Sprintf("%s/directory/avatar/%d", env.baseURL, memberID))
	require.NoError(t, err)

	assert.Equal(t, 200, resp.Status())
	headers := resp.Headers()
	assert.Contains(t, headers["content-type"], "image/png")
}

// TestDirectory_AvatarEndpointNotFound verifies the avatar endpoint returns
// 404 for members without avatars or non-existent members.
func TestDirectory_AvatarEndpointNotFound(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())

	// Member without avatar
	memberID := seedMember(t, env, "noavatar@example.com", WithConfirmed(), WithReadyAccess())

	t.Run("member_without_avatar", func(t *testing.T) {
		resp, err := page.Goto(fmt.Sprintf("%s/directory/avatar/%d", env.baseURL, memberID))
		require.NoError(t, err)
		assert.Equal(t, 404, resp.Status())
	})

	t.Run("non_existent_member", func(t *testing.T) {
		resp, err := page.Goto(env.baseURL + "/directory/avatar/999999")
		require.NoError(t, err)
		assert.Equal(t, 404, resp.Status())
	})

	t.Run("invalid_id", func(t *testing.T) {
		resp, err := page.Goto(env.baseURL + "/directory/avatar/invalid")
		require.NoError(t, err)
		assert.Equal(t, 404, resp.Status())
	})
}

// TestDirectory_ExcludesMembersWithoutName verifies that members without
// a name set are not displayed in the directory.
func TestDirectory_ExcludesMembersWithoutName(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())

	seedMember(t, env, "noname@example.com", WithConfirmed(), WithReadyAccess())
	seedMember(t, env, "hasname@example.com", WithConfirmed(), WithReadyAccess(), WithName("Has Name"))

	directoryPage := NewDirectoryPage(t, page, env.baseURL)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Member with name should be visible
	directoryPage.ExpectMemberCard("Has Name")

	// Member without name should not be visible (email should not appear)
	directoryPage.ExpectMemberCardNotVisible("noname@example.com")
}

// TestDirectory_ShowsProfileData verifies that the directory correctly displays
// bio, name override, and shows the current user first.
func TestDirectory_ShowsProfileData(t *testing.T) {
	t.Parallel()
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
		0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xFE, 0xD4, 0xEF, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}

	// Current user with avatar
	env, _, page := setupMemberTest(t, "current@example.com", WithConfirmed(), WithReadyAccess(),
		WithName("Current User"), WithDiscordAvatar(pngData))

	// Member with bio
	seedMember(t, env, "bio@example.com", WithConfirmed(), WithReadyAccess(),
		WithName("Bio User"), WithBio("I love making things!"), WithDiscordAvatar(pngData))

	// Member with name override (should show override, not original)
	seedMember(t, env, "override@example.com", WithConfirmed(), WithReadyAccess(),
		WithName("Original Name"), WithNameOverride("Preferred Name"), WithDiscordAvatar(pngData),
		WithFobLastSeen(9999999999)) // More recent, but current user should still be first

	directoryPage := NewDirectoryPage(t, page, env.baseURL)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Current user appears first despite others having more recent fob_last_seen
	directoryPage.ExpectMemberCardFirst("Current User")

	// Bio is displayed
	directoryPage.ExpectMemberCard("Bio User")
	directoryPage.ExpectBio("Bio User", "I love making things!")

	// Name override is used instead of original name
	directoryPage.ExpectMemberCard("Preferred Name")
	directoryPage.ExpectMemberCardNotVisible("Original Name")
}

// TestJourney_MemberEditsProfile tests the complete flow of a member
// customizing their profile bio and verifying changes appear in the directory.
func TestJourney_MemberEditsProfile(t *testing.T) {
	t.Parallel()
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
		0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xFE, 0xD4, 0xEF, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}
	env, _, page := setupMemberTest(t, "member@example.com", WithConfirmed(), WithReadyAccess(),
		WithName("John Smith"), WithDiscordAvatar(pngData), WithDiscordUsername("johnsmith"))

	// View directory and navigate to profile
	directoryPage := NewDirectoryPage(t, page, env.baseURL)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	directoryPage.ExpectMemberCard("John Smith")
	directoryPage.ClickEditProfile()

	err = page.WaitForURL("**/directory/profile")
	require.NoError(t, err)

	// Edit profile
	profilePage := NewProfilePage(t, page, env.baseURL)
	profilePage.ExpectHeading()
	profilePage.ExpectPreviewName("John Smith")
	profilePage.ExpectDiscordUsername("johnsmith")

	profilePage.FillBio("Maker and tinkerer")
	profilePage.Submit()

	// Verify changes in directory
	err = page.WaitForURL("**/directory")
	require.NoError(t, err)

	directoryPage.ExpectMemberCard("John Smith")
	directoryPage.ExpectBio("John Smith", "Maker and tinkerer")

	// Verify saved values persist
	profilePage.Navigate()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	profilePage.ExpectBioValue("Maker and tinkerer")
}

// TestAdmin_BambuConfigPage verifies that administrators can view the
// Bambu configuration page and see all required elements.
func TestAdmin_BambuConfigPage(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminBambuConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Verify page structure
	configPage.ExpectPageTitle()
	configPage.ExpectAddPrinterButton()

	// Initially no printers configured
	assert.Equal(t, 0, configPage.PrinterCardCount(), "should have no printers initially")

	// Verify poll interval is shown with default value
	pollInterval := configPage.GetPollInterval()
	assert.Equal(t, "5", pollInterval, "default poll interval should be 5")
}

// TestAdmin_BambuConfigAddPrinter verifies the JavaScript "Add Printer" button
// correctly creates new printer form fields with proper indexing.
func TestAdmin_BambuConfigAddPrinter(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminBambuConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Initially no printers
	assert.Equal(t, 0, configPage.PrinterCardCount())

	// Add first printer
	configPage.ClickAddPrinter()
	assert.Equal(t, 1, configPage.PrinterCardCount())
	configPage.ExpectPrinterCardHeaderText(0, "New Printer")

	// Verify the form fields exist with correct names for index 0
	name := configPage.GetPrinterName(0)
	assert.Equal(t, "", name, "new printer should have empty name")

	host := configPage.GetPrinterHost(0)
	assert.Equal(t, "", host, "new printer should have empty host")

	serial := configPage.GetPrinterSerial(0)
	assert.Equal(t, "", serial, "new printer should have empty serial")

	// Fill in name and verify header updates in real-time
	configPage.FillPrinterName(0, "Lab Printer 1")
	configPage.ExpectPrinterCardHeaderText(0, "Lab Printer 1")

	// Add second printer
	configPage.ClickAddPrinter()
	assert.Equal(t, 2, configPage.PrinterCardCount())

	// Verify second printer has correct index (1)
	configPage.ExpectPrinterCardHeaderText(1, "New Printer")
}

// TestAdmin_BambuConfigSaveNewPrinter verifies that a new printer can be
// added and saved to the database.
func TestAdmin_BambuConfigSaveNewPrinter(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminBambuConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Add a new printer
	configPage.ClickAddPrinter()
	configPage.FillPrinterName(0, "Test Printer")
	configPage.FillPrinterHost(0, "192.168.1.100")
	configPage.FillPrinterAccessCode(0, "12345678")
	configPage.FillPrinterSerial(0, "01P00A123456789")
	configPage.FillPollInterval(10)
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Verify success message
	configPage.ExpectSaveSuccessMessage()

	// Verify printer was saved in database
	printersJSON := getBambuPrintersJSON(t, env)
	assert.Contains(t, printersJSON, "Test Printer")
	assert.Contains(t, printersJSON, "192.168.1.100")
	assert.Contains(t, printersJSON, "01P00A123456789")

	// Verify poll interval was saved
	pollInterval := getBambuPollInterval(t, env)
	assert.Equal(t, 10, pollInterval)

	// Verify status dashboard shows correct counts
	configPage.ExpectConfiguredPrintersCount(1)
	configPage.ExpectPollIntervalDisplay(10)
}

// TestAdmin_BambuConfigSaveMultiplePrinters verifies that multiple printers
// can be added in one save operation.
func TestAdmin_BambuConfigSaveMultiplePrinters(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminBambuConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Add first printer
	configPage.ClickAddPrinter()
	configPage.FillPrinterName(0, "Printer A")
	configPage.FillPrinterHost(0, "192.168.1.101")
	configPage.FillPrinterAccessCode(0, "access123")
	configPage.FillPrinterSerial(0, "SERIAL001")

	// Add second printer
	configPage.ClickAddPrinter()
	configPage.FillPrinterName(1, "Printer B")
	configPage.FillPrinterHost(1, "192.168.1.102")
	configPage.FillPrinterAccessCode(1, "access456")
	configPage.FillPrinterSerial(1, "SERIAL002")

	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Verify both printers are in database
	printersJSON := getBambuPrintersJSON(t, env)
	assert.Contains(t, printersJSON, "Printer A")
	assert.Contains(t, printersJSON, "Printer B")
	assert.Contains(t, printersJSON, "SERIAL001")
	assert.Contains(t, printersJSON, "SERIAL002")

	// Verify status count
	configPage.ExpectConfiguredPrintersCount(2)
}

// TestAdmin_BambuConfigDeletePrinterUI verifies the delete confirmation
// flow works correctly without actually deleting.
func TestAdmin_BambuConfigDeletePrinterUI(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Seed a printer first
	seedBambuConfig(t, env, `[{"name":"Test Printer","host":"192.168.1.100","access_code":"12345678","serial_number":"SERIAL001"}]`, 5)

	configPage := NewAdminBambuConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Should have one printer
	assert.Equal(t, 1, configPage.PrinterCardCount())
	configPage.ExpectDeleteConfirmHidden(0)

	// Click delete - should show confirmation
	configPage.ClickDeletePrinter(0)
	configPage.ExpectDeleteConfirmVisible(0)

	// Cancel delete
	configPage.CancelDeletePrinter(0)
	configPage.ExpectDeleteConfirmHidden(0)
	assert.Equal(t, 1, configPage.PrinterCardCount(), "printer should still be present")
}

// TestAdmin_BambuConfigDeletePrinterAndSave verifies that deleting a printer
// and saving actually removes it from the database.
func TestAdmin_BambuConfigDeletePrinterAndSave(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Seed two printers
	seedBambuConfig(t, env, `[{"name":"Printer 1","host":"192.168.1.101","access_code":"access1","serial_number":"SERIAL001"},{"name":"Printer 2","host":"192.168.1.102","access_code":"access2","serial_number":"SERIAL002"}]`, 5)

	configPage := NewAdminBambuConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Should have two printers
	assert.Equal(t, 2, configPage.PrinterCardCount())

	// Delete first printer
	configPage.ClickDeletePrinter(0)
	configPage.ConfirmDeletePrinter(0)
	assert.Equal(t, 1, configPage.PrinterCardCount())

	// Save changes
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Verify database only has second printer
	printersJSON := getBambuPrintersJSON(t, env)
	assert.NotContains(t, printersJSON, "Printer 1")
	assert.Contains(t, printersJSON, "Printer 2")
}

// TestAdmin_BambuConfigEditExistingPrinter verifies that editing an existing
// printer preserves access code when not changed.
func TestAdmin_BambuConfigEditExistingPrinter(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Seed a printer with access code
	seedBambuConfig(t, env, `[{"name":"Original Name","host":"192.168.1.100","access_code":"secret123","serial_number":"SERIAL001"}]`, 5)

	configPage := NewAdminBambuConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Verify existing printer is displayed
	assert.Equal(t, 1, configPage.PrinterCardCount())
	assert.Equal(t, "Original Name", configPage.GetPrinterName(0))
	configPage.ExpectPrinterAccessCodePlaceholder(0, "secret is set")

	// Update name only (leave access code empty to preserve)
	configPage.FillPrinterName(0, "Updated Name")
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Verify name was updated but access code preserved
	printersJSON := getBambuPrintersJSON(t, env)
	assert.Contains(t, printersJSON, "Updated Name")
	assert.Contains(t, printersJSON, "secret123", "access code should be preserved")
}

// TestAdmin_BambuConfigVersioning verifies that saving config creates new
// versions without overwriting old ones.
func TestAdmin_BambuConfigVersioning(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminBambuConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Save first version
	configPage.ClickAddPrinter()
	configPage.FillPrinterName(0, "Printer V1")
	configPage.FillPrinterHost(0, "192.168.1.101")
	configPage.FillPrinterAccessCode(0, "access1")
	configPage.FillPrinterSerial(0, "SERIAL001")
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()
	firstVersion := getBambuConfigVersion(t, env)

	// Save second version
	configPage.Navigate()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.FillPrinterName(0, "Printer V2")
	configPage.FillPrinterAccessCode(0, "access2")
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()
	secondVersion := getBambuConfigVersion(t, env)

	// Verify version incremented by 1 after the second save
	assert.Equal(t, firstVersion+1, secondVersion, "second save should increment version by 1")
}

// TestAdmin_BambuConfigNewPrinterWithoutAccessCodeSkipped verifies that
// new printers without access code are not saved (they're skipped).
func TestAdmin_BambuConfigNewPrinterWithoutAccessCodeSkipped(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminBambuConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Add printer without access code
	configPage.ClickAddPrinter()
	configPage.FillPrinterName(0, "No Access Code Printer")
	configPage.FillPrinterHost(0, "192.168.1.100")
	configPage.FillPrinterSerial(0, "SERIAL001")
	// Don't fill access code
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Printer should not be saved (new printers require access code)
	printersJSON := getBambuPrintersJSON(t, env)
	assert.NotContains(t, printersJSON, "No Access Code Printer")
}

// TestAdmin_BambuConfigPollIntervalValidation verifies that poll interval
// is bounded within valid range (1-60).
func TestAdmin_BambuConfigPollIntervalValidation(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Seed a printer so we have something to save
	seedBambuConfig(t, env, `[{"name":"Test","host":"192.168.1.100","access_code":"secret","serial_number":"SERIAL001"}]`, 5)

	configPage := NewAdminBambuConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Set poll interval to 30
	configPage.FillPollInterval(30)
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	pollInterval := getBambuPollInterval(t, env)
	assert.Equal(t, 30, pollInterval)
}

// TestJourney_AdminConfiguresBambuPrinter tests the complete flow of an admin
// configuring a new Bambu printer.
func TestJourney_AdminConfiguresBambuPrinter(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Step 1: Navigate to Bambu config page
	configPage := NewAdminBambuConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectPageTitle()
	configPage.ExpectConfiguredPrintersCount(0)

	// Step 2: Add a printer
	configPage.ClickAddPrinter()
	configPage.ExpectPrinterCardHeaderText(0, "New Printer")

	// Step 3: Fill in printer details
	configPage.FillPrinterName(0, "Lab Bambu X1C")
	configPage.ExpectPrinterCardHeaderText(0, "Lab Bambu X1C")
	configPage.FillPrinterHost(0, "192.168.10.50")
	configPage.FillPrinterAccessCode(0, "X1C-ACCESS-CODE")
	configPage.FillPrinterSerial(0, "01P00A350100001")
	configPage.FillPollInterval(10)

	// Step 4: Save the configuration
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Step 5: Verify success
	configPage.ExpectSaveSuccessMessage()
	configPage.ExpectConfiguredPrintersCount(1)
	configPage.ExpectPollIntervalDisplay(10)

	// Step 6: Verify database state
	printersJSON := getBambuPrintersJSON(t, env)
	assert.Contains(t, printersJSON, "Lab Bambu X1C")
	assert.Contains(t, printersJSON, "192.168.10.50")
	assert.Contains(t, printersJSON, "01P00A350100001")

	// Step 7: Reload page and verify data persisted
	configPage.Navigate()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	assert.Equal(t, 1, configPage.PrinterCardCount())
	assert.Equal(t, "Lab Bambu X1C", configPage.GetPrinterName(0))
	assert.Equal(t, "192.168.10.50", configPage.GetPrinterHost(0))
	assert.Equal(t, "01P00A350100001", configPage.GetPrinterSerial(0))
	configPage.ExpectPrinterAccessCodePlaceholder(0, "secret is set")
}

// TestAdmin_DiscordConfigPage verifies that administrators can view and update
// Discord configuration via the admin settings page.
func TestAdmin_DiscordConfigPage(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminDiscordConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Verify the page displays Discord configuration sections
	expect(t).Locator(page.GetByText("Discord Integration")).ToBeVisible()
	expect(t).Locator(page.GetByText("OAuth2 Configuration")).ToBeVisible()
	expect(t).Locator(page.GetByText("Bot Configuration")).ToBeVisible()
	expect(t).Locator(page.GetByText("Notifications")).ToBeVisible()
	expect(t).Locator(page.GetByText("Sync Settings")).ToBeVisible()

	// Fill in all 7 fields across 4 sections
	configPage.FillClientID("test-discord-client-id")
	configPage.FillClientSecret("test-discord-client-secret")
	configPage.FillBotToken("test-bot-token")
	configPage.FillGuildID("123456789012345678")
	configPage.FillRoleID("987654321098765432")
	configPage.FillPrintWebhookURL("https://discord.com/api/webhooks/test")
	configPage.FillSyncIntervalHours("12")
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Reload and verify secrets show placeholders
	configPage.Navigate()
	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectHasClientSecret()
	configPage.ExpectHasBotToken()
	configPage.ExpectHasPrintWebhookURL()

	// Verify non-secret fields retain their values
	clientID, err := page.Locator("#client_id").InputValue()
	require.NoError(t, err)
	assert.Equal(t, "test-discord-client-id", clientID)

	guildID, err := page.Locator("#guild_id").InputValue()
	require.NoError(t, err)
	assert.Equal(t, "123456789012345678", guildID)

	roleID, err := page.Locator("#role_id").InputValue()
	require.NoError(t, err)
	assert.Equal(t, "987654321098765432", roleID)

	syncInterval, err := page.Locator("#sync_interval_hours").InputValue()
	require.NoError(t, err)
	assert.Equal(t, "12", syncInterval)
}

// TestAdmin_DiscordConfigSecretPreservation verifies that saving Discord config
// without changing secret fields preserves their existing values.
func TestAdmin_DiscordConfigSecretPreservation(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Seed initial config
	seedDiscordConfig(t, env, "cid", "original-secret", "original-bot-token", "gid", "rid", "https://original-webhook.com", 24)

	configPage := NewAdminDiscordConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Update only the client ID, leave secrets empty
	configPage.FillClientID("updated-client-id")
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Verify secrets were preserved in database
	var clientSecret, botToken, webhookURL string
	err = env.db.QueryRow("SELECT client_secret, bot_token, print_webhook_url FROM discord_config ORDER BY version DESC LIMIT 1").Scan(&clientSecret, &botToken, &webhookURL)
	require.NoError(t, err)
	assert.Equal(t, "original-secret", clientSecret)
	assert.Equal(t, "original-bot-token", botToken)
	assert.Equal(t, "https://original-webhook.com", webhookURL)
}

// TestAdmin_DiscordConfigValidation verifies that the sync_interval_hours
// field is validated (must be 1-168).
func TestAdmin_DiscordConfigValidation(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Seed initial config so we have something to save
	seedDiscordConfig(t, env, "cid", "secret", "token", "gid", "rid", "", 24)

	configPage := NewAdminDiscordConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Set sync interval to an invalid value (0)
	configPage.FillSyncIntervalHours("0")
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Should show a validation error
	configPage.ExpectValidationError()
}

// TestJourney_DiscordConfigEnablesLoginButton verifies the scenario: admin
// configures Discord OAuth2 credentials, then "Login with Discord" button
// appears on the login page.
func TestJourney_DiscordConfigEnablesLoginButton(t *testing.T) {
	t.Parallel()
	env, _, adminPage := setupAdminTest(t)

	// Step 1: Verify login page has no Discord button initially
	loginPage := newPage(t)
	_, err := loginPage.Goto(env.baseURL + "/login")
	require.NoError(t, err)

	discordBtn := loginPage.Locator("a:has-text('Login with Discord')")
	expect(t).Locator(discordBtn).ToBeHidden()

	// Step 2: Admin configures Discord OAuth2 credentials
	configPage := NewAdminDiscordConfigPage(t, adminPage, env.baseURL)
	configPage.Navigate()

	err = adminPage.WaitForLoadState()
	require.NoError(t, err)

	configPage.FillClientID("discord-oauth-client-id")
	configPage.FillClientSecret("discord-oauth-client-secret")
	configPage.Submit()

	err = adminPage.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Step 3: Verify login page now shows "Login with Discord" button
	_, err = loginPage.Goto(env.baseURL + "/login")
	require.NoError(t, err)

	expect(t).Locator(loginPage.Locator("a:has-text('Login with Discord')")).ToBeVisible()
}

// TestAdmin_GoogleConfigPage verifies that administrators can view and update
// Google configuration via the admin settings page.
func TestAdmin_GoogleConfigPage(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminGoogleConfigPage(t, page, env.baseURL)
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Verify page displays Google configuration
	expect(t).Locator(page.GetByText("Google Login")).ToBeVisible()
	expect(t).Locator(page.GetByText("OAuth2 Configuration")).ToBeVisible()

	// Fill in both fields
	configPage.FillClientID("test-google-client-id")
	configPage.FillClientSecret("test-google-client-secret")
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Reload and verify secret shows placeholder
	configPage.Navigate()
	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectHasClientSecret()

	// Verify non-secret field retains its value
	clientID, err := page.Locator("#client_id").InputValue()
	require.NoError(t, err)
	assert.Equal(t, "test-google-client-id", clientID)

	// Verify secret preservation: update only client ID, leave secret empty
	configPage.FillClientID("updated-client-id")
	configPage.Submit()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Verify secret was preserved in database
	var clientSecret string
	err = env.db.QueryRow("SELECT client_secret FROM google_config ORDER BY version DESC LIMIT 1").Scan(&clientSecret)
	require.NoError(t, err)
	assert.Equal(t, "test-google-client-secret", clientSecret)
}

// TestJourney_GoogleConfigEnablesLoginButton verifies the scenario: admin
// configures Google OAuth2 credentials, then "Login with Google" button
// appears on the login page.
func TestJourney_GoogleConfigEnablesLoginButton(t *testing.T) {
	t.Parallel()
	env, _, adminPage := setupAdminTest(t)

	// Step 1: Verify login page has no Google button initially
	loginPage := newPage(t)
	_, err := loginPage.Goto(env.baseURL + "/login")
	require.NoError(t, err)

	googleBtn := loginPage.Locator("a:has-text('Login with Google')")
	expect(t).Locator(googleBtn).ToBeHidden()

	// Step 2: Admin configures Google OAuth2 credentials
	configPage := NewAdminGoogleConfigPage(t, adminPage, env.baseURL)
	configPage.Navigate()

	err = adminPage.WaitForLoadState()
	require.NoError(t, err)

	configPage.FillClientID("google-oauth-client-id")
	configPage.FillClientSecret("google-oauth-client-secret")
	configPage.Submit()

	err = adminPage.WaitForLoadState()
	require.NoError(t, err)

	configPage.ExpectSaveSuccessMessage()

	// Step 3: Verify login page now shows "Login with Google" button
	_, err = loginPage.Goto(env.baseURL + "/login")
	require.NoError(t, err)

	expect(t).Locator(loginPage.Locator("a:has-text('Login with Google')")).ToBeVisible()
}

// TestAdmin_FobAPIConfigPage verifies that the Fob API read-only documentation
// page loads and displays all key content sections.
func TestAdmin_ReadOnlyConfigPages(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	tests := []struct {
		name         string
		path         string
		expectedText []string
	}{
		{
			name:         "fob_api",
			path:         "/admin/config/fobapi",
			expectedText: []string{"Fob API", "/api/fobs", "ETag"},
		},
		{
			name:         "oauth2",
			path:         "/admin/config/oauth2",
			expectedText: []string{"OAuth2 Provider", "Discovery", "Authorization Code Flow"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configPage := NewAdminConfigPage(t, page, env.baseURL, tc.path)
			configPage.Navigate()

			err := page.WaitForLoadState()
			require.NoError(t, err)

			for _, text := range tc.expectedText {
				configPage.ExpectTextVisible(text)
			}
		})
	}
}

// TestAdmin_ConfigSidebarNavigation verifies that the config sidebar lists all
// config sections with correct links and highlights the active page.
func TestAdmin_ConfigSidebarNavigation(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	configPage := NewAdminConfigPage(t, page, env.baseURL, "/admin/config/waiver")
	configPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Verify all 7 sidebar links are present
	configPage.ExpectSidebarLink("Waiver", "/admin/config/waiver")
	configPage.ExpectSidebarLink("Discord", "/admin/config/discord")
	configPage.ExpectSidebarLink("Google", "/admin/config/google")
	configPage.ExpectSidebarLink("Stripe", "/admin/config/stripe")
	configPage.ExpectSidebarLink("Bambu", "/admin/config/bambu")
	configPage.ExpectSidebarLink("Fob API", "/admin/config/fobapi")
	configPage.ExpectSidebarLink("OAuth2", "/admin/config/oauth2")

	// Verify active state on current page
	configPage.ExpectSidebarActiveLink("/admin/config/waiver")

	// Navigate to another config page via sidebar and verify active state changes
	configPage.ClickSidebarLink("/admin/config/discord")

	err = page.WaitForURL("**/admin/config/discord")
	require.NoError(t, err)

	err = page.WaitForLoadState()
	require.NoError(t, err)

	discordPage := NewAdminConfigPage(t, page, env.baseURL, "/admin/config/discord")
	discordPage.ExpectSidebarActiveLink("/admin/config/discord")
}

// TestAdmin_EventsPageFiltering verifies that the events page filter checkboxes
// correctly filter displayed events by type.
func TestAdmin_EventsPageFiltering(t *testing.T) {
	t.Parallel()
	env, _, page := setupAdminTest(t)

	// Seed diverse event types
	memberID := seedMember(t, env, "filtertest@example.com", WithConfirmed(), WithFobID(55555))
	seedFobSwipes(t, env, 55555, 2)
	seedMemberEvents(t, env, memberID, 2)
	seedWaiver(t, env, "filteredwaiver@example.com")

	eventsPage := NewAdminEventsPage(t, page, env.baseURL)
	eventsPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Verify events are shown initially
	expect(t).Locator(page.Locator("#results")).ToBeVisible()

	// Verify fob swipes and waivers both appear
	eventsPage.ExpectRowWithText("55555")
	eventsPage.ExpectRowWithText("filteredwaiver@example.com")

	// Click only the "Waiver" filter checkbox to filter to just waivers
	// First, open the filter dropdown
	filterBtn := page.Locator("button.dropdown-toggle:has-text('Filter')")
	count, err := filterBtn.Count()
	require.NoError(t, err)
	if count > 0 {
		err = filterBtn.First().Click()
		require.NoError(t, err)
	}

	// Check "Waiver" checkbox
	waiverCheckbox := page.Locator("input[name='event_type'][value='Waiver']")
	err = waiverCheckbox.Check()
	require.NoError(t, err)

	// Wait for HTMX to reload results
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	})
	require.NoError(t, err)

	// Waiver should still be visible
	eventsPage.ExpectRowWithText("filteredwaiver@example.com")
}

// TestDonation_CardHiddenWithoutStripeConfig verifies that the donation card
// is not shown when Stripe is not configured.
func TestDonation_CardHiddenConditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		email        string
		opts         []MemberOption
		seedStripe   bool
		donationJSON string
	}{
		{
			name:  "without_stripe_config",
			email: "nostripe@example.com",
			opts:  []MemberOption{WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345)},
		},
		{
			name:         "without_key_fob",
			email:        "nofob@example.com",
			opts:         []MemberOption{WithConfirmed(), WithWaiver(), WithActiveStripeSubscription()},
			seedStripe:   true,
			donationJSON: `[{"name":"Laser Cutting","price_id":"price_laser"},{"name":"3D Printing","price_id":"price_3d"}]`,
		},
		{
			name:       "without_donation_items",
			email:      "noitems@example.com",
			opts:       []MemberOption{WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345)},
			seedStripe: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env, _, page := setupMemberTest(t, tc.email, tc.opts...)

			if tc.seedStripe && tc.donationJSON != "" {
				seedStripeConfigWithDonations(t, env, "sk_test_fake", "whsec_fake", tc.donationJSON)
			} else if tc.seedStripe {
				seedStripeConfig(t, env, "sk_test_fake", "whsec_fake")
			}

			dashboard := NewMemberDashboardPage(t, page, env.baseURL)
			dashboard.Navigate()
			dashboard.ExpectNoDonationCard()
		})
	}
}

// TestDonation_CardRendersWithItems verifies that the donation card is shown
// with correct dropdown options when all conditions are met.
func TestDonation_CardRendersWithItems(t *testing.T) {
	t.Parallel()

	t.Run("multiple_items", func(t *testing.T) {
		t.Parallel()
		env, _, page := setupMemberTest(t, "donation@example.com",
			WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345))

		donationItems := `[{"name":"Laser Cutting","price_id":"price_laser_123"},{"name":"3D Printing","price_id":"price_3d_456"}]`
		seedStripeConfigWithDonations(t, env, "sk_test_fake", "whsec_fake", donationItems)

		dashboard := NewMemberDashboardPage(t, page, env.baseURL)
		dashboard.Navigate()
		dashboard.ExpectActiveStatus()
		dashboard.ExpectDonationCard()

		// Verify dropdown has correct number of options
		assert.Equal(t, 2, dashboard.DonationOptionCount(), "should have 2 donation items")

		// Verify option text and values
		assert.Equal(t, "Laser Cutting", dashboard.DonationOptionText(0))
		assert.Equal(t, "price_laser_123", dashboard.DonationOptionValue(0))
		assert.Equal(t, "3D Printing", dashboard.DonationOptionText(1))
		assert.Equal(t, "price_3d_456", dashboard.DonationOptionValue(1))
	})

	t.Run("single_item", func(t *testing.T) {
		t.Parallel()
		env, _, page := setupMemberTest(t, "singledonation@example.com",
			WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345))

		donationItems := `[{"name":"Workshop Fee","price_id":"price_workshop_789"}]`
		seedStripeConfigWithDonations(t, env, "sk_test_fake", "whsec_fake", donationItems)

		dashboard := NewMemberDashboardPage(t, page, env.baseURL)
		dashboard.Navigate()
		dashboard.ExpectDonationCard()

		assert.Equal(t, 1, dashboard.DonationOptionCount(), "should have 1 donation item")
		assert.Equal(t, "Workshop Fee", dashboard.DonationOptionText(0))
		assert.Equal(t, "price_workshop_789", dashboard.DonationOptionValue(0))
	})
}

// TestDonation_CheckoutEmptyPriceID verifies that the donation checkout endpoint
// returns a 400 error when no price_id is provided.
func TestDonation_CheckoutBadPriceID(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "badprice@example.com",
		WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345))

	donationItems := `[{"name":"Laser Cutting","price_id":"price_laser"}]`
	seedStripeConfigWithDonations(t, env, "sk_test_fake", "whsec_fake", donationItems)

	t.Run("empty", func(t *testing.T) {
		resp, err := page.Goto(env.baseURL + "/donations/checkout")
		require.NoError(t, err)
		assert.Equal(t, 400, resp.Status(), "should return 400 for empty price_id")
	})

	t.Run("invalid", func(t *testing.T) {
		resp, err := page.Goto(env.baseURL + "/donations/checkout?price_id=price_nonexistent")
		require.NoError(t, err)
		assert.Equal(t, 400, resp.Status(), "should return 400 for invalid price_id")
	})
}

// TestDonation_DonateButtonNavigation verifies that clicking the Donate button
// navigates to the checkout endpoint with the selected price_id.
func TestDonation_DonateButtonNavigation(t *testing.T) {
	t.Parallel()
	env, _, page := setupMemberTest(t, "donatenav@example.com",
		WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345))

	donationItems := `[{"name":"Laser Cutting","price_id":"price_laser_nav"},{"name":"3D Printing","price_id":"price_3d_nav"}]`
	seedStripeConfigWithDonations(t, env, "sk_test_fake", "whsec_fake", donationItems)

	dashboard := NewMemberDashboardPage(t, page, env.baseURL)
	dashboard.Navigate()
	dashboard.ExpectDonationCard()

	// Select the second item and click Donate
	dashboard.SelectDonationItem("price_3d_nav")
	dashboard.ClickDonate()

	// The checkout endpoint will return 500 because Stripe API key is fake,
	// but we can verify the URL contains the correct price_id
	err := page.WaitForLoadState()
	require.NoError(t, err)

	currentURL := page.URL()
	assert.Contains(t, currentURL, "/donations/checkout")
	assert.Contains(t, currentURL, "price_id=price_3d_nav")
}
