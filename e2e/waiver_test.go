package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaiver_Display(t *testing.T) {
	clearTestData(t)

	page := newPage(t)
	waiverPage := NewWaiverPage(t, page)

	waiverPage.Navigate()
	waiverPage.ExpectWaiverText()

	// Check that the form elements are present
	expect(t).Locator(page.Locator("#agree1")).ToBeVisible()
	expect(t).Locator(page.Locator("#agree2")).ToBeVisible()
	expect(t).Locator(page.Locator("#name")).ToBeVisible()
	expect(t).Locator(page.Locator("#email")).ToBeVisible()
}

func TestWaiver_Submission(t *testing.T) {
	clearTestData(t)

	page := newPage(t)
	waiverPage := NewWaiverPage(t, page)

	waiverPage.Navigate()
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("Test Person")
	waiverPage.FillEmail("waiver@example.com")
	waiverPage.Submit()

	waiverPage.ExpectSuccessMessage()

	// Verify waiver was stored in database
	var name, email string
	err := testDB.QueryRow("SELECT name, email FROM waivers WHERE email = ?", "waiver@example.com").Scan(&name, &email)
	require.NoError(t, err)
	assert.Equal(t, "Test Person", name)
	assert.Equal(t, "waiver@example.com", email)
}

func TestWaiver_LinkToMember(t *testing.T) {
	clearTestData(t)

	// Create a member first
	memberID := seedMember(t, "existing@example.com", WithConfirmed())

	// Verify member has no waiver
	var waiverID *int64
	err := testDB.QueryRow("SELECT waiver FROM members WHERE id = ?", memberID).Scan(&waiverID)
	require.NoError(t, err)
	assert.Nil(t, waiverID, "member should not have a waiver initially")

	page := newPage(t)
	waiverPage := NewWaiverPage(t, page)

	waiverPage.Navigate()
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("Existing Member")
	waiverPage.FillEmail("existing@example.com")
	waiverPage.Submit()

	waiverPage.ExpectSuccessMessage()

	// Verify waiver is linked to member (via trigger)
	err = testDB.QueryRow("SELECT waiver FROM members WHERE id = ?", memberID).Scan(&waiverID)
	require.NoError(t, err)
	assert.NotNil(t, waiverID, "member should now have a waiver")
}

func TestWaiver_CheckboxValidation(t *testing.T) {
	clearTestData(t)

	page := newPage(t)
	waiverPage := NewWaiverPage(t, page)

	waiverPage.Navigate()

	// Fill in name and email but don't check boxes
	waiverPage.FillName("No Checkboxes")
	waiverPage.FillEmail("nocheckbox@example.com")

	// Try to submit - should fail due to HTML5 validation
	// The form uses 'required' attribute on checkboxes
	waiverPage.Submit()

	// We should still be on the waiver page (form didn't submit due to validation)
	waiverPage.ExpectWaiverText()

	// Verify no waiver was created
	var count int
	err := testDB.QueryRow("SELECT COUNT(*) FROM waivers WHERE email = ?", "nocheckbox@example.com").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "waiver should not be created without checkboxes")
}

func TestWaiver_WithRedirect(t *testing.T) {
	clearTestData(t)

	page := newPage(t)
	waiverPage := NewWaiverPage(t, page)

	waiverPage.NavigateWithRedirect("/")
	waiverPage.CheckAgree1()
	waiverPage.CheckAgree2()
	waiverPage.FillName("Redirect Test")
	waiverPage.FillEmail("redirect@example.com")
	waiverPage.Submit()

	// Should show success message with redirect link
	waiverPage.ExpectSuccessMessage()
	expect(t).Locator(page.Locator("a:has-text('Done')")).ToBeVisible()
}
