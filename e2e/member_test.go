package e2e

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDashboard_OnboardingStates(t *testing.T) {
	tests := []struct {
		name       string
		opts       []MemberOption
		expectFunc func(*testing.T, *MemberDashboardPage)
	}{
		{
			name: "missing_waiver",
			opts: []MemberOption{WithConfirmed()},
			expectFunc: func(t *testing.T, d *MemberDashboardPage) {
				d.ExpectMissingWaiverAlert()
			},
		},
		{
			name: "missing_payment",
			opts: []MemberOption{WithConfirmed(), WithWaiver()},
			expectFunc: func(t *testing.T, d *MemberDashboardPage) {
				d.ExpectMissingPaymentAlert()
				d.ExpectStepComplete("Sign Liability Waiver")
				d.ExpectStepPending("Get Your Key Fob")
			},
		},
		{
			name: "missing_keyfob",
			opts: []MemberOption{WithConfirmed(), WithWaiver(), WithActiveStripeSubscription()},
			expectFunc: func(t *testing.T, d *MemberDashboardPage) {
				d.ExpectMissingKeyFobAlert()
				d.ExpectStepComplete("Sign Liability Waiver")
				d.ExpectStepComplete("Set Up Payment")
			},
		},
		{
			name: "fully_active",
			opts: []MemberOption{WithConfirmed(), WithWaiver(), WithActiveStripeSubscription(), WithFobID(12345)},
			expectFunc: func(t *testing.T, d *MemberDashboardPage) {
				d.ExpectActiveStatus()
				d.ExpectOnboardingChecklist()
				d.ExpectStepComplete("Sign Liability Waiver")
				d.ExpectStepComplete("Set Up Payment")
				d.ExpectStepComplete("Get Your Key Fob")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, page := setupMemberTest(t, tc.name+"@example.com", tc.opts...)
			dashboard := NewMemberDashboardPage(t, page)
			dashboard.Navigate()
			tc.expectFunc(t, dashboard)
		})
	}
}

func TestDashboard_DiscordLinking(t *testing.T) {
	t.Run("shows_link_button_when_not_linked", func(t *testing.T) {
		_, page := setupMemberTest(t, "nodiscord@example.com",
			WithConfirmed(),
			WithWaiver(),
			WithActiveStripeSubscription(),
			WithFobID(12345),
		)
		dashboard := NewMemberDashboardPage(t, page)
		dashboard.Navigate()
		expect(t).Locator(page.Locator("a:has-text('Link Discord')")).ToBeVisible()
	})

	t.Run("hides_link_button_when_linked", func(t *testing.T) {
		_, page := setupMemberTest(t, "hasdiscord@example.com",
			WithConfirmed(),
			WithWaiver(),
			WithActiveStripeSubscription(),
			WithFobID(12345),
			WithDiscord("123456789"),
		)
		dashboard := NewMemberDashboardPage(t, page)
		dashboard.Navigate()
		expect(t).Locator(page.Locator("a:has-text('Link Discord')")).ToBeHidden()
	})
}

func TestDashboard_RequiresAuthentication(t *testing.T) {
	page := setupUnauthenticatedTest(t)

	_, err := page.Goto(baseURL + "/")
	require.NoError(t, err)

	err = page.WaitForURL("**/login**")
	require.NoError(t, err)
}
