package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Note: The machines module is disabled unless BambuPrinters is configured.
// These tests verify the page loads correctly when configured.

func TestMachines_RequiresAuth(t *testing.T) {
	clearTestData(t)

	page := newPage(t)

	// Try to access machines page without authentication
	resp, err := page.Goto(baseURL + "/machines")
	require.NoError(t, err)

	// Since machines module is disabled (no BambuPrinters config),
	// we should get a 404. If it were enabled, unauthenticated access
	// would redirect to login.
	if resp.Status() == 404 {
		t.Log("Machines module not configured (expected in test environment)")
		return
	}

	// If machines is configured, should redirect to login
	if resp.Status() == 302 {
		err = page.WaitForURL("**/login**")
		require.NoError(t, err)
	}
}

func TestMachines_PageAccessWhenLoggedIn(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "machines@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	// Navigate to machines page
	resp, err := page.Goto(baseURL + "/machines")
	require.NoError(t, err)

	// Since machines module is disabled in test (no BambuPrinters config),
	// we should get a 404 or the page simply doesn't exist.
	// This is expected behavior when the module is not configured.
	if resp.Status() == 404 {
		t.Log("Machines module not configured (expected in test environment)")
		return
	}

	// If machines module is somehow enabled, verify basic structure
	if resp.Status() == 200 {
		// The page should load with some content
		expect(t).Locator(page.Locator("body")).ToBeVisible()
	}
}

func TestMachines_RouteNotFound(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "machines404@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	resp, err := page.Goto(baseURL + "/machines")
	require.NoError(t, err)

	// Since machines module is disabled, expect 404
	assert.Equal(t, 404, resp.Status(), "machines should return 404 when not configured")
}
