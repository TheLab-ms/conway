package members

import (
	"database/sql"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/julienschmidt/httprouter"
)

//go:generate templ generate

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/", router.WithAuth(m.renderMemberView))
}

func (m *Module) renderMemberView(r *http.Request, ps httprouter.Params) engine.Response {
	authdUser := auth.GetUserMeta(r.Context()).ID

	mem := member{}
	err := m.db.QueryRowContext(r.Context(), `SELECT id, email, m.access_status FROM members m WHERE m.id = $1`, authdUser).Scan(&mem.ID, &mem.Email, &mem.AccessStatus)
	if err != nil {
		return engine.Errorf("querying the database: %s", err)
	}

	return engine.Component(renderMember(&mem))
}
