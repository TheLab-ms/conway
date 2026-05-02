package e2e

import (
	"database/sql"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// triggerExists returns true if a SQL trigger with the given name exists in
// sqlite_master.
func triggerExists(t *testing.T, env *TestEnv, name string) bool {
	t.Helper()
	var got string
	err := env.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='trigger' AND name = ?", name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	require.NoError(t, err)
	return got == name
}

// TestTriggers_CreateEventTrigger verifies that POSTing a valid event trigger
// creates a row in the `triggers` table AND a corresponding SQLite trigger.
func TestTriggers_CreateEventTrigger(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	tok := generateAuthToken(t, env, adminID)

	form := url.Values{}
	form.Set("name", "test-event-trigger")
	form.Set("enabled", "on")
	form.Set("trigger_type", "event")
	form.Set("trigger_table", "members")
	form.Set("trigger_op", "UPDATE")
	form.Set("when_clause", "OLD.name != NEW.name")
	form.Set("action_sql", "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'NameChanged', 'name changed');")

	resp := doFormAs(t, "POST", env.baseURL+"/admin/triggers/new", tok, form)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	var id int64
	require.NoError(t, env.db.QueryRow(
		"SELECT id FROM triggers WHERE name = ?", "test-event-trigger").Scan(&id))

	assert.True(t, triggerExists(t, env, "user_trigger_"+itoa(id)),
		"expected SQLite trigger user_trigger_%d to exist", id)
}

// TestTriggers_CreateEventTriggerInvalidExpression verifies that a syntactically
// invalid action_sql causes the trigger creation to fail (no SQL trigger is
// installed). The router returns a 5xx error.
func TestTriggers_CreateEventTriggerInvalidExpression(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	tok := generateAuthToken(t, env, adminID)

	form := url.Values{}
	form.Set("name", "bad-trigger")
	form.Set("enabled", "on")
	form.Set("trigger_type", "event")
	form.Set("trigger_table", "members")
	form.Set("trigger_op", "UPDATE")
	form.Set("when_clause", "")
	form.Set("action_sql", "THIS IS NOT VALID SQL AT ALL;")

	resp := doFormAs(t, "POST", env.baseURL+"/admin/triggers/new", tok, form)
	defer resp.Body.Close()
	assert.GreaterOrEqual(t, resp.StatusCode, 400, "expected an error response, got %d", resp.StatusCode)

	// No SQL trigger should exist with the name pattern.
	var count int
	require.NoError(t, env.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'user_trigger_%'").Scan(&count))

	// Filter out any SQL triggers that may exist from the seeded defaults
	// (they all have user_trigger_<id> names too). Verify by checking that the
	// row corresponding to "bad-trigger" has no matching sqlite_master entry.
	var id sql.NullInt64
	_ = env.db.QueryRow("SELECT id FROM triggers WHERE name = ?", "bad-trigger").Scan(&id)
	if id.Valid {
		assert.False(t, triggerExists(t, env, "user_trigger_"+itoa(id.Int64)),
			"SQL trigger should not exist for invalid expression")
	}
}

// TestTriggers_DeleteEventTrigger creates an event trigger then deletes it,
// verifying both the DB row and the SQLite trigger are removed.
func TestTriggers_DeleteEventTrigger(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	tok := generateAuthToken(t, env, adminID)

	// Create
	form := url.Values{}
	form.Set("name", "to-delete")
	form.Set("enabled", "on")
	form.Set("trigger_type", "event")
	form.Set("trigger_table", "members")
	form.Set("trigger_op", "INSERT")
	form.Set("action_sql", "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'Created', '');")
	resp := doFormAs(t, "POST", env.baseURL+"/admin/triggers/new", tok, form)
	resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	var id int64
	require.NoError(t, env.db.QueryRow(
		"SELECT id FROM triggers WHERE name = ?", "to-delete").Scan(&id))
	require.True(t, triggerExists(t, env, "user_trigger_"+itoa(id)))

	// Delete
	resp2 := doFormAs(t, "POST",
		env.baseURL+"/admin/triggers/"+itoa(id)+"/delete", tok, url.Values{})
	resp2.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp2.StatusCode)

	// Both row and SQL trigger gone.
	var count int
	require.NoError(t, env.db.QueryRow(
		"SELECT COUNT(*) FROM triggers WHERE id = ?", id).Scan(&count))
	assert.Equal(t, 0, count, "trigger row should be deleted")
	assert.False(t, triggerExists(t, env, "user_trigger_"+itoa(id)),
		"SQL trigger should be dropped")
}

// TestTriggers_CreateTimedTrigger verifies POSTing a valid timed trigger
// (with a Go-duration interval and a :last named parameter in the SQL)
// creates a row of trigger_type='timed'. No SQLite trigger is installed.
func TestTriggers_CreateTimedTrigger(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	tok := generateAuthToken(t, env, adminID)

	form := url.Values{}
	form.Set("name", "timed-test")
	form.Set("enabled", "on")
	form.Set("trigger_type", "timed")
	form.Set("interval", "24h")
	form.Set("action_sql", "INSERT INTO metrics (series, value) VALUES ('timed-test', :last);")

	resp := doFormAs(t, "POST", env.baseURL+"/admin/triggers/new", tok, form)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	var (
		id              int64
		ttype           string
		intervalSeconds int64
		actionSQL       string
	)
	require.NoError(t, env.db.QueryRow(
		"SELECT id, trigger_type, interval_seconds, action_sql FROM triggers WHERE name = ?",
		"timed-test").Scan(&id, &ttype, &intervalSeconds, &actionSQL))
	assert.Equal(t, "timed", ttype)
	assert.Equal(t, int64(86400), intervalSeconds)
	assert.Contains(t, actionSQL, ":last")

	// Timed triggers must NOT have a corresponding SQLite trigger.
	assert.False(t, triggerExists(t, env, "user_trigger_"+itoa(id)),
		"timed trigger should not install a SQLite trigger")
}

// TestTriggers_TimedTriggerIntervalValidation verifies that a non-positive
// interval (e.g. "0s") is rejected.
func TestTriggers_TimedTriggerIntervalValidation(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	tok := generateAuthToken(t, env, adminID)

	form := url.Values{}
	form.Set("name", "bad-interval")
	form.Set("enabled", "on")
	form.Set("trigger_type", "timed")
	form.Set("interval", "0s")
	form.Set("action_sql", "INSERT INTO metrics (series, value) VALUES ('x', 1);")

	resp := doFormAs(t, "POST", env.baseURL+"/admin/triggers/new", tok, form)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// No row should have been inserted.
	var count int
	require.NoError(t, env.db.QueryRow(
		"SELECT COUNT(*) FROM triggers WHERE name = ?", "bad-interval").Scan(&count))
	assert.Equal(t, 0, count)
}

// TestTriggers_RequiresLeadership verifies the create endpoint rejects
// non-leadership members with 403 and unauthenticated requests with a /login
// redirect.
func TestTriggers_RequiresLeadership(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	plebID := seedMember(t, env, "pleb@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, plebID)

	form := url.Values{}
	form.Set("name", "shouldnt-exist")
	form.Set("trigger_type", "event")
	form.Set("trigger_table", "members")
	form.Set("trigger_op", "UPDATE")
	form.Set("action_sql", "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'X', '');")

	// Non-leadership → 403
	resp := doFormAs(t, "POST", env.baseURL+"/admin/triggers/new", tok, form)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Unauthenticated → redirect to /login
	resp2 := doFormAs(t, "POST", env.baseURL+"/admin/triggers/new", "", form)
	resp2.Body.Close()
	assert.Equal(t, http.StatusFound, resp2.StatusCode)
	assert.Contains(t, resp2.Header.Get("Location"), "/login")

	var count int
	require.NoError(t, env.db.QueryRow(
		"SELECT COUNT(*) FROM triggers WHERE name = ?", "shouldnt-exist").Scan(&count))
	assert.Equal(t, 0, count)
}

// TestTriggers_ListPage verifies the admin triggers config page renders 200
// for leadership and contains seeded default trigger names.
func TestTriggers_ListPage(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	adminID := seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	tok := generateAuthToken(t, env, adminID)

	resp := doGetAs(t, env.baseURL+"/admin/config/triggers", tok)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	// Spot-check a couple of seeded defaults appear on the page.
	assert.True(t,
		strings.Contains(html, "Email confirmed") ||
			strings.Contains(html, "active-members") ||
			strings.Contains(html, "Discount changed"),
		"expected at least one seeded trigger to be rendered on the page")
}
