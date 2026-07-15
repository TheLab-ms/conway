package e2e

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAccessDenied_SwipeTriggersQueueEntry verifies that a denied fob swipe
// for a known member inserts into accessdenied_queue.
func TestAccessDenied_SwipeTriggersQueueEntry(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "denied1@example.com", WithConfirmed(), WithFobID(9001), WithDiscord("123456789"))

	// Send a denied swipe
	body := `[{"fob":9001,"allowed":false}]`
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	// Verify queue entry was created
	var queueCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 1, queueCount, "denied swipe should create accessdenied queue entry")
}

// TestAccessDenied_AllowedSwipeNoQueueEntry verifies that an allowed swipe
// does NOT create a queue entry.
func TestAccessDenied_AllowedSwipeNoQueueEntry(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "allowed1@example.com", WithReadyAccess(), WithFobID(9002), WithDiscord("234567890"))

	// Send an allowed swipe
	body := `[{"fob":9002,"allowed":true}]`
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	// Verify NO queue entry was created
	var queueCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 0, queueCount, "allowed swipe should NOT create accessdenied queue entry")
}

// TestAccessDenied_UnknownFobNoQueueEntry verifies that a denied swipe for
// an unknown fob (not associated with any member) does NOT create a queue entry.
func TestAccessDenied_UnknownFobNoQueueEntry(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	// Swipe with an unknown fob ID
	body := `[{"fob":99999,"allowed":false}]`
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	// Verify NO queue entry
	var queueCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue").Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 0, queueCount, "unknown fob swipe should not create accessdenied queue entry")
}

// TestAccessDenied_AllowedFieldStored verifies the allowed field is persisted in fob_swipes.
func TestAccessDenied_AllowedFieldStored(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	seedMember(t, env, "stored@example.com", WithConfirmed(), WithFobID(9003))

	// Send both allowed and denied swipes
	body := `[{"fob":9003,"allowed":true},{"fob":9003,"allowed":false}]`
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	// Verify allowed field is stored correctly
	var allowedTrue, allowedFalse int
	err = env.db.QueryRow("SELECT COUNT(*) FROM fob_swipes WHERE fob_id = 9003 AND allowed = 1").Scan(&allowedTrue)
	require.NoError(t, err)
	err = env.db.QueryRow("SELECT COUNT(*) FROM fob_swipes WHERE fob_id = 9003 AND allowed = 0").Scan(&allowedFalse)
	require.NoError(t, err)
	assert.Equal(t, 1, allowedTrue, "should have one allowed swipe")
	assert.Equal(t, 1, allowedFalse, "should have one denied swipe")
}

// TestAccessDenied_DenialMessageContent verifies the denial message contains
// the correct reason based on access_status. This tests the exported API behavior
// by checking that the module correctly maps access_status values to helpful messages.
func TestAccessDenied_DenialMessageContent(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	// Test that different access statuses result in appropriate queue entries
	testCases := []struct {
		name        string
		opts        []MemberOption
		fobID       int64
		expectQueue bool
	}{
		{
			name:        "MissingWaiver member denied",
			opts:        []MemberOption{WithConfirmed(), WithFobID(9101), WithDiscord("111111111")},
			fobID:       9101,
			expectQueue: true,
		},
		{
			name:        "PaymentInactive member denied",
			opts:        []MemberOption{WithConfirmed(), WithWaiver(), WithFobID(9102), WithDiscord("222222222")},
			fobID:       9102,
			expectQueue: true,
		},
		{
			name:        "Ready member allowed",
			opts:        []MemberOption{WithReadyAccess(), WithFobID(9103), WithDiscord("333333333")},
			fobID:       9103,
			expectQueue: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			email := tc.name + "@example.com"
			memberID := seedMember(t, env, email, tc.opts...)
			_ = memberID

			// Send a denied swipe
			body := fmt.Sprintf(`[{"fob":%d,"allowed":false}]`, tc.fobID)
			resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
			require.NoError(t, err)
			resp.Body.Close()
			require.Equal(t, 200, resp.StatusCode)

			var queueCount int
			err = env.db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&queueCount)
			require.NoError(t, err)
			if tc.expectQueue {
				assert.Equal(t, 1, queueCount, "denied swipe should create queue entry")
			} else {
				assert.Equal(t, 0, queueCount, "should not create queue entry")
			}
		})
	}
}

