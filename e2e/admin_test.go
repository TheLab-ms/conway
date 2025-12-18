package e2e

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdmin_RequiresLeadership(t *testing.T) {
	clearTestData(t)

	// Create a regular member (not leadership)
	memberID := seedMember(t, "regular@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	// Try to access admin page
	resp, err := page.Goto(baseURL + "/admin/members")
	require.NoError(t, err)

	// Should get 403 Forbidden
	assert.Equal(t, 403, resp.Status())

	// Should show error message
	locator := page.GetByText("You must be a member of leadership")
	expect(t).Locator(locator).ToBeVisible()
}

func TestAdmin_MembersList(t *testing.T) {
	clearTestData(t)

	// Create an admin
	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	// Create some test members
	seedMember(t, "member1@example.com", WithConfirmed())
	seedMember(t, "member2@example.com", WithConfirmed())
	seedMember(t, "member3@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	adminPage := NewAdminMembersListPage(t, page)
	adminPage.Navigate()

	// Wait for the initial load
	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Should show members in the list
	// The list uses HTMX to load, so we need to wait for it
	expect(t).Locator(page.Locator("#results")).ToBeVisible()
}

func TestAdmin_Search(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	// Create members with distinct emails for searching
	seedMember(t, "searchable@example.com", WithConfirmed())
	seedMember(t, "findme@example.com", WithConfirmed())
	seedMember(t, "other@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	adminPage := NewAdminMembersListPage(t, page)
	adminPage.Navigate()

	// Wait for initial load
	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Search for "searchable"
	adminPage.Search("searchable")

	// Should find the matching member
	adminPage.ExpectMemberInList("searchable@example.com")
}

func TestAdmin_ClickToDetail(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	targetID := seedMember(t, "target@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	adminPage := NewAdminMembersListPage(t, page)
	adminPage.Navigate()

	err := page.WaitForLoadState()
	require.NoError(t, err)

	// Search for the target member
	adminPage.Search("target@example.com")

	// Click on the row
	adminPage.ClickMemberRow("target@example.com")

	// Should navigate to member detail page
	err = page.WaitForURL(fmt.Sprintf("**/admin/members/%d", targetID))
	require.NoError(t, err)
}

func TestAdmin_EditBasics(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	targetID := seedMember(t, "edittarget@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	// Navigate directly to member detail page
	_, err := page.Goto(baseURL + "/admin/members/" + strconv.FormatInt(targetID, 10))
	require.NoError(t, err)

	detail := NewAdminMemberDetailPage(t, page)

	// Fill in new values
	detail.FillFobID("54321")
	detail.FillAdminNotes("Test notes from E2E test")

	// Submit the form
	detail.SubmitBasicsForm()

	// Wait for page to reload
	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Verify changes in database
	var fobID int64
	var notes string
	err = testDB.QueryRow("SELECT fob_id, admin_notes FROM members WHERE id = ?", targetID).Scan(&fobID, &notes)
	require.NoError(t, err)
	assert.Equal(t, int64(54321), fobID)
	assert.Equal(t, "Test notes from E2E test", notes)
}

func TestAdmin_EditDesignations(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	targetID := seedMember(t, "designations@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	_, err := page.Goto(baseURL + "/admin/members/" + strconv.FormatInt(targetID, 10))
	require.NoError(t, err)

	detail := NewAdminMemberDetailPage(t, page)

	// Toggle leadership
	detail.ToggleLeadership()
	detail.SubmitDesignationsForm()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Verify changes in database
	var leadership bool
	err = testDB.QueryRow("SELECT leadership FROM members WHERE id = ?", targetID).Scan(&leadership)
	require.NoError(t, err)
	assert.True(t, leadership, "member should now be leadership")
}

func TestAdmin_GenerateLoginQR(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	targetID := seedMember(t, "qrtest@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	// Navigate to the login code endpoint directly
	resp, err := page.Goto(baseURL + "/admin/members/" + strconv.FormatInt(targetID, 10) + "/logincode")
	require.NoError(t, err)

	// Should return a PNG image
	assert.Equal(t, 200, resp.Status())
	headers := resp.Headers()
	assert.Contains(t, headers["content-type"], "image/png")
}

func TestAdmin_DeleteMember(t *testing.T) {
	clearTestData(t)

	adminID := seedMember(t, "admin@example.com",
		WithConfirmed(),
		WithLeadership(),
	)

	targetID := seedMember(t, "deleteme@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, adminID)
	page := newPageInContext(t, ctx)

	_, err := page.Goto(baseURL + "/admin/members/" + strconv.FormatInt(targetID, 10))
	require.NoError(t, err)

	detail := NewAdminMemberDetailPage(t, page)

	// Click delete button
	detail.ClickDeleteMember()

	// Wait for confirm button to appear
	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Confirm deletion
	detail.ConfirmDelete()

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Verify member was deleted
	var count int
	err = testDB.QueryRow("SELECT COUNT(*) FROM members WHERE id = ?", targetID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "member should be deleted")
}
