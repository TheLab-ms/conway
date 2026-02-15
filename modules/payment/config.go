package payment

import "github.com/TheLab-ms/conway/engine/config"

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
		Description: configDescription(),
		Type:        Config{},
		Sections: []config.SectionDef{
			{
				Name:        "api",
				Title:       "API Configuration",
				Description: apiSectionDescription(),
			},
			{
				Name:        "webhook",
				Title:       "Webhook Configuration",
				Description: webhookSectionDescription(m.self.String()),
			},
		},
		Order: 20,
	}
}
