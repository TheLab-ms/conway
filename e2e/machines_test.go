package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Machines module tests
// The test app is configured with 3 mock printers:
// - "Printer A" (test-001): Available (no job, no error)
// - "Printer B" (test-002): In Use (has JobFinishedTimestamp)
// - "Printer C" (test-003): Failed (has ErrorCode)

func TestMachines_RequiresAuth(t *testing.T) {
	clearTestData(t)

	page := newPage(t)

	// Try to access machines page without authentication
	_, err := page.Goto(baseURL + "/machines")
	require.NoError(t, err)

	// Should redirect to login page
	err = page.WaitForURL("**/login**")
	require.NoError(t, err)
}

func TestMachines_PageStructure(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "machines@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	machinesPage := NewMachinesPage(t, page)
	machinesPage.Navigate()

	// Verify page heading
	machinesPage.ExpectHeading()

	// Verify all 3 printer cards are rendered
	machinesPage.ExpectPrinterCard("Printer A")
	machinesPage.ExpectPrinterCard("Printer B")
	machinesPage.ExpectPrinterCard("Printer C")
}

func TestMachines_AvailableStatus(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "machines-avail@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	machinesPage := NewMachinesPage(t, page)
	machinesPage.Navigate()

	// Printer A should show "Available" status (green badge)
	machinesPage.ExpectStatusBadge("Printer A", "Available")

	// Available printers should NOT have a stop button
	machinesPage.ExpectNoStopButton("Printer A")
}

func TestMachines_InUseStatus(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "machines-inuse@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	machinesPage := NewMachinesPage(t, page)
	machinesPage.Navigate()

	// Printer B should show "In Use" status (yellow badge)
	machinesPage.ExpectStatusBadge("Printer B", "In Use")

	// Should show time remaining
	machinesPage.ExpectTimeRemaining("Printer B")

	// In-use printers should have a stop button
	machinesPage.ExpectStopButton("Printer B")
}

func TestMachines_FailedStatus(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "machines-failed@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	machinesPage := NewMachinesPage(t, page)
	machinesPage.Navigate()

	// Printer C should show "Failed" status (red badge)
	machinesPage.ExpectStatusBadge("Printer C", "Failed")

	// Should display the error code
	machinesPage.ExpectErrorCode("Printer C", "HMS_0300_0100_0001")

	// Failed printers should have a stop button
	machinesPage.ExpectStopButton("Printer C")
}

func TestMachines_CameraStreamElements(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "machines-camera@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	machinesPage := NewMachinesPage(t, page)
	machinesPage.Navigate()

	// Each printer card should have a camera image element
	machinesPage.ExpectCameraImg("Printer A")
	machinesPage.ExpectCameraImg("Printer B")
	machinesPage.ExpectCameraImg("Printer C")
}

func TestMachines_StopButtonForm(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "machines-stop@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	machinesPage := NewMachinesPage(t, page)
	machinesPage.Navigate()

	// Verify the stop button form for Printer B (in use)
	stopButton := machinesPage.StopButton("Printer B")
	expect(t).Locator(stopButton).ToBeVisible()

	// Verify the form has the correct action URL
	form := machinesPage.PrinterCard("Printer B").Locator("form")
	action, err := form.GetAttribute("action")
	require.NoError(t, err)
	assert.Equal(t, "/machines/test-002/stop", action)

	// Verify the form has a confirmation dialog (onsubmit contains confirm)
	onsubmit, err := form.GetAttribute("onsubmit")
	require.NoError(t, err)
	assert.Contains(t, onsubmit, "confirm")
}

func TestMachines_StopButtonStyles(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "machines-styles@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	machinesPage := NewMachinesPage(t, page)
	machinesPage.Navigate()

	// Printer B (in use) should have outline-danger button
	btnB := machinesPage.StopButton("Printer B")
	classB, err := btnB.GetAttribute("class")
	require.NoError(t, err)
	assert.Contains(t, classB, "btn-outline-danger")

	// Printer C (failed) should have solid danger button
	btnC := machinesPage.StopButton("Printer C")
	classC, err := btnC.GetAttribute("class")
	require.NoError(t, err)
	assert.Contains(t, classC, "btn-danger")
	assert.NotContains(t, classC, "btn-outline-danger")
}

func TestMachines_ResponsiveLayout(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "machines-layout@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	machinesPage := NewMachinesPage(t, page)
	machinesPage.Navigate()

	// Verify the layout uses Bootstrap grid (col-md-4)
	cards := page.Locator(".col-md-4 .card")
	count, err := cards.Count()
	require.NoError(t, err)
	assert.Equal(t, 3, count, "should have 3 printer cards in responsive grid")
}
