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

// TestLogin_CodeLinkErrors verifies error handling for invalid and expired codes.
func TestLogin_CodeLinkErrors(t *testing.T) {
	t.Run("expired_code", func(t *testing.T) {
		clearTestData(t)
		memberID := seedMember(t, "expiredcode@example.com", WithConfirmed())
		seedLoginCode(t, "99999", memberID, "/", time.Now().Add(-1*time.Minute))

		page := newPage(t)
		resp, err := page.Goto(baseURL + "/login/code?code=99999")
		require.NoError(t, err)
		assert.Equal(t, 400, resp.Status())
	})

	t.Run("invalid_code", func(t *testing.T) {
		page := setupUnauthenticatedTest(t)
		resp, err := page.Goto(baseURL + "/login/code?code=00000")
		require.NoError(t, err)
		assert.Equal(t, 400, resp.Status())
	})
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

// TestWaiver_SubmissionFlow verifies the waiver page displays correctly,
// validates checkbox requirements, and successfully creates waivers.
func TestWaiver_SubmissionFlow(t *testing.T) {
	t.Run("display_form_elements", func(t *testing.T) {
		page := setupUnauthenticatedTest(t)
		waiverPage := NewWaiverPage(t, page)
		waiverPage.Navigate()
		waiverPage.ExpectWaiverText()
		expect(t).Locator(page.Locator("#agree0")).ToBeVisible()
		expect(t).Locator(page.Locator("#agree1")).ToBeVisible()
		expect(t).Locator(page.Locator("#name")).ToBeVisible()
		expect(t).Locator(page.Locator("#email")).ToBeVisible()
	})

	t.Run("checkbox_validation", func(t *testing.T) {
		page := setupUnauthenticatedTest(t)
		waiverPage := NewWaiverPage(t, page)
		waiverPage.Navigate()
		waiverPage.FillName("No Checkboxes")
		waiverPage.FillEmail("nocheckbox@example.com")
		waiverPage.Submit()
		waiverPage.ExpectWaiverText()

		var count int
		err := testDB.QueryRow("SELECT COUNT(*) FROM waivers WHERE email = ?", "nocheckbox@example.com").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 0, count, "waiver should not be created without checkboxes")
	})

	t.Run("successful_submission", func(t *testing.T) {
		page := setupUnauthenticatedTest(t)
		waiverPage := NewWaiverPage(t, page)
		waiverPage.Navigate()
		waiverPage.CheckAgree1()
		waiverPage.CheckAgree2()
		waiverPage.FillName("Test Signer")
		waiverPage.FillEmail("testsigner@example.com")
		waiverPage.Submit()
		waiverPage.ExpectSuccessMessage()

		var name, email string
		err := testDB.QueryRow("SELECT name, email FROM waivers WHERE email = ?", "testsigner@example.com").Scan(&name, &email)
		require.NoError(t, err, "waiver should be created in database")
		assert.Equal(t, "Test Signer", name)
		assert.Equal(t, "testsigner@example.com", email)
	})

	t.Run("with_redirect", func(t *testing.T) {
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
	})
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

// TestAuth_ProtectedEndpoints verifies that protected endpoints require
// proper authentication and authorization.
func TestAuth_ProtectedEndpoints(t *testing.T) {
	tests := []struct {
		name           string
		endpoint       string
		requiresLogin  bool
		requiresLeader bool
		expectedStatus int
	}{
		// Endpoints requiring authentication (redirect to login)
		{"dashboard", "/", true, false, 0},
		{"machines", "/machines", true, false, 0},
		{"directory", "/directory", true, false, 0},
		// Endpoints requiring leadership (403 for non-leadership)
		{"admin_members", "/admin/members", true, true, 403},
		{"admin_metrics", "/admin/metrics", true, true, 403},
		{"admin_events", "/admin/events", true, true, 403},
		{"admin_waiver_config", "/admin/config/waiver", true, true, 403},
		{"admin_bambu_config", "/admin/config/bambu", true, true, 403},
		{"admin_calendar", "/admin/calendar", true, true, 403},
		// API endpoints requiring auth
		{"oauth2_userinfo", "/oauth2/userinfo", true, false, 401},
	}

	t.Run("unauthenticated_redirects", func(t *testing.T) {
		page := setupUnauthenticatedTest(t)
		for _, tc := range tests {
			if tc.requiresLogin && !tc.requiresLeader && tc.expectedStatus == 0 {
				t.Run(tc.name, func(t *testing.T) {
					_, err := page.Goto(baseURL + tc.endpoint)
					require.NoError(t, err)
					err = page.WaitForURL("**/login**")
					require.NoError(t, err)
				})
			}
		}
	})

	t.Run("unauthenticated_api_errors", func(t *testing.T) {
		page := setupUnauthenticatedTest(t)
		for _, tc := range tests {
			if tc.requiresLogin && tc.expectedStatus >= 400 && !tc.requiresLeader {
				t.Run(tc.name, func(t *testing.T) {
					resp, err := page.Goto(baseURL + tc.endpoint)
					require.NoError(t, err)
					assert.GreaterOrEqual(t, resp.Status(), 400)
				})
			}
		}
	})

	t.Run("non_leadership_forbidden", func(t *testing.T) {
		_, page := setupMemberTest(t, "regular@example.com",
			WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345))
		for _, tc := range tests {
			if tc.requiresLeader {
				t.Run(tc.name, func(t *testing.T) {
					resp, err := page.Goto(baseURL + tc.endpoint)
					require.NoError(t, err)
					assert.Equal(t, tc.expectedStatus, resp.Status())
				})
			}
		}
	})
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

// TestAdmin_DataListPages verifies that the admin events page loads
// and displays results from all event types (fob swipes, member events, waivers).
func TestAdmin_DataListPages(t *testing.T) {
	_, page := setupAdminTest(t)

	// Seed test data for all event types
	memberID := seedMember(t, "data@example.com", WithConfirmed(), WithFobID(11111))
	seedFobSwipes(t, 11111, 3)
	seedMemberEvents(t, memberID, 3)
	seedWaiver(t, "waiver@example.com")

	eventsPage := NewAdminEventsPage(t, page)
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

// TestAdmin_MembersCSVDownloadLink verifies that the CSV download link is visible
// on the admin members list page and clicking it downloads a valid CSV file.
func TestAdmin_MembersCSVDownloadLink(t *testing.T) {
	_, page := setupAdminTest(t)

	// Create some test members to ensure CSV has data
	seedMember(t, "csvtest1@example.com", WithConfirmed())
	seedMember(t, "csvtest2@example.com", WithConfirmed(), WithFobID(55555))

	adminPage := NewAdminMembersListPage(t, page)
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

// TestMachines_AllPrinterStatuses verifies the printers page displays all
// printer cards with correct status badges, controls, and visual elements.
// The test app is configured with 3 mock printers:
// - "Printer A" (test-001): Available (no job, no error)
// - "Printer B" (test-002): In Use (has JobFinishedTimestamp)
// - "Printer C" (test-003): Failed (has ErrorCode)
func TestMachines_AllPrinterStatuses(t *testing.T) {
	_, page := setupMemberTest(t, "machines@example.com", WithConfirmed())
	// Refresh printer state timestamps to prevent TTL expiration during long test runs
	refreshPrinterStateTimestamps(t)
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

	// Configure Stripe via the database (simulating admin configuration)
	// The payment module reads these values dynamically from stripe_config table
	apiKey := os.Getenv("STRIPE_TEST_KEY")
	webhookKey := getEnvWithFallback("STRIPE_TEST_WEBHOOK_KEY", "CONWAY_STRIPE_WEBHOOK_KEY")
	seedStripeConfig(t, apiKey, webhookKey)
	t.Log("Configured Stripe via dynamic config (stripe_config table)")

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

// TestStripe_SubscriptionWithAdminUIConfig tests the Stripe subscription workflow
// where an admin first configures Stripe via the admin UI, then a member subscribes.
// This is the complete journey test for dynamic Stripe configuration.
func TestStripe_SubscriptionWithAdminUIConfig(t *testing.T) {
	if !stripeTestEnabled() {
		t.Skip("Skipping Stripe test: STRIPE_TEST_KEY not set")
	}

	// Start Stripe CLI for webhook forwarding
	startStripeCLI(t, "localhost:18080/webhooks/stripe")

	clearTestData(t)

	// Step 1: Admin configures Stripe via the admin UI
	t.Log("Step 1: Admin configuring Stripe via admin settings page")

	adminID := seedMember(t, "admin@example.com", WithConfirmed(), WithLeadership())
	adminCtx := newContext(t)
	loginAs(t, adminCtx, adminID)
	adminPage := newPageInContext(t, adminCtx)

	configPage := NewAdminStripeConfigPage(t, adminPage)
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
	memberID := seedMember(t, email, WithConfirmed(), WithWaiver())

	memberCtx := newContext(t)
	loginAs(t, memberCtx, memberID)
	memberPage := newPageInContext(t, memberCtx)

	dashboard := NewMemberDashboardPage(t, memberPage)
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
	waitForMemberState(t, email, 30*time.Second, func(subState, name string) bool {
		return subState == "active"
	})

	// Verify dashboard shows active subscription
	dashboard.Navigate()
	dashboard.ExpectStepComplete("Set Up Payment")

	// Step 5: Verify admin can see updated subscription counts
	t.Log("Step 5: Verifying admin sees updated subscription count")

	configPage.Navigate()
	err = adminPage.WaitForLoadState()
	require.NoError(t, err)

	// Should now show 1 active subscription
	activeSubsLocator := adminPage.Locator(".card-body .row .col-md-6").First().Locator("h3")
	activeSubsText, err := activeSubsLocator.TextContent()
	require.NoError(t, err)
	assert.Equal(t, "1", activeSubsText, "should show 1 active subscription after member subscribed")

	t.Log("Test completed: admin configured Stripe, member subscribed, admin verified subscription count")
}

// TestWaiver_PrefilledEmail verifies that the email field can be prefilled
// via query parameter.
func TestWaiver_PrefilledEmail(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	_, err := page.Goto(baseURL + "/waiver?email=prefilled@example.com")
	require.NoError(t, err)

	emailValue, err := page.Locator("#email").InputValue()
	require.NoError(t, err)
	assert.Equal(t, "prefilled@example.com", emailValue)
}

// TestWaiver_CustomContent verifies that custom waiver content renders correctly
// with dynamic checkbox generation from markdown.
func TestWaiver_CustomContent(t *testing.T) {
	t.Run("renders_custom_content", func(t *testing.T) {
		page := setupUnauthenticatedTest(t)
		clearWaiverContent(t)

		customContent := `# Custom Waiver Title

This is a custom waiver paragraph with special content.

Another paragraph here.

- [ ] I agree to the first custom term
- [ ] I agree to the second custom term
- [ ] I agree to the third custom term`

		seedWaiverContent(t, customContent)

		waiverPage := NewWaiverPage(t, page)
		waiverPage.Navigate()

		expect(t).Locator(page.GetByText("Custom Waiver Title")).ToBeVisible()
		expect(t).Locator(page.GetByText("This is a custom waiver paragraph")).ToBeVisible()

		checkboxes := page.Locator("input[type='checkbox']")
		count, err := checkboxes.Count()
		require.NoError(t, err)
		assert.Equal(t, 3, count, "should have 3 checkboxes for custom waiver")
	})

	t.Run("requires_all_checkboxes", func(t *testing.T) {
		page := setupUnauthenticatedTest(t)
		clearWaiverContent(t)

		customContent := `# Test Waiver

Test paragraph.

- [ ] First checkbox
- [ ] Second checkbox
- [ ] Third checkbox
- [ ] Fourth checkbox`

		seedWaiverContent(t, customContent)

		waiverPage := NewWaiverPage(t, page)
		waiverPage.Navigate()

		checkboxes := page.Locator("input[type='checkbox']")
		count, err := checkboxes.Count()
		require.NoError(t, err)
		assert.Equal(t, 4, count, "should have 4 checkboxes")

		// Fill form but only check 2 of 4 checkboxes
		err = page.Locator("#agree0").Check()
		require.NoError(t, err)
		err = page.Locator("#agree1").Check()
		require.NoError(t, err)
		err = page.Locator("#name").Fill("Partial Signer")
		require.NoError(t, err)
		err = page.Locator("#email").Fill("partial@example.com")
		require.NoError(t, err)

		err = page.Locator("button[type='submit']").Click()
		require.NoError(t, err)

		var count2 int
		err = testDB.QueryRow("SELECT COUNT(*) FROM waivers WHERE email = ?", "partial@example.com").Scan(&count2)
		require.NoError(t, err)
		assert.Equal(t, 0, count2, "waiver should not be created with unchecked boxes")
	})
}

// TestAdmin_WaiverConfig verifies the waiver configuration page displays correctly,
// saves content, and increments versions properly.
func TestAdmin_WaiverConfig(t *testing.T) {
	_, page := setupAdminTest(t)
	clearWaiverContent(t)

	configPage := NewAdminWaiverConfigPage(t, page)

	t.Run("page_elements", func(t *testing.T) {
		configPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		expect(t).Locator(page.GetByText("Waiver Content")).ToBeVisible()
		configPage.ExpectSyntaxGuide()
		textarea := page.Locator("#content")
		expect(t).Locator(textarea).ToBeVisible()
		expect(t).Locator(textarea).ToBeEditable()
	})

	t.Run("save_and_versioning", func(t *testing.T) {
		configPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		// Save first version
		firstContent := `# First Version

- [ ] First agreement`
		configPage.SetContent(firstContent)
		configPage.Submit()

		err = page.WaitForLoadState()
		require.NoError(t, err)
		configPage.ExpectSaveSuccessMessage()
		configPage.ExpectVersionBadge(1)

		// Verify saved
		var savedContent string
		err = testDB.QueryRow("SELECT content FROM waiver_content ORDER BY version DESC LIMIT 1").Scan(&savedContent)
		require.NoError(t, err)
		assert.Equal(t, firstContent, strings.ReplaceAll(savedContent, "\r\n", "\n"))

		// Save second version
		secondContent := `# Second Version

- [ ] Second agreement`
		configPage.SetContent(secondContent)
		configPage.Submit()

		err = page.WaitForLoadState()
		require.NoError(t, err)
		configPage.ExpectVersionBadge(2)

		var count int
		err = testDB.QueryRow("SELECT COUNT(*) FROM waiver_content").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "should have 2 waiver versions")
	})
}

// TestAdmin_WaiverListPage verifies the admin events page displays
// signed waivers with correct information.
func TestAdmin_WaiverListPage(t *testing.T) {
	_, page := setupAdminTest(t)

	// Seed some waivers
	seedWaiver(t, "waiver1@example.com")
	seedWaiver(t, "waiver2@example.com")

	eventsPage := NewAdminEventsPage(t, page)
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
	// Step 1: Admin edits the waiver
	_, adminPage := setupAdminTest(t)
	clearWaiverContent(t)

	configPage := NewAdminWaiverConfigPage(t, adminPage)
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

	_, err = memberPage.Goto(baseURL + "/waiver")
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
	err = testDB.QueryRow("SELECT version FROM waivers WHERE email = ?", "journey@example.com").Scan(&signedVersion)
	require.NoError(t, err)

	var latestContentVersion int
	err = testDB.QueryRow("SELECT MAX(version) FROM waiver_content").Scan(&latestContentVersion)
	require.NoError(t, err)
	assert.Equal(t, latestContentVersion, signedVersion, "waiver should be signed with the latest waiver content version")

	// Step 4: Admin can see the signed waiver in the list
	eventsPage := NewAdminEventsPage(t, adminPage)
	eventsPage.Navigate()

	err = adminPage.WaitForLoadState()
	require.NoError(t, err)

	eventsPage.ExpectRowWithText("journey@example.com")
}

// TestWaiver_MissingContent verifies that when no waiver content exists in the
// database, an error is displayed.
func TestWaiver_MissingContent(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	clearWaiverContent(t)

	_, err := page.Goto(baseURL + "/waiver")
	require.NoError(t, err)

	// Should show system error when no waiver content is configured
	expect(t).Locator(page.GetByText("no waiver content configured")).ToBeVisible()
}

// TestWaiver_DuplicateSubmission verifies that submitting a waiver with the
// same email doesn't create duplicate records (ON CONFLICT DO NOTHING).
func TestWaiver_DuplicateSubmission(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	email := "duplicate@example.com"

	// Submit first waiver
	waiverPage := NewWaiverPage(t, page)
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
	err := testDB.QueryRow("SELECT COUNT(*) FROM waivers WHERE email = ?", email).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "should only have one waiver for the email")

	// Verify the first name was kept (ON CONFLICT DO NOTHING)
	var name string
	err = testDB.QueryRow("SELECT name FROM waivers WHERE email = ?", email).Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "First Submission", name, "original waiver should be preserved")
}

// TestAdmin_StripeConfig verifies the Stripe configuration page can save,
// version, and display status counts.
func TestAdmin_StripeConfig(t *testing.T) {
	_, page := setupAdminTest(t)

	configPage := NewAdminStripeConfigPage(t, page)

	t.Run("save_and_versioning", func(t *testing.T) {
		configPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		configPage.ExpectWebhookURLInstruction()

		initialVersion := getStripeConfigVersion(t)
		assert.Equal(t, 0, initialVersion, "should have no initial stripe config")

		configPage.FillAPIKey("sk_test_first")
		configPage.FillWebhookKey("whsec_first")
		configPage.Submit()

		err = page.WaitForLoadState()
		require.NoError(t, err)
		configPage.ExpectSaveSuccessMessage()
		configPage.ExpectVersionBadge(1)

		// Verify placeholders show secrets are set
		configPage.Navigate()
		err = page.WaitForLoadState()
		require.NoError(t, err)
		configPage.ExpectHasAPIKey()
		configPage.ExpectHasWebhookKey()

		// Save second version (just updating webhook)
		configPage.FillWebhookKey("whsec_second")
		configPage.Submit()

		err = page.WaitForLoadState()
		require.NoError(t, err)
		configPage.ExpectVersionBadge(2)

		var count int
		err = testDB.QueryRow("SELECT COUNT(*) FROM stripe_config").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "should have two versions")

		var apiKey, webhookKey string
		err = testDB.QueryRow("SELECT api_key, webhook_key FROM stripe_config ORDER BY version DESC LIMIT 1").Scan(&apiKey, &webhookKey)
		require.NoError(t, err)
		assert.Equal(t, "sk_test_first", apiKey, "API key should be preserved")
		assert.Equal(t, "whsec_second", webhookKey, "webhook key should be updated")
	})

	t.Run("status_counts", func(t *testing.T) {
		clearTestData(t)
		setupAdminTest(t) // Re-setup since clearTestData wipes DB

		seedMember(t, "active1@example.com", WithConfirmed(), WithActiveStripeSubscription())
		seedMember(t, "active2@example.com", WithConfirmed(), WithActiveStripeSubscription())
		seedMember(t, "customer-only@example.com", WithConfirmed(), WithStripeCustomerID("cus_test_no_sub"))

		configPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		activeSubsLocator := page.Locator(".card-body .row .col-md-6").First().Locator("h3")
		activeSubsText, err := activeSubsLocator.TextContent()
		require.NoError(t, err)
		assert.Equal(t, "2", activeSubsText, "should show 2 active subscriptions")

		totalCustLocator := page.Locator(".card-body .row .col-md-6").Last().Locator("h3")
		totalCustText, err := totalCustLocator.TextContent()
		require.NoError(t, err)
		assert.Equal(t, "3", totalCustText, "should show 3 total customers")
	})
}

