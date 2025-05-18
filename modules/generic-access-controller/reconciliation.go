package gac

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/peering"
)

func NewReconciliationLoop(client *peering.Client, gacClient *Client, lastSync *atomic.Pointer[time.Time]) engine.Proc {
	return func(ctx context.Context) error {
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
			err := syncAccessControllerConfig(state, gacClient)
			if err == nil {
				slog.Info("sync'd access controller", "fobCount", len(state.EnabledFobs))
			} else {
				slog.Error("failed to sync access controller", "error", err)
			}

			// Scrape events
			withScrapeCursor("gac.cursor", func(last int) int {
				err = gacClient.ListSwipes(last, func(cs *CardSwipe) error {
					// Prefer our clock over the access controller's for non-historical events
					client.BufferEvent(&peering.Event{
						UID:       fmt.Sprintf("gac-%d", cs.ID),
						Timestamp: time.Now().Unix(),
						FobSwipe: &peering.FobSwipeEvent{
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
	}
}

func syncAccessControllerConfig(state *peering.State, client *Client) error {
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

		err := client.RemoveCard(card.ID)
		if err == nil {
			slog.Info("removed fob from access controller", "fob", card.Number)
		} else {
			slog.Error("error while removing card from access controller", "error", err)
		}
	}

	// Forward reconciliation
	for _, fob := range state.EnabledFobs {
		if _, ok := currentByID[fob]; ok {
			continue // already active
		}

		err := client.AddCard(int(fob), fmt.Sprintf("conway%d", fob))
		if err == nil {
			slog.Info("added fob to access controller", "fob", fob)
		} else {
			slog.Error("error while adding card to access controller", "error", err)
		}
	}

	return nil
}

func withScrapeCursor(fp string, fn func(last int) int) {
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
