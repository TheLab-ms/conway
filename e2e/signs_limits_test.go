package e2e

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSigns_RateLimit5PerMinute verifies the per-member 5/minute submission
// rate limit on POST /signs/{slug}. The 6th submission within 60s should
// return 429.
func TestSigns_RateLimit5PerMinute(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "ratelimit@example.com",
		WithConfirmed(), WithActiveStripeSubscription())

	form := url.Values{
		"field_MachineName": {"Bambu X1C"},
		"field_Issue":       {"Nozzle clogged"},
	}
	body := form.Encode()

	post := func() int {
		req := authedRequest(t, env, memberID, http.MethodPost,
			"/signs/maintenance", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := noRedirectClient().Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}

	// First 5 should succeed (303).
	for i := 0; i < 5; i++ {
		got := post()
		require.Equal(t, http.StatusSeeOther, got, "submit #%d should succeed", i+1)
	}

	// 6th in the same minute should be rate-limited.
	require.Equal(t, http.StatusTooManyRequests, post(),
		"6th submit within 60s should return 429")
}

// TestSigns_OutstandingLimit20 verifies the per-member 20-outstanding cap.
// We pre-seed the queue with 20 rows directly so we don't need to wait for
// the rate limiter to roll over.
func TestSigns_OutstandingLimit20(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "outstanding@example.com",
		WithConfirmed(), WithActiveStripeSubscription())

	// Insert 20 outstanding rows with old `created` timestamps so they fall
	// outside the 60s rate-limit window but still count toward the
	// outstanding cap. send_at is set far in the future to keep the worker
	// from draining them before our assertion.
	for i := 0; i < 20; i++ {
		_, err := env.db.Exec(`
			INSERT INTO signs_print_queue
			    (created, send_at, member_id, discord_username, template_slug,
			     machine_name, issue, fields_json)
			VALUES (unixepoch() - 3600, unixepoch() + 86400, ?, '', 'maintenance',
			        '', '', '{"MachineName":"M","Issue":"I"}')`, memberID)
		require.NoError(t, err)
	}

	// Sanity check: 20 rows, none counted toward the per-minute window.
	var outstanding, recent int
	require.NoError(t, env.db.QueryRow(
		`SELECT COUNT(*) FROM signs_print_queue WHERE member_id = ?`, memberID).Scan(&outstanding))
	require.Equal(t, 20, outstanding)
	require.NoError(t, env.db.QueryRow(
		`SELECT COUNT(*) FROM signs_print_queue WHERE member_id = ? AND created > unixepoch() - 60`,
		memberID).Scan(&recent))
	require.Equal(t, 0, recent, "old rows should not count toward the per-minute window")

	// 21st POST should be rejected with 429.
	form := url.Values{
		"field_MachineName": {"Bambu X1C"},
		"field_Issue":       {"21st"},
	}
	req := authedRequest(t, env, memberID, http.MethodPost, "/signs/maintenance",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode,
		"21st outstanding submission should return 429")
}

// TestSigns_NonRetryableTemplateError verifies that a malformed submission
// (missing required fields against a known template) returns a non-retryable
// 4xx response and never reaches the print queue. The submit handler treats
// missing required fields as a hard client error and re-renders the form
// with status 400 — we assert that contract here so a regression cannot
// silently start retrying or queueing broken rows.
func TestSigns_NonRetryableTemplateError(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "broken-tmpl@example.com",
		WithConfirmed(), WithActiveStripeSubscription())

	// Empty form: required fields (MachineName + Issue on the default
	// maintenance template) are missing.
	req := authedRequest(t, env, memberID, http.MethodPost, "/signs/maintenance",
		strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.GreaterOrEqual(t, resp.StatusCode, 400)
	require.Less(t, resp.StatusCode, 500,
		"a missing-required-field submit should be a 4xx client error, not a 5xx")

	// Nothing should have been queued.
	var n int
	require.NoError(t, env.db.QueryRow(
		`SELECT COUNT(*) FROM signs_print_queue WHERE member_id = ?`, memberID).Scan(&n))
	assert.Equal(t, 0, n, "broken submissions must not be queued (not retryable)")
}
