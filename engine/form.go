package engine

import (
	"database/sql"
	"net/http"
)

type FormHandler struct {
	Query  string
	Fields []string
}

func (f *FormHandler) Handler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		args := []any{
			sql.Named("route_id", r.PathValue("id")),
		}
		for _, field := range f.Fields {
			args = append(args, sql.Named(field, r.FormValue(field)))
		}

		_, err := db.ExecContext(r.Context(), f.Query, args...)
		if err != nil {
			SystemError(w, err.Error())
			return
		}

		http.Redirect(w, r, r.Referer(), http.StatusSeeOther)
	}
}
