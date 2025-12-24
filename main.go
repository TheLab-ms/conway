// Conway is (unsurprisingly) the main server of Conway.
// It's responsible for handling requests from the internet and storing persistent state in sqlite.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/url"
	"os"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/db"
	"github.com/TheLab-ms/conway/modules"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/TheLab-ms/conway/modules/discordwebhook"
	"github.com/TheLab-ms/conway/modules/email"
	"github.com/TheLab-ms/conway/modules/machines"
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

	// DiscordWebhooks is a JSON map of channel_id -> webhook_url for Discord notifications
	DiscordWebhooks string
	// DiscordPrintChannel is the channel_id to use for 3D print notifications
	DiscordPrintChannel string
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

	// Parse Discord webhook configuration
	var webhookURLs map[string]string
	if conf.DiscordWebhooks != "" {
		if err := json.Unmarshal([]byte(conf.DiscordWebhooks), &webhookURLs); err != nil {
			slog.Error("failed to parse Discord webhook config", "error", err)
		}
	}

	// Create Discord webhook module
	var webhookSender discordwebhook.Sender
	if len(webhookURLs) > 0 {
		webhookSender = discordwebhook.NewHTTPSender()
	}
	discordWebhookMod := discordwebhook.New(database, webhookSender, webhookURLs)

	var machinesMod *machines.Module
	if conf.BambuPrinters != "" {
		machinesMod = machines.New(conf.BambuPrinters, database, conf.DiscordPrintChannel)
	} else {
		slog.Info("machines module disabled because no devices were configured")
	}

	if conf.AccessControllerHost == "" {
		slog.Info("generic access controller module disabled because a URL was not configured")
	}

	if conf.DiscordClientID == "" {
		slog.Info("discord module disabled because a client ID was not configured")
	}

	a := engine.NewApp(conf.HttpAddr, router)

	authModule := modules.Register(a, modules.Options{
		Database:             database,
		Self:                 self,
		AuthIssuer:           engine.NewTokenIssuer("auth.pem"),
		OAuthIssuer:          engine.NewTokenIssuer("oauth2.pem"),
		FobIssuer:            engine.NewTokenIssuer("fobs.pem"),
		DiscordIssuer:        engine.NewTokenIssuer("discord-oauth.pem"),
		Turnstile:            tso,
		EmailSender:          sender,
		StripeWebhookKey:     conf.StripeWebhookKey,
		SpaceHost:            conf.SpaceHost,
		MachinesModule:       machinesMod,
		DiscordWebhookModule: discordWebhookMod,
		AccessControllerHost: conf.AccessControllerHost,
		DiscordClientID:      conf.DiscordClientID,
		DiscordClientSecret:  conf.DiscordClientSecret,
		DiscordBotToken:      conf.DiscordBotToken,
		DiscordGuildID:       conf.DiscordGuildID,
		DiscordRoleID:        conf.DiscordRoleID,
	})
	a.Router.Authenticator = authModule // IMPORTANT

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
