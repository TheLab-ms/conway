package signs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/a-h/templ"
)

// renderTemplatesPanel is wired as Spec.ExtraContent for the signs config page.
// It renders the cleaned-up templates list (replacing the old generic
// JSON-blob array editor) below the printer config card.
func (m *Module) renderTemplatesPanel(ctx context.Context) templ.Component {
	_, templates := m.snapshot()
	return renderTemplatesListPanel(templates)
}

// --- editor handlers ---

func (m *Module) handleTemplateNew(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	tmpl := Template{Orientation: "portrait"}
	view := newEditorView(tmpl, true, "", "")
	renderTemplateEditor(view).Render(r.Context(), w)
}

func (m *Module) handleTemplateEdit(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	_, templates := m.snapshot()
	tmpl, ok := findTemplate(templates, slug)
	if !ok {
		engine.ClientError(w, "Not Found", "Unknown sign template.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	view := newEditorView(tmpl, false, "", r.URL.Query().Get("ok"))
	renderTemplateEditor(view).Render(r.Context(), w)
}

// templateForm holds the parsed editor submission.
type templateForm struct {
	OriginalSlug string
	Template     Template
}

// parseTemplateForm reads slug/name/description/orientation/body and the
// repeated field_name[]/field_label[]/field_placeholder[]/field_required[]/
// field_multiline[] groups into a Template + FieldsJSON.
func parseTemplateForm(r *http.Request) (templateForm, error) {
	if err := r.ParseForm(); err != nil {
		return templateForm{}, err
	}

	t := Template{
		Slug:        strings.TrimSpace(r.FormValue("slug")),
		Name:        strings.TrimSpace(r.FormValue("name")),
		Description: strings.TrimSpace(r.FormValue("description")),
		Orientation: strings.TrimSpace(r.FormValue("orientation")),
		Body:        r.FormValue("body"),
	}
	if t.Orientation != "landscape" {
		t.Orientation = "portrait"
	}

	names := r.Form["field_name[]"]
	labels := r.Form["field_label[]"]
	placeholders := r.Form["field_placeholder[]"]
	required := r.Form["field_required[]"]      // value per index ("on" or empty)
	multiline := r.Form["field_multiline[]"]    // value per index ("on" or empty)

	fields := make([]FieldDef, 0, len(names))
	for i, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		fd := FieldDef{Name: n}
		if i < len(labels) {
			fd.Label = strings.TrimSpace(labels[i])
		}
		if fd.Label == "" {
			fd.Label = n
		}
		if i < len(placeholders) {
			fd.Placeholder = strings.TrimSpace(placeholders[i])
		}
		if i < len(required) && truthy(required[i]) {
			fd.Required = true
		}
		if i < len(multiline) && truthy(multiline[i]) {
			fd.Multiline = true
		}
		fields = append(fields, fd)
	}

	if len(fields) == 0 {
		t.FieldsJSON = ""
	} else {
		t.FieldsJSON = mustMarshalFields(fields)
	}

	return templateForm{
		OriginalSlug: r.PathValue("slug"),
		Template:     t,
	}, nil
}

func truthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "on" || v == "true" || v == "1" || v == "yes"
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var fieldNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

func validateTemplate(t Template) string {
	if t.Name == "" {
		return "Name is required."
	}
	if !slugRe.MatchString(t.Slug) {
		return "Slug must start with a letter or digit and contain only lowercase letters, digits, dashes and underscores."
	}
	if strings.TrimSpace(t.Body) == "" {
		return "Body cannot be empty."
	}
	for _, fd := range t.ParsedFields() {
		if !fieldNameRe.MatchString(fd.Name) {
			return fmt.Sprintf("Field name %q is invalid. Use letters, digits and underscores; start with a letter.", fd.Name)
		}
	}
	return ""
}

func (m *Module) handleTemplateSave(w http.ResponseWriter, r *http.Request) {
	if m.configStore == nil {
		engine.SystemError(w, "config store not configured")
		return
	}

	form, err := parseTemplateForm(r)
	if err != nil {
		engine.ClientError(w, "Bad Request", err.Error(), http.StatusBadRequest)
		return
	}

	creating := form.OriginalSlug == "new"
	if msg := validateTemplate(form.Template); msg != "" {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadRequest)
		view := newEditorView(form.Template, creating, msg, "")
		renderTemplateEditor(view).Render(r.Context(), w)
		return
	}

	cfg, _, err := m.configStore.Load(r.Context(), "signs")
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	c, ok := cfg.(*Config)
	if !ok {
		engine.SystemError(w, "unexpected config type")
		return
	}

	// Locate existing entry by original slug; reject collisions if the slug
	// is being changed (or if creating with a duplicate slug).
	existingIdx := -1
	for i, t := range c.Templates {
		if t.Slug == form.OriginalSlug {
			existingIdx = i
			break
		}
	}
	if form.Template.Slug != form.OriginalSlug || creating {
		for i, t := range c.Templates {
			if t.Slug == form.Template.Slug && i != existingIdx {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusConflict)
				view := newEditorView(form.Template, creating,
					fmt.Sprintf("A template with slug %q already exists.", form.Template.Slug), "")
				renderTemplateEditor(view).Render(r.Context(), w)
				return
			}
		}
	}

	if existingIdx >= 0 && !creating {
		c.Templates[existingIdx] = form.Template
	} else {
		c.Templates = append(c.Templates, form.Template)
	}

	if err := m.configStore.Save(r.Context(), "signs", c, true); err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	m.eventLogger.LogEvent(r.Context(), 0, "TemplateSaved",
		form.Template.Slug, form.Template.Name, true, "")

	// Force-reload so UI reflects the change immediately.
	m.reloadConfig(r.Context())

	http.Redirect(w, r, "/admin/signs/templates/"+form.Template.Slug+"?ok=1", http.StatusSeeOther)
}

