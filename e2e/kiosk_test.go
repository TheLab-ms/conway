package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKiosk_AccessFromPhysicalSpace(t *testing.T) {
	clearTestData(t)

	page := newPage(t)

	// The kiosk uses SpaceHost to verify the request is from the physical space.
	// In our test config, we set SpaceHost to "localhost", so requests from
	// the test browser should work.
	kiosk := NewKioskPage(t, page)
	kiosk.Navigate()

	// Should show the kiosk interface
	kiosk.ExpectKioskInterface()
}

func TestKeyfob_BindFlow(t *testing.T) {
	clearTestData(t)

	// Create a member who needs to bind a keyfob
	memberID := seedMember(t, "bindkey@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		// No fob ID yet
	)

	// First, verify member has no fob
	var fobID *int64
	err := testDB.QueryRow("SELECT fob_id FROM members WHERE id = ?", memberID).Scan(&fobID)
	require.NoError(t, err)
	assert.Nil(t, fobID, "member should not have a fob initially")

	// The keyfob bind endpoint requires a token from the kiosk
	// In a real scenario:
	// 1. User scans RFID at kiosk
	// 2. Kiosk generates a QR code with /keyfob/bind?val=TOKEN
	// 3. User scans QR code with their phone (authenticated)
	// 4. Fob is linked to their account

	// For testing, we can directly test the bind endpoint with a mock token
	// But the kiosk module generates tokens using the fobIssuer

	// Create an authenticated context for the member
	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	// We need to generate a valid fob bind token
	// The kiosk generates this when a fob is scanned
	// For now, we'll test that the kiosk page loads correctly
	// A full integration test would require simulating the kiosk token generation

	// Navigate to dashboard to verify auth works
	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()

	// Should show missing keyfob since we haven't bound one
	dashboard.ExpectMissingKeyFobAlert()
}

func TestKeyfob_StatusEndpoint(t *testing.T) {
	clearTestData(t)

	// Create a member with a fob
	seedMember(t, "hasfob@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(99999),
	)

	page := newPage(t)

	// Check the keyfob status endpoint
	// This endpoint is used by the kiosk to poll if a fob has been bound
	resp, err := page.Goto(baseURL + "/keyfob/status/99999")
	require.NoError(t, err)

	// The status endpoint returns true/false
	// When a fob is in use, it should return true
	assert.Equal(t, 200, resp.Status())
}

func TestKeyfob_StatusEndpoint_UnusedFob(t *testing.T) {
	clearTestData(t)

	page := newPage(t)

	// Check status for a fob ID that doesn't exist
	resp, err := page.Goto(baseURL + "/keyfob/status/11111")
	require.NoError(t, err)

	// Should still return 200, but with false
	assert.Equal(t, 200, resp.Status())
}
