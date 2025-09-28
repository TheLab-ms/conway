package fobapi

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
)

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /api/fobs", m.handleListFobs)
}

func (m *Module) handleListFobs(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("CF-Connecting-IP") != "" {
		// Only requests from the LAN are allowed
		w.WriteHeader(403)
		return
	}

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
