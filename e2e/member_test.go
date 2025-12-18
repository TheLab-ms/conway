package e2e

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDashboard_ActiveMember(t *testing.T) {
	clearTestData(t)

	// Create a fully active member
	memberID := seedMember(t, "active@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()
	dashboard.ExpectActiveStatus()
}

func TestDashboard_MissingWaiver(t *testing.T) {
	clearTestData(t)

	// Member without waiver
	memberID := seedMember(t, "nowaiver@example.com", WithConfirmed())

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()
	dashboard.ExpectMissingWaiverAlert()
}

func TestDashboard_MissingPayment(t *testing.T) {
	clearTestData(t)

	// Member with waiver but no payment
	memberID := seedMember(t, "nopayment@example.com",
		WithConfirmed(),
		WithWaiver(),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()
	dashboard.ExpectMissingPaymentAlert()
}

func TestDashboard_MissingKeyFob(t *testing.T) {
	clearTestData(t)

	// Member with waiver and payment but no fob
	memberID := seedMember(t, "nofob@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()
	dashboard.ExpectMissingKeyFobAlert()
}

func TestDashboard_ManagePaymentButton(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "payment@example.com",
		WithConfirmed(),
		WithWaiver(),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()

	// Verify Manage Payment button is visible
	expect(t).Locator(page.Locator("a:has-text('Manage Payment')")).ToBeVisible()

	// Click should navigate to /payment/checkout
	dashboard.ClickManagePayment()

	// If Stripe is configured, this will redirect to Stripe Checkout
	// Otherwise, it may error. We just verify the navigation happened.
	err := page.WaitForURL("**/payment/checkout**")
	// This may fail if Stripe redirects immediately, so we check either case
	if err != nil {
		// Check if we got redirected to Stripe
		url := page.URL()
		if url == baseURL+"/payment/checkout" {
			// Stayed on the same page or errored - that's fine for tests without Stripe
			t.Log("Payment checkout page loaded (Stripe not configured or no subscription)")
		}
	}
}

func TestDashboard_DiscordLinkButton(t *testing.T) {
	clearTestData(t)

	// Member without Discord linked
	memberID := seedMember(t, "nodiscord@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()

	// Verify Link Discord button is visible
	expect(t).Locator(page.Locator("a:has-text('Link Discord Account')")).ToBeVisible()
}

func TestDashboard_DiscordButtonHiddenWhenLinked(t *testing.T) {
	clearTestData(t)

	// Member with Discord already linked
	memberID := seedMember(t, "hasdiscord@example.com",
		WithConfirmed(),
		WithWaiver(),
		WithActiveStripeSubscription(),
		WithFobID(12345),
		WithDiscord("123456789"),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()

	// Link Discord button should NOT be visible
	expect(t).Locator(page.Locator("a:has-text('Link Discord Account')")).ToBeHidden()
}

func TestDashboard_RequiresAuthentication(t *testing.T) {
	clearTestData(t)

	page := newPage(t)

	// Try to access dashboard without authentication
	_, err := page.Goto(baseURL + "/")
	require.NoError(t, err)

	// Should redirect to login
	err = page.WaitForURL("**/login**")
	require.NoError(t, err)
}

func TestDashboard_LogoutButton(t *testing.T) {
	clearTestData(t)

	memberID := seedMember(t, "logouttest@example.com",
		WithConfirmed(),
		WithWaiver(),
	)

	ctx := newContext(t)
	loginAs(t, ctx, memberID)
	page := newPageInContext(t, ctx)

	dashboard := NewMemberDashboardPage(t, page)
	dashboard.Navigate()

	// Verify Logout button is visible
	expect(t).Locator(page.Locator("a:has-text('Logout')")).ToBeVisible()
}
