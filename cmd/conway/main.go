// Conway is (unsurprisingly) the main server of Conway.
// It's responsible for handling requests from the internet and storing persistent state in sqlite.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"

	"github.com/TheLab-ms/conway/db"
	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/admin"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/TheLab-ms/conway/modules/discord"
	"github.com/TheLab-ms/conway/modules/email"
	"github.com/TheLab-ms/conway/modules/fobapi"
	"github.com/TheLab-ms/conway/modules/kiosk"
	"github.com/TheLab-ms/conway/modules/machines"
	"github.com/TheLab-ms/conway/modules/members"
	"github.com/TheLab-ms/conway/modules/metrics"
	"github.com/TheLab-ms/conway/modules/oauth2"
	"github.com/TheLab-ms/conway/modules/payment"
	"github.com/TheLab-ms/conway/modules/peering"
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
}

func main() {
	conf, err := env.ParseAsWithOptions[Config](env.Options{Prefix: "CONWAY_", UseFieldNameByDefault: true})
	if err != nil {
		panic(err)
	}
	stripe.Key = conf.StripeKey

	app, err := newApp(conf, getSelfURL(conf))
	if err != nil {
		panic(err)
	}

	app.Run(context.TODO())
}

func newApp(conf Config, self *url.URL) (*engine.App, error) {
	a := engine.NewApp(conf.HttpAddr)

	db, err := db.New("conway.sqlite3")
	if err != nil {
		panic(err)
	}

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
		tokenIss   = engine.NewTokenIssuer("auth.pem")
		loginIss   = engine.NewTokenIssuer("auth.pem")
		gliderIss  = engine.NewTokenIssuer("glider.pem")
		oauthIss   = engine.NewTokenIssuer("oauth2.pem")
		fobIss     = engine.NewTokenIssuer("fobs.pem")
		discordIss = engine.NewTokenIssuer("discord-oauth.pem")
	)

	authModule := auth.New(db, self, tso, tokenIss, loginIss)
	a.Add(authModule)
	a.Router.Authenticator = authModule // IMPORTANT

	a.Add(peering.New(db, gliderIss))
	a.Add(email.New(db, sender))
	a.Add(oauth2.New(db, self, oauthIss))
	a.Add(payment.New(db, conf.StripeWebhookKey, self))
	a.Add(admin.New(db, self, tokenIss))
	a.Add(members.New(db))
	a.Add(waiver.New(db))
	a.Add(kiosk.New(db, self, fobIss, conf.SpaceHost))
	a.Add(metrics.New(db))
	a.Add(machines.New(db))
	a.Add(pruning.New(db))
	a.Add(fobapi.New(db))

	if conf.DiscordClientID != "" {
		a.Add(discord.New(db, self, discordIss, conf.DiscordClientID, conf.DiscordClientSecret, conf.DiscordBotToken, conf.DiscordGuildID, conf.DiscordRoleID))
	} else {
		slog.Info("discord module disabled because a client ID was not configured")
	}

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
