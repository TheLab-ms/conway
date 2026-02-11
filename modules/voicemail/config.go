package voicemail

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/TheLab-ms/conway/engine/config"
)

// Config holds Twilio voicemail configuration.
type Config struct {
	AccountSID           string `json:"account_sid" config:"label=Account SID,section=api,help=Your Twilio Account SID (starts with AC)."`
	AuthToken            string `json:"auth_token" config:"label=Auth Token,secret,section=api,help=Used for webhook signature validation and API calls to download recordings."`
	GreetingText         string `json:"greeting_text" config:"label=Greeting Message,section=voicemail,default=Hello you have reached the makerspace. Please leave a message after the beep.,help=Text-to-speech greeting callers hear before recording."`
	LeadershipWebhookURL string `json:"leadership_webhook_url" config:"label=Leadership Discord Webhook URL,secret,section=notifications,help=Discord webhook URL for the leadership channel. New voicemail notifications will be posted here."`
}

// ConfigSpec returns the Twilio voicemail configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module: "twilio",
		Title:  "Twilio Voice / Voicemail",
		Description: `<strong>How Twilio Voicemail Works</strong>
<ul class="mb-0 mt-2">
	<li><strong>Inbound Calls:</strong> Twilio sends a webhook when a call arrives. Conway plays a greeting and records a voicemail (up to 60 seconds).</li>
	<li><strong>Recording:</strong> When the recording is ready, Conway downloads it, stores it locally, and deletes it from Twilio.</li>
	<li><strong>Notifications:</strong> A Discord message is sent to the leadership channel for each new voicemail.</li>
	<li><strong>Retention:</strong> Recordings are automatically deleted after 30 days.</li>
</ul>`,
		Type: Config{},
		Sections: []config.SectionDef{
			{
				Name:        "api",
				Title:       "Twilio API Credentials",
				Description: `Get your credentials from the <a href="https://console.twilio.com/" target="_blank" rel="noopener">Twilio Console</a>.`,
			},
			{
				Name:        "voicemail",
				Title:       "Voicemail Settings",
			},
			{
				Name:        "notifications",
				Title:       "Notifications",
				Description: `Configure Discord notifications for new voicemails. Create a webhook in Discord: Channel Settings &gt; Integrations &gt; Webhooks.`,
			},
		},
		Order: 30,
	}
}

// twilioConfig holds the loaded configuration values.
type twilioConfig struct {
	accountSID           string
	authToken            string
	greetingText         string
	leadershipWebhookURL string
}

// loadConfig loads the current Twilio configuration from the database.
func (m *Module) loadConfig(ctx context.Context) (*twilioConfig, error) {
	row := m.db.QueryRowContext(ctx,
		`SELECT account_sid, auth_token, greeting_text, leadership_webhook_url
		 FROM twilio_config ORDER BY version DESC LIMIT 1`)

	cfg := &twilioConfig{}
	err := row.Scan(&cfg.accountSID, &cfg.authToken, &cfg.greetingText, &cfg.leadershipWebhookURL)
	if err == sql.ErrNoRows {
		return &twilioConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading twilio config: %w", err)
	}
	return cfg, nil
}