func (m *Module) handleTemplateDelete(w http.ResponseWriter, r *http.Request) {
	if m.configStore == nil {
		engine.SystemError(w, "config store not configured")
		return
	}
	slug := r.PathValue("slug")

	cfg, _, err := m.configStore.Load(r.Context(), "signs")
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	c, ok := cfg.(*Config)
	if !ok {
		engine.SystemError(w, "unexpected config type")
		return
	}

	filtered := make([]Template, 0, len(c.Templates))
	deletedName := ""
	for _, t := range c.Templates {
		if t.Slug == slug {
			deletedName = t.Name
			continue
		}
		filtered = append(filtered, t)
	}
	c.Templates = filtered
	if err := m.configStore.Save(r.Context(), "signs", c, true); err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	m.eventLogger.LogEvent(r.Context(), 0, "TemplateDeleted", slug, deletedName, true, "")
	m.reloadConfig(r.Context())
	http.Redirect(w, r, "/admin/config/signs", http.StatusSeeOther)
}

func (m *Module) handleTemplateDuplicate(w http.ResponseWriter, r *http.Request) {
	if m.configStore == nil {
		engine.SystemError(w, "config store not configured")
		return
	}
	slug := r.PathValue("slug")

	cfg, _, err := m.configStore.Load(r.Context(), "signs")
	if err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	c, ok := cfg.(*Config)
	if !ok {
		engine.SystemError(w, "unexpected config type")
		return
	}

	src, found := findTemplate(c.Templates, slug)
	if !found {
		engine.ClientError(w, "Not Found", "Unknown template.", http.StatusNotFound)
		return
	}

	// Pick the first available "{slug}-copy", "{slug}-copy-2", … name.
	taken := make(map[string]bool, len(c.Templates))
	for _, t := range c.Templates {
		taken[t.Slug] = true
	}
	dup := src
	dup.Slug = src.Slug + "-copy"
	for i := 2; taken[dup.Slug]; i++ {
		dup.Slug = fmt.Sprintf("%s-copy-%d", src.Slug, i)
	}
	dup.Name = src.Name + " (copy)"
	c.Templates = append(c.Templates, dup)

	if err := m.configStore.Save(r.Context(), "signs", c, true); err != nil {
		engine.SystemError(w, err.Error())
		return
	}
	m.eventLogger.LogEvent(r.Context(), 0, "TemplateDuplicated", dup.Slug, dup.Name, true,
		"source="+src.Slug)
	m.reloadConfig(r.Context())
	http.Redirect(w, r, "/admin/signs/templates/"+dup.Slug, http.StatusSeeOther)
}

