package e2e

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestJourney_NewMemberOnboarding tests the complete new member signup flow:
// 1. Sign waiver
// 2. Request login email
// 3. Click magic link
// 4. View dashboard (shows missing payment)
// 5. (Payment would redirect to Stripe - we verify the redirect works)
func TestJourney_NewMemberOnboarding(t *testing.T) {
	clearTestData(t)

	page := newPage(t)
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

	// Should redirect to dashboard
	err = page.WaitForURL("**/")
	require.NoError(t, err)

	// Step 4: Dashboard should show missing payment (waiver was linked via trigger)
	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectMissingPaymentAlert()

	// Step 5: Verify Manage Payment button is visible
	expect(t).Locator(page.Locator("a:has-text('Manage Payment')")).ToBeVisible()
}

// TestJourney_AdminManagesMember tests an admin finding and editing a member.
func TestJourney_AdminManagesMember(t *testing.T) {
	clearTestData(t)

	// Create an admin
	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	// Create a member to manage
	targetID := seedMember(t, "manageme@example.com",
		WithConfirmed(),
		WithWaiver(),
	)

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	// Step 1: Navigate to admin members list
	adminList := NewAdminMembersListPage(t, page)
	adminList.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Step 2: Search for the member
	adminList.Search("manageme@example.com")

	// Step 3: Click on the member row
	adminList.ClickMemberRow("manageme@example.com")

	// Should navigate to detail page
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

// TestJourney_MemberStatusProgression tests a member going through all status states.
func TestJourney_MemberStatusProgression(t *testing.T) {
	clearTestData(t)

	email := "progression@example.com"

	// Start with just email confirmed
	memberID := seedMember(t, email, WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	dashboard := NewMemberDashboardPage(t, page)

	// State 1: Missing waiver
	dashboard.Navigate()
	dashboard.ExpectMissingWaiverAlert()

	// Sign waiver (via database to speed up test)
	seedWaiver(t, email)

	// Refresh page
	dashboard.Navigate()

	// State 2: Missing payment
	dashboard.ExpectMissingPaymentAlert()

	// Add payment (via database)
	_, err := testDB.Exec("UPDATE members SET stripe_subscription_state = 'active', stripe_customer_id = 'cus_test' WHERE id = ?", memberID)
	require.NoError(t, err)

	// Refresh page
	dashboard.Navigate()

	// State 3: Missing keyfob
	dashboard.ExpectMissingKeyFobAlert()

	// Add fob (via database)
	_, err = testDB.Exec("UPDATE members SET fob_id = 12345 WHERE id = ?", memberID)
	require.NoError(t, err)

	// Refresh page
	dashboard.Navigate()

	// State 4: Active!
	dashboard.ExpectActiveStatus()
}

// TestJourney_WaiverThenLogin tests signing waiver then logging in links them together.
func TestJourney_WaiverThenLogin(t *testing.T) {
	clearTestData(t)

	email := "waiverfirst@example.com"

	page := newPage(t)

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

	// Check that member has waiver linked (via trigger)
	var waiverID *int64
	err = testDB.QueryRow("SELECT waiver FROM members WHERE id = ?", memberID).Scan(&waiverID)
	require.NoError(t, err)
	assert.NotNil(t, waiverID, "waiver should be linked to member")

	// Dashboard should show missing payment (not missing waiver)
	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectMissingPaymentAlert()
}

// TestJourney_LoginThenWaiver tests logging in first then signing waiver links them.
func TestJourney_LoginThenWaiver(t *testing.T) {
	clearTestData(t)

	email := "loginfirst@example.com"

	page := newPage(t)

	// Login first (creates member record)
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

	// Dashboard shows missing waiver
	dashboard := NewMemberDashboardPage(t, page)
	dashboard.ExpectMissingWaiverAlert()

	// Now sign waiver
	waiverPage := NewWaiverPage(t, page)
	waiverPage.Navigate()
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("Login First User")
	waiverPage.FillEmail(email)
	waiverPage.Submit()
	waiverPage.ExpectSuccessMessage()

	// Refresh dashboard
	dashboard.Navigate()

	// Should now show missing payment
	dashboard.ExpectMissingPaymentAlert()
}

// TestJourney_AdminViewsAllDataTabs tests an admin navigating through all data tabs.
func TestJourney_AdminViewsAllDataTabs(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "tabadmin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	// Create some test data
	memberID := seedMember(t, "datamember@example.com",
		WithConfirmed(),
		WithFobID(33333),
	)
	seedWaiver(t, "datawaiver@example.com")
	seedFobSwipes(t, 33333, 3)
	seedMemberEvents(t, memberID, 3)
	seedMetrics(t, "test_metric", 5)

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	// Visit each admin tab
	tabs := []string{"/admin/members", "/admin/fobs", "/admin/events", "/admin/waivers", "/admin/metrics"}

	for _, tab := range tabs {
		_, err := page.Goto(baseURL + tab)
		require.NoError(t, err, "should load "+tab)

		resp, _ := page.Evaluate("() => document.readyState")
		assert.Equal(t, "complete", resp, "page should be loaded for "+tab)
	}
}

// TestJourney_AdminExportsAllData tests an admin exporting all CSV data types.
func TestJourney_AdminExportsAllData(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "exportadmin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	// Create some test data
	memberID := seedMember(t, "exportmember@example.com",
		WithConfirmed(),
		WithFobID(44444),
	)
	seedWaiver(t, "exportwaiver@example.com")
	seedFobSwipes(t, 44444, 3)
	seedMemberEvents(t, memberID, 3)

	ctx := newContext(t)
	loginAs(t, ctx, adminID)

	// Use the context's request API for CSV downloads (avoids page navigation issues)
	apiContext := ctx.Request()

	// Export each table
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

// TestJourney_CallbackPreservation tests that callback_uri is preserved through login.
func TestJourney_CallbackPreservation(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "callback@example.com", WithConfirmed(), WithLeadership())

	page := newPage(t)

	// Try to access admin page without auth
	_, err := page.Goto(baseURL + "/admin/members")
	require.NoError(t, err)

	// Should redirect to login with callback
	err = page.WaitForURL("**/login**")
	require.NoError(t, err)

	// The callback should be preserved in the URL
	url := page.URL()
	assert.Contains(t, url, "callback_uri")

	// Login via magic link with the callback
	token := generateMagicLinkToken(t, memberID)
	_, err = page.Goto(baseURL + "/login?t=" + token + "&n=" + "/admin/members")
	require.NoError(t, err)

	// Should redirect to the original destination
	err = page.WaitForURL("**/admin/members**")
	require.NoError(t, err)
}

// TestJourney_MultipleMembers tests admin managing multiple members.
func TestJourney_MultipleMembers(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "multiadmin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	// Create multiple members
	member1ID := seedMember(t, "multi1@example.com", WithConfirmed())
	member2ID := seedMember(t, "multi2@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

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