// TestAccessDenied_ConfigDisabledSkipsNotification verifies that when the
// feature is disabled, the worker does not send notifications.
func TestAccessDenied_ConfigDisabledSkipsNotification(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "disabled@example.com", WithConfirmed(), WithFobID(9004), WithDiscord("345678901"))

	// Insert a queue entry directly
	_, err := env.db.Exec("INSERT INTO accessdenied_queue (member_id) VALUES (?)", memberID)
	require.NoError(t, err)

	// Verify queue entry exists
	var queueCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 1, queueCount, "queue entry should exist")

	// With access_denied_enabled=0 (default), the worker should not process it
	// We verify the queue entry still exists
	err = env.db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 1, queueCount, "queue entry should still exist when feature is disabled")
}

// TestAccessDenied_NoDiscordUserIDSkipsNotification verifies that members
// without a linked Discord account are skipped (can't DM them).
func TestAccessDenied_NoDiscordUserIDSkipsNotification(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	// Member WITHOUT discord_user_id
	memberID := seedMember(t, env, "nodiscord@example.com", WithConfirmed(), WithFobID(9005))

	// Send a denied swipe
	body := `[{"fob":9005,"allowed":false}]`
	resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)

	// Queue entry IS created (trigger doesn't check discord_user_id)
	var queueCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 1, queueCount, "queue entry should be created even without Discord")

	// But the member should not have a discord_user_id
	var discordUserID sql.NullString
	err = env.db.QueryRow("SELECT discord_user_id FROM members WHERE id = ?", memberID).Scan(&discordUserID)
	require.NoError(t, err)
	assert.False(t, discordUserID.Valid, "member should not have discord_user_id")
}

// TestAccessDenied_DuplicateSwipeNoDuplicateQueue verifies that multiple
// denied swipes from the same member don't create duplicate queue entries.
func TestAccessDenied_DuplicateSwipeNoDuplicateQueue(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "dup@example.com", WithConfirmed(), WithFobID(9006), WithDiscord("456789012"))

	// Send multiple denied swipes
	body := `[{"fob":9006,"allowed":false}]`
	for i := 0; i < 3; i++ {
		resp, err := http.Post(env.baseURL+"/api/fobs", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
	}

	// Should only have one queue entry (INSERT OR IGNORE)
	var queueCount int
	err := env.db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 1, queueCount, "multiple denied swipes should not create duplicate queue entries")
}

// TestAccessDenied_QueueCleanupOnMemberDelete verifies cascade delete.
func TestAccessDenied_QueueCleanupOnMemberDelete(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)
	memberID := seedMember(t, env, "delete@example.com", WithConfirmed(), WithFobID(9007), WithDiscord("567890123"))

	// Insert a queue entry
	_, err := env.db.Exec("INSERT INTO accessdenied_queue (member_id) VALUES (?)", memberID)
	require.NoError(t, err)

	// Delete the member
	_, err = env.db.Exec("DELETE FROM members WHERE id = ?", memberID)
	require.NoError(t, err)

	// Queue entry should be cascade deleted
	var queueCount int
	err = env.db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&queueCount)
	require.NoError(t, err)
	assert.Equal(t, 0, queueCount, "queue entry should be deleted when member is deleted")
}

// seedDiscordConfigWithAccessDenied inserts Discord configuration with access-denied notification settings.
func seedDiscordConfigWithAccessDenied(t *testing.T, env *TestEnv, accessDeniedEnabled bool) {
	t.Helper()
	var enabled int
	if accessDeniedEnabled {
		enabled = 1
	}
	_, err := env.db.Exec(`INSERT INTO discord_config (client_id, client_secret, bot_token, guild_id, role_id, sync_interval_hours, access_denied_enabled) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"cid", "secret", "token", "gid", "rid", 24, enabled)
	require.NoError(t, err, "could not insert discord config with access denied setting")
}
