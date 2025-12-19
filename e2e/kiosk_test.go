package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKiosk_AccessFromPhysicalSpace(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	kiosk := NewKioskPage(t, page)
	kiosk.Navigate()
	kiosk.ExpectKioskInterface()
}

func TestKeyfob_BindFlow(t *testing.T) {
	memberID, page := setupMemberTest(t, "bindkey@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
	)

	// Verify member has no fob initially
	var fobID *int64
	err := testDB.QueryRow("SELECT fob_id FROM members WHERE id = ?", memberID).Scan(&fobID)
	require.NoError(t, err)
	assert.Nil(t, fobID, "member should not have a fob initially")

	// Navigate to dashboard to verify missing keyfob alert
	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()
	dashboard.ExpectMissingKeyFobAlert()
}

func TestKeyfob_StatusEndpoint(t *testing.T) {
	page := setupUnauthenticatedTest(t)
	seedMember(t, "hasfob@example.com", WithConfirmed(), WithFobID(99999))

	t.Run("existing_fob", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/keyfob/status/99999")
		require.NoError(t, err)
		assert.Equal(t, 200, resp.Status())
	})

	t.Run("unused_fob", func(t *testing.T) {
		resp, err := page.Goto(baseURL + "/keyfob/status/11111")
		require.NoError(t, err)
		assert.Equal(t, 200, resp.Status())
	})
}
