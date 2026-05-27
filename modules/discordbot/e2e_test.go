package discordbot

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/members"
	"github.com/TheLab-ms/conway/modules/members/memberdb"
	"github.com/stretchr/testify/require"
)

// newTestModule wires up a Module backed by an in-memory DB with the members
// schema preloaded, the bot config injected via configOverride, and a fake
// webhook queuer that captures outbound calls.
func newTestModule(t *testing.T, cfg Config) (*Module, *fakeQueuer) {
	t.Helper()
	db := members.NewTestDB(t)
	fq := &fakeQueuer{}
	m := New(db, engine.NewEventLogger(db, "discordbot"), fq)
	m.configOverride = func(context.Context) (*Config, error) {
		c := cfg
		return &c, nil
	}
	return m, fq
}

// insertMemberRaw inserts a member directly via SQL, bypassing the members
// module's Go-level helpers. This more accurately simulates what happens in
// production: the discordbot trigger fires on raw INSERTs from any path.
func insertMemberRaw(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	res, err := db.Exec(
		"INSERT INTO members (name, email, created) VALUES (?, ?, strftime('%s','now'))",
		"E2E User", email)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// queueCount returns the number of rows currently in discordbot_signup_queue.
func queueCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM discordbot_signup_queue").Scan(&n))
	return n
}

// TestE2E_TriggerEnqueuesOnMemberInsert proves the AFTER INSERT trigger on
// members actually populates discordbot_signup_queue. Without this, the rest
// of the bot is dead code.
func TestE2E_TriggerEnqueuesOnMemberInsert(t *testing.T) {
	t.Parallel()
	m, _ := newTestModule(t, Config{})
	require.Equal(t, 0, queueCount(t, m.db))

	id := insertMemberRaw(t, m.db, "first@example.com")
	require.Equal(t, 1, queueCount(t, m.db))

	// Verify the queued row references the right member.
	var queued int64
	require.NoError(t, m.db.QueryRow(
		"SELECT member_id FROM discordbot_signup_queue").Scan(&queued))
	require.Equal(t, id, queued)
}

// TestE2E_FullPipeline_EnabledDeliversWebhook covers the happy path:
// insert member → trigger enqueues → GetItem returns the row → ProcessItem
// dispatches to the webhook queue with the expected payload → UpdateItem
// deletes the row.
func TestE2E_FullPipeline_EnabledDeliversWebhook(t *testing.T) {
	t.Parallel()
	webhookURL := "https://discord.example/webhooks/123/abc"
	m, fq := newTestModule(t, Config{
		Enabled:                 true,
		SignupChannelWebhookURL: webhookURL,
		ApplicationPublicKey:    strings.Repeat("ab", 32), // 32 bytes hex
	})

	id := insertMemberRaw(t, m.db, "applicant@example.com")
	ctx := context.Background()

	item, err := m.GetItem(ctx)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, id, item.MemberID)
	require.Equal(t, "applicant@example.com", item.Email)

	require.NoError(t, m.ProcessItem(ctx, item))

	// fakeQueuer must have been called with the right URL and a payload
	// that decodes back to a well-formed select-menu message.
	require.Len(t, fq.msgs, 1)
	require.Equal(t, webhookURL, fq.msgs[0].url)

	var payload webhookPayload
	require.NoError(t, json.Unmarshal([]byte(fq.msgs[0].payload), &payload))
	require.Equal(t, botUsername, payload.Username)
	require.Len(t, payload.Components, 1)
	menu := payload.Components[0].Components[0]
	require.Equal(t, fmt.Sprintf("%s%d", customIDPrefix, id), menu.CustomID)
	require.Equal(t, len(memberdb.DiscountTypes), len(menu.Options))

	// UpdateItem(success=true) drops the row.
	require.NoError(t, m.UpdateItem(ctx, item, true))
	require.Equal(t, 0, queueCount(t, m.db))

	// Next GetItem call returns sql.ErrNoRows (queue empty).
	_, err = m.GetItem(ctx)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// TestE2E_DisabledDropsWithoutWebhook proves that when the bot is disabled
// (or the webhook URL is empty), ProcessItem returns nil so UpdateItem
// removes the queue row without calling the webhook queuer — preventing a
// backlog from accumulating before configuration arrives.
func TestE2E_DisabledDropsWithoutWebhook(t *testing.T) {
	t.Parallel()
	m, fq := newTestModule(t, Config{Enabled: false})
	insertMemberRaw(t, m.db, "x@y.z")

	ctx := context.Background()
	item, err := m.GetItem(ctx)
	require.NoError(t, err)

	require.NoError(t, m.ProcessItem(ctx, item))
	require.Empty(t, fq.msgs, "no webhook should be queued when disabled")

	require.NoError(t, m.UpdateItem(ctx, item, true))
	require.Equal(t, 0, queueCount(t, m.db))
}

