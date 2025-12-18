package members

import (
	"database/sql"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /{$}", router.WithAuthn(m.renderMemberView))
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

	w.Header().Set("Content-Type", "text/html")
	renderMember(&mem).Render(r.Context(), w)
}
