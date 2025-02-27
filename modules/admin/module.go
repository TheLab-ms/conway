package admin

import (
	"database/sql"
	"net/http"
	"strconv"

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
		view := view
		route := "/admin" + view.RelPath
		nav = append(nav, &navbarTab{Title: view.Title, Path: route})

		router.Handle("GET", route, router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
			return engine.Component(renderAdminList(nav, view.Title, "/admin/search"+view.RelPath))
		})))

		router.Handle("POST", "/admin/search"+view.RelPath, router.WithAuth(m.onlyLeadership(func(r *http.Request, ps httprouter.Params) engine.Response {
			const limit = 20
			txn, err := m.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return engine.Errorf("starting db transaction: %s", err)
			}
			defer txn.Rollback()

			q, rowCountQuery, args := view.BuildQuery(r)

			// Get the row count
			var rowCount int64
			err = txn.QueryRowContext(r.Context(), rowCountQuery, args...).Scan(&rowCount)
			if err != nil {
				return engine.Errorf("getting row count: %s", err)
			}
			currentPage, _ := strconv.ParseInt(r.FormValue("currentpage"), 10, 0)

			// Query
			args = append(args, sql.Named("limit", limit), sql.Named("offset", max(currentPage-1, 0)*limit))
			results, err := txn.QueryContext(r.Context(), q, args...)
			if err != nil {
				return engine.Errorf("querying the database: %s", err)
			}
			defer results.Close()

			rows, err := view.BuildRows(results)
			if err != nil {
				return engine.Errorf("scanning the query results: %s", err)
			}

			return engine.Component(renderAdminListElements(view.Rows, rows, max(currentPage, 1), max(rowCount/limit, 1)))
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
			return engine.ClientErrorf(403, "You must be a member of leadership to access this page")
		}
		return next(r, ps)
	}
}
