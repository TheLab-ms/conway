package discord

import (
	"fmt"

	"github.com/TheLab-ms/conway/engine/config"
)

// Config holds Discord-related configuration.
type Config struct {
	// OAuth2 Configuration
	ClientID     string `json:"client_id" config:"label=Client ID,section=oauth,help=The Application ID from Discord Developer Portal."`
	ClientSecret string `json:"client_secret" config:"label=Client Secret,secret,section=oauth,help=Keep this confidential."`

	// Bot Configuration
	BotToken string `json:"bot_token" config:"label=Bot Token,secret,section=bot"`
	GuildID  string `json:"guild_id" config:"label=Server (Guild) ID,section=bot,help=Right-click your server name in Discord (with Developer Mode enabled) and select \"Copy Server ID\"."`
	RoleID   string `json:"role_id" config:"label=Member Role ID,section=bot,help=The role to assign to paying members. Right-click the role in Server Settings > Roles and select \"Copy Role ID\"."`

	// Notifications
	PrintWebhookURL  string `json:"print_webhook_url" config:"label=3D Print Notification Webhook URL,secret,section=webhooks,help=Webhook URL for 3D printer completion and failure notifications."`
	SignupWebhookURL string `json:"signup_webhook_url" config:"label=New Member Signup Webhook URL,secret,section=webhooks,help=Webhook URL to notify when a new member signs up."`

	// Message Templates (Go text/template syntax)
	SignupMessageTemplate         string `json:"signup_message_template" config:"label=Signup Message Template,section=templates,multiline,rows=3,help=Template for new member signup notifications. Available fields: {{.Email}} {{.MemberID}}"`
	PrintCompletedMessageTemplate string `json:"print_completed_message_template" config:"label=Print Completed Template,section=templates,multiline,rows=3,help=Template for print completion notifications. Available fields: {{.Mention}} {{.PrinterName}} {{.FileName}}"`
	PrintFailedMessageTemplate    string `json:"print_failed_message_template" config:"label=Print Failed Template,section=templates,multiline,rows=3,help=Template for print failure notifications. Available fields: {{.Mention}} {{.PrinterName}} {{.FileName}} {{.ErrorCode}}"`

	// Sync Settings
	SyncIntervalHours int `json:"sync_interval_hours" config:"label=Full Reconciliation Interval (hours),section=sync,default=24,min=1,max=168,help=How often to fully reconcile all Discord role assignments. Default: 24 hours."`
}

// Default message templates used when no custom template is configured.
const (
	DefaultSignupTemplate         = `New member signed up: **{{.Email}}** (member ID: {{.MemberID}})`
	DefaultPrintCompletedTemplate = `{{.Mention}}: your print has completed successfully on {{.PrinterName}}.`
	DefaultPrintFailedTemplate    = `{{if .Mention}}{{.Mention}}: your{{else}}A{{end}} print on {{.PrinterName}} has failed with error code: {{.ErrorCode}}.`
)

// SignupTemplate returns the configured signup template or the default.
func (c *Config) SignupTemplate() string {
	if c.SignupMessageTemplate != "" {
		return c.SignupMessageTemplate
	}
	return DefaultSignupTemplate
}

// PrintCompletedTemplate returns the configured print completed template or the default.
func (c *Config) PrintCompletedTmpl() string {
	if c.PrintCompletedMessageTemplate != "" {
		return c.PrintCompletedMessageTemplate
	}
	return DefaultPrintCompletedTemplate
}

// PrintFailedTemplate returns the configured print failed template or the default.
func (c *Config) PrintFailedTmpl() string {
	if c.PrintFailedMessageTemplate != "" {
		return c.PrintFailedMessageTemplate
	}
	return DefaultPrintFailedTemplate
}

// Validate validates the Discord configuration.
func (c *Config) Validate() error {
	if c.SyncIntervalHours < 1 {
		return fmt.Errorf("sync interval must be at least 1 hour")
	}
	if c.SyncIntervalHours > 168 {
		return fmt.Errorf("sync interval cannot exceed 168 hours (1 week)")
	}
	return nil
}

// ConfigSpec returns the Discord configuration specification.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "discord",
		Title:       "Discord Integration",
		Description: configDescription(),
		Type:        Config{},
		Sections: []config.SectionDef{
			{
				Name:        "oauth",
				Title:       "OAuth2 Configuration",
				Description: oauthSectionDescription(m.self.String()),
			},
			{
				Name:        "bot",
				Title:       "Bot Configuration",
				Description: botSectionDescription(),
			},
			{
				Name:        "webhooks",
				Title:       "Notifications",
				Description: webhooksSectionDescription(),
			},
			{
				Name:        "templates",
				Title:       "Message Templates",
				Description: templatesSectionDescription(),
			},
			{
				Name:  "sync",
				Title: "Sync Settings",
			},
		},
		Order: 10,
	}
}
