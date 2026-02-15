package members

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
)

//go:embed schema.sql
var migration string

const defaultTTL = 2 * 365 * 24 * 60 * 60 // 2 years in seconds

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	engine.MustMigrate(db, migration)
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /{$}", router.WithAuthn(m.renderMemberView))
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "fob swipes",
		"DELETE FROM fob_swipes WHERE timestamp < unixepoch() - ?", defaultTTL)))
	mgr.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "member events",
		"DELETE FROM member_events WHERE created < unixepoch() - ?", defaultTTL)))
}

func (m *Module) renderMemberView(w http.ResponseWriter, r *http.Request) {
	authdUser := auth.GetUserMeta(r.Context()).ID

	mem := member{}
	err := m.db.QueryRowContext(r.Context(), `
		SELECT id, email, access_status, discord_user_id IS NOT NULL,
			waiver IS NOT NULL, payment_status IS NOT NULL, fob_id IS NOT NULL AND fob_id != 0
		FROM members m WHERE m.id = $1`, authdUser).Scan(
		&mem.ID, &mem.Email, &mem.AccessStatus, &mem.DiscordLinked,
		&mem.WaiverSigned, &mem.PaymentActive, &mem.HasKeyFob)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Check if Stripe is configured by an admin and load donation items
	var apiKey string
	var donationItemsJSON string
	err = m.db.QueryRowContext(r.Context(),
		`SELECT api_key, donation_items_json FROM stripe_config ORDER BY version DESC LIMIT 1`).Scan(&apiKey, &donationItemsJSON)
	if err == nil && apiKey != "" {
		mem.StripeConfigured = true
		if donationItemsJSON != "" {
			var items []donationItem
			if json.Unmarshal([]byte(donationItemsJSON), &items) == nil {
				mem.DonationItems = items
			}
		}
	}

	w.Header().Set("Content-Type", "text/html")
	renderMember(&mem).Render(r.Context(), w)
}

func NewTestDB(t *testing.T) *sql.DB {
	d := engine.OpenTestDB(t)
	engine.MustMigrate(d, migration)
	return d
}
