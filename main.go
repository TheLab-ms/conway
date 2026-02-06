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
	"github.com/TheLab-ms/conway/modules"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/caarlos0/env/v11"
)

type Config struct {
	HttpAddr string `envDefault:":8080"`

	// SpaceHost is a hostname that resolves to the public IP used to egress the makerspace LAN.
	SpaceHost string `envDefault:"localhost"`
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
	database, err := engine.OpenDB("conway.sqlite3")
	if err != nil {
		panic(err)
	}

	router := engine.NewRouter()
	router.HandleFunc("/healthz", auth.OnlyLAN(engine.ServeHealthProbe(database)))

	a := engine.NewApp(conf.HttpAddr, router, database)

	modules.Register(a, modules.Options{
		Database:      database,
		Self:          self,
		AuthIssuer:    engine.NewTokenIssuer("auth.pem"),
		OAuthIssuer:   engine.NewTokenIssuer("oauth2.pem"),
		FobIssuer:     engine.NewTokenIssuer("fobs.pem"),
		DiscordIssuer: engine.NewTokenIssuer("discord-oauth.pem"),
		SpaceHost:     conf.SpaceHost,
	})

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
