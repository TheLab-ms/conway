package waiver

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/TheLab-ms/conway/engine"
)

//go:generate go run github.com/a-h/templ/cmd/templ generate

const migration = `
CREATE TABLE IF NOT EXISTS waiver_content (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    content TEXT NOT NULL,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
) STRICT;

INSERT OR IGNORE INTO waiver_content (version, content) VALUES (
    1,
    '# Liability Waiver

This is a sample liability waiver. Please customize this content to match your organization''s requirements.

Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris.

1. I acknowledge that participation in activities may involve inherent risks and I voluntarily assume all such risks.

2. I understand that I am personally responsible for my safety and actions while on the premises.

3. I affirm that I am at least 18 years of age and mentally competent to sign this liability waiver.

- [ ] By checking here, you are consenting to the use of your electronic signature in lieu of an original signature on paper.
- [ ] By checking this box, I agree and acknowledge to be bound by this waiver and release.'
);
`

type Module struct {
	db *sql.DB
}

func New(db *sql.DB) *Module {
	engine.MustMigrate(db, migration)
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
