package engine

import (
	"database/sql"
	"net/http"
)

type PostFormHandler struct {
	Query  string
	Fields []string
}

func (p *PostFormHandler) Handler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		args := []any{
			sql.Named("route_id", r.PathValue("id")),
		}
		for _, field := range p.Fields {
			args = append(args, sql.Named(field, r.FormValue(field)))
		}

		_, err := db.ExecContext(r.Context(), p.Query, args...)
		if err != nil {
			SystemError(w, err.Error())
			return
		}

		http.Redirect(w, r, r.Referer(), http.StatusSeeOther)
	}
}

type DeleteFormHandler struct {
	Table    string
	Redirect string
}

func (d *DeleteFormHandler) Handler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, err := db.ExecContext(r.Context(), "DELETE FROM "+d.Table+" WHERE id = $1", r.PathValue("id"))
		if err != nil {
			SystemError(w, err.Error())
			return
		}
		http.Redirect(w, r, d.Redirect, http.StatusSeeOther)
	}
}
