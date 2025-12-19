package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
