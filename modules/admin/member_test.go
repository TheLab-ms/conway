package admin

import (
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/testutil"
)

func TestRenderSingleMember(t *testing.T) {
	// Helper function to create pointers
	strPtr := func(s string) *string { return &s }
	int64Ptr := func(i int64) *int64 { return &i }
	float64Ptr := func(f float64) *float64 { return &f }

	// Helper to create a member with required fields set to safe defaults
	createMember := func() *member {
		return &member{
			FobID:           int64Ptr(0),
			RootFamilyEmail: strPtr(""),
			DiscordUserID:   "",
		}
	}

	// Base time for consistent test results
	baseTime := time.Date(2023, 6, 15, 12, 0, 0, 0, time.UTC)
	recentTime := baseTime.Add(-2 * time.Hour)
	oldTime := baseTime.Add(-30 * 24 * time.Hour)

	tabs := []*navbarTab{
		{Title: "Members", Path: "/admin/members"},
		{Title: "Events", Path: "/admin/events"},
	}

	tests := []struct {
		name        string
		member      *member
		events      []*memberEvent
		fixtureName string
		description string
	}{
		{
			name: "minimal_member",
			member: func() *member {
				m := createMember()
				m.ID = 123
				m.AccessStatus = "Ready"
				m.Name = "John Doe"
				m.Email = "john@example.com"
				m.Confirmed = true
				m.Created = engine.LocalTime{Time: baseTime}
				return m
			}(),
			events:      []*memberEvent{},
			fixtureName: "_minimal",
			description: "Minimal member with basic required fields only",
		},
		{
			name: "complete_member_active",
			member: func() *member {
				m := createMember()
				m.ID = 456
				m.AccessStatus = "Ready"
				m.Name = "Jane Smith"
				m.Email = "jane@example.com"
				m.Confirmed = true
				m.Created = engine.LocalTime{Time: baseTime}
				m.AdminNotes = "VIP member, longtime contributor"
				m.Leadership = true
				m.NonBillable = false
				m.FobID = int64Ptr(12345)
				m.StripeSubID = strPtr("sub_1234567890")
				m.StripeStatus = strPtr("active")
				m.DiscountType = strPtr("military")
				m.BillAnnually = true
				m.FobLastSeen = &engine.LocalTime{Time: recentTime}
				m.DiscordUserID = "123456789012345678"
				return m
			}(),
			events: []*memberEvent{
				{
					Created: baseTime.Add(-1 * time.Hour),
					Event:   "MembershipActivated",
					Details: "Membership activated via Stripe",
				},
				{
					Created: baseTime.Add(-2 * time.Hour),
					Event:   "PaymentReceived",
					Details: "Monthly payment processed",
				},
			},
			fixtureName: "_complete_active",
			description: "Complete active member with all fields populated",
		},
		{
			name: "inactive_member",
			member: &member{
				ID:              789,
				AccessStatus:    "PaymentInactive",
				Name:            "Bob Johnson",
				Email:           "bob@example.com",
				Confirmed:       false,
				Created:         engine.LocalTime{Time: oldTime},
				AdminNotes:      "Payment issues, needs follow up",
				Leadership:      false,
				NonBillable:     false,
				FobID:           int64Ptr(54321),
				StripeSubID:     nil,
				StripeStatus:    nil,
				PaypalSubID:     nil,
				PaypalPrice:     nil,
				DiscountType:    nil,
				RootFamilyEmail: strPtr(""),
				BillAnnually:    false,
				FobLastSeen:     &engine.LocalTime{Time: oldTime},
				DiscordUserID:   "",
			},
			events: []*memberEvent{
				{
					Created: baseTime.Add(-24 * time.Hour),
					Event:   "PaymentFailed",
					Details: "Credit card declined",
				},
			},
			fixtureName: "_inactive",
			description: "Inactive member with payment issues",
		},
		{
			name: "stripe_unknown_status",
			member: &member{
				ID:              101,
				AccessStatus:    "Ready",
				Name:            "Alice Wilson",
				Email:           "alice@example.com",
				Confirmed:       true,
				Created:         engine.LocalTime{Time: baseTime},
				FobID:           int64Ptr(11111),
				StripeSubID:     strPtr("sub_unknown"),
				StripeStatus:    nil, // nil status should show "unknown"
				PaypalSubID:     nil,
				PaypalPrice:     nil,
				DiscountType:    nil,
				RootFamilyEmail: strPtr(""),
				BillAnnually:    false,
				FobLastSeen:     nil,
				DiscordUserID:   "",
			},
			events:      []*memberEvent{},
			fixtureName: "_stripe_unknown",
			description: "Member with Stripe subscription but unknown status",
		},
		{
			name: "stripe_empty_status",
			member: func() *member {
				m := createMember()
				m.ID = 102
				m.AccessStatus = "Ready"
				m.Name = "Charlie Brown"
				m.Email = "charlie@example.com"
				m.Confirmed = true
				m.Created = engine.LocalTime{Time: baseTime}
				m.FobID = int64Ptr(22222)
				m.StripeSubID = strPtr("sub_empty")
				m.StripeStatus = strPtr("") // empty status should show "unknown"
				return m
			}(),
			events:      []*memberEvent{},
			fixtureName: "_stripe_empty",
			description: "Member with empty Stripe status",
		},
		{
			name: "paypal_member",
			member: func() *member {
				m := createMember()
				m.ID = 103
				m.AccessStatus = "Ready"
				m.Name = "David Davis"
				m.Email = "david@example.com"
				m.Confirmed = true
				m.Created = engine.LocalTime{Time: baseTime}
				m.FobID = int64Ptr(33333)
				m.PaypalSubID = strPtr("I-PAYPAL123456")
				m.PaypalPrice = float64Ptr(75.00)
				return m
			}(),
			events:      []*memberEvent{},
			fixtureName: "_paypal",
			description: "Member with PayPal subscription",
		},
		{
			name: "family_discount",
			member: func() *member {
				m := createMember()
				m.ID = 104
				m.AccessStatus = "Ready"
				m.Name = "Emma Evans"
				m.Email = "emma@example.com"
				m.Confirmed = true
				m.Created = engine.LocalTime{Time: baseTime}
				m.FobID = int64Ptr(44444)
				m.DiscountType = strPtr("family")
				m.RootFamilyEmail = strPtr("family-root@example.com")
				return m
			}(),
			events:      []*memberEvent{},
			fixtureName: "_family_discount",
			description: "Member with family discount",
		},
		{
			name: "all_discounts",
			member: func() *member {
				m := createMember()
				m.ID = 105
				m.AccessStatus = "Ready"
				m.Name = "Frank Foster"
				m.Email = "frank@example.com"
				m.Confirmed = true
				m.Created = engine.LocalTime{Time: baseTime}
				m.FobID = int64Ptr(55555)
				m.DiscountType = strPtr("educator")
				return m
			}(),
			events:      []*memberEvent{},
			fixtureName: "_educator_discount",
			description: "Member with educator discount",
		},
		{
			name: "non_billable_leadership",
			member: func() *member {
				m := createMember()
				m.ID = 106
				m.AccessStatus = "Ready"
				m.Name = "Grace Green"
				m.Email = "grace@example.com"
				m.Confirmed = true
				m.Created = engine.LocalTime{Time: baseTime}
				m.AdminNotes = "Board member, lifetime access"
				m.Leadership = true
				m.NonBillable = true
				m.FobID = int64Ptr(66666)
				return m
			}(),
			events:      []*memberEvent{},
			fixtureName: "_leadership_nonbillable",
			description: "Leadership member with non-billable status",
		},
		{
			name: "many_events",
			member: func() *member {
				m := createMember()
				m.ID = 107
				m.AccessStatus = "Ready"
				m.Name = "Henry Hill"
				m.Email = "henry@example.com"
				m.Confirmed = true
				m.Created = engine.LocalTime{Time: baseTime}
				m.FobID = int64Ptr(77777)
				return m
			}(),
			events: []*memberEvent{
				{Created: baseTime.Add(-1 * time.Hour), Event: "Event1", Details: "Details1"},
				{Created: baseTime.Add(-2 * time.Hour), Event: "Event2", Details: "Details2"},
				{Created: baseTime.Add(-3 * time.Hour), Event: "Event3", Details: "Details3"},
				{Created: baseTime.Add(-4 * time.Hour), Event: "Event4", Details: "Details4"},
				{Created: baseTime.Add(-5 * time.Hour), Event: "Event5", Details: "Details5"},
				{Created: baseTime.Add(-6 * time.Hour), Event: "Event6", Details: "Details6"},
				{Created: baseTime.Add(-7 * time.Hour), Event: "Event7", Details: "Details7"},
				{Created: baseTime.Add(-8 * time.Hour), Event: "Event8", Details: "Details8"},
				{Created: baseTime.Add(-9 * time.Hour), Event: "Event9", Details: "Details9"},
				{Created: baseTime.Add(-10 * time.Hour), Event: "Event10", Details: "Details10"},
				{Created: baseTime.Add(-11 * time.Hour), Event: "Event11", Details: "Details11"}, // This should trigger the "more than 10" message
			},
			fixtureName: "_many_events",
			description: "Member with more than 10 events",
		},
		{
			name: "stripe_inactive_no_sub",
			member: func() *member {
				m := createMember()
				m.ID = 108
				m.AccessStatus = "Ready"
				m.Name = "Ivy Johnson"
				m.Email = "ivy@example.com"
				m.Confirmed = true
				m.Created = engine.LocalTime{Time: baseTime}
				m.FobID = int64Ptr(88888)
				m.StripeSubID = nil // No Stripe subscription
				m.StripeStatus = strPtr("canceled")
				return m
			}(),
			events:      []*memberEvent{},
			fixtureName: "_stripe_no_sub",
			description: "Member with no Stripe subscription ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := renderSingleMember(tabs, tt.member, tt.events)
			testutil.RenderSnapshotWithName(t, component, tt.fixtureName)
		})
	}
}