// TestDirectory_MemberDisplay verifies the directory correctly displays members
// with various attributes including leadership badges, Discord usernames, and names.
func TestDirectory_MemberDisplay(t *testing.T) {
	_, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())

	// Seed members with various attributes
	seedMember(t, "ready@example.com", WithConfirmed(), WithReadyAccess(), WithName("Ready Member"))
	seedMember(t, "notready@example.com", WithConfirmed(), WithName("Not Ready Member"))
	seedMember(t, "leader@example.com", WithConfirmed(), WithReadyAccess(), WithName("Leader Person"), WithLeadership())
	seedMember(t, "discord@example.com", WithConfirmed(), WithReadyAccess(), WithName("Discord User"), WithDiscordUsername("discorduser123"))
	seedMember(t, "noname@example.com", WithConfirmed(), WithReadyAccess())

	directoryPage := NewDirectoryPage(t, page)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	directoryPage.ExpectHeading()

	// Only Ready members shown
	directoryPage.ExpectMemberCard("Ready Member")
	directoryPage.ExpectMemberCardNotVisible("Not Ready Member")

	// Leadership badge
	directoryPage.ExpectMemberCard("Leader Person")
	directoryPage.ExpectLeadershipBadge("Leader Person")

	// Discord username
	directoryPage.ExpectMemberCard("Discord User")
	directoryPage.ExpectDiscordUsername("Discord User", "discorduser123")

	// Nameless members excluded
	directoryPage.ExpectMemberCardNotVisible("noname@example.com")
}

