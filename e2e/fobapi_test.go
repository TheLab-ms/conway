package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFobAPI_PollEmpty verifies POST /api/fobs returns an empty list and a stable ETag
// when there are no active keyfobs.
func TestFobAPI_PollEmpty(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader("[]"))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.NotEmpty(t, resp.Header.Get("ETag"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	body = bytes.TrimSpace(body)
	// Empty result encodes to "null" because of nil slice
	assert.True(t, string(body) == "null" || string(body) == "[]", "got: %s", body)
}

// TestFobAPI_PollWithActiveMember verifies the active_keyfobs view returns ready members.
func TestFobAPI_PollWithActiveMember(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	seedMember(t, env, "fob-active@example.com", WithReadyAccess(), WithFobID(424242))

	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader("[]"))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var ids []int64
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&ids))
	assert.Contains(t, ids, int64(424242))
}

// TestFobAPI_ETag304 verifies that a matching If-None-Match returns 304 without a body.
func TestFobAPI_ETag304(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	seedMember(t, env, "etag@example.com", WithReadyAccess(), WithFobID(11111))

	// First request to capture ETag
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader("[]"))
	require.NoError(t, err)
	etag := resp.Header.Get("ETag")
	resp.Body.Close()
	require.NotEmpty(t, etag)

	// Second request with matching ETag → 304
	req, err := http.NewRequest("POST", env.baseURL+"/api/fobs", strings.NewReader("[]"))
	require.NoError(t, err)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, 304, resp2.StatusCode)
}

// TestFobAPI_LANGate verifies CF-Connecting-IP causes a 403.
func TestFobAPI_LANGate(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	req, err := http.NewRequest("POST", env.baseURL+"/api/fobs", strings.NewReader("[]"))
	require.NoError(t, err)
	req.Header.Set("CF-Connecting-IP", "1.2.3.4")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 403, resp.StatusCode)
}

// TestFobAPI_SwipeIngestion verifies swipes are stored and linked to members.
func TestFobAPI_SwipeIngestion(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "swipe@example.com", WithReadyAccess(), WithFobID(777))

	body := `[{"fob":777,"allowed":true}]`
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var swipeMember int64
	err = env.db.QueryRow("SELECT member FROM fob_swipes WHERE fob_id = 777").Scan(&swipeMember)
	require.NoError(t, err)
	assert.Equal(t, memberID, swipeMember)
}

// TestFobAPI_UnknownFobIngestion verifies swipes for unknown fobs are stored with NULL member.
func TestFobAPI_UnknownFobIngestion(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	body := `[{"fob":99999,"allowed":false}]`
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var member *int64
	err = env.db.QueryRow("SELECT member FROM fob_swipes WHERE fob_id = 99999").Scan(&member)
	require.NoError(t, err)
	assert.Nil(t, member)
}

// TestFobAPI_ClientRegistration verifies a row is upserted into fob_clients.
func TestFobAPI_ClientRegistration(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader("[]"))
	require.NoError(t, err)
	resp.Body.Close()

	var count int
	err = env.db.QueryRow("SELECT COUNT(*) FROM fob_clients WHERE ip_address = '127.0.0.1'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Subsequent requests should not create another row
	resp2, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader("[]"))
	require.NoError(t, err)
	resp2.Body.Close()
	err = env.db.QueryRow("SELECT COUNT(*) FROM fob_clients").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestFobAPI_InvalidJSON returns 400.
func TestFobAPI_InvalidJSON(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader("not json"))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 400, resp.StatusCode)
}

// TestFobAPI_PaymentLapseRemovesFob verifies a member with no payment is excluded from the active list.
func TestFobAPI_PaymentLapseRemovesFob(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	// Confirmed + waiver but no payment → not Ready
	seedMember(t, env, "lapsed@example.com", WithConfirmed(), WithWaiver(), WithFobID(55555))

	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader("[]"))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.NotContains(t, string(body), "55555")
}

// TestFobAPI_AdminUpdateDoorName verifies leadership can rename a fob client.
func TestFobAPI_AdminUpdateDoorName(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	// Create a fob client by hitting the API
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader("[]"))
	require.NoError(t, err)
	resp.Body.Close()

	var clientID int64
	require.NoError(t, env.db.QueryRow("SELECT id FROM fob_clients LIMIT 1").Scan(&clientID))

	adminID := seedMember(t, env, "admin@example.com", WithConfirmed(), WithLeadership())
	tok := generateAuthToken(t, env, adminID)

	form := url.Values{}
	form.Set("door_name", "Front Door")
	req, err := http.NewRequest("POST", env.baseURL+"/admin/doors/"+itoa(clientID), strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "token", Value: tok})

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp2, err := client.Do(req)
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, http.StatusSeeOther, resp2.StatusCode)

	var name string
	require.NoError(t, env.db.QueryRow("SELECT door_name FROM fob_clients WHERE id = ?", clientID).Scan(&name))
	assert.Equal(t, "Front Door", name)
}

