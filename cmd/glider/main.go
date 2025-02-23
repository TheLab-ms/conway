// Glider is an agent of Conway that runs in the makerspace LAN.
// Unlike Conway's main process, it isn't reachable from the internet.
// Instead it provides a buffer for data that is reported to Conway, and a cache for the other direction.
package main

import (
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"time"

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

	err = os.MkdirAll(filepath.Join(conf.StateDir, "events"), 0755)
	if err != nil {
		panic(err)
	}

	// Flush buffered events to the server periodically
	go func() {
		for {
			jitterSleep(time.Second / 2)
			err = flushEvents(&conf)
			if err != nil {
				slog.Error("failed to flush events to server", "error", err)
				continue
			}
		}
	}()

	var lastRevision int64
	for {
		jitterSleep(time.Second)

		// Get the current expected state from the Conway server
		state, err := getState(&conf, lastRevision)
		if err != nil {
			slog.Error("failed to get state from server", "error", err)
			continue
		}
		if state == nil {
			continue // nothing has changed
		}
		slog.Info("got state from server", "revision", state.Revision, "lastRevision", lastRevision)
		lastRevision = state.Revision

		// Sync the access controller
		// TODO
	}
}

func jitterSleep(dur time.Duration) {
	time.Sleep(dur + time.Duration(float64(dur)*0.2*(rand.Float64()-0.5)))
}
