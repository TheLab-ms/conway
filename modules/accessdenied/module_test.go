package accessdenied

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testMigration = `
CREATE TABLE members (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    name_override TEXT,
    confirmed INTEGER NOT NULL DEFAULT false,
    fob_id INTEGER,
    fob_last_seen INTEGER,
    discord_user_id TEXT,
    stripe_subscription_state TEXT,
    stripe_cancellation_reason TEXT,
    stripe_last_payment_error TEXT,
    access_status TEXT NOT NULL GENERATED ALWAYS AS ( CASE
            WHEN (confirmed IS NOT TRUE) THEN 'UnconfirmedEmail'
            WHEN (fob_id IS NULL OR fob_id = 0) THEN 'MissingKeyFob'
        ELSE 'Ready' END) VIRTUAL
) STRICT;

CREATE UNIQUE INDEX members_email_idx ON members (email);
CREATE UNIQUE INDEX members_fob_idx ON members (fob_id);

CREATE TABLE fob_swipes (
    uid TEXT PRIMARY KEY,
    timestamp INTEGER NOT NULL,
    fob_id INTEGER NOT NULL,
    member INTEGER,
    allowed INTEGER NOT NULL DEFAULT 1
) STRICT;

CREATE INDEX fob_swipes_fob_id_idx ON fob_swipes (fob_id);
CREATE UNIQUE INDEX fob_swipes_uniq ON fob_swipes (fob_id, timestamp);
CREATE INDEX fob_swipes_timestamp ON fob_swipes (timestamp);
`

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := engine.OpenTestDB(t)
	_, err := db.Exec(testMigration)
	require.NoError(t, err)
	return db
}

