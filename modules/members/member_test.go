package members

import (
	"testing"

	"github.com/TheLab-ms/conway/engine/testutil"
)

func TestRenderMember(t *testing.T) {
	tests := []struct {
		name        string
		member      *member
		fixtureName string
		description string
	}{
		{
			name: "active_member",
			member: &member{
				ID:            123,
				AccessStatus:  "Ready",
				DiscordLinked: true,
				Email:         "user@example.com",
			},
			fixtureName: "_active",
			description: "Active member with Discord linked",
		},
		{
			name: "member_no_discord",
			member: &member{
				ID:            456,
				AccessStatus:  "Ready",
				DiscordLinked: false,
				Email:         "user2@example.com",
			},
			fixtureName: "_no_discord",
			description: "Active member without Discord linked",
		},
		{
			name: "missing_waiver",
			member: &member{
				ID:            789,
				AccessStatus:  "MissingWaiver",
				DiscordLinked: false,
				Email:         "user3@example.com",
			},
			fixtureName: "_missing_waiver",
			description: "Member missing liability waiver",
		},
		{
			name: "missing_keyfob",
			member: &member{
				ID:            101,
				AccessStatus:  "MissingKeyFob",
				DiscordLinked: true,
				Email:         "user4@example.com",
			},
			fixtureName: "_missing_keyfob",
			description: "Member missing key fob",
		},
		{
			name: "family_inactive",
			member: &member{
				ID:            102,
				AccessStatus:  "FamilyInactive",
				DiscordLinked: false,
				Email:         "family@example.com",
			},
			fixtureName: "_family_inactive",
			description: "Family member with inactive root account",
		},
		{
			name: "payment_inactive",
			member: &member{
				ID:            103,
				AccessStatus:  "PaymentInactive",
				DiscordLinked: true,
				Email:         "payment@example.com",
			},
			fixtureName: "_payment_inactive",
			description: "Member with inactive payment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderMember(tt.member)
			testutil.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}

func TestRenderMembershipStatus(t *testing.T) {
	tests := []struct {
		name        string
		member      *member
		fixtureName string
		description string
	}{
		{
			name: "default_active",
			member: &member{
				ID:           123,
				AccessStatus: "Ready",
				Email:        "user@example.com",
			},
			fixtureName: "_default_status",
			description: "Default case - active member",
		},
		{
			name: "unknown_status",
			member: &member{
				ID:           123,
				AccessStatus: "SomeUnknownStatus",
				Email:        "user@example.com",
			},
			fixtureName: "_unknown_status",
			description: "Unknown status should use default case",
		},
		{
			name: "missing_waiver_status",
			member: &member{
				ID:           456,
				AccessStatus: "MissingWaiver",
				Email:        "waiver@example.com",
			},
			fixtureName: "_waiver_status",
			description: "Missing waiver status with link to waiver form",
		},
		{
			name: "missing_keyfob_status",
			member: &member{
				ID:           789,
				AccessStatus: "MissingKeyFob",
				Email:        "keyfob@example.com",
			},
			fixtureName: "_keyfob_status",
			description: "Missing key fob status",
		},
		{
			name: "family_inactive_status",
			member: &member{
				ID:           101,
				AccessStatus: "FamilyInactive",
				Email:        "family@example.com",
			},
			fixtureName: "_family_status",
			description: "Family inactive status",
		},
		{
			name: "payment_inactive_status",
			member: &member{
				ID:           102,
				AccessStatus: "PaymentInactive",
				Email:        "payment@example.com",
			},
			fixtureName: "_payment_status",
			description: "Payment inactive status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderMembershipStatus(tt.member)
			testutil.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}
