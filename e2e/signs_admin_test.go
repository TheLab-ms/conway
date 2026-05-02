package e2e

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These HTTP-level tests cover the admin-side template editor introduced
// alongside the signs UX overhaul (modules/signs/admin.go + admin.templ).
// They mirror the style of signs_test.go (raw HTTP, JWT cookie auth) and
// intentionally avoid Playwright — the editor is heavy on inline JS but
// the server contract (form keys, redirects, status codes, PDF preview)
// is what we want to lock down with regression tests.

// adminSignsRequest authenticates as a leadership member.
func adminSignsRequest(t *testing.T, env *TestEnv, adminID int64, method, path, body string) *http.Response {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := authedRequest(t, env, adminID, method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	return resp
}

// readBody drains and closes a response body, returning the contents.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	require.NoError(t, err)
	return buf.String()
}

func TestSignsAdmin_RequiresLeadership(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	// Confirmed + active subscription, but NOT leadership.
	memberID := seedMember(t, env, "non-leader@example.com",
		WithConfirmed(), WithActiveStripeSubscription())

	paths := []string{
		"/admin/config/signs",
		"/admin/signs/templates/new",
		"/admin/signs/templates/maintenance",
	}
	for _, p := range paths {
		req := authedRequest(t, env, memberID, http.MethodGet, p, nil)
		resp, err := noRedirectClient().Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equalf(t, http.StatusForbidden, resp.StatusCode,
			"non-leadership member should be 403'd from %s, got %d", p, resp.StatusCode)
	}
}

func TestSignsAdmin_ConfigPageListsTemplates(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "signs-admin@example.com",
		WithConfirmed(), WithLeadership())

	resp := adminSignsRequest(t, env, adminID, http.MethodGet, "/admin/config/signs", "")
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// The new templates panel rendered via Spec.ExtraContent should show
	// the seeded default template plus the "+ New Template" affordance.
	require.Contains(t, body, "Out of Service",
		"templates panel should list the default template")
	require.Contains(t, body, "/admin/signs/templates/new",
		"page should link to the new-template editor")
	require.Contains(t, body, "/admin/signs/templates/maintenance",
		"page should link to the existing template editor")

	// The old generic JSON-blob ArrayField editor must NOT render — it's
	// the entire reason for this overhaul. Any leftover textarea named
	// templates[N][fields_json] would indicate the Hidden flag broke.
	require.NotContains(t, body, `name="templates[0][fields_json]"`,
		"generic JSON editor should not render for the Hidden Templates field")
}

func TestSignsAdmin_NewTemplateEditorRenders(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "signs-admin@example.com",
		WithConfirmed(), WithLeadership())

	resp := adminSignsRequest(t, env, adminID, http.MethodGet, "/admin/signs/templates/new", "")
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `id="t-name"`, "editor should render the name input")
	require.Contains(t, body, `id="t-slug"`, "editor should render the slug input")
	require.Contains(t, body, `id="t-body"`, "editor should render the markdown body textarea")
	require.Contains(t, body, `id="preview-frame"`, "editor should render the preview iframe")
	require.Contains(t, body, `action="/admin/signs/templates/new"`,
		"new editor form should POST to the new endpoint")
}

func TestSignsAdmin_EditTemplateRendersExisting(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "signs-admin@example.com",
		WithConfirmed(), WithLeadership())

	resp := adminSignsRequest(t, env, adminID, http.MethodGet,
		"/admin/signs/templates/maintenance", "")
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "Out of Service",
		"editor should pre-fill the existing template's name")
	require.Contains(t, body, `value="maintenance"`,
		"editor should pre-fill the existing slug")
	require.Contains(t, body, `name="field_name[]"`,
		"editor should render at least one field row for the existing template's fields")
}

func TestSignsAdmin_EditUnknownTemplateIs404(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "signs-admin@example.com",
		WithConfirmed(), WithLeadership())

	resp := adminSignsRequest(t, env, adminID, http.MethodGet,
		"/admin/signs/templates/does-not-exist", "")
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestSignsAdmin_CreateTemplateSavesAndRedirects(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "signs-admin@example.com",
		WithConfirmed(), WithLeadership())

	form := url.Values{
		"slug":                    {"safety-notice"},
		"name":                    {"Safety Notice"},
		"description":             {"Posted when a hazard is identified."},
		"orientation":             {"portrait"},
		"body":                    {"# SAFETY NOTICE\n\nIssue: **{{.Hazard}}**"},
		"field_name[]":            {"Hazard"},
		"field_label[]":           {"Hazard"},
		"field_placeholder[]":     {"Describe the hazard"},
		"field_required[]":        {"on"},
		"field_multiline[]":       {""},
	}
	resp := adminSignsRequest(t, env, adminID, http.MethodPost,
		"/admin/signs/templates/new", form.Encode())
	resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode,
		"successful save should 303 to the editor for the saved template")
	require.Equal(t, "/admin/signs/templates/safety-notice?ok=1", resp.Header.Get("Location"))

	// Round-trip: the templates panel should now include the new template,
	// and the member-facing form should accept submissions for its slug.
	listResp := adminSignsRequest(t, env, adminID, http.MethodGet, "/admin/config/signs", "")
	listBody := readBody(t, listResp)
	require.Contains(t, listBody, "Safety Notice")
	require.Contains(t, listBody, "/admin/signs/templates/safety-notice")
}

