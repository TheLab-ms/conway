// Package modules provides shared module registration for Conway.
package modules

import (
	"database/sql"
	"net/url"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/admin"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/TheLab-ms/conway/modules/directory"
	"github.com/TheLab-ms/conway/modules/discord"
	"github.com/TheLab-ms/conway/modules/discordwebhook"
	"github.com/TheLab-ms/conway/modules/email"
	"github.com/TheLab-ms/conway/modules/fobapi"
	"github.com/TheLab-ms/conway/modules/google"
	"github.com/TheLab-ms/conway/modules/kiosk"
	"github.com/TheLab-ms/conway/modules/machines"
	"github.com/TheLab-ms/conway/modules/members"
	"github.com/TheLab-ms/conway/modules/metrics"
	"github.com/TheLab-ms/conway/modules/oauth2"
	"github.com/TheLab-ms/conway/modules/payment"
	"github.com/TheLab-ms/conway/modules/signs"
	"github.com/TheLab-ms/conway/modules/triggers"
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
	GoogleIssuer  *engine.TokenIssuer

	// Auth options
	Turnstile *auth.TurnstileOptions

	// Email sender (nil to disable sending)
	EmailSender email.Sender

	// Kiosk config
	SpaceHost string

	// Signs module: optional injectable printer (for tests). nil => IPP from config.
	SignsPrinter signs.Printer
	// OnSignsModule, if set, is called with the signs module after registration.
	// This is a TEST-ONLY hook: production code should not need access to
	// internal module handles. Used by e2e tests to drive ProcessOne.
	OnSignsModule func(*signs.Module)

	// OnEmailModule, if set, is called with the email module after registration.
	// TEST-ONLY hook used to drive the workqueue manually and swap the Sender.
	OnEmailModule func(*email.Module)

	// OnMachinesModule, if set, is called with the machines module after registration.
	// TEST-ONLY hook used to inject test stream sources.
	OnMachinesModule func(*machines.Module)
}

// Register adds all modules to the app and returns the auth module
// (which must be set as the router's authenticator by the caller).
func Register(a *engine.App, opts Options) *auth.Module {
	// Auth module must be created and set as authenticator FIRST,
	// before any modules that use WithAuthn are added.
	authModule := auth.New(opts.Database, opts.Self, opts.Turnstile, opts.AuthIssuer)
	a.Router.Authenticator = authModule

	// Members module registered early to apply base schema
	// before other modules attempt to use the members tables.
	a.Add(members.New(opts.Database))

	// Now add auth routes
	a.Add(authModule)

	emailMod := email.New(opts.Database, opts.EmailSender)
	a.Add(emailMod)
	if opts.OnEmailModule != nil {
		opts.OnEmailModule(emailMod)
	}
	a.Add(oauth2.New(opts.Database, opts.Self, opts.OAuthIssuer))
	a.Add(payment.New(opts.Database, opts.Self, engine.NewEventLogger(opts.Database, "stripe")))
	a.Add(waiver.New(opts.Database))
	a.Add(kiosk.New(opts.Database, opts.Self, opts.FobIssuer, opts.SpaceHost))
	a.Add(metrics.New(opts.Database))
	a.Add(fobapi.New(opts.Database, opts.Self))
	a.Add(directory.New(opts.Database))

	// Discord modules registered before machines, since the machines
	// migration creates triggers that reference discord tables.
	webhookSender := discordwebhook.NewHTTPSender()
	discordWebhookMod := discordwebhook.New(opts.Database, webhookSender)
	a.Add(discordWebhookMod)

	discordMod := discord.New(opts.Database, opts.Self, opts.DiscordIssuer, engine.NewEventLogger(opts.Database, "discord"))
	discordMod.SetLoginCompleter(authModule.CompleteLoginForMember)
	discordMod.SetSignupConfirm(authModule.RenderSignupConfirmation)
	discordMod.SetConfigLoader(a.ConfigStore())
	authModule.DiscordLoginEnabled = discordMod.IsLoginEnabled
	a.Add(discordMod)

	machinesMod := machines.New(opts.Database, engine.NewEventLogger(opts.Database, "bambu"))

	a.Add(machinesMod)
	machinesMod.SetConfigLoader(a.ConfigStore())
	if opts.OnMachinesModule != nil {
		opts.OnMachinesModule(machinesMod)
	}

	// Google OAuth login module
	googleMod := google.New(opts.Database, opts.Self, opts.GoogleIssuer)
	googleMod.SetLoginCompleter(authModule.CompleteLoginForMember)
	googleMod.SetSignupConfirm(authModule.RenderSignupConfirmation)
	googleMod.SetConfigLoader(a.ConfigStore())
	authModule.GoogleLoginEnabled = googleMod.IsLoginEnabled
	a.Add(googleMod)

	// Triggers module: unified SQL trigger management. Registered after
	// discordwebhook (needs discord_webhook_queue table) and after machines
	// (which may reference discord tables), but before admin.
	a.Add(triggers.New(opts.Database))

	// Signs module: queue-backed printing of letter-paper signs over IPP.
	signsMod := signs.New(opts.Database, engine.NewEventLogger(opts.Database, "signs"))
	if opts.SignsPrinter != nil {
		signsMod.SetPrinter(opts.SignsPrinter)
	}
	a.Add(signsMod)
	signsMod.SetConfigLoader(a.ConfigStore())
	if opts.OnSignsModule != nil {
		opts.OnSignsModule(signsMod)
	}

	// Admin module added last so it can access the fully-populated config registry
	adminMod := admin.New(opts.Database, opts.Self, opts.AuthIssuer, engine.NewEventLogger(opts.Database, "admin"))
	adminMod.SetConfigRegistry(a.Configs())
	adminMod.SetConfigStore(a.ConfigStore())
	a.Add(adminMod)

	return authModule
}