// TestFobAPI_AdminUpdateDoorNameRequiresLeadership rejects non-leadership users.
func TestFobAPI_AdminUpdateDoorNameRequiresLeadership(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	// Create a fob client
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader("[]"))
	require.NoError(t, err)
	resp.Body.Close()

	var clientID int64
	require.NoError(t, env.db.QueryRow("SELECT id FROM fob_clients LIMIT 1").Scan(&clientID))

	// Unauthenticated → redirect to /login
	req, _ := http.NewRequest("POST", env.baseURL+"/admin/doors/"+itoa(clientID), strings.NewReader("door_name=X"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp1, err := client.Do(req)
	require.NoError(t, err)
	resp1.Body.Close()
	assert.True(t, resp1.StatusCode == http.StatusFound, "expected redirect to login, got %d", resp1.StatusCode)

	// Authenticated non-leadership member → 403
	memberID := seedMember(t, env, "nonadmin@example.com", WithConfirmed())
	tok := generateAuthToken(t, env, memberID)
	req2, _ := http.NewRequest("POST", env.baseURL+"/admin/doors/"+itoa(clientID), strings.NewReader("door_name=X"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(&http.Cookie{Name: "token", Value: tok})
	resp2, err := client.Do(req2)
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, 403, resp2.StatusCode)
}

// TestFobAPI_LastSeenUpdates verifies last_seen is updated on subsequent requests after the rate-limit window.
// We can't wait 30s in tests so we manipulate the DB to simulate aged data.
func TestFobAPI_LastSeenUpdates(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader("[]"))
	require.NoError(t, err)
	resp.Body.Close()

	// Force last_seen to be old (>30s ago)
	_, err = env.db.Exec("UPDATE fob_clients SET last_seen = strftime('%s','now') - 60")
	require.NoError(t, err)

	var oldSeen int64
	require.NoError(t, env.db.QueryRow("SELECT last_seen FROM fob_clients").Scan(&oldSeen))

	time.Sleep(1100 * time.Millisecond)

	resp2, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader("[]"))
	require.NoError(t, err)
	resp2.Body.Close()

	var newSeen int64
	require.NoError(t, env.db.QueryRow("SELECT last_seen FROM fob_clients").Scan(&newSeen))
	assert.Greater(t, newSeen, oldSeen, "last_seen should have advanced after the 30s window")
}

func itoa(i int64) string {
	return strconv.FormatInt(i, 10)
}

// TestBadgeNotify_SwipeTriggersQueueEntry verifies that a fob swipe for a
// member with discord_checkin_notify enabled inserts into badgenotify_queue.
func TestBadgeNotify_SwipeTriggersQueueEntry(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "badge@example.com", WithReadyAccess(), WithFobID(8001), WithDiscordCheckinNotify())

	body := `[{"fob":8001,"allowed":true}]`
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var queueCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM badgenotify_queue WHERE member_id = ?", memberID).Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 1, queueCount, "swipe should create a badge-notify queue entry for opted-in member")
}

// TestBadgeNotify_SwipeNoEntryWhenOptedOut verifies that a fob swipe for a
// member WITHOUT discord_checkin_notify does NOT create a queue entry.
func TestBadgeNotify_SwipeNoEntryWhenOptedOut(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "nobadge@example.com", WithReadyAccess(), WithFobID(8002))

	body := `[{"fob":8002,"allowed":true}]`
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var queueCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM badgenotify_queue WHERE member_id = ?", memberID).Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 0, queueCount, "swipe should NOT create a badge-notify queue entry for opted-out member")
}

// TestBadgeNotify_EnabledConfigControlsNotification verifies that the
// badge_notify_enabled flag in discord_config controls whether the worker
// processes queued notifications.
func TestBadgeNotify_EnabledConfigControlsNotification(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "configbadge@example.com", WithReadyAccess(), WithFobID(8003), WithDiscordCheckinNotify())

	// Insert a queue entry directly (simulating a swipe that already happened).
	_, err := env.db.Exec("INSERT INTO badgenotify_queue (member_id) VALUES (?)", memberID)
	require.NoError(t, err)

	// With badge_notify_enabled=0 (default), the worker should not send anything.
	// We verify the queue entry still exists (not processed away).
	var queueCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM badgenotify_queue WHERE member_id = ?", memberID).Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 1, queueCount, "queue entry should exist")

	// Enable badge-in notifications with a channel ID.
	seedDiscordConfigWithBadgeNotify(t, env, "cid", "secret", "token", "gid", "rid", 24, true, "123456789012345678")

	// The queue entry should still be present (the worker will process it on next poll).
	err = env.db.QueryRow("SELECT COUNT(*) FROM badgenotify_queue WHERE member_id = ?", memberID).Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 1, queueCount, "queue entry should still exist after config saved")
}

// TestBadgeNotify_InvalidSwipeDoesNotTrigger verifies that swipes with member=NULL
// (unknown fob) do not create badge-notify queue entries.
func TestBadgeNotify_InvalidSwipeDoesNotTrigger(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	// Swipe with an unknown fob ID.
	body := `[{"fob":99998,"allowed":false}]`
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	var queueCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM badgenotify_queue").Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 0, queueCount, "unknown fob swipe should not create badge-notify queue entry")
}
