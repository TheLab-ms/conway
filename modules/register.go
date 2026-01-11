// Package modules provides shared module registration for Conway.
package modules

import (
	"database/sql"
	"net/url"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/admin"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/TheLab-ms/conway/modules/discord"
	"github.com/TheLab-ms/conway/modules/discordwebhook"
	"github.com/TheLab-ms/conway/modules/email"
	"github.com/TheLab-ms/conway/modules/fobapi"
	gac "github.com/TheLab-ms/conway/modules/generic-access-controller"
	"github.com/TheLab-ms/conway/modules/kiosk"
	"github.com/TheLab-ms/conway/modules/machines"
	"github.com/TheLab-ms/conway/modules/members"
	"github.com/TheLab-ms/conway/modules/metrics"
	"github.com/TheLab-ms/conway/modules/oauth2"
	"github.com/TheLab-ms/conway/modules/payment"
	"github.com/TheLab-ms/conway/modules/waiver"
)

// Options configures module registration.
type Options struct {
	Database *sql.DB
	Self     *url.URL

	// Token issuers
	AuthIssuer    *engine.TokenIssuer
	OAuthIssuer   *engine.TokenIssuer
	FobIssuer     *engine.TokenIssuer
	DiscordIssuer *engine.TokenIssuer

	// Auth options
	Turnstile *auth.TurnstileOptions

	// Email sender (nil to disable sending)
	EmailSender email.Sender

	// Payment config
	StripeWebhookKey string

	// Kiosk config
	SpaceHost string

	// Machines config (nil disables the module)
	MachinesModule *machines.Module

	// Discord Webhook module (nil disables the module)
	DiscordWebhookModule *discordwebhook.Module

	// Generic Access Controller config (empty disables the module)
	AccessControllerHost string

	// Discord config (empty ClientID disables the module)
	DiscordClientID     string
	DiscordClientSecret string
	DiscordBotToken     string
	DiscordGuildID      string
	DiscordRoleID       string
}

// Register adds all modules to the app and returns the auth module
// (which must be set as the router's authenticator by the caller).
func Register(a *engine.App, opts Options) *auth.Module {
	// Members module must be registered first to apply base schema
	// before other modules attempt to use the members tables.
	a.Add(members.New(opts.Database))

	authModule := auth.New(opts.Database, opts.Self, opts.Turnstile, opts.AuthIssuer)
	a.Add(authModule)
	a.Router.Authenticator = authModule // Must set before adding modules that use WithAuthn

	a.Add(email.New(opts.Database, opts.EmailSender))
	a.Add(oauth2.New(opts.Database, opts.Self, opts.OAuthIssuer))
	a.Add(payment.New(opts.Database, opts.StripeWebhookKey, opts.Self))
	a.Add(admin.New(opts.Database, opts.Self, opts.AuthIssuer))
	a.Add(waiver.New(opts.Database))
	a.Add(kiosk.New(opts.Database, opts.Self, opts.FobIssuer, opts.SpaceHost))
	a.Add(metrics.New(opts.Database))
	a.Add(fobapi.New(opts.Database))

	if opts.MachinesModule != nil {
		a.Add(opts.MachinesModule)
	}

	if opts.DiscordWebhookModule != nil {
		a.Add(opts.DiscordWebhookModule)
	}

	if opts.AccessControllerHost != "" {
		a.Add(gac.New(opts.Database, opts.AccessControllerHost))
	}

	if opts.DiscordClientID != "" {
		a.Add(discord.New(
			opts.Database,
			opts.Self,
			opts.DiscordIssuer,
			opts.DiscordClientID,
			opts.DiscordClientSecret,
			opts.DiscordBotToken,
			opts.DiscordGuildID,
			opts.DiscordRoleID,
		))
	}

	return authModule
}