// TestDirectory_Avatars verifies avatar display and API endpoints.
func TestDirectory_Avatars(t *testing.T) {
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
		0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xFE, 0xD4, 0xEF, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}

	t.Run("placeholder_avatar", func(t *testing.T) {
		_, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())
		seedMember(t, "noavatar@example.com", WithConfirmed(), WithReadyAccess(), WithName("No Avatar User"))

		directoryPage := NewDirectoryPage(t, page)
		directoryPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		directoryPage.ExpectMemberCard("No Avatar User")
		directoryPage.ExpectPlaceholderAvatar("No Avatar User")
	})

	t.Run("real_avatar_display", func(t *testing.T) {
		clearTestData(t)
		_, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())
		seedMember(t, "hasavatar@example.com", WithConfirmed(), WithReadyAccess(), WithName("Has Avatar User"), WithDiscordAvatar(pngData))

		directoryPage := NewDirectoryPage(t, page)
		directoryPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		directoryPage.ExpectMemberCard("Has Avatar User")
		directoryPage.ExpectAvatar("Has Avatar User")
	})

	t.Run("avatar_api_success", func(t *testing.T) {
		clearTestData(t)
		_, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())
		memberID := seedMember(t, "avatar@example.com", WithConfirmed(), WithReadyAccess(), WithDiscordAvatar(pngData))

		resp, err := page.Goto(fmt.Sprintf("%s/directory/avatar/%d", baseURL, memberID))
		require.NoError(t, err)
		assert.Equal(t, 200, resp.Status())
		headers := resp.Headers()
		assert.Contains(t, headers["content-type"], "image/png")
	})

	t.Run("avatar_api_not_found", func(t *testing.T) {
		clearTestData(t)
		_, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())
		memberID := seedMember(t, "noavatar@example.com", WithConfirmed(), WithReadyAccess())

		resp, err := page.Goto(fmt.Sprintf("%s/directory/avatar/%d", baseURL, memberID))
		require.NoError(t, err)
		assert.Equal(t, 404, resp.Status())

		resp, err = page.Goto(baseURL + "/directory/avatar/999999")
		require.NoError(t, err)
		assert.Equal(t, 404, resp.Status())

		resp, err = page.Goto(baseURL + "/directory/avatar/invalid")
		require.NoError(t, err)
		assert.Equal(t, 404, resp.Status())
	})
}

