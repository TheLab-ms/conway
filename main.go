// Conway is (unsurprisingly) the main server of Conway.
// It's responsible for handling requests from the internet and storing persistent state in sqlite.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/url"
	"os"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/db"
	"github.com/TheLab-ms/conway/modules/admin"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/TheLab-ms/conway/modules/discord"
	"github.com/TheLab-ms/conway/modules/email"
	"github.com/TheLab-ms/conway/modules/fobapi"
	gac "github.com/TheLab-ms/conway/modules/generic-access-controller"
	"github.com/TheLab-ms/conway/modules/kiosk"
	"github.com/TheLab-ms/conway/modules/machines"
	"github.com/TheLab-ms/conway/modules/members"
	"github.com/TheLab-ms/conway/modules/metrics"
	"github.com/TheLab-ms/conway/modules/oauth2"
	"github.com/TheLab-ms/conway/modules/payment"
	"github.com/TheLab-ms/conway/modules/pruning"
	"github.com/TheLab-ms/conway/modules/waiver"
	"github.com/caarlos0/env/v11"
	"github.com/stripe/stripe-go/v78"
)

type Config struct {
	HttpAddr string `envDefault:":8080"`

	// SpaceHost is a hostname that resolves to the public IP used to egress the makerspace LAN.
	SpaceHost string `envDefault:"localhost"`

	StripeKey        string
	StripeWebhookKey string

	DiscordClientID     string
	DiscordClientSecret string
	DiscordBotToken     string
	DiscordGuildID      string
	DiscordRoleID       string

	EmailFrom string

	TurnstileSiteKey string
	TurnstileSecret  string

	AccessControllerHost string

	BambuPrinters string
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// The Bambu library logs a lot of noise using the stdlib log package.
	// We can just disable the logger entirely since Conway uses slog.
	log.SetOutput(io.Discard)

	conf, err := env.ParseAsWithOptions[Config](env.Options{Prefix: "CONWAY_", UseFieldNameByDefault: true})
	if err != nil {
		panic(err)
	}
	stripe.Key = conf.StripeKey

	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		err := engine.CheckHealthProbe("http://localhost:8080/healthz") // assume server is running on the default port
		if err != nil {
			panic(err)
		}
		return
	}

	app, err := newApp(conf, getSelfURL(conf))
	if err != nil {
		panic(err)
	}

	app.Run(context.TODO())
}

func newApp(conf Config, self *url.URL) (*engine.App, error) {
	database, err := db.Open("conway.sqlite3")
	if err != nil {
		panic(err)
	}

	router := engine.NewRouter()
	router.HandleFunc("/healthz", auth.OnlyLAN(engine.ServeHealthProbe(database)))

	var tso *auth.TurnstileOptions
	if conf.TurnstileSiteKey != "" {
		tso = &auth.TurnstileOptions{
			SiteKey: conf.TurnstileSiteKey,
			Secret:  conf.TurnstileSecret,
		}
	}

	var sender email.Sender
	if conf.EmailFrom != "" {
		sender = email.NewGoogleSmtpSender(conf.EmailFrom)
	}

	var (
		authIss    = engine.NewTokenIssuer("auth.pem")
		oauthIss   = engine.NewTokenIssuer("oauth2.pem")
		fobIss     = engine.NewTokenIssuer("fobs.pem")
		discordIss = engine.NewTokenIssuer("discord-oauth.pem")
	)

	a := engine.NewApp(conf.HttpAddr, router)

	authModule := auth.New(database, self, tso, authIss)
	a.Add(authModule)
	a.Router.Authenticator = authModule // IMPORTANT

	a.Add(email.New(database, sender))
	a.Add(oauth2.New(database, self, oauthIss))
	a.Add(payment.New(database, conf.StripeWebhookKey, self))
	a.Add(admin.New(database, self, authIss))
	a.Add(members.New(database))
	a.Add(waiver.New(database))
	a.Add(kiosk.New(database, self, fobIss, conf.SpaceHost))
	a.Add(metrics.New(database))
	a.Add(pruning.New(database))
	a.Add(fobapi.New(database))

	if conf.BambuPrinters != "" {
		a.Add(machines.New(conf.BambuPrinters))
	} else {
		slog.Info("machines module disabled because no devices were configured")
	}

	if conf.AccessControllerHost != "" {
		a.Add(gac.New(database, conf.AccessControllerHost))
	} else {
		slog.Info("generic access controller module disabled because a URL was not configured")
	}

	if conf.DiscordClientID != "" {
		a.Add(discord.New(database, self, discordIss, conf.DiscordClientID, conf.DiscordClientSecret, conf.DiscordBotToken, conf.DiscordGuildID, conf.DiscordRoleID))
	} else {
		slog.Info("discord module disabled because a client ID was not configured")
	}

	db.MustMigrate(database, db.BaseMigration)
	return a, nil
}

func getSelfURL(conf Config) *url.URL {
	str := os.Getenv("SELF_URL")
	if str == "" {
		conn, err := net.Dial("udp4", "8.8.8.8:53")
		if err != nil {
			panic(err)
		}
		conn.Close()

		_, port, _ := net.SplitHostPort(conf.HttpAddr)
		str = fmt.Sprintf("http://%s:%s", conn.LocalAddr().(*net.UDPAddr).IP, port)
		slog.Info("discovered self URL", "url", str)
	}

	self, err := url.Parse(str)
	if err != nil {
		panic(err)
	}
	return self
}
