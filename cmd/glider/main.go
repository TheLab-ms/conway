// Glider is an agent of Conway that runs in the makerspace LAN.
// Unlike Conway's main process, it isn't reachable from the internet.
// Instead it provides a buffer for data that is reported to Conway, and a cache for the other direction.
package main

import (
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/modules/api"
	gac "github.com/TheLab-ms/conway/modules/generic-access-controller"
	"github.com/caarlos0/env/v11"
)

type Config struct {
	ConwayURL            string `env:",required"`
	ConwayToken          string `env:",required"`
	StateDir             string `env:",required" envDefault:"./state"`
	AccessControllerHost string
}

func main() {
	conf, err := env.ParseAsWithOptions[Config](env.Options{Prefix: "GLIDER_", UseFieldNameByDefault: true})
	if err != nil {
		panic(err)
	}
	client := api.NewGliderClient(conf.ConwayURL, conf.ConwayToken, conf.StateDir)
	gacClient := gac.Client{Addr: conf.AccessControllerHost, Timeout: time.Second * 5}

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

	// Loop to sync the access controller
	lastSync := atomic.Pointer[time.Time]{}
	if conf.AccessControllerHost != "" {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()

			for {
				state := client.GetState()
				if state == nil {
					slog.Info("refusing to sync access controller because Conway state is unknown")
					<-client.StateTransitions
					continue
				}

				// Sync the fob IDs
				err := syncAccessControllerConfig(state, &gacClient)
				if err == nil {
					slog.Info("sync'd access controller", "fobCount", len(state.EnabledFobs))
				} else {
					slog.Error("failed to sync access controller", "error", err)
				}

				// Scrape events
				withScrapeCursor(&conf, "gac", func(last int) int {
					err = gacClient.ListSwipes(last, func(cs *gac.CardSwipe) error {
						// Prefer our clock over the access controller's for non-historical events
						ts := cs.Time
						if time.Since(cs.Time).Abs() < time.Hour {
							ts = time.Now()
						}

						client.BufferEvent(&api.GliderEvent{
							UID:       fmt.Sprintf("gac-%d", cs.ID),
							Timestamp: ts.Unix(),
							FobSwipe: &api.FobSwipeEvent{
								FobID: int64(cs.CardID),
							},
						})
						slog.Info("scraped access controller event", "eventID", cs.ID, "fobID", cs.CardID)

						last = max(last, cs.ID)
						return nil
					})
					if err != nil {
						slog.Error("failed to scrape access controller events", "error", err)
					}
					return last
				})

				now := time.Now()
				lastSync.Store(&now)
				select {
				case <-ticker.C:
				case <-client.StateTransitions:
				}
			}
		}()
	}

	// Watchdog for the other goroutines
	for {
		jitterSleep(time.Second)

		if ls := lastSync.Load(); ls != nil && time.Since(*ls) > time.Minute*15 {
			panic("access controller sync loop is stuck")
		}
	}
}

func jitterSleep(dur time.Duration) {
	time.Sleep(dur + time.Duration(float64(dur)*0.2*(rand.Float64()-0.5)))
}

func syncAccessControllerConfig(state *api.GliderState, client *gac.Client) error {
	cards, err := client.ListCards()
	if err != nil {
		return err
	}

	expectedByID := map[int64]struct{}{}
	for _, fob := range state.EnabledFobs {
		expectedByID[fob] = struct{}{}
	}

	// Backward reconciliation
	currentByID := map[int64]struct{}{}
	for _, card := range cards {
		if _, ok := expectedByID[int64(card.Number)]; ok {
			currentByID[int64(card.Number)] = struct{}{}
			continue // still active
		}

		slog.Info("removing fob from access controller", "fob", card.Number)
		// TODO
	}

	// Forward reconciliation
	for _, fob := range state.EnabledFobs {
		if _, ok := currentByID[fob]; ok {
			continue // already active
		}

		slog.Info("adding fob to access controller", "fob", fob)
		// TODO
	}

	return nil
}

func withScrapeCursor(conf *Config, name string, fn func(last int) int) {
	fp := filepath.Join(conf.StateDir, name+".cursor")
	raw, err := os.ReadFile(fp)
	if err != nil && !os.IsNotExist(err) {
		panic(err)
	}
	i, _ := strconv.Atoi(string(raw))

	next := fn(i)
	if next == i {
		return
	}

	err = os.WriteFile(fp, []byte(strconv.Itoa(next)), 0644)
	if err != nil {
		panic(err)
	}
}