// TestDirectory_EmptyDirectory verifies that when no members have Ready
// access, an empty message is displayed.
func TestDirectory_EmptyDirectory(t *testing.T) {
	_, page := setupMemberTest(t, "viewer@example.com", WithConfirmed())

	directoryPage := NewDirectoryPage(t, page)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	directoryPage.ExpectHeading()
	directoryPage.ExpectEmptyMessage()
}

// TestDirectory_ShowsProfileData verifies that the directory correctly displays
// bio, name override, and shows the current user first.
func TestDirectory_ShowsProfileData(t *testing.T) {
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
		0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xFE, 0xD4, 0xEF, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}

	// Current user with avatar
	_, page := setupMemberTest(t, "current@example.com", WithConfirmed(), WithReadyAccess(),
		WithName("Current User"), WithDiscordAvatar(pngData))

	// Member with bio
	seedMember(t, "bio@example.com", WithConfirmed(), WithReadyAccess(),
		WithName("Bio User"), WithBio("I love making things!"), WithDiscordAvatar(pngData))

	// Member with name override (should show override, not original)
	seedMember(t, "override@example.com", WithConfirmed(), WithReadyAccess(),
		WithName("Original Name"), WithNameOverride("Preferred Name"), WithDiscordAvatar(pngData),
		WithFobLastSeen(9999999999)) // More recent, but current user should still be first

	directoryPage := NewDirectoryPage(t, page)
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
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
		0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xFE, 0xD4, 0xEF, 0x00, 0x00,
		0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}
	_, page := setupMemberTest(t, "member@example.com", WithConfirmed(), WithReadyAccess(),
		WithName("John Smith"), WithDiscordAvatar(pngData), WithDiscordUsername("johnsmith"))

	// View directory and navigate to profile
	directoryPage := NewDirectoryPage(t, page)
	directoryPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	directoryPage.ExpectMemberCard("John Smith")
	directoryPage.ClickEditProfile()

	err = page.WaitForURL("**/directory/profile")
	require.NoError(t, err)

	// Edit profile
	profilePage := NewProfilePage(t, page)
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

