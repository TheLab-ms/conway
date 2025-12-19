package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminSettings_RequiresLeadership(t *testing.T) {
	_, page := setupMemberTest(t, "regular@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
	)

	resp, err := page.Goto(baseURL + "/admin/settings")
	require.NoError(t, err)

	assert.Equal(t, 403, resp.Status())
	locator := page.GetByText("You must be a member of leadership")
	expect(t).Locator(locator).ToBeVisible()
}

func TestAdminSettings_RendersSections(t *testing.T) {
	_, page := setupAdminTest(t)

	_, err := page.Goto(baseURL + "/admin/settings")
	require.NoError(t, err)

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Verify the settings page title
	expect(t).Locator(page.Locator("h1")).ToHaveText("Settings")

	// Verify section cards are rendered for each module
	expect(t).Locator(page.GetByText("Core Settings")).ToBeVisible()
	expect(t).Locator(page.GetByText("Stripe")).ToBeVisible()
	expect(t).Locator(page.GetByText("Discord")).ToBeVisible()
	expect(t).Locator(page.GetByText("Gmail")).ToBeVisible()
	expect(t).Locator(page.GetByText("Authentication (Turnstile)")).ToBeVisible()
	expect(t).Locator(page.GetByText("Machines (Bambu Printers)")).ToBeVisible()
	expect(t).Locator(page.GetByText("Access Controller")).ToBeVisible()
}

func TestAdminSettings_ShowsFieldLabels(t *testing.T) {
	_, page := setupAdminTest(t)

	_, err := page.Goto(baseURL + "/admin/settings")
	require.NoError(t, err)

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Check for specific field labels
	expect(t).Locator(page.GetByText("Public URL")).ToBeVisible()
	expect(t).Locator(page.GetByText("API Key")).ToBeVisible() // Stripe API Key
	expect(t).Locator(page.GetByText("From Address")).ToBeVisible()
	expect(t).Locator(page.GetByText("Client ID")).ToBeVisible() // Discord
	expect(t).Locator(page.GetByText("Site Key")).ToBeVisible()  // Turnstile
}

func TestAdminSettings_SensitiveFieldsShowBadge(t *testing.T) {
	_, page := setupAdminTest(t)

	_, err := page.Goto(baseURL + "/admin/settings")
	require.NoError(t, err)

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Sensitive fields should have a "Sensitive" badge
	badges := page.Locator(".badge:has-text('Sensitive')")
	count, err := badges.Count()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 5, "expected at least 5 sensitive field badges")
}

func TestAdminSettings_FormValuesCanBeEdited(t *testing.T) {
	_, page := setupAdminTest(t)

	_, err := page.Goto(baseURL + "/admin/settings")
	require.NoError(t, err)

	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Fill in a non-sensitive field value
	testURL := "https://test.example.com"
	err = page.Locator("#core\\.self_url").Fill(testURL)
	require.NoError(t, err)

	// Fill in another field
	testHost := "test.local"
	err = page.Locator("#kiosk\\.space_host").Fill(testHost)
	require.NoError(t, err)

	// Verify the values are in the inputs
	selfURLInput := page.Locator("#core\\.self_url")
	expect(t).Locator(selfURLInput).ToHaveValue(testURL)

	spaceHostInput := page.Locator("#kiosk\\.space_host")
	expect(t).Locator(spaceHostInput).ToHaveValue(testHost)
}

func TestAdminSettings_SensitiveFieldsShowPlaceholder(t *testing.T) {
	_, page := setupAdminTest(t)

	_, err := page.Goto(baseURL + "/admin/settings")
	require.NoError(t, err)
	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Sensitive fields that are NOT set should show "Not set" placeholder
	stripeKeyInput := page.Locator("#stripe\\.key")
	placeholder, err := stripeKeyInput.GetAttribute("placeholder")
	require.NoError(t, err)
	assert.Equal(t, "Not set", placeholder)
}

func TestAdminSettings_BambuPrintersAddRemove(t *testing.T) {
	_, page := setupAdminTest(t)

	_, err := page.Goto(baseURL + "/admin/settings")
	require.NoError(t, err)
	err = page.WaitForLoadState()
	require.NoError(t, err)

	// Initially, there should be no printer entries
	printerEntries := page.Locator(".printer-entry")
	count, err := printerEntries.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Click "Add Printer" button
	err = page.Locator("button:has-text('+ Add Printer')").Click()
	require.NoError(t, err)

	// Now there should be one printer entry
	count, err = printerEntries.Count()
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Fill in printer details
	err = page.Locator(".printer-name").First().Fill("Test Printer")
	require.NoError(t, err)
	err = page.Locator(".printer-host").First().Fill("192.168.1.100")
	require.NoError(t, err)
	err = page.Locator(".printer-access-code").First().Fill("testcode")
	require.NoError(t, err)
	err = page.Locator(".printer-serial").First().Fill("TEST12345")
	require.NoError(t, err)

	// Add a second printer
	err = page.Locator("button:has-text('+ Add Printer')").Click()
	require.NoError(t, err)

	count, err = printerEntries.Count()
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Remove the first printer
	err = page.Locator("button:has-text('Remove')").First().Click()
	require.NoError(t, err)

	// Verify one was removed
	count, err = printerEntries.Count()
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestAdminSettings_HTTPAddrNotShown(t *testing.T) {
	_, page := setupAdminTest(t)

	_, err := page.Goto(baseURL + "/admin/settings")
	require.NoError(t, err)
	err = page.WaitForLoadState()
	require.NoError(t, err)

	// HTTP Address should NOT be on the settings page (it's an env var)
	httpAddrInput := page.Locator("#core\\.http_addr")
	count, err := httpAddrInput.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "core.http_addr should not be on settings page")

	// Also check the label isn't visible
	httpAddrLabel := page.GetByText("HTTP Address")
	count, err = httpAddrLabel.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count, "HTTP Address label should not be visible")
}
