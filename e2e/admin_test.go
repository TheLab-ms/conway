package e2e

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdmin_RequiresLeadership(t *testing.T) {
	_, page := setupMemberTest(t, "regular@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
	)

	resp, err := page.Goto(baseURL + "/admin/members")
	require.NoError(t, err)

	assert.Equal(t, 403, resp.Status())
	locator := page.GetByText("You must be a member of leadership")
	expect(t).Locator(locator).ToBeVisible()
}

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

func TestAdmin_GenerateLoginQR(t *testing.T) {
	_, page := setupAdminTest(t)
	targetID := seedMember(t, "qrtest@example.com", WithConfirmed())

	resp, err := page.Goto(baseURL + "/admin/members/" + strconv.FormatInt(targetID, 10) + "/logincode")
	require.NoError(t, err)

	assert.Equal(t, 200, resp.Status())
	headers := resp.Headers()
	assert.Contains(t, headers["content-type"], "image/png")
}

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
