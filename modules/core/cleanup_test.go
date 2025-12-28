package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupFobSwipes(t *testing.T) {
	ctx := t.Context()
	db := NewTestDB(t)
	m := &Module{db: db}

	now := time.Now().Unix()
	twoYearsAgo := now - (2*365*24*60*60 + 1) // Just over 2 years ago
	oneYearAgo := now - (365 * 24 * 60 * 60)  // 1 year ago

	// Insert old swipe (>2 years old)
	_, err := db.ExecContext(ctx, `INSERT INTO fob_swipes (uid, fob_id, timestamp) VALUES ('old', 123, ?)`, twoYearsAgo)
	require.NoError(t, err)

	// Insert recent swipe (1 year old)
	_, err = db.ExecContext(ctx, `INSERT INTO fob_swipes (uid, fob_id, timestamp) VALUES ('recent', 456, ?)`, oneYearAgo)
	require.NoError(t, err)

	// Run cleanup
	result := m.cleanupFobSwipes(ctx)
	assert.False(t, result)

	// Verify only recent swipe remains
	var count int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM fob_swipes").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var uid string
	err = db.QueryRowContext(ctx, "SELECT uid FROM fob_swipes").Scan(&uid)
	require.NoError(t, err)
	assert.Equal(t, "recent", uid)
}

func TestCleanupMemberEvents(t *testing.T) {
	ctx := t.Context()
	db := NewTestDB(t)
	m := &Module{db: db}

	// Insert a member first (required for member_events foreign key)
	_, err := db.ExecContext(ctx, `INSERT INTO members (id, email, confirmed) VALUES (1, 'test@example.com', 1)`)
	require.NoError(t, err)

	now := time.Now().Unix()
	twoYearsAgo := now - (2*365*24*60*60 + 1) // Just over 2 years ago
	oneYearAgo := now - (365 * 24 * 60 * 60)  // 1 year ago

	// Insert old event (>2 years old)
	_, err = db.ExecContext(ctx, `INSERT INTO member_events (member, event, details, created) VALUES (1, 'OldEvent', 'old', ?)`, twoYearsAgo)
	require.NoError(t, err)

	// Insert recent event (1 year old)
	_, err = db.ExecContext(ctx, `INSERT INTO member_events (member, event, details, created) VALUES (1, 'RecentEvent', 'recent', ?)`, oneYearAgo)
	require.NoError(t, err)

	// Run cleanup
	result := m.cleanupMemberEvents(ctx)
	assert.False(t, result)

	// Verify only recent event remains
	var count int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM member_events").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var event string
	err = db.QueryRowContext(ctx, "SELECT event FROM member_events").Scan(&event)
	require.NoError(t, err)
	assert.Equal(t, "RecentEvent", event)
}
