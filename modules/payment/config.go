package payment

import "github.com/TheLab-ms/conway/engine/config"

// DonationItem represents a configurable donation option that maps to a Stripe Price.
type DonationItem struct {
	Name    string `json:"name" config:"label=Name,placeholder=e.g. 3D Printing Materials,required"`
	PriceID string `json:"price_id" config:"label=Stripe Price ID,placeholder=price_...,required"`
}

// Config holds Stripe-related configuration.
type Config struct {
	APIKey        string         `json:"api_key" config:"label=Secret Key,secret,section=api,help=The API secret key (starts with <code>sk_test_</code> or <code>sk_live_</code>)."`
	WebhookKey    string         `json:"webhook_key" config:"label=Webhook Signing Secret,secret,section=webhook,help=The webhook signing secret (starts with <code>whsec_</code>). Used to verify webhook authenticity."`
	DonationItems []DonationItem `json:"donation_items" config:"label=Donation Items,item=Donation Item,key=Name"`
}

// ConfigSpec returns the Stripe configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "stripe",
		Title:       "Stripe",
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
		ArrayFields: []config.ArrayFieldDef{
			{
				FieldName: "DonationItems",
				Label:     "Donation Items",
				ItemLabel: "Donation Item",
				Help:      "Configure donation options that members can choose from. Each item must reference an existing Stripe Price ID.",
				KeyField:  "Name",
			},
		},
		Order:    20,
		Category: "Integrations",
	}
}
