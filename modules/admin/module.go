package admin

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
	var nav []*navbarTab
	for _, view := range listViews {
		route := "/admin" + view.RelPath
		nav = append(nav, &navbarTab{Title: view.Title, Path: route})

		router.Handle("GET", route, router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
			return engine.Component(renderAdminList(nav, "Members", "/admin/search"+view.RelPath))
		})))

		router.Handle("POST", "/admin/search"+view.RelPath, router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
			q, args := view.BuildQuery(r)
			results, err := m.db.QueryContext(r.Context(), q, args...)
			if err != nil {
				return engine.Errorf("querying the database: %s", err)
			}
			defer results.Close()

			rows := view.BuildRows(results)
			if err := results.Err(); err != nil {
				return engine.Errorf("scanning the query results: %s", err)
			}

			return engine.Component(renderAdminListElements(view.Rows, rows))
		})))
	}

	router.Handle("GET", "/admin", router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
		return engine.Redirect(nav[0].Path, http.StatusSeeOther)
	})))

	router.Handle("GET", "/admin/members/:id", router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
		mem, events, err := querySingleMember(r.Context(), m.db, ps.ByName("id"))
		if err != nil {
			return engine.Errorf("querying the database: %s", err)
		}
		return engine.Component(renderSingleMember(nav, mem, events))
	})))

	for _, handle := range formHandlers {
		router.Handle("POST", handle.Path, router.WithAuth(m.onlyLeadership(handle.BuildHandler(m.db))))
	}
}

func (m *Module) onlyLeadership(next engine.Handler) engine.Handler {
	return func(r *http.Request, ps httprouter.Params) engine.Response {
		if meta := auth.GetUserMeta(r.Context()); meta == nil || !meta.Leadership {
			return engine.ClientErrorf("You must be a member of leadership to access this page")
		}
		return next(r, ps)
	}
}