// TestE2E_EnabledButNoWebhookURLAlsoDrops covers the second branch of the
// "not configured" guard in ProcessItem.
func TestE2E_EnabledButNoWebhookURLAlsoDrops(t *testing.T) {
	t.Parallel()
	m, fq := newTestModule(t, Config{Enabled: true, SignupChannelWebhookURL: ""})
	insertMemberRaw(t, m.db, "x@y.z")

	ctx := context.Background()
	item, err := m.GetItem(ctx)
	require.NoError(t, err)
	require.NoError(t, m.ProcessItem(ctx, item))
	require.Empty(t, fq.msgs)
}

// TestE2E_QueueOrderingFIFO proves GetItem returns the oldest unsent signup
// first, so notifications fire in the order members joined.
func TestE2E_QueueOrderingFIFO(t *testing.T) {
	t.Parallel()
	m, _ := newTestModule(t, Config{})

	// Insert with explicit ascending created timestamps to make ordering
	// deterministic (strftime('%s','now') has 1s resolution which would
	// otherwise tie all three).
	for i, email := range []string{"a@e.io", "b@e.io", "c@e.io"} {
		res, err := m.db.Exec(
			"INSERT INTO members (name, email, created) VALUES (?, ?, ?)",
			"u", email, 1000+i)
		require.NoError(t, err)
		id, _ := res.LastInsertId()
		// Backdate the queue row to match insertion order regardless of
		// the trigger's strftime resolution.
		_, err = m.db.Exec(
			"UPDATE discordbot_signup_queue SET created = ? WHERE member_id = ?",
			1000+i, id)
		require.NoError(t, err)
	}

	ctx := context.Background()
	for _, want := range []string{"a@e.io", "b@e.io", "c@e.io"} {
		item, err := m.GetItem(ctx)
		require.NoError(t, err)
		require.Equal(t, want, item.Email)
		require.NoError(t, m.UpdateItem(ctx, item, true))
	}
}

// TestE2E_MemberDeleteLeavesOrphanedQueueRow documents that the FK
// constraint on discordbot_signup_queue.member_id is declarative only —
// SQLite's `PRAGMA foreign_keys` is not enabled anywhere in Conway, so
// deleting a member with a pending notification leaves an orphan row in the
// queue. The worker's GetItem JOIN against members then silently skips it
// until it's pruned manually. This test exists to catch the day someone
// turns on FK enforcement, at which point the assertion should flip.
func TestE2E_MemberDeleteLeavesOrphanedQueueRow(t *testing.T) {
	t.Parallel()
	m, _ := newTestModule(t, Config{})

	id := insertMemberRaw(t, m.db, "doomed@example.com")
	require.Equal(t, 1, queueCount(t, m.db))

	_, err := m.db.Exec("DELETE FROM members WHERE id = ?", id)
	require.NoError(t, err)

	// FK is declarative; row is orphaned (not cascaded).
	require.Equal(t, 1, queueCount(t, m.db))

	// GetItem's JOIN against members filters out the orphan so it isn't
	// retried in a tight loop.
	_, err = m.GetItem(context.Background())
	require.ErrorIs(t, err, sql.ErrNoRows,
		"orphaned queue row must not be returned by GetItem")
}

// TestE2E_FailedProcessLogsAuditEvent ensures we record an audit event when
// UpdateItem(success=false) runs, so admins can investigate undeliverable
// notifications.
func TestE2E_FailedProcessLogsAuditEvent(t *testing.T) {
	t.Parallel()
	m, _ := newTestModule(t, Config{})

	id := insertMemberRaw(t, m.db, "x@y.z")
	ctx := context.Background()
	item, err := m.GetItem(ctx)
	require.NoError(t, err)

	require.NoError(t, m.UpdateItem(ctx, item, false))
	require.Equal(t, 0, queueCount(t, m.db))

	var n int
	require.NoError(t, m.db.QueryRow(
		`SELECT COUNT(*) FROM module_events
		 WHERE module='discordbot' AND event_type='SignupNotifyError' AND member=?`,
		id).Scan(&n))
	require.Equal(t, 1, n)
}

// TestConfig_Validate covers the hex/length guard on the application public
// key.
func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	// Empty key: allowed (lets admins partially configure).
	require.NoError(t, (&Config{}).Validate())

	// Valid 32-byte hex key.
	good := hex.EncodeToString(make([]byte, 32))
	require.NoError(t, (&Config{ApplicationPublicKey: good}).Validate())

	// Non-hex characters.
	err := (&Config{ApplicationPublicKey: "zz"}).Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "hex")

	// Hex but wrong length.
	short := hex.EncodeToString(make([]byte, 16))
	err = (&Config{ApplicationPublicKey: short}).Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "32 bytes")
}
