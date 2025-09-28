package fobapi

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
	"github.com/google/uuid"
)

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /api/fobs", engine.OnlyLAN(m.handleList))
	router.HandleFunc("POST /api/fobs/events", engine.OnlyLAN(m.handleEvent))
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	const q = "SELECT fob_id FROM active_keyfobs ORDER BY fob_id"
	rows, err := m.db.QueryContext(r.Context(), q)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	defer rows.Close()

	hasher := sha256.New()
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		fmt.Fprintf(hasher, "%d,", id)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	etag := hex.EncodeToString(hasher.Sum(nil))
	if r.Header.Get("If-None-Match") == etag {
		// Client already has the latest state
		w.WriteHeader(304)
		return
	}

	w.Header().Set("ETag", etag)
	json.NewEncoder(w).Encode(&ids)
}

func (m *Module) handleEvent(w http.ResponseWriter, r *http.Request) {
	events := []*fobEvent{}
	err := json.NewDecoder(r.Body).Decode(&events)
	if err != nil {
		http.Error(w, "invalid json", 400)
		return
	}

	for _, event := range events {
		_, err = m.db.ExecContext(r.Context(), "INSERT INTO fob_swipes (uid, timestamp, fob_id, member) VALUES ($1, $2, $3, (SELECT id FROM members WHERE fob_id = $3)) ON CONFLICT DO NOTHING", uuid.NewString(), event.Timestamp, event.FobID)
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}
	}
	slog.Info("stored fob swipe events", "count", len(events))
	w.WriteHeader(204)
}

type fobEvent struct {
	Timestamp int64 `json:"ts"`
	FobID     int64 `json:"fob"`
}