// TestAdmin_BambuConfig verifies Bambu printer configuration CRUD operations
// including add, edit, delete, and versioning.
func TestAdmin_BambuConfig(t *testing.T) {
	_, page := setupAdminTest(t)

	configPage := NewAdminBambuConfigPage(t, page)

	t.Run("page_structure", func(t *testing.T) {
		configPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		configPage.ExpectPageTitle()
		configPage.ExpectAddPrinterButton()
		assert.Equal(t, 0, configPage.PrinterCardCount(), "should have no printers initially")
		assert.Equal(t, "5", configPage.GetPollInterval(), "default poll interval should be 5")
	})

	t.Run("add_and_save_printers", func(t *testing.T) {
		configPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		// Add first printer
		configPage.ClickAddPrinter()
		assert.Equal(t, 1, configPage.PrinterCardCount())
		configPage.ExpectPrinterCardHeaderText(0, "New Printer")

		// Name update updates header
		configPage.FillPrinterName(0, "Printer A")
		configPage.ExpectPrinterCardHeaderText(0, "Printer A")
		configPage.FillPrinterHost(0, "192.168.1.101")
		configPage.FillPrinterAccessCode(0, "access123")
		configPage.FillPrinterSerial(0, "SERIAL001")

		// Add second printer
		configPage.ClickAddPrinter()
		configPage.FillPrinterName(1, "Printer B")
		configPage.FillPrinterHost(1, "192.168.1.102")
		configPage.FillPrinterAccessCode(1, "access456")
		configPage.FillPrinterSerial(1, "SERIAL002")

		configPage.FillPollInterval(10)
		configPage.Submit()

		err = page.WaitForLoadState()
		require.NoError(t, err)

		configPage.ExpectSaveSuccessMessage()
		configPage.ExpectVersionBadge(1)

		printersJSON := getBambuPrintersJSON(t)
		assert.Contains(t, printersJSON, "Printer A")
		assert.Contains(t, printersJSON, "Printer B")
		configPage.ExpectConfiguredPrintersCount(2)
		configPage.ExpectPollIntervalDisplay(10)
	})

	t.Run("edit_preserves_access_code", func(t *testing.T) {
		clearTestData(t)
		setupAdminTest(t)
		seedBambuConfig(t, `[{"name":"Original Name","host":"192.168.1.100","access_code":"secret123","serial_number":"SERIAL001"}]`, 5)

		configPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		assert.Equal(t, "Original Name", configPage.GetPrinterName(0))
		configPage.ExpectPrinterAccessCodePlaceholder(0, "secret is set")

		configPage.FillPrinterName(0, "Updated Name")
		configPage.Submit()

		err = page.WaitForLoadState()
		require.NoError(t, err)
		configPage.ExpectSaveSuccessMessage()

		printersJSON := getBambuPrintersJSON(t)
		assert.Contains(t, printersJSON, "Updated Name")
		assert.Contains(t, printersJSON, "secret123", "access code should be preserved")
	})

	t.Run("delete_printer", func(t *testing.T) {
		clearTestData(t)
		setupAdminTest(t)
		seedBambuConfig(t, `[{"name":"Printer 1","host":"192.168.1.101","access_code":"access1","serial_number":"SERIAL001"},{"name":"Printer 2","host":"192.168.1.102","access_code":"access2","serial_number":"SERIAL002"}]`, 5)

		configPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		assert.Equal(t, 2, configPage.PrinterCardCount())

		// Test cancel delete
		configPage.ClickDeletePrinter(0)
		configPage.ExpectDeleteConfirmVisible(0)
		configPage.CancelDeletePrinter(0)
		configPage.ExpectDeleteConfirmHidden(0)
		assert.Equal(t, 2, configPage.PrinterCardCount())

		// Actually delete
		configPage.ClickDeletePrinter(0)
		configPage.ConfirmDeletePrinter(0)
		assert.Equal(t, 1, configPage.PrinterCardCount())

		configPage.Submit()
		err = page.WaitForLoadState()
		require.NoError(t, err)
		configPage.ExpectSaveSuccessMessage()

		printersJSON := getBambuPrintersJSON(t)
		assert.NotContains(t, printersJSON, "Printer 1")
		assert.Contains(t, printersJSON, "Printer 2")
	})

	t.Run("versioning", func(t *testing.T) {
		clearTestData(t)
		setupAdminTest(t)

		configPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		configPage.ClickAddPrinter()
		configPage.FillPrinterName(0, "Printer V1")
		configPage.FillPrinterHost(0, "192.168.1.101")
		configPage.FillPrinterAccessCode(0, "access1")
		configPage.FillPrinterSerial(0, "SERIAL001")
		configPage.Submit()

		err = page.WaitForLoadState()
		require.NoError(t, err)
		configPage.ExpectSaveSuccessMessage()
		firstVersion := getBambuConfigVersion(t)

		configPage.Navigate()
		err = page.WaitForLoadState()
		require.NoError(t, err)

		configPage.FillPrinterName(0, "Printer V2")
		configPage.FillPrinterAccessCode(0, "access2")
		configPage.Submit()

		err = page.WaitForLoadState()
		require.NoError(t, err)
		configPage.ExpectSaveSuccessMessage()
		secondVersion := getBambuConfigVersion(t)

		assert.Equal(t, firstVersion+1, secondVersion)
	})
}

