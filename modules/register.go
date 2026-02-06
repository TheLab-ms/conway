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

	a.Add(email.New(opts.Database, opts.EmailSender))
	a.Add(oauth2.New(opts.Database, opts.Self, opts.OAuthIssuer))
	a.Add(payment.New(opts.Database, opts.Self, engine.NewEventLogger(opts.Database, "stripe")))
	a.Add(waiver.New(opts.Database))
	a.Add(kiosk.New(opts.Database, opts.Self, opts.FobIssuer, opts.SpaceHost))
	a.Add(metrics.New(opts.Database))
	a.Add(fobapi.New(opts.Database))
	a.Add(directory.New(opts.Database))

	a.Add(machines.New(opts.Database, engine.NewEventLogger(opts.Database, "bambu")))

	// Discord modules - always register, they check config dynamically
	// Discord webhook module for notifications
	webhookSender := discordwebhook.NewHTTPSender()
	discordWebhookMod := discordwebhook.New(opts.Database, webhookSender)
	a.Add(discordWebhookMod)

	// Discord OAuth/role sync module
	discordMod := discord.New(opts.Database, opts.Self, opts.DiscordIssuer, engine.NewEventLogger(opts.Database, "discord"))
	discordMod.SetLoginCompleter(authModule.CompleteLoginForMember)
	authModule.DiscordLoginEnabled = discordMod.IsLoginEnabled
	a.Add(discordMod)

	// Google OAuth login module
	googleMod := google.New(opts.Database, opts.Self, opts.GoogleIssuer)
	googleMod.SetLoginCompleter(authModule.CompleteLoginForMember)
	authModule.GoogleLoginEnabled = googleMod.IsLoginEnabled
	a.Add(googleMod)

	// Admin module added last so it can access the fully-populated config registry
	adminMod := admin.New(opts.Database, opts.Self, opts.AuthIssuer, engine.NewEventLogger(opts.Database, "admin"))
	adminMod.SetConfigRegistry(a.Configs())
	a.Add(adminMod)

	return authModule
}