// handlePreview renders the submitted template body+fields against sample
// values and returns the resulting PDF inline. Called from the editor's
// preview pane via fetch() / form-submit-into-iframe.
func (m *Module) handlePreview(w http.ResponseWriter, r *http.Request) {
	form, err := parseTemplateForm(r)
	if err != nil {
		previewError(w, "Bad request: "+err.Error())
		return
	}
	t := form.Template
	if strings.TrimSpace(t.Body) == "" {
		previewError(w, "Add some markdown content to see a preview.")
		return
	}

	// Sample values: use values posted as preview_<FieldName>; fall back
	// to placeholder text or "(SampleValue)".
	user := "preview-user"
	data := SignData{
		"DiscordHandle": user,
		"Date":          time.Now().Format("Mon Jan 2, 2006 3:04 PM"),
	}
	for _, fd := range t.ParsedFields() {
		v := strings.TrimSpace(r.FormValue("preview_" + fd.Name))
		if v == "" {
			v = fd.Placeholder
		}
		if v == "" {
			v = "(" + fd.Label + ")"
		}
		data[fd.Name] = v
	}

	pdf, err := RenderSign(t, data)
	if err != nil {
		previewError(w, "Render error: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="preview.pdf"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Write(pdf)
}

// previewError returns a small standalone HTML page that the preview iframe
// can render in place of the PDF when rendering fails. Returning HTML (vs.
// the default text/plain error) keeps the iframe styling consistent.
func previewError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	fmt.Fprintf(w,
		`<!doctype html><html><body style="margin:0;padding:24px;font-family:system-ui;background:#fff3cd;color:#664d03;">`+
			`<strong>Preview unavailable</strong><pre style="white-space:pre-wrap;margin-top:8px;font-size:13px;">%s</pre></body></html>`,
		templ.EscapeString(msg))
}

// --- editor view model ---

// editorView is the value passed to renderTemplateEditor. It pre-computes a
// few things (e.g. JSON sample-values payload, parsed fields) so the templ
// stays declarative.
type editorView struct {
	Template      Template
	IsNew         bool
	ErrorMessage  string
	JustSaved     bool
	Fields        []FieldDef
	OriginalSlug  string
	FieldsAsJSON  string // pretty JSON for the (collapsible) advanced view
	FormAction    string
}

func newEditorView(t Template, isNew bool, errMsg, ok string) editorView {
	v := editorView{
		Template:     t,
		IsNew:        isNew,
		ErrorMessage: errMsg,
		JustSaved:    ok != "",
		Fields:       t.ParsedFields(),
		OriginalSlug: t.Slug,
	}
	if isNew {
		v.OriginalSlug = "new"
		v.FormAction = "/admin/signs/templates/new"
	} else {
		v.FormAction = "/admin/signs/templates/" + t.Slug
	}
	if len(v.Fields) > 0 {
		b, _ := json.MarshalIndent(v.Fields, "", "  ")
		v.FieldsAsJSON = string(b)
	}
	return v
}

// templateMeta is a compact projection passed to the list panel.
type templateMeta struct {
	Slug        string
	Name        string
	Description string
	Orientation string
	FieldCount  int
}

func metaFor(t Template) templateMeta {
	return templateMeta{
		Slug:        t.Slug,
		Name:        t.Name,
		Description: t.Description,
		Orientation: t.Orientation,
		FieldCount:  len(t.ParsedFields()),
	}
}
