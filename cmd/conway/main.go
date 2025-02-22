// Conway is (unsurprisingly) the main server of Conway.
// It's responsible for handling requests from the internet and storing persistent state in sqlite.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/smtp"
	"net/url"
	"os"

	"github.com/TheLab-ms/conway/db"
	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/admin"
	"github.com/TheLab-ms/conway/modules/api"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/TheLab-ms/conway/modules/keyfob"
	"github.com/TheLab-ms/conway/modules/members"
	"github.com/TheLab-ms/conway/modules/oauth2"
	"github.com/TheLab-ms/conway/modules/payment"
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

	SmtpAddr string
	SmtpFrom string
	SmtpUser string
	SmtpPass string
}

func main() {
	conf, err := env.ParseAsWithOptions[Config](env.Options{Prefix: "CONWAY_", UseFieldNameByDefault: true})
	if err != nil {
		panic(err)
	}
	stripe.Key = conf.StripeKey

	var ec *auth.EmailConfig
	if conf.SmtpAddr != "" {
		host, _, _ := net.SplitHostPort(conf.SmtpAddr)
		ec = &auth.EmailConfig{
			Addr: conf.SmtpAddr,
			From: conf.SmtpFrom,
			Auth: smtp.PlainAuth("", conf.SmtpUser, conf.SmtpPass, host),
		}
	}

	db, err := db.New("conway.sqlite3")
	if err != nil {
		panic(err)
	}

	app, _, err := newApp(db, conf, getSelfURL(), ec)
	if err != nil {
		panic(err)
	}

	app.Run(context.TODO())
}

func getSelfURL() *url.URL {
	str := "http://localhost:8080"
	if env := os.Getenv("SELF_URL"); env != "" {
		str = env
	}

	self, err := url.Parse(str)
	if err != nil {
		panic(err)
	}
	return self
}

func newApp(db *sql.DB, conf Config, self *url.URL, ec *auth.EmailConfig) (*engine.App, *auth.Module, error) {
	a := engine.NewApp(conf.HttpAddr)

	authModule, err := auth.New(db, self, ec)
	if err != nil {
		return nil, nil, fmt.Errorf("creating auth module: %w", err)
	}
	a.Add(authModule)
	a.Router.Authenticator = authModule // IMPORTANT

	apiModule, err := api.New(db)
	if err != nil {
		return nil, nil, fmt.Errorf("creating api module: %w", err)
	}
	a.Add(apiModule)

	a.Add(oauth2.New(db, self, authModule))
	a.Add(payment.New(db, conf.StripeWebhookKey, self))
	a.Add(admin.New(db))
	a.Add(members.New(db))
	a.Add(waiver.New(db))
	a.Add(keyfob.New(db, conf.SpaceHost))

	return a, authModule, nil
}