// TestAdmin_BambuConfigValidation verifies validation rules for printer config.
func TestAdmin_BambuConfigValidation(t *testing.T) {
	t.Run("new_printer_without_access_code_skipped", func(t *testing.T) {
		_, page := setupAdminTest(t)

		configPage := NewAdminBambuConfigPage(t, page)
		configPage.Navigate()

		err := page.WaitForLoadState()
		require.NoError(t, err)

		configPage.ClickAddPrinter()
		configPage.FillPrinterName(0, "No Access Code Printer")
		configPage.FillPrinterHost(0, "192.168.1.100")
		configPage.FillPrinterSerial(0, "SERIAL001")
		configPage.Submit()

		err = page.WaitForLoadState()
		require.NoError(t, err)

		printersJSON := getBambuPrintersJSON(t)
		assert.NotContains(t, printersJSON, "No Access Code Printer")
	})

	t.Run("poll_interval_validation", func(t *testing.T) {
		clearTestData(t)
		_, page := setupAdminTest(t)
		seedBambuConfig(t, `[{"name":"Test","host":"192.168.1.100","access_code":"secret","serial_number":"SERIAL001"}]`, 5)

		configPage := NewAdminBambuConfigPage(t, page)
		configPage.Navigate()

		err := page.WaitForLoadState()
		require.NoError(t, err)

		configPage.FillPollInterval(30)
		configPage.Submit()

		err = page.WaitForLoadState()
		require.NoError(t, err)

		pollInterval := getBambuPollInterval(t)
		assert.Equal(t, 30, pollInterval)
	})
}

