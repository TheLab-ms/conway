package fobapi

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/google/uuid"
)

// SignatureHeader is the HTTP header carrying the base64-encoded raw
// Ed25519 signature over the response body of POST /api/fobs. Clients
// that have been configured with a trusted public key must verify this
// header and reject any response that lacks a valid signature.
const SignatureHeader = "X-Fob-Signature"

const migration = `
CREATE TABLE IF NOT EXISTS fob_clients (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ip_address TEXT NOT NULL UNIQUE,
    door_name TEXT NOT NULL DEFAULT '',
    last_seen INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
) STRICT;
`

type Module struct {
	db     *sql.DB
	self   *url.URL
	signer *engine.Ed25519Signer
}

func New(db *sql.DB, self *url.URL, signer *engine.Ed25519Signer) *Module {
	engine.MustMigrate(db, migration)

	// Add fob_client column to fob_swipes (idempotent - ignore error if exists)
	db.Exec("ALTER TABLE fob_swipes ADD COLUMN fob_client INTEGER REFERENCES fob_clients(id)")

	return &Module{db: db, self: self, signer: signer}
}

// PublicKeyBase64 returns the base64-encoded Ed25519 public key used to sign
// responses, or an empty string if no signer is configured (tests).
func (m *Module) PublicKeyBase64() string {
	if m.signer == nil {
		return ""
	}
	return m.signer.PublicKeyBase64()
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("POST /api/fobs", auth.OnlyLAN(m.handle))
	router.HandleFunc("POST /admin/doors/{id}", router.WithLeadership(m.handleUpdateDoorName))
}

func (m *Module) handle(w http.ResponseWriter, r *http.Request) {
	// Extract the client IP address (strip port from RemoteAddr)
	clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// Fallback: use RemoteAddr as-is if it has no port
		clientIP = r.RemoteAddr
	}

	// Upsert the fob client, updating last_seen at most once per 30 seconds
	_, err = m.db.ExecContext(r.Context(),
		`INSERT INTO fob_clients (ip_address, last_seen) VALUES ($1, strftime('%s', 'now'))
		 ON CONFLICT(ip_address) DO UPDATE SET last_seen = strftime('%s', 'now')
		 WHERE fob_clients.last_seen < strftime('%s', 'now') - 30`,
		clientIP)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Get the client ID for annotating swipe events
	var clientID int64
	err = m.db.QueryRowContext(r.Context(),
		"SELECT id FROM fob_clients WHERE ip_address = $1", clientIP).Scan(&clientID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Store fob swipe events, if any were provided
	events := []*fobEvent{}
	buf, _ := io.ReadAll(r.Body)
	err = json.Unmarshal(buf, &events)
	if err != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	for _, event := range events {
		_, err := m.db.ExecContext(r.Context(),
			`INSERT INTO fob_swipes (uid, timestamp, fob_id, member, fob_client)
			 VALUES ($1, strftime('%s', 'now'), $2, (SELECT id FROM members WHERE fob_id = $2), $3)
			 ON CONFLICT DO NOTHING`,
			uuid.NewString(), event.FobID, clientID)
		if err != nil {
			engine.SystemError(w, err.Error())
			return
		}
	}
	if l := len(events); l > 0 {
		slog.Info("stored fob swipe events", "count", len(events), "client", clientIP)
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

	// Serialize the body into a buffer so we can sign the exact bytes the
	// client will see. The access-controller verifies the signature against
	// the raw response body before parsing it.
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(&ids); err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	if m.signer != nil {
		sig := m.signer.Sign(body.Bytes())
		w.Header().Set(SignatureHeader, base64.StdEncoding.EncodeToString(sig))
	}
	w.Header().Set("ETag", etag)
	w.Write(body.Bytes())
}

// handleUpdateDoorName allows admins to assign a door name to a fob API client.
func (m *Module) handleUpdateDoorName(w http.ResponseWriter, r *http.Request) {
	clientID := r.PathValue("id")
	doorName := r.FormValue("door_name")

	_, err := m.db.ExecContext(r.Context(),
		"UPDATE fob_clients SET door_name = $1 WHERE id = $2",
		doorName, clientID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.Redirect(w, r, "/admin/config/fobapi", http.StatusSeeOther)
}

// fobClient represents a tracked fob API client (access controller).
type fobClient struct {
	ID        int64
	IPAddress string
	DoorName  string
	LastSeen  engine.LocalTime
}

// listClients returns all known fob API clients, ordered by last seen time.
func (m *Module) listClients(ctx context.Context) ([]*fobClient, error) {
	rows, err := m.db.QueryContext(ctx,
		"SELECT id, ip_address, door_name, last_seen FROM fob_clients ORDER BY last_seen DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clients []*fobClient
	for rows.Next() {
		c := &fobClient{}
		if err := rows.Scan(&c.ID, &c.IPAddress, &c.DoorName, &c.LastSeen); err != nil {
			return nil, err
		}
		clients = append(clients, c)
	}
	return clients, rows.Err()
}

type fobEvent struct {
	FobID   int64 `json:"fob"`
	Allowed bool  `json:"allowed"`
}
