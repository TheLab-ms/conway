// Peering implements a simple HTTP protocol used to communicate between Conway and Glider.
package peering

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/TheLab-ms/conway/engine"
)

type Module struct {
	db     *sql.DB
	issuer *engine.TokenIssuer
}

func New(db *sql.DB, iss *engine.TokenIssuer) *Module {
	return &Module{db: db, issuer: iss}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /api/peering/state", m.withAuth(m.handleGetGliderState))
	router.HandleFunc("POST /api/peering/events", m.withAuth(m.handlePostGliderEvents))
}

func (m *Module) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		_, err := m.issuer.Verify(token)
		if err != nil {
			http.Error(w, "invalid token", 401)
			return
		}
		next(w, r)
	}
}

func (m *Module) handleGetGliderState(w http.ResponseWriter, r *http.Request) {
	tx, err := m.db.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelLinearizable, ReadOnly: true})
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	defer tx.Rollback()

	var resp State
	err = tx.QueryRowContext(r.Context(), "SELECT revision FROM glider_state WHERE id = 1").Scan(&resp.Revision)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Bail out if we don't have a newer state than the client
	if after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 0); err == nil && after >= resp.Revision {
		w.WriteHeader(204)
		return
	}

	q, err := tx.QueryContext(r.Context(), "SELECT fob_id FROM active_keyfobs")
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	defer q.Close()

	for q.Next() {
		var id int64
		if err := q.Scan(&id); err != nil {
			engine.SystemError(w, err.Error())
			return
		}
		resp.EnabledFobs = append(resp.EnabledFobs, id)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(&resp)
}

func (m *Module) handlePostGliderEvents(w http.ResponseWriter, r *http.Request) {
	events := []*Event{}
	dec := json.NewDecoder(r.Body)
	for {
		event := &Event{}
		err := dec.Decode(event)
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		events = append(events, event)
	}

	tx, err := m.db.BeginTx(r.Context(), nil)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	defer tx.Rollback()

	for _, event := range events {
		if event.FobSwipe != nil {
			_, err = tx.ExecContext(r.Context(), "INSERT INTO fob_swipes (uid, timestamp, fob_id, member) VALUES ($1, $2, $3, (SELECT id FROM members WHERE fob_id = $3)) ON CONFLICT DO NOTHING", event.UID, event.Timestamp, event.FobSwipe.FobID)
			if err != nil {
				engine.SystemError(w, err.Error())
				return
			}
		}
		if event.PrinterEvent != nil {
			_, err = tx.ExecContext(r.Context(), "INSERT INTO printer_events (uid, timestamp, printer_name, job_finished_timestamp, error_code) VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING", event.UID, event.Timestamp, event.PrinterEvent.PrinterName, event.PrinterEvent.JobFinishedTimestamp, event.PrinterEvent.ErrorCode)
			if err != nil {
				engine.SystemError(w, err.Error())
				return
			}
		}
	}

	if err := tx.Commit(); err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	slog.Info("stored Glider events", "count", len(events))
	w.WriteHeader(204)
}
