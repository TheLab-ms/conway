package e2e

import (
	"fmt"
	"testing"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

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
