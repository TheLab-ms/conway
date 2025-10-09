package fobapi

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/google/uuid"
)

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("POST /api/fobs", auth.OnlyLAN(m.handle))
}

func (m *Module) handle(w http.ResponseWriter, r *http.Request) {
	// Store fob swipe events, if any were provided
	events := []*fobEvent{}
	buf, _ := io.ReadAll(r.Body)
	err := json.Unmarshal(buf, &events)
	if err != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	for _, event := range events {
		_, err := m.db.ExecContext(r.Context(), "INSERT INTO fob_swipes (uid, timestamp, fob_id, member) VALUES ($1, strftime('%s', 'now'), $2, (SELECT id FROM members WHERE fob_id = $2)) ON CONFLICT DO NOTHING", uuid.NewString(), event.FobID)
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}
	}
	if l := len(events); l > 0 {
		slog.Info("stored fob swipe events", "count", len(events))
	}

	// Query for the current enabled keyfobs
	const q = "SELECT fob_id FROM active_keyfobs ORDER BY fob_id"
	rows, err := m.db.QueryContext(r.Context(), q)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	defer rows.Close()

	// Transform the query response into the json response and etag hash
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

	// Return the response, or a 304 if the client already has the latest data
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	w.Header().Set("ETag", etag)
	json.NewEncoder(w).Encode(&ids)
}

type fobEvent struct {
	FobID   int64 `json:"fob"`
	Allowed bool  `json:"allowed"`
}
