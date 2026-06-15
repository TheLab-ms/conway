package discordbot

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/members"
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
// module's Go-level helpers.
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

// requestDiscount simulates a member requesting a discount: it sets
// discount_type and flips discount_status to 'requested', which is what the
// enqueue trigger keys off of.
func requestDiscount(t *testing.T, db *sql.DB, memberID int64, discountType string) {
	t.Helper()
	_, err := db.Exec(
		"UPDATE members SET discount_type = ?, discount_status = 'requested' WHERE id = ?",
		discountType, memberID)
	require.NoError(t, err)
}

// queueCount returns the number of rows in discordbot_discount_request_queue.
func queueCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM discordbot_discount_request_queue").Scan(&n))
	return n
}

// TestE2E_TriggerEnqueuesOnDiscountRequest proves the AFTER UPDATE trigger on
// members populates the request queue when discount_status becomes
// 'requested'. Without this, leadership never gets notified.
func TestE2E_TriggerEnqueuesOnDiscountRequest(t *testing.T) {
	t.Parallel()
	m, _ := newTestModule(t, Config{})

	id := insertMemberRaw(t, m.db, "first@example.com")
	require.Equal(t, 0, queueCount(t, m.db), "signup alone must NOT enqueue")

	requestDiscount(t, m.db, id, "student")
	require.Equal(t, 1, queueCount(t, m.db))

	var queued int64
	require.NoError(t, m.db.QueryRow(
		"SELECT member_id FROM discordbot_discount_request_queue").Scan(&queued))
	require.Equal(t, id, queued)
}

// TestE2E_NoEnqueueOnSignupOrUnrelatedUpdate guards the core requirement:
// leadership is only notified on discount requests, never on signup or
// unrelated status changes.
func TestE2E_NoEnqueueOnSignupOrUnrelatedUpdate(t *testing.T) {
	t.Parallel()
	m, _ := newTestModule(t, Config{})

	id := insertMemberRaw(t, m.db, "quiet@example.com")
	require.Equal(t, 0, queueCount(t, m.db))

	// Unrelated profile changes must not enqueue.
	_, err := m.db.Exec("UPDATE members SET name = ? WHERE id = ?", "Renamed", id)
	require.NoError(t, err)
	_, err = m.db.Exec("UPDATE members SET confirmed = 1 WHERE id = ?", id)
	require.NoError(t, err)
	require.Equal(t, 0, queueCount(t, m.db))

	// An admin directly setting a (status-less) discount must not enqueue.
	_, err = m.db.Exec("UPDATE members SET discount_type = 'military' WHERE id = ?", id)
	require.NoError(t, err)
	require.Equal(t, 0, queueCount(t, m.db))
}

// TestE2E_ApprovingDoesNotReEnqueue proves the transition requested->approved
// does not fire the enqueue trigger again.
func TestE2E_ApprovingDoesNotReEnqueue(t *testing.T) {
	t.Parallel()
	m, _ := newTestModule(t, Config{})

	id := insertMemberRaw(t, m.db, "x@y.z")
	requestDiscount(t, m.db, id, "student")
	require.Equal(t, 1, queueCount(t, m.db))

	_, err := m.db.Exec("UPDATE members SET discount_status = 'approved' WHERE id = ?", id)
	require.NoError(t, err)
	require.Equal(t, 1, queueCount(t, m.db), "approval must not enqueue a second notification")
}

