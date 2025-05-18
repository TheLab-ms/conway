// Glider is an agent of Conway that runs in the makerspace LAN.
// Unlike Conway's main process, it isn't reachable from the internet.
// Instead it provides a buffer for data that is reported to Conway, and a cache for the other direction.
package main

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/engine"
	gac "github.com/TheLab-ms/conway/modules/generic-access-controller"
	"github.com/TheLab-ms/conway/modules/peering"
	"github.com/caarlos0/env/v11"
)

type Config struct {
	ConwayURL            string `env:",required"`
	AccessControllerHost string
}

func main() {
	conf, err := env.ParseAsWithOptions[Config](env.Options{Prefix: "GLIDER_", UseFieldNameByDefault: true})
	if err != nil {
		panic(err)
	}
	client := peering.NewClient(conf.ConwayURL, ".", engine.NewTokenIssuer("glider.pem"))
	gacClient := gac.Client{Addr: conf.AccessControllerHost, Timeout: time.Second * 5}

	var procs engine.ProcMgr

	// Loop to asynchronously flush events to Conway
	procs.Add(engine.Poll(time.Second/2, func(ctx context.Context) bool {
		err = client.FlushEvents()
		if err != nil {
			slog.Error("failed to flush events to server", "error", err)
		}
		return false
	}))

	// Loop to asynchronously warm the Conway state cache
	procs.Add(engine.Poll(time.Second, func(ctx context.Context) bool {
		err = client.WarmCache()
		if err != nil {
			slog.Error("failed to warm Conway cache", "error", err)
		}
		return false
	}))

	// Loop to sync the access controller
	lastSync := atomic.Pointer[time.Time]{}
	if conf.AccessControllerHost != "" {
		procs.Add(gac.NewReconciliationLoop(client, &gacClient, &lastSync))
	}

	// Watchdog for the main reconciliation goroutine
	procs.Add(engine.Poll(time.Second, func(ctx context.Context) bool {
		if ls := lastSync.Load(); ls != nil && time.Since(*ls) > time.Minute*15 {
			panic("access controller sync loop is stuck")
		}
		return false
	}))
}
