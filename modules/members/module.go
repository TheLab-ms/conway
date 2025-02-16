package members

import (
	"database/sql"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/auth"
	"github.com/julienschmidt/httprouter"
)

// TODO: Snapshot tests

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

	// TODO:
	// - View contact info (?)
	// - View payment status w/ link
	//   - Details for payment status
	// - View badge status

	mem := member{}
	err := m.db.QueryRowContext(r.Context(), `SELECT id, m.access_status FROM members m WHERE m.id = $1`, authdUser).Scan(&mem.ID, &mem.AccessStatus)
	if err != nil {
		return engine.Errorf("querying the database: %s", err)
	}

	return engine.Component(renderMember(&mem))
}

// TODO
// func (m *Module) updateMemberBasics(r *http.Request, ps httprouter.Params) engine.Response {
// 	id := ps.ByName("id")
// 	name := r.FormValue("name")
// 	email := r.FormValue("email")
// 	fobID, _ := strconv.ParseInt(r.FormValue("fob_id"), 10, 64)
// 	adminNotes := r.FormValue("admin_notes")

// 	_, err := m.db.ExecContext(r.Context(), "UPDATE members SET name = $1, email = $2,  fob_id = $3, admin_notes = $4 WHERE id = $5", name, email, fobID, adminNotes, id)
// 	if err != nil {
// 		return engine.Errorf("updating member: %s", err)
// 	}

// 	return engine.Redirect(fmt.Sprintf("/admin/members/%s", id), http.StatusSeeOther)
// }
