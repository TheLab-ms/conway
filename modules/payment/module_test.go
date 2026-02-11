package payment

import (
	"testing"

	"github.com/stripe/stripe-go/v78"
)

func TestNormalizeSubscriptionStatus(t *testing.T) {
	tests := []struct {
		name   string
		input  stripe.SubscriptionStatus
		expect stripe.SubscriptionStatus
	}{
		{"trialing becomes active", stripe.SubscriptionStatusTrialing, stripe.SubscriptionStatusActive},
		{"active stays active", stripe.SubscriptionStatusActive, stripe.SubscriptionStatusActive},
		{"canceled stays canceled", stripe.SubscriptionStatusCanceled, stripe.SubscriptionStatusCanceled},
		{"past_due stays past_due", stripe.SubscriptionStatusPastDue, stripe.SubscriptionStatusPastDue},
		{"unpaid stays unpaid", stripe.SubscriptionStatusUnpaid, stripe.SubscriptionStatusUnpaid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeSubscriptionStatus(tt.input)
			if got != tt.expect {
				t.Errorf("normalizeSubscriptionStatus(%q) = %q, want %q", tt.input, got, tt.expect)
			}
		})
	}
}
