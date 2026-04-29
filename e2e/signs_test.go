package e2e

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// noRedirectClient returns an HTTP client that surfaces 3xx responses
// instead of automatically following them, so tests can assert on the
// auth redirect behaviour.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// authedRequest returns a request with an auth cookie for memberID set.
func authedRequest(t *testing.T, env *TestEnv, memberID int64, method, path string, body *strings.Reader) *http.Request {
	t.Helper()
	var r *http.Request
	var err error
	if body != nil {
		r, err = http.NewRequest(method, env.baseURL+path, body)
	} else {
		r, err = http.NewRequest(method, env.baseURL+path, nil)
	}
	require.NoError(t, err)
	token := generateAuthToken(t, env, memberID)
	r.AddCookie(&http.Cookie{Name: "token", Value: token, Path: "/"})
	return r
}

func TestSigns_RequireAuth(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	resp, err := noRedirectClient().Get(env.baseURL + "/signs")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusFound, resp.StatusCode, "expected redirect to login")
	loc := resp.Header.Get("Location")
	require.Contains(t, loc, "/login", "redirect should point to /login, got %q", loc)
}

func TestSigns_IndexShowsTemplate(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "signsmember@example.com",
		WithConfirmed(), WithActiveStripeSubscription())

	req := authedRequest(t, env, memberID, http.MethodGet, "/signs", nil)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	buf := new(bytes.Buffer)
	_, err = buf.ReadFrom(resp.Body)
	require.NoError(t, err)
	body := buf.String()
	// DefaultMaintenanceTemplate.Name is "Out of Service" — keep this
	// literal assertion as a smoke test, but also assert the structural
	// link to the template's form so we don't depend solely on copy.
	require.Contains(t, body, "Out of Service", "index should list the default template name")
	require.Contains(t, body, "/signs/maintenance", "index should link to the maintenance form")
}

func TestSigns_IndexBlocksInactiveMembers(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	// Only confirmed — no active subscription / fob => inactive.
	memberID := seedMember(t, env, "inactive@example.com", WithConfirmed())

	req := authedRequest(t, env, memberID, http.MethodGet, "/signs", nil)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode,
		"inactive members should be 403'd from /signs")
}

func TestSigns_SubmitQueuesAndPrints(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	require.NotNil(t, env.SignsModule, "SignsModule should be wired into TestEnv")
	require.NotNil(t, env.SignsPrinter, "SignsPrinter should be wired into TestEnv")

	memberID := seedMember(t, env, "signs-submit@example.com",
		WithConfirmed(), WithActiveStripeSubscription(), WithDiscordUsername("alice"))

	form := url.Values{
		"field_MachineName": {"Bambu X1C"},
		"field_Issue":       {"Nozzle clogged"},
	}
	req := authedRequest(t, env, memberID, http.MethodPost, "/signs/maintenance",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	// submit() responds with 303 SeeOther on success.
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "expected redirect after submit")

	// Confirm a row was queued.
	var queued int
	err = env.db.QueryRow(`SELECT COUNT(*) FROM signs_print_queue`).Scan(&queued)
	require.NoError(t, err)
	require.Equal(t, 1, queued, "expected 1 queued print")

	// Drain the queue synchronously. ProcessOne intentionally bypasses the
	// background worker loop so tests can run without sleeping or racing
	// the scheduler.
	processed := env.SignsModule.ProcessOne(context.Background())
	require.True(t, processed, "ProcessOne should report work was done")

	jobs := env.SignsPrinter.Jobs()
	require.Len(t, jobs, 1, "fake printer should have received exactly one job")
	require.NotEmpty(t, jobs[0].PDF, "PDF bytes must not be empty")
	require.True(t, bytes.HasPrefix(jobs[0].PDF, []byte("%PDF-")),
		"PDF bytes should start with %%PDF-, got %q", string(jobs[0].PDF[:min(8, len(jobs[0].PDF))]))
	require.Contains(t, jobs[0].JobName, "Bambu X1C", "job name should include machine name")

	// Queue should now be empty.
	err = env.db.QueryRow(`SELECT COUNT(*) FROM signs_print_queue`).Scan(&queued)
	require.NoError(t, err)
	require.Equal(t, 0, queued, "queue should be empty after successful processing")
}

// TestSigns_RecentPrintsPanel asserts the index renders the recent prints
// panel after at least one print exists in the queue/history.
func TestSigns_RecentPrintsPanel(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "signs-recents@example.com",
		WithConfirmed(), WithActiveStripeSubscription(), WithDiscordUsername("alice"))

	form := url.Values{
		"field_MachineName": {"Bambu X1C"},
		"field_Issue":       {"Nozzle clogged"},
	}
	req := authedRequest(t, env, memberID, http.MethodPost, "/signs/maintenance",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// Index should now render the "Recent prints" panel including the
	// machine name we just submitted.
	idxReq := authedRequest(t, env, memberID, http.MethodGet, "/signs", nil)
	idxResp, err := noRedirectClient().Do(idxReq)
	require.NoError(t, err)
	defer idxResp.Body.Close()
	require.Equal(t, http.StatusOK, idxResp.StatusCode)
	buf := new(bytes.Buffer)
	_, err = buf.ReadFrom(idxResp.Body)
	require.NoError(t, err)
	body := buf.String()
	require.Contains(t, body, "Recent prints", "recent prints panel should render after a submit")
	require.Contains(t, body, "Bambu X1C", "machine name should appear in recent prints")
}

func TestSigns_SubmitRequiresAuth(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	form := url.Values{
		"field_MachineName": {"Some Machine"},
		"field_Issue":       {"Some issue"},
	}
	req, err := http.NewRequest(http.MethodPost, env.baseURL+"/signs/maintenance",
		strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Auth middleware redirects (302) unauthenticated requests to /login.
	require.Equal(t, http.StatusFound, resp.StatusCode,
		"expected redirect to login on unauthenticated POST")
	require.Contains(t, resp.Header.Get("Location"), "/login")

	var queued int
	err = env.db.QueryRow(`SELECT COUNT(*) FROM signs_print_queue`).Scan(&queued)
	require.NoError(t, err)
	require.Equal(t, 0, queued, "no row should be queued for unauthenticated POST")
}

func TestSigns_UnknownTemplate(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "signs-404@example.com",
		WithConfirmed(), WithActiveStripeSubscription())

	req := authedRequest(t, env, memberID, http.MethodGet, "/signs/nonexistent-template", nil)
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestSigns_SeedConfigHelper exercises the seedSignsConfig helper to
// guarantee it produces a row that can be parsed by the module's loader.
func TestSigns_SeedConfigHelper(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	seedSignsConfig(t, env, "test.local", 631, "TestPrinter")

	var host, queue string
	var port int
	err := env.db.QueryRow(
		`SELECT printer_host, printer_port, printer_queue
		 FROM signs_config ORDER BY version DESC LIMIT 1`).
		Scan(&host, &port, &queue)
	require.NoError(t, err)
	require.Equal(t, "test.local", host)
	require.Equal(t, 631, port)
	require.Equal(t, "TestPrinter", queue)
}
