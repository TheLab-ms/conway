package waiver

import (
	"database/sql"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /waiver", m.renderWaiverView)
	router.HandleFunc("POST /waiver", m.handleSubmitWaiver)
}

func (m *Module) renderWaiverView(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	renderWaiver(false, "", r.URL.Query().Get("email"), r.URL.Query().Get("r")).Render(r.Context(), w)
}

func (m *Module) handleSubmitWaiver(w http.ResponseWriter, r *http.Request) {
	a1 := r.FormValue("agree1")
	a2 := r.FormValue("agree2")
	if a1 != "on" || a2 != "on" {
		engine.ClientError(w, "Agreement Required", "You must agree to all terms to continue", 400)
		return
	}

	name := r.FormValue("name")
	email := r.FormValue("email")
	_, err := m.db.ExecContext(r.Context(), "INSERT INTO waivers (name, email, version) VALUES ($1, $2, 1) ON CONFLICT DO NOTHING", name, email)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/html")
	renderWaiver(true, name, email, r.FormValue("r")).Render(r.Context(), w)
}
