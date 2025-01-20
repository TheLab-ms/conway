package api

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/TheLab-ms/conway/engine"
	"github.com/google/uuid"
	"github.com/julienschmidt/httprouter"
)

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) (*Module, error) {
	var id int
	if err := db.QueryRow("SELECT id FROM api_tokens").Scan(&id); err != nil {
		slog.Info("generating initial API token...")
		token := uuid.Must(uuid.NewRandom()).String() + "-" + uuid.Must(uuid.NewRandom()).String() // mega uuid lol
		_, err = db.Exec("INSERT INTO api_tokens (label, token) VALUES ('Automatically generated', ?)", token)
		if err != nil {
			return nil, err
		}
	}
	return &Module{db: db}, nil
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.Handle("GET", "/api/members", m.withAuth(m.handleListMembers))
	router.Handle("PATCH", "/api/members/:id", m.withAuth(m.handlePatchMember))
	router.Handle("DELETE", "/api/members/:id", m.withAuth(m.handleDeleteMember))
}

func (m *Module) withAuth(next engine.Handler) engine.Handler {
	return func(r *http.Request, ps httprouter.Params) engine.Response {
		var id int
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		err := m.db.QueryRowContext(r.Context(), "SELECT id FROM api_tokens WHERE token = $1", token).Scan(&id)
		if err != nil {
			return engine.Unauthorized(err)
		}
		return next(r, ps)
	}
}

func (m *Module) handleListMembers(r *http.Request, ps httprouter.Params) engine.Response {
	val, err := queryToJSON(m.db, "SELECT * FROM members")
	if err != nil {
		return engine.Error(err)
	}
	return engine.JSON(val)
}

func (m *Module) handlePatchMember(r *http.Request, ps httprouter.Params) engine.Response {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return engine.ClientErrorf("reading request body: %s", err)
	}

	err = jsonToTable(m.db, "members", "email", ps.ByName("id"), raw)
	if err != nil {
		return engine.Error(err)
	}

	return engine.Empty()
}

func (m *Module) handleDeleteMember(r *http.Request, ps httprouter.Params) engine.Response {
	result, err := m.db.ExecContext(r.Context(), "DELETE FROM members WHERE email = $1", ps.ByName("id"))
	if err != nil {
		return engine.Error(err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return engine.NotFoundf("member not found")
	}
	return engine.Empty()
}