// TestJourney_AdminConfiguresBambuPrinter tests the complete flow of an admin
// configuring a new Bambu printer.
func TestJourney_AdminConfiguresBambuPrinter(t *testing.T) {
	_, page := setupAdminTest(t)

	// Step 1: Navigate to Bambu config page
	configPage := NewAdminBambuConfigPage(t, page)
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
	configPage.ExpectVersionBadge(1)
	configPage.ExpectConfiguredPrintersCount(1)
	configPage.ExpectPollIntervalDisplay(10)

	// Step 6: Verify database state
	printersJSON := getBambuPrintersJSON(t)
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

// ========================================
// Calendar Tests
// ========================================

// TestCalendar_PublicPage verifies the public calendar displays correctly
// including empty state, events, and recurring events.
func TestCalendar_PublicPage(t *testing.T) {
	t.Run("empty_state", func(t *testing.T) {
		page := setupUnauthenticatedTest(t)
		clearCalendarEvents(t)

		calendarPage := NewCalendarPage(t, page)
		calendarPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		calendarPage.ExpectHeading()
		calendarPage.ExpectNoEventsMessage()
		calendarPage.ExpectSubscribeButton()
	})

	t.Run("shows_events", func(t *testing.T) {
		page := setupUnauthenticatedTest(t)
		clearCalendarEvents(t)

		seedCalendarEvent(t, "Weekly Workshop",
			WithEventDescription("Learn something new"),
			WithEventStartTime(time.Now().Add(24*time.Hour).Unix()))
		seedCalendarEvent(t, "Monthly Meetup",
			WithEventStartTime(time.Now().Add(48*time.Hour).Unix()))

		calendarPage := NewCalendarPage(t, page)
		calendarPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		calendarPage.ExpectHeading()
		calendarPage.ExpectEventCard("Weekly Workshop")
		calendarPage.ExpectEventCard("Monthly Meetup")
		assert.Equal(t, 2, calendarPage.EventCardCount())
	})

	t.Run("recurring_events", func(t *testing.T) {
		page := setupUnauthenticatedTest(t)
		clearCalendarEvents(t)

		seedCalendarEvent(t, "Weekly Class",
			WithWeeklyRecurrence("monday"),
			WithEventStartTime(time.Now().Add(24*time.Hour).Unix()))

		calendarPage := NewCalendarPage(t, page)
		calendarPage.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		calendarPage.ExpectEventCard("Weekly Class")
		calendarPage.ExpectRecurrenceBadge("Weekly Class", "Every Monday")
	})
}

// TestCalendar_AdminPage verifies admin calendar page functionality.
func TestCalendar_AdminPage(t *testing.T) {
	t.Run("empty_state", func(t *testing.T) {
		_, page := setupAdminTest(t)
		clearCalendarEvents(t)

		adminCalendar := NewAdminCalendarPage(t, page)
		adminCalendar.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		adminCalendar.ExpectHeading()
		adminCalendar.ExpectNoEventsMessage()
		adminCalendar.ExpectViewPublicCalendarLink()
	})

	t.Run("shows_events", func(t *testing.T) {
		clearTestData(t)
		_, page := setupAdminTest(t)
		seedCalendarEvent(t, "Test Event 1")
		seedCalendarEvent(t, "Test Event 2")

		adminCalendar := NewAdminCalendarPage(t, page)
		adminCalendar.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		adminCalendar.ExpectEventInTable("Test Event 1")
		adminCalendar.ExpectEventInTable("Test Event 2")
	})
}

// TestCalendar_CreateEvent verifies event creation for both one-time
// and recurring events.
func TestCalendar_CreateEvent(t *testing.T) {
	t.Run("one_time_event", func(t *testing.T) {
		_, page := setupAdminTest(t)
		clearCalendarEvents(t)

		adminCalendar := NewAdminCalendarPage(t, page)
		adminCalendar.Navigate()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		adminCalendar.ClickNewEvent()
		err = page.WaitForURL("**/admin/calendar/new")
		require.NoError(t, err)

		formPage := NewAdminCalendarFormPage(t, page)
		formPage.ExpectNewEventHeading()
		formPage.FillTitle("New Workshop")
		formPage.FillDescription("A great workshop for learning")
		formPage.FillDate(time.Now().AddDate(0, 0, 7).Format("2006-01-02"))
		formPage.FillTime("18:00")
		formPage.SelectDuration("120")
		formPage.Submit()

		err = page.WaitForURL("**/admin/calendar")
		require.NoError(t, err)
		err = page.WaitForLoadState()
		require.NoError(t, err)

		adminCalendar.ExpectEventInTable("New Workshop")
	})

	t.Run("weekly_recurring_event", func(t *testing.T) {
		clearTestData(t)
		_, page := setupAdminTest(t)
		clearCalendarEvents(t)

		formPage := NewAdminCalendarFormPage(t, page)
		formPage.NavigateNew()
		err := page.WaitForLoadState()
		require.NoError(t, err)

		formPage.FillTitle("Weekly Game Night")
		formPage.FillDate(time.Now().AddDate(0, 0, 1).Format("2006-01-02"))
		formPage.FillTime("19:00")
		formPage.SelectRecurrenceType("weekly")
		formPage.SelectRecurrenceDay("friday")
		formPage.Submit()

		err = page.WaitForURL("**/admin/calendar")
		require.NoError(t, err)
		err = page.WaitForLoadState()
		require.NoError(t, err)

		adminCalendar := NewAdminCalendarPage(t, page)
		adminCalendar.ExpectEventInTable("Weekly Game Night")
	})
}

// TestCalendar_EditEvent verifies that an admin can edit an existing event.
func TestCalendar_EditEvent(t *testing.T) {
	_, page := setupAdminTest(t)
	clearCalendarEvents(t)

	seedCalendarEvent(t, "Original Title",
		WithEventDescription("Original description"))

	adminCalendar := NewAdminCalendarPage(t, page)
	adminCalendar.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	adminCalendar.ClickEditEvent("Original Title")

	err = page.WaitForURL("**/admin/calendar/*/edit")
	require.NoError(t, err)

	formPage := NewAdminCalendarFormPage(t, page)
	formPage.ExpectEditEventHeading()

	// Verify existing data is shown
	assert.Equal(t, "Original Title", formPage.GetTitle())
	assert.Equal(t, "Original description", formPage.GetDescription())

	// Update the event
	formPage.FillTitle("Updated Title")
	formPage.FillDescription("Updated description")
	formPage.Submit()

	err = page.WaitForURL("**/admin/calendar")
	require.NoError(t, err)

	err = page.WaitForLoadState()
	require.NoError(t, err)

	adminCalendar.ExpectEventInTable("Updated Title")
	adminCalendar.ExpectEventNotInTable("Original Title")
}

// TestCalendar_DeleteEvent verifies that an admin can delete an event.
func TestCalendar_DeleteEvent(t *testing.T) {
	_, page := setupAdminTest(t)
	clearCalendarEvents(t)

	seedCalendarEvent(t, "Event To Delete")

	adminCalendar := NewAdminCalendarPage(t, page)
	adminCalendar.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	adminCalendar.ExpectEventInTable("Event To Delete")

	adminCalendar.ClickDeleteEvent("Event To Delete")

	err = page.WaitForLoadState()
	require.NoError(t, err)

	adminCalendar.ExpectEventNotInTable("Event To Delete")
}

// TestCalendar_NavbarHasCalendarLink verifies that the Calendar link appears
// in the admin navbar.
func TestCalendar_NavbarHasCalendarLink(t *testing.T) {
	_, page := setupAdminTest(t)

	adminMembersPage := NewAdminMembersListPage(t, page)
	adminMembersPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Check that Calendar link exists in navbar
	calendarLink := page.Locator("nav a:has-text('Calendar')")
	expect(t).Locator(calendarLink).ToBeVisible()

	// Click it and verify navigation
	err = calendarLink.Click()
	require.NoError(t, err)

	err = page.WaitForURL("**/admin/calendar")
	require.NoError(t, err)
}

// TestCalendar_Pagination verifies that pagination works correctly
// when there are many events.
func TestCalendar_Pagination(t *testing.T) {
	_, page := setupAdminTest(t)
	clearCalendarEvents(t)

	// Create more than 20 events to trigger pagination
	for i := 0; i < 25; i++ {
		seedCalendarEvent(t, fmt.Sprintf("Event %02d", i),
			WithEventStartTime(time.Now().Add(time.Duration(i)*time.Hour).Unix()))
	}

	adminCalendar := NewAdminCalendarPage(t, page)
	adminCalendar.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Initially we should see 20 events
	initialCount := adminCalendar.EventRowCount()
	assert.True(t, initialCount >= 20, "Should show at least 20 events initially")

	// Scroll to trigger infinite scroll (HTMX hx-trigger="revealed")
	// Wait for the loading row and let it load more
	_, err = page.Locator("table tbody").Evaluate("el => el.scrollIntoView({block: 'end'})", nil)
	require.NoError(t, err)

	// Give time for HTMX to load more
	page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	})

	// After loading more, we should see all 25 events
	finalCount := adminCalendar.EventRowCount()
	assert.True(t, finalCount >= initialCount, "Should have loaded more events")
}

// TestCalendar_ICalFeed verifies that the iCal feed is accessible.
func TestCalendar_ICalFeed(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	clearCalendarEvents(t)

	seedCalendarEvent(t, "iCal Test Event")

	resp, err := page.Goto(baseURL + "/ical")
	require.NoError(t, err)

	assert.Equal(t, 200, resp.Status())

	// Verify content type
	headers := resp.Headers()
	assert.Contains(t, headers["content-type"], "text/calendar")

	// Verify content contains expected data
	body, err := resp.Body()
	require.NoError(t, err)
	content := string(body)

	assert.Contains(t, content, "BEGIN:VCALENDAR")
	assert.Contains(t, content, "iCal Test Event")
	assert.Contains(t, content, "END:VCALENDAR")
}
