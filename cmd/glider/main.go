// Glider is an agent of Conway that runs in the makerspace LAN.
// Unlike Conway's main process, it isn't reachable from the internet.
// Instead it provides a buffer for data that is reported to Conway, and a cache for the other direction.
package main

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/TheLab-ms/conway/modules/api"
	"github.com/caarlos0/env/v11"
)

type Config struct {
	ConwayURL   string `env:",required"`
	ConwayToken string `env:",required"`
	StateDir    string `env:",required" envDefault:"./state"`
}

func main() {
	conf, err := env.ParseAsWithOptions[Config](env.Options{Prefix: "GLIDER_", UseFieldNameByDefault: true})
	if err != nil {
		panic(err)
	}
	client := api.NewGliderClient(conf.ConwayURL, conf.ConwayToken, conf.StateDir)

	// Loop to asynchronously flush events to Conway
	go func() {
		for {
			jitterSleep(time.Second / 2)
			err = client.FlushEvents()
			if err != nil {
				slog.Error("failed to flush events to server", "error", err)
				continue
			}
		}
	}()

	// Loop to asynchronously warm the Conway state cache
	go func() {
		for {
			jitterSleep(time.Second)
			err = client.WarmCache()
			if err != nil {
				slog.Error("failed to warm Conway cache", "error", err)
				continue
			}
		}
	}()

	// Loop to sync the access controller configurations
	go func() {
		ticker := time.NewTicker(time.Second * 30)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
			case <-client.StateTransitions:
			}

			state := client.GetState()
			if state == nil {
				slog.Info("refusing to sync access controller because Conway state is unknown")
				continue
			}

			slog.Info("syncing access controller", "fobCount", len(state.EnabledFobs))
			// TODO
		}
	}()

	<-context.Background().Done() // run the other goroutines forever
}

func jitterSleep(dur time.Duration) {
	time.Sleep(dur + time.Duration(float64(dur)*0.2*(rand.Float64()-0.5)))
}