// TestE2E_FullPipeline_EnabledDeliversWebhook covers the happy path: request
// enqueues -> GetItem returns the row -> ProcessItem dispatches to the channel
// queue with the Approve-button payload -> UpdateItem deletes the row.
func TestE2E_FullPipeline_EnabledDeliversWebhook(t *testing.T) {
	t.Parallel()
	channelID := "123456789012345678"
	m, fq := newTestModule(t, Config{
		Enabled:              true,
		LeadershipChannelID:  channelID,
		ApplicationPublicKey: strings.Repeat("ab", 32),
	})

	id := insertMemberRaw(t, m.db, "applicant@example.com")
	requestDiscount(t, m.db, id, "student")
	ctx := context.Background()

	item, err := m.GetItem(ctx)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, id, item.MemberID)
	require.Equal(t, "applicant@example.com", item.Email)
	require.Equal(t, "student", item.DiscountType)

	require.NoError(t, m.ProcessItem(ctx, item))

	require.Len(t, fq.msgs, 1)
	require.Equal(t, channelID, fq.msgs[0].channelID)
	require.Contains(t, fq.msgs[0].payload, fmt.Sprintf("%s%d", approveCustomIDPrefix, id))

	require.NoError(t, m.UpdateItem(ctx, item, true))
	require.Equal(t, 0, queueCount(t, m.db))

	_, err = m.GetItem(ctx)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// TestE2E_DisabledDropsWithoutWebhook: when disabled, ProcessItem returns nil
// so UpdateItem removes the row without calling the webhook queuer.
func TestE2E_DisabledDropsWithoutWebhook(t *testing.T) {
	t.Parallel()
	m, fq := newTestModule(t, Config{Enabled: false})
	id := insertMemberRaw(t, m.db, "x@y.z")
	requestDiscount(t, m.db, id, "student")

	ctx := context.Background()
	item, err := m.GetItem(ctx)
	require.NoError(t, err)

	require.NoError(t, m.ProcessItem(ctx, item))
	require.Empty(t, fq.msgs)

	require.NoError(t, m.UpdateItem(ctx, item, true))
	require.Equal(t, 0, queueCount(t, m.db))
}

// TestE2E_EnabledButNoChannelAlsoDrops covers the second guard branch.
func TestE2E_EnabledButNoChannelAlsoDrops(t *testing.T) {
	t.Parallel()
	m, fq := newTestModule(t, Config{Enabled: true, LeadershipChannelID: ""})
	id := insertMemberRaw(t, m.db, "x@y.z")
	requestDiscount(t, m.db, id, "student")

	ctx := context.Background()
	item, err := m.GetItem(ctx)
	require.NoError(t, err)
	require.NoError(t, m.ProcessItem(ctx, item))
	require.Empty(t, fq.msgs)
}

// TestE2E_QueueOrderingFIFO proves GetItem returns the oldest request first.
func TestE2E_QueueOrderingFIFO(t *testing.T) {
	t.Parallel()
	m, _ := newTestModule(t, Config{})

	for i, email := range []string{"a@e.io", "b@e.io", "c@e.io"} {
		id := insertMemberRaw(t, m.db, email)
		requestDiscount(t, m.db, id, "student")
		// Backdate the queue row so ordering is deterministic despite the
		// trigger's 1s strftime resolution.
		_, err := m.db.Exec(
			"UPDATE discordbot_discount_request_queue SET created = ? WHERE member_id = ?",
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

// TestE2E_FailedProcessLogsAuditEvent ensures we record an audit event when
// UpdateItem(success=false) runs.
func TestE2E_FailedProcessLogsAuditEvent(t *testing.T) {
	t.Parallel()
	m, _ := newTestModule(t, Config{})

	id := insertMemberRaw(t, m.db, "x@y.z")
	requestDiscount(t, m.db, id, "student")
	ctx := context.Background()
	item, err := m.GetItem(ctx)
	require.NoError(t, err)

	require.NoError(t, m.UpdateItem(ctx, item, false))
	require.Equal(t, 0, queueCount(t, m.db))

	var n int
	require.NoError(t, m.db.QueryRow(
		`SELECT COUNT(*) FROM module_events
		 WHERE module='discordbot' AND event_type='DiscountRequestNotifyError' AND member=?`,
		id).Scan(&n))
	require.Equal(t, 1, n)
}

// TestConfig_Validate covers the hex/length guard on the application public
// key.
func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, (&Config{}).Validate())

	good := hex.EncodeToString(make([]byte, 32))
	require.NoError(t, (&Config{ApplicationPublicKey: good}).Validate())

	err := (&Config{ApplicationPublicKey: "zz"}).Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "hex")

	short := hex.EncodeToString(make([]byte, 16))
	err = (&Config{ApplicationPublicKey: short}).Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "32 bytes")
}
