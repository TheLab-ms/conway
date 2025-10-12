package gac

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/TheLab-ms/conway/engine"
)

func NewReconciliationLoop(db *sql.DB, gacClient *Client) engine.Proc {
	var lastFobs []int64
	return engine.Poll(time.Second*20, func(ctx context.Context) bool {
		// The scrape cursor doesn't really make sense now that this function runs
		// in-process to sqlite. But this code will hopefully go away soon anyway.
		withScrapeCursor("gac.cursor", func(last int) int {
			err := gacClient.ListSwipes(last, func(cs *CardSwipe) error {
				_, err := db.ExecContext(ctx, "INSERT INTO fob_swipes (uid, timestamp, fob_id, member) VALUES ($1, $2, $3, (SELECT id FROM members WHERE fob_id = $3)) ON CONFLICT DO NOTHING", fmt.Sprintf("gac-%d", cs.ID), cs.Time, cs.CardID)
				if err != nil {
					return err
				}
				slog.Info("scraped access controller event", "eventID", cs.ID, "fobID", cs.CardID)
				last = max(last, cs.ID)
				return nil
			})
			if err != nil {
				slog.Error("failed to scrape access controller events", "error", err)
			}
			return last
		})

		// List fobs
		q, err := db.QueryContext(ctx, "SELECT fob_id FROM active_keyfobs")
		if err != nil {
			slog.Error("listing fobs", "error", err)
			return false
		}
		defer q.Close()

		var fobs []int64
		for q.Next() {
			var id int64
			if err := q.Scan(&id); err != nil {
				slog.Error("scanning keyfob row", "error", err)
				return false
			}
			fobs = append(fobs, id)
		}

		// Bail out if nothing has changed since last sync
		if slices.Equal(lastFobs, fobs) {
			return false
		}

		// Sync!
		err = syncAccessControllerConfig(fobs, gacClient)
		if err == nil {
			slog.Info("sync'd access controller", "fobCount", len(fobs))
			lastFobs = fobs
		} else {
			slog.Error("failed to sync access controller", "error", err)
		}

		return false
	})
}

func syncAccessControllerConfig(fobs []int64, client *Client) error {
	cards, err := client.ListCards()
	if err != nil {
		return err
	}

	expectedByID := map[int64]struct{}{}
	for _, fob := range fobs {
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
	for _, fob := range fobs {
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
