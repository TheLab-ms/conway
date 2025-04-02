package waiver

import (
	"database/sql"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
	"github.com/julienschmidt/httprouter"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/waiver", m.renderWaiverView)
	router.Handle("POST", "/waiver", m.handleSubmitWaiver)
}

func (m *Module) renderWaiverView(r *http.Request, ps httprouter.Params) engine.Response {
	return engine.Component(renderWaiver(false, "", r.URL.Query().Get("email"), r.URL.Query().Get("r")))
}

func (m *Module) handleSubmitWaiver(r *http.Request, ps httprouter.Params) engine.Response {
	a1 := r.FormValue("agree1")
	a2 := r.FormValue("agree2")
	if a1 != "on" || a2 != "on" {
		return engine.ClientErrorf(400, "you must agree to all terms")
	}

	name := r.FormValue("name")
	email := r.FormValue("email")
	_, err := m.db.ExecContext(r.Context(), "INSERT INTO waivers (name, email, version) VALUES ($1, $2, 1)", name, email)
	if err != nil {
		return engine.Errorf("inserting signed waiver: %s", err)
	}

	return engine.Component(renderWaiver(true, name, email, r.FormValue("r")))
}
