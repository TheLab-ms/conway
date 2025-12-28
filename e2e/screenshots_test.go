package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/stretchr/testify/require"
)

// screenshotDir is the directory where screenshots are saved
const screenshotDir = "e2e/screenshots"

// saveScreenshot saves a screenshot of the current page state.
func saveScreenshot(t *testing.T, page playwright.Page, name string) string {
	t.Helper()

	// Ensure screenshots directory exists
	err := os.MkdirAll(screenshotDir, 0755)
	require.NoError(t, err, "could not create screenshots directory")

	path := filepath.Join(screenshotDir, name+".png")
	_, err = page.Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(path),
		FullPage: playwright.Bool(true),
	})
	require.NoError(t, err, "could not save screenshot")

	t.Logf("Screenshot saved: %s", path)
	return path
}

// TestScreenshots_AllPages captures screenshots of all major pages for visual verification.
// This test is meant to be run after design changes to verify the new design system.
func TestScreenshots_AllPages(t *testing.T) {
	clearTestData(t)

	// Seed test data
	memberID := seedMember(t, "screenshot-test@example.com", WithConfirmed(), WithWaiver(), WithLeadership())
	activeMemberID := seedMember(t, "active-member@example.com", WithConfirmed(), WithWaiver(), WithFobID(123), WithActiveStripeSubscription())
	seedMember(t, "other@example.com", WithConfirmed())

	// Allow time for subscription data
	time.Sleep(100 * time.Millisecond)

	t.Run("login_page", func(t *testing.T) {
		page := newPage(t)
		_, err := page.Goto(baseURL + "/login")
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		saveScreenshot(t, page, "01_login_page")
	})

	t.Run("login_sent_page", func(t *testing.T) {
		page := newPage(t)
		_, err := page.Goto(baseURL + "/login/sent?email=test@example.com")
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		saveScreenshot(t, page, "02_login_sent_page")
	})

	t.Run("waiver_page", func(t *testing.T) {
		page := newPage(t)
		_, err := page.Goto(baseURL + "/waiver")
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		saveScreenshot(t, page, "03_waiver_page")
	})

	t.Run("member_dashboard_onboarding", func(t *testing.T) {
		page := newPage(t)
		newMemberID := seedMember(t, "new-member@example.com", WithConfirmed())
		loginPageAs(t, page, newMemberID)

		_, err := page.Goto(baseURL + "/")
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		saveScreenshot(t, page, "04_member_dashboard_onboarding")
	})

	t.Run("member_dashboard_active", func(t *testing.T) {
		page := newPage(t)
		loginPageAs(t, page, activeMemberID)

		_, err := page.Goto(baseURL + "/")
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		saveScreenshot(t, page, "05_member_dashboard_active")
	})

	t.Run("machines_page", func(t *testing.T) {
		page := newPage(t)
		loginPageAs(t, page, memberID)

		_, err := page.Goto(baseURL + "/machines")
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		saveScreenshot(t, page, "06_machines_page")
	})

	t.Run("kiosk_page", func(t *testing.T) {
		page := newPage(t)
		_, err := page.Goto(baseURL + "/kiosk")
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		saveScreenshot(t, page, "07_kiosk_page")
	})

	t.Run("admin_members_list", func(t *testing.T) {
		page := newPage(t)
		loginPageAs(t, page, memberID)

		_, err := page.Goto(baseURL + "/admin/members")
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		// Wait for HTMX to load members
		page.Locator("table").WaitFor()
		saveScreenshot(t, page, "08_admin_members_list")
	})

	t.Run("admin_member_detail", func(t *testing.T) {
		page := newPage(t)
		loginPageAs(t, page, memberID)

		_, err := page.Goto(baseURL + "/admin/members/" + itoa(activeMemberID))
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		saveScreenshot(t, page, "09_admin_member_detail")
	})

	t.Run("admin_metrics", func(t *testing.T) {
		page := newPage(t)
		loginPageAs(t, page, memberID)

		_, err := page.Goto(baseURL + "/admin/metrics")
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		// Allow charts to render
		time.Sleep(500 * time.Millisecond)
		saveScreenshot(t, page, "10_admin_metrics")
	})

	t.Run("admin_fobs", func(t *testing.T) {
		page := newPage(t)
		loginPageAs(t, page, memberID)

		_, err := page.Goto(baseURL + "/admin/fobs")
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		page.Locator("table").WaitFor()
		saveScreenshot(t, page, "11_admin_fobs")
	})

	t.Run("error_page", func(t *testing.T) {
		page := newPage(t)
		// Access a protected page without auth to trigger error
		_, err := page.Goto(baseURL + "/admin/members")
		require.NoError(t, err)
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		saveScreenshot(t, page, "12_error_page")
	})
}

func itoa(i int64) string {
	return fmt.Sprintf("%d", i)
}