func TestSignsAdmin_CreateTemplateRejectsBadSlug(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "signs-admin@example.com",
		WithConfirmed(), WithLeadership())

	form := url.Values{
		"slug":        {"BadSlug!"}, // uppercase + punctuation -> rejected
		"name":        {"Bad"},
		"orientation": {"portrait"},
		"body":        {"# Hi"},
	}
	resp := adminSignsRequest(t, env, adminID, http.MethodPost,
		"/admin/signs/templates/new", form.Encode())
	body := readBody(t, resp)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, body, "Slug must")
}

func TestSignsAdmin_CreateTemplateRejectsSlugCollision(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "signs-admin@example.com",
		WithConfirmed(), WithLeadership())

	form := url.Values{
		"slug":        {"maintenance"}, // already exists (default template)
		"name":        {"Conflict"},
		"orientation": {"portrait"},
		"body":        {"# Hi"},
	}
	resp := adminSignsRequest(t, env, adminID, http.MethodPost,
		"/admin/signs/templates/new", form.Encode())
	body := readBody(t, resp)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Contains(t, body, "already exists")
}

func TestSignsAdmin_DuplicateTemplate(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "signs-admin@example.com",
		WithConfirmed(), WithLeadership())

	resp := adminSignsRequest(t, env, adminID, http.MethodPost,
		"/admin/signs/templates/maintenance/duplicate", "")
	resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/admin/signs/templates/maintenance-copy", resp.Header.Get("Location"))

	// Second duplicate should pick the "-copy-2" suffix.
	resp2 := adminSignsRequest(t, env, adminID, http.MethodPost,
		"/admin/signs/templates/maintenance/duplicate", "")
	resp2.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp2.StatusCode)
	require.Equal(t, "/admin/signs/templates/maintenance-copy-2", resp2.Header.Get("Location"))
}

func TestSignsAdmin_DeleteTemplate(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "signs-admin@example.com",
		WithConfirmed(), WithLeadership())

	// Create a throwaway template first so we don't delete the default
	// one that other tests in this package might rely on (each test runs
	// against its own env, but keeping the default around guards against
	// flakiness if the TestEnv ever becomes shared).
	create := url.Values{
		"slug":        {"trash-me"},
		"name":        {"Trash"},
		"orientation": {"portrait"},
		"body":        {"# Trash"},
	}
	createResp := adminSignsRequest(t, env, adminID, http.MethodPost,
		"/admin/signs/templates/new", create.Encode())
	createResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, createResp.StatusCode)

	delResp := adminSignsRequest(t, env, adminID, http.MethodPost,
		"/admin/signs/templates/trash-me/delete", "")
	delResp.Body.Close()
	require.Equal(t, http.StatusSeeOther, delResp.StatusCode)
	require.Equal(t, "/admin/config/signs", delResp.Header.Get("Location"))

	// The editor should now 404 for the deleted slug.
	gone := adminSignsRequest(t, env, adminID, http.MethodGet,
		"/admin/signs/templates/trash-me", "")
	gone.Body.Close()
	require.Equal(t, http.StatusNotFound, gone.StatusCode)
}

func TestSignsAdmin_PreviewReturnsPDF(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "signs-admin@example.com",
		WithConfirmed(), WithLeadership())

	form := url.Values{
		"slug":        {"preview-test"},
		"name":        {"Preview Test"},
		"orientation": {"portrait"},
		"body":        {"# Preview\n\nValue: **{{.Foo}}**"},
		"field_name[]":  {"Foo"},
		"field_label[]": {"Foo"},
		"preview_Foo":   {"hello-world"},
	}
	resp := adminSignsRequest(t, env, adminID, http.MethodPost,
		"/admin/signs/preview", form.Encode())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/pdf", resp.Header.Get("Content-Type"))
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")),
		"preview body should start with %%PDF- magic, got %q",
		string(buf.Bytes()[:min(8, buf.Len())]))
}

func TestSignsAdmin_PreviewEmptyBodyShowsHTMLError(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "signs-admin@example.com",
		WithConfirmed(), WithLeadership())

	form := url.Values{
		"slug":        {"empty"},
		"name":        {"Empty"},
		"orientation": {"portrait"},
		"body":        {"   "}, // whitespace only -> rejected
	}
	resp := adminSignsRequest(t, env, adminID, http.MethodPost,
		"/admin/signs/preview", form.Encode())
	body := readBody(t, resp)
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html",
		"empty-body preview should return styled HTML, not text/plain")
	require.Contains(t, body, "Preview unavailable")
}
