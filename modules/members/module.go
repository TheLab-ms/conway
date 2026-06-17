package members

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/TheLab-ms/conway/modules/members/memberdb"
)

//go:embed schema.sql
var migration string

const defaultTTL = 2 * 365 * 24 * 60 * 60 // 2 years in seconds

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	engine.MustMigrate(db, migration)
	migrateMembers(db)
	seedReferralSources(db)
	return &Module{db: db}
}

// migrateMembers applies additive column migrations that can't be expressed via
// CREATE TABLE IF NOT EXISTS on pre-existing databases. SQLite doesn't support
// IF NOT EXISTS for ALTER TABLE, so we Exec separately and ignore the error
// raised when the column already exists.
func migrateMembers(db *sql.DB) {
	db.Exec(`ALTER TABLE members ADD COLUMN discount_status TEXT CHECK (discount_status IN ('requested', 'approved'))`)
	db.Exec(`ALTER TABLE members ADD COLUMN heard_about TEXT NOT NULL DEFAULT ''`)
	engine.MustMigrate(db, `
CREATE TABLE IF NOT EXISTS members_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    referral_sources_json TEXT NOT NULL DEFAULT '[]'
) STRICT;
`)
}

func seedReferralSources(db *sql.DB) {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM members_config").Scan(&count); err != nil {
		slog.Error("failed to check members config", "error", err)
		return
	}
	if count > 0 {
		return
	}

	const defaults = `[{"label":"Friend or member"},{"label":"Social media"},{"label":"Google or web search"},{"label":"Event or open house"},{"label":"Sign or walk-in"},{"label":"Other"}]`
	if _, err := db.Exec("INSERT INTO members_config (referral_sources_json) VALUES (?)", defaults); err != nil {
		slog.Error("failed to seed referral sources", "error", err)
	}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /{$}", router.WithAuthn(m.renderMemberView))
	router.HandleFunc("POST /discount/request", router.WithAuthn(m.handleDiscountRequest))
	router.HandleFunc("POST /discount/remove", router.WithAuthn(m.handleDiscountRemove))
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
	var discountType *string
	var discountStatus *string
	err := m.db.QueryRowContext(r.Context(), `
		SELECT id, email, access_status, discord_user_id IS NOT NULL,
			waiver IS NOT NULL, payment_status IS NOT NULL, fob_id IS NOT NULL AND fob_id != 0,
			discount_type, discount_status
		FROM members m WHERE m.id = $1`, authdUser).Scan(
		&mem.ID, &mem.Email, &mem.AccessStatus, &mem.DiscordLinked,
		&mem.WaiverSigned, &mem.PaymentActive, &mem.HasKeyFob,
		&discountType, &discountStatus)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	// Derive the member-facing discount state.
	//   - pending: a request is awaiting leadership approval.
	//   - active:  a discount is set and usable (approved via the member flow
	//     or set directly by an admin, which is status-less). Usable discounts
	//     apply at checkout and let "Set Up Payment" show.
	if discountType != nil {
		mem.DiscountType = *discountType
		mem.DiscountLabel = memberdb.DiscountLabel(*discountType)
	}
	if discountStatus != nil && *discountStatus == "requested" {
		mem.DiscountPending = true
	} else if discountType != nil {
		mem.DiscountActive = true
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

// handleDiscountRequest records a member's request for a membership discount.
// It sets discount_type and marks discount_status='requested', which fires the
// trigger that notifies leadership. Any discount tier (including family) may be
// requested; leadership completes any required linkage on approval.
func (m *Module) handleDiscountRequest(w http.ResponseWriter, r *http.Request) {
	memberID := auth.GetUserMeta(r.Context()).ID

	chosen := r.FormValue("discount")
	if chosen == "" || !memberdb.IsValidDiscountType(chosen) {
		http.Error(w, "invalid discount type", http.StatusBadRequest)
		return
	}

	_, err := m.db.ExecContext(r.Context(),
		`UPDATE members SET discount_type = ?, discount_status = 'requested' WHERE id = ?`,
		chosen, memberID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleDiscountRemove clears a member's discount entirely, regardless of
// approval status. Members may remove a pending request or an approved
// discount at any time.
func (m *Module) handleDiscountRemove(w http.ResponseWriter, r *http.Request) {
	memberID := auth.GetUserMeta(r.Context()).ID

	_, err := m.db.ExecContext(r.Context(),
		`UPDATE members SET discount_type = NULL, discount_status = NULL WHERE id = ?`,
		memberID)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func NewTestDB(t *testing.T) *sql.DB {
	d := engine.OpenTestDB(t)
	engine.MustMigrate(d, migration)
	migrateMembers(d)
	return d
}
