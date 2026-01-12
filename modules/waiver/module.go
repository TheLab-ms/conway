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

var defaultContent = &WaiverContent{
	Version: 1,
	Content: `# TheLab Liability Waiver

I agree and acknowledge as follows:

1. I WAIVE ANY AND ALL RIGHTS OF RECOVERY, CLAIM, ACTION OR CAUSE OF ACTION AGAINST THELAB.MS FOR ANY INJURY OR DAMAGE THAT MAY OCCUR, REGARDLESS OF CAUSE OR ORIGIN, INCLUDING NEGLIGENCE AND GROSS NEGLIGENCE.

2. I also understand that I am personally responsible for my safety and actions and that I will follow all safety instructions and signage while at TheLab.ms.

3. I affirm that I am at least 18 years of age and mentally competent to sign this liability waiver.

- [ ] By checking here, you are consenting to the use of your electronic signature in lieu of an original signature on paper.
- [ ] By checking this box, I agree and acknowledge to be bound by this waiver and release and further agree and acknowledge that this waiver and release shall also apply to all of my future participation in TheLab.
`,
}

func (m *Module) getLatestWaiverContent(ctx context.Context) *WaiverContent {
	row := m.db.QueryRowContext(ctx,
		"SELECT version, content FROM waiver_content ORDER BY version DESC LIMIT 1")

	content := &WaiverContent{}
	err := row.Scan(&content.Version, &content.Content)
	if err != nil {
		return defaultContent
	}
	return content
}

func (m *Module) renderWaiverView(w http.ResponseWriter, r *http.Request) {
	content := m.getLatestWaiverContent(r.Context())
	parsed := ParseWaiverMarkdown(content.Content)
	w.Header().Set("Content-Type", "text/html")
	renderWaiver(false, "", r.URL.Query().Get("email"), r.URL.Query().Get("r"), content.Version, parsed).Render(r.Context(), w)
}

func (m *Module) handleSubmitWaiver(w http.ResponseWriter, r *http.Request) {
	content := m.getLatestWaiverContent(r.Context())
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
	_, err := m.db.ExecContext(r.Context(), "INSERT INTO waivers (name, email, version) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", name, email, content.Version)
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/html")
	renderWaiver(true, name, email, r.FormValue("r"), content.Version, parsed).Render(r.Context(), w)
}
