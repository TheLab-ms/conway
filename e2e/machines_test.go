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
	page := setupUnauthenticatedTest(t)

	_, err := page.Goto(baseURL + "/machines")
	require.NoError(t, err)

	err = page.WaitForURL("**/login**")
	require.NoError(t, err)
}

func TestMachines_AllPrinterStatuses(t *testing.T) {
	_, page := setupMemberTest(t, "machines@example.com", WithConfirmed())
	machinesPage := NewMachinesPage(t, page)
	machinesPage.Navigate()

	t.Run("page_structure", func(t *testing.T) {
		machinesPage.ExpectHeading()
		machinesPage.ExpectPrinterCard("Printer A")
		machinesPage.ExpectPrinterCard("Printer B")
		machinesPage.ExpectPrinterCard("Printer C")

		// Verify responsive layout uses Bootstrap grid
		cards := page.Locator(".col-md-4 .card")
		count, err := cards.Count()
		require.NoError(t, err)
		assert.Equal(t, 3, count, "should have 3 printer cards in responsive grid")

		// Each printer card should have a camera image element
		machinesPage.ExpectCameraImg("Printer A")
		machinesPage.ExpectCameraImg("Printer B")
		machinesPage.ExpectCameraImg("Printer C")
	})

	t.Run("available_printer", func(t *testing.T) {
		machinesPage.ExpectStatusBadge("Printer A", "Available")
		machinesPage.ExpectNoStopButton("Printer A")
	})

	t.Run("in_use_printer", func(t *testing.T) {
		machinesPage.ExpectStatusBadge("Printer B", "In Use")
		machinesPage.ExpectTimeRemaining("Printer B")
		machinesPage.ExpectStopButton("Printer B")

		// Verify stop button form
		stopButton := machinesPage.StopButton("Printer B")
		expect(t).Locator(stopButton).ToBeVisible()

		form := machinesPage.PrinterCard("Printer B").Locator("form")
		action, err := form.GetAttribute("action")
		require.NoError(t, err)
		assert.Equal(t, "/machines/test-002/stop", action)

		onsubmit, err := form.GetAttribute("onsubmit")
		require.NoError(t, err)
		assert.Contains(t, onsubmit, "confirm")

		// In-use printer has outline-danger button
		classB, err := stopButton.GetAttribute("class")
		require.NoError(t, err)
		assert.Contains(t, classB, "btn-outline-danger")
	})

	t.Run("failed_printer", func(t *testing.T) {
		machinesPage.ExpectStatusBadge("Printer C", "Failed")
		machinesPage.ExpectErrorCode("Printer C", "HMS_0300_0100_0001")
		machinesPage.ExpectStopButton("Printer C")

		// Failed printer has solid danger button
		btnC := machinesPage.StopButton("Printer C")
		classC, err := btnC.GetAttribute("class")
		require.NoError(t, err)
		assert.Contains(t, classC, "btn-danger")
		assert.NotContains(t, classC, "btn-outline-danger")
	})
}
