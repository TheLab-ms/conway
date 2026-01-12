package waiver

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	return &Module{db: db}
}

func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("GET /waiver", m.renderWaiverView)
	router.HandleFunc("POST /waiver", m.handleSubmitWaiver)
}

type WaiverContent struct {
	Version int
	Content string
}

func (m *Module) getLatestWaiverContent(ctx context.Context) (*WaiverContent, error) {
	row := m.db.QueryRowContext(ctx,
		"SELECT version, content FROM waiver_content ORDER BY version DESC LIMIT 1")

	content := &WaiverContent{}
	err := row.Scan(&content.Version, &content.Content)
	if err != nil {
		return nil, fmt.Errorf("no waiver content configured: %w", err)
	}
	return content, nil
}

func (m *Module) renderWaiverView(w http.ResponseWriter, r *http.Request) {
	content, err := m.getLatestWaiverContent(r.Context())
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	parsed := ParseWaiverMarkdown(content.Content)
	w.Header().Set("Content-Type", "text/html")
	renderWaiver(false, "", r.URL.Query().Get("email"), r.URL.Query().Get("r"), content.Version, parsed).Render(r.Context(), w)
}

func (m *Module) handleSubmitWaiver(w http.ResponseWriter, r *http.Request) {
	content, err := m.getLatestWaiverContent(r.Context())
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	parsed := ParseWaiverMarkdown(content.Content)

	// Validate all checkboxes are checked
	for i := range parsed.Checkboxes {
		if r.FormValue(fmt.Sprintf("agree%d", i)) != "on" {
			engine.ClientError(w, "Agreement Required", "You must agree to all terms to continue", 400)
			return
		}
	}

	name := r.FormValue("name")
	email := r.FormValue("email")
	_, err = m.db.ExecContext(r.Context(), "INSERT INTO waivers (name, email, version) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", name, email, content.Version)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/html")
	renderWaiver(true, name, email, r.FormValue("r"), content.Version, parsed).Render(r.Context(), w)
}
