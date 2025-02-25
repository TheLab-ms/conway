package engine

import (
	"database/sql"
	"net/http"

	"github.com/julienschmidt/httprouter"
)

type PostFormHandler struct {
	Query  string
	Fields []string
}

func (p *PostFormHandler) Handler(db *sql.DB) Handler {
	return func(r *http.Request, ps httprouter.Params) Response {
		args := []any{
			sql.Named("route_id", ps.ByName("id")),
		}
		for _, field := range p.Fields {
			args = append(args, sql.Named(field, r.FormValue(field)))
		}

		_, err := db.ExecContext(r.Context(), p.Query, args...)
		if err != nil {
			return Errorf("updating member: %s", err)
		}

		return Redirect(r.Referer(), http.StatusSeeOther)
	}
}