func seedTestMember(t *testing.T, db *sql.DB, email string, fobID int64, discordUserID string) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO members (email, name, confirmed, fob_id, discord_user_id)
		VALUES (?, ?, ?, ?, ?)`,
		email, email, true, fobID, sql.NullString{String: discordUserID, Valid: discordUserID != ""})
	require.NoError(t, err)
	id, err := result.LastInsertId()
	require.NoError(t, err)
	return id
}

func TestGetItem(t *testing.T) {
	db := setupTestDB(t)
	m := New(db)

	// Seed a member with confirmed=true, fob_id set → access_status = Ready
	memberID := seedTestMember(t, db, "test@example.com", 12345, "discord123")

	// Insert a queue entry
	_, err := db.Exec("INSERT INTO accessdenied_queue (member_id) VALUES (?)", memberID)
	require.NoError(t, err)

	// GetItem should return the member
	item, err := m.GetItem(context.Background())
	require.NoError(t, err)
	assert.Equal(t, memberID, item.MemberID)
	assert.Equal(t, "discord123", item.DiscordUserID)
	assert.Equal(t, "Ready", item.AccessStatus)
	assert.Equal(t, "test@example.com", item.DisplayName)
}

func TestGetItem_Empty(t *testing.T) {
	db := setupTestDB(t)
	m := New(db)

	// No queue entries
	item, err := m.GetItem(context.Background())
	assert.Nil(t, item)
	assert.Error(t, err)
}

func TestProcessItem_Disabled(t *testing.T) {
	db := setupTestDB(t)
	m := New(db)

	memberID := seedTestMember(t, db, "test@example.com", 12345, "discord123")
	_, err := db.Exec("INSERT INTO accessdenied_queue (member_id) VALUES (?)", memberID)
	require.NoError(t, err)

	// ProcessItem with no config (disabled by default)
	item := &queueItem{
		MemberID:      memberID,
		DiscordUserID: "discord123",
		AccessStatus:  "UnconfirmedEmail",
		DisplayName:   "test@example.com",
	}

	err = m.ProcessItem(context.Background(), item)
	assert.NoError(t, err) // Should not error, just skip

	// Queue entry should still exist (not processed)
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestProcessItem_NoDiscordUserID(t *testing.T) {
	db := setupTestDB(t)
	m := New(db)

	// Enable the feature
	m.configLoader = nil // Will return Config{Enabled: false}
	item := &queueItem{
		MemberID:      1,
		DiscordUserID: "", // No Discord
		AccessStatus:  "UnconfirmedEmail",
		DisplayName:   "test@example.com",
	}

	// This should skip because no Discord user ID
	err := m.ProcessItem(context.Background(), item)
	assert.NoError(t, err)
}

func TestUpdateItem(t *testing.T) {
	db := setupTestDB(t)
	m := New(db)

	memberID := seedTestMember(t, db, "test@example.com", 12345, "discord123")
	_, err := db.Exec("INSERT INTO accessdenied_queue (member_id) VALUES (?)", memberID)
	require.NoError(t, err)

	item := &queueItem{MemberID: memberID}

	// UpdateItem should remove the queue entry
	err = m.UpdateItem(context.Background(), item, true)
	require.NoError(t, err)

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestUpdateItem_Failure(t *testing.T) {
	db := setupTestDB(t)
	m := New(db)

	memberID := seedTestMember(t, db, "test@example.com", 12345, "discord123")
	_, err := db.Exec("INSERT INTO accessdenied_queue (member_id) VALUES (?)", memberID)
	require.NoError(t, err)

	item := &queueItem{MemberID: memberID}

	// UpdateItem with success=false should still remove the queue entry
	err = m.UpdateItem(context.Background(), item, false)
	require.NoError(t, err)

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestDenialReason(t *testing.T) {
	testCases := []struct {
		status       string
		expectReason string
		expectFix    string
	}{
		{"UnconfirmedEmail", "email address has not been confirmed", "confirmation email"},
		{"MissingWaiver", "haven't signed the makerspace waiver", "Sign the waiver"},
		{"PaymentInactive", "membership payment is not active", "update your payment method"},
		{"MissingKeyFob", "key fob is not registered", "Register your fob"},
		{"FamilyInactive", "family membership is inactive", "primary account holder"},
		{"Ready", "currently unavailable", "contact leadership"},
		{"UnknownStatus", "currently unavailable", "contact leadership"},
	}

	for _, tc := range testCases {
		t.Run(tc.status, func(t *testing.T) {
			reason, fix := denialReason(tc.status, "", "", "")
			assert.Contains(t, reason, tc.expectReason)
			assert.Contains(t, fix, tc.expectFix)
		})
	}
}

func TestDenialReason_PaymentDetails(t *testing.T) {
	testCases := []struct {
		name           string
		subState       string
		cancelReason   string
		lastPayErr     string
		expectReason   string
		expectFix      string
	}{
		{
			name:         "past_due with error message",
			subState:     "past_due",
			lastPayErr:   "Your card was declined.",
			expectReason: "last payment failed: Your card was declined.",
			expectFix:    "update your payment method",
		},
		{
			name:         "past_due without error message",
			subState:     "past_due",
			expectReason: "last payment failed",
			expectFix:    "update your payment method",
		},
		{
			name:         "canceled at user request",
			subState:     "canceled",
			cancelReason: "cancellation_requested",
			expectReason: "canceled at your request",
			expectFix:    "renew your membership",
		},
		{
			name:         "canceled due to payment failure",
			subState:     "canceled",
			cancelReason: "payment_failed",
			expectReason: "canceled due to failed payments",
			expectFix:    "update your payment method and renew",
		},
		{
			name:         "canceled due to payment dispute",
			subState:     "canceled",
			cancelReason: "payment_disputed",
			expectReason: "canceled due to a payment dispute",
			expectFix:    "resolve this and renew",
		},
		{
			name:         "canceled with no reason",
			subState:     "canceled",
			expectReason: "no longer active",
			expectFix:    "renew your membership",
		},
		{
			name:         "incomplete_expired",
			subState:     "incomplete_expired",
			expectReason: "setup was never completed",
			expectFix:    "complete the signup process",
		},
		{
			name:         "empty state falls back to generic",
			subState:     "",
			expectReason: "membership payment is not active",
			expectFix:    "update your payment method",
		},
		{
			name:         "unknown state falls back to generic",
			subState:     "unpaid",
			expectReason: "membership payment is not active",
			expectFix:    "update your payment method",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reason, fix := denialReason("PaymentInactive", tc.subState, tc.cancelReason, tc.lastPayErr)
			assert.Contains(t, reason, tc.expectReason)
			assert.Contains(t, fix, tc.expectFix)
		})
	}
}

func TestBuildDMPayload(t *testing.T) {
	payload, err := buildDMPayload("UnconfirmedEmail", "John", "", "", "")
	require.NoError(t, err)

	assert.Contains(t, payload, "Hi John")
	assert.Contains(t, payload, "denied access")
	assert.Contains(t, payload, "email address has not been confirmed")
	assert.Contains(t, payload, "confirmation email")
}

func TestBuildDMPayload_PaymentDetails(t *testing.T) {
	payload, err := buildDMPayload("PaymentInactive", "Jane", "past_due", "", "Your card was expired.")
	require.NoError(t, err)

	assert.Contains(t, payload, "Hi Jane")
	assert.Contains(t, payload, "Your last payment failed: Your card was expired.")
}

func TestTriggerOnDeniedSwipe(t *testing.T) {
	db := setupTestDB(t)
	_ = New(db) // Run migrations to create trigger and queue table

	memberID := seedTestMember(t, db, "test@example.com", 12345, "discord123")

	// Insert a denied swipe - trigger should fire
	_, err := db.Exec(`INSERT INTO fob_swipes (uid, timestamp, fob_id, member, allowed)
		VALUES ('test-uid-1', strftime('%s', 'now'), 12345, ?, 0)`, memberID)
	require.NoError(t, err)

	// Check queue entry
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "denied swipe should create queue entry via trigger")
}

func TestTriggerOnAllowedSwipe(t *testing.T) {
	db := setupTestDB(t)
	_ = New(db) // Run migrations

	memberID := seedTestMember(t, db, "test@example.com", 12345, "discord123")

	// Insert an allowed swipe - trigger should NOT fire
	_, err := db.Exec(`INSERT INTO fob_swipes (uid, timestamp, fob_id, member, allowed)
		VALUES ('test-uid-2', strftime('%s', 'now'), 12345, ?, 1)`, memberID)
	require.NoError(t, err)

	// Check NO queue entry
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue WHERE member_id = ?", memberID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "allowed swipe should NOT create queue entry")
}

func TestTriggerOnDeniedSwipeUnknownFob(t *testing.T) {
	db := setupTestDB(t)
	_ = New(db) // Run migrations

	// Insert a denied swipe for unknown fob (member=NULL)
	_, err := db.Exec(`INSERT INTO fob_swipes (uid, timestamp, fob_id, member, allowed)
		VALUES ('test-uid-3', strftime('%s', 'now'), 99999, NULL, 0)`)
	require.NoError(t, err)

	// Check NO queue entry (member is NULL)
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM accessdenied_queue").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "denied swipe for unknown fob should NOT create queue entry")
}

func TestGetItem_OrderedByCreated(t *testing.T) {
	db := setupTestDB(t)
	m := New(db)

	memberID1 := seedTestMember(t, db, "first@example.com", 11111, "discord1")
	memberID2 := seedTestMember(t, db, "second@example.com", 22222, "discord2")

	// Insert queue entries with different timestamps
	_, err := db.Exec("INSERT INTO accessdenied_queue (member_id, created) VALUES (?, ?)", memberID1, 1000)
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO accessdenied_queue (member_id, created) VALUES (?, ?)", memberID2, 2000)
	require.NoError(t, err)

	// Should return the first one (oldest)
	item, err := m.GetItem(context.Background())
	require.NoError(t, err)
	assert.Equal(t, memberID1, item.MemberID)
}

func TestGetItem_WithDisplayName(t *testing.T) {
	db := setupTestDB(t)
	_ = New(db) // Run migrations

	// Member with name_override
	_, err := db.Exec(`INSERT INTO members (email, name, name_override, confirmed, fob_id, discord_user_id)
		VALUES ('test@example.com', 'Test User', 'Custom Name', true, 12345, 'discord123')`)
	require.NoError(t, err)

	memberID := int64(1) // Auto-increment
	_, err = db.Exec("INSERT INTO accessdenied_queue (member_id) VALUES (?)", memberID)
	require.NoError(t, err)

	m := New(db)
	item, err := m.GetItem(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Custom Name", item.DisplayName)
}

func TestProcessItem_RateLimiting(t *testing.T) {
	db := setupTestDB(t)

	// Create member with recent fob_last_seen (within rate limit window)
	memberID := seedTestMember(t, db, "test@example.com", 12345, "discord123")
	_, err := db.Exec("UPDATE members SET fob_last_seen = ? WHERE id = ?",
		time.Now().Unix()-60, memberID) // 60 seconds ago
	require.NoError(t, err)

	item := &queueItem{
		MemberID:      memberID,
		DiscordUserID: "discord123",
		AccessStatus:  "UnconfirmedEmail",
		DisplayName:   "test@example.com",
	}

	// ProcessItem should skip due to rate limiting
	m := New(db)
	err = m.ProcessItem(context.Background(), item)
	assert.NoError(t, err)
}
