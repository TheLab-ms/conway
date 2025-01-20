package profile

import (
	"database/sql"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
	"github.com/julienschmidt/httprouter"
)

//go:generate templ generate

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module { return &Module{db: db} }

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/profile", router.WithAuth(m.renderProfileView))
}

func (m *Module) renderProfileView(r *http.Request, ps httprouter.Params) engine.Response {
	mem := &member{
		SubscriptionID: "", // TODO
	}
	return engine.Component(renderProfile(mem))
}
