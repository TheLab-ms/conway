package twilio

import (
	"fmt"

	"github.com/TheLab-ms/conway/engine/config"
)

// Config is the persisted Twilio configuration.
//
// AccountSid + AuthToken come from the Twilio console
// (https://console.twilio.com). The auth token is used both as the basic-auth
// password for downloading recordings and as the HMAC-SHA1 key Twilio uses
// to sign webhook requests, so it MUST be kept secret.
type Config struct {
	AccountSid string `json:"account_sid" config:"label=Account SID,section=auth,help=From the Twilio console (Account Info). Starts with 'AC'."`
	AuthToken  string `json:"auth_token" config:"label=Auth Token,secret,section=auth,help=From the Twilio console. Used to verify incoming webhook signatures and to download recordings."`

	VoiceGreeting        string `json:"voice_greeting" config:"label=Voicemail Greeting,type=textarea,rows=3,section=voice,help=Spoken to callers before recording. Plain text — Twilio's text-to-speech reads it aloud."`
	TranscriptionEnabled bool   `json:"transcription_enabled" config:"label=Enable Transcription,section=voice,help=Twilio will attempt to transcribe each voicemail. Costs a small amount per message."`

	RetentionDays int `json:"retention_days" config:"label=Retention (days),section=retention,default=30,min=1,max=1825,help=Messages and recordings are permanently deleted after this many days."`
}

// Validate checks the configuration before saving.
func (c *Config) Validate() error {
	if c.RetentionDays < 1 || c.RetentionDays > maxRetentionDays {
		return fmt.Errorf("retention must be between 1 and %d days", maxRetentionDays)
	}
	if c.AccountSid != "" && len(c.AccountSid) < 10 {
		return fmt.Errorf("account SID looks invalid")
	}
	return nil
}

// ConfigSpec describes the Twilio configuration page in the admin UI.
func (m *Module) ConfigSpec() config.Spec {
	selfURL := ""
	if m.self != nil {
		selfURL = m.self.String()
	}
	return config.Spec{
		Module:      "twilio",
		Title:       "Twilio",
		Description: configDescription(),
		Type:        Config{},
		Sections: []config.SectionDef{
			{
				Name:        "auth",
				Title:       "Twilio Account",
				Description: authSectionDescription(),
			},
			{
				Name:        "voice",
				Title:       "Voice / Voicemail",
				Description: voiceSectionDescription(selfURL),
			},
			{
				Name:        "retention",
				Title:       "Message Retention",
				Description: retentionSectionDescription(),
			},
		},
		Order:    20,
		Category: "Integrations",
	}
}
