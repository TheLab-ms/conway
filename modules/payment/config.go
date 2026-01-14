package payment

import (
	"github.com/TheLab-ms/conway/engine/config"
)

// Config holds Stripe-related configuration.
type Config struct {
	APIKey     string `json:"api_key" config:"label=Secret Key,secret,section=api,help=The API secret key (starts with <code>sk_test_</code> or <code>sk_live_</code>)."`
	WebhookKey string `json:"webhook_key" config:"label=Webhook Signing Secret,secret,section=webhook,help=The webhook signing secret (starts with <code>whsec_</code>). Used to verify webhook authenticity."`
}

// ConfigSpec returns the Stripe configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "stripe",
		Title:       "Stripe Integration",
		Description: `<strong>How Stripe Integration Works</strong>
<ul class="mb-0 mt-2">
	<li><strong>Subscriptions:</strong> Members pay for membership via Stripe Checkout. Conway receives webhooks when subscription status changes.</li>
	<li><strong>Billing Portal:</strong> Active members can manage their subscription (update payment method, cancel) via Stripe's billing portal.</li>
	<li><strong>Discounts:</strong> Coupons configured in Stripe with matching <code>discountTypes</code> metadata are automatically applied.</li>
</ul>`,
		Type: Config{},
		Sections: []config.SectionDef{
			{
				Name:        "api",
				Title:       "API Configuration",
				Description: `Get your API keys from the <a href="https://dashboard.stripe.com/apikeys" target="_blank" rel="noopener">Stripe Dashboard</a>. Use test keys for development and live keys for production.`,
			},
			{
				Name:        "webhook",
				Title:       "Webhook Configuration",
				Description: `Create a webhook endpoint in the <a href="https://dashboard.stripe.com/webhooks" target="_blank" rel="noopener">Stripe Dashboard</a>. Subscribe to events: <code>customer.subscription.created</code>, <code>customer.subscription.updated</code>, <code>customer.subscription.deleted</code>`,
			},
		},
		Order: 20,
	}
}
