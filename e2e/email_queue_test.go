package e2e

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/modules/email"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainEmailOnce drives the email module's workqueue interface (GetItem →
// ProcessItem → UpdateItem) once. This mirrors what engine.PollWorkqueue does
// in production. The e2e test environment does not run the ProcMgr so we drive
// the worker directly.
func drainEmailOnce(t *testing.T, env *TestEnv) bool {
	t.Helper()
	require.NotNil(t, env.EmailModule, "EmailModule not exposed on TestEnv")
	ctx := context.Background()
	item, err := env.EmailModule.GetItem(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	require.NoError(t, err)
	procErr := env.EmailModule.ProcessItem(ctx, item)
	require.NoError(t, env.EmailModule.UpdateItem(ctx, item, procErr == nil))
	return true
}

// TestEmailQueue_DrainHappyPath inserts an outbound_mail row and verifies that
// driving the worker drains it (row deleted on success).
func TestEmailQueue_DrainHappyPath(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	_, err := env.db.Exec(`INSERT INTO outbound_mail (recipient, subject, body) VALUES (?, ?, ?)`,
		"happy@example.com", "Hello", "Body text")
	require.NoError(t, err)

	require.True(t, drainEmailOnce(t, env), "expected one item to be drained")

	var count int
	require.NoError(t, env.db.QueryRow(
		`SELECT COUNT(*) FROM outbound_mail WHERE recipient = ?`, "happy@example.com").Scan(&count))
	assert.Equal(t, 0, count, "row should be deleted after successful send")
}

// TestEmailQueue_RetryOnFailure swaps the Sender for a stub that returns an
// error, then verifies the worker leaves the row in the queue and pushes
// send_at into the future (exponential backoff).
func TestEmailQueue_RetryOnFailure(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	require.NotNil(t, env.EmailModule)
	env.EmailModule.Sender = email.Sender(func(ctx context.Context, to, subj string, msg []byte) error {
		return errors.New("boom")
	})

	res, err := env.db.Exec(
		`INSERT INTO outbound_mail (created, send_at, recipient, subject, body) VALUES (strftime('%s','now') - 10, strftime('%s','now'), ?, ?, ?)`,
		"retry@example.com", "Subject", "Body")
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)

	var origSendAt, origCreated int64
	require.NoError(t, env.db.QueryRow(
		`SELECT send_at, created FROM outbound_mail WHERE id = ?`, id).Scan(&origSendAt, &origCreated))

	require.True(t, drainEmailOnce(t, env), "expected the failing item to be picked up")

	// The row should still exist; send_at should have been pushed forward.
	var newSendAt int64
	require.NoError(t, env.db.QueryRow(
		`SELECT send_at FROM outbound_mail WHERE id = ?`, id).Scan(&newSendAt))
	assert.Greater(t, newSendAt, origSendAt, "send_at should advance after a failure")
}

// TestEmailQueue_ExpiresAfterTTL inserts a row whose created timestamp is older
// than the 1-hour TTL. The hourly cleanup query should delete it. We invoke the
// same DELETE the cleanup uses.
func TestEmailQueue_ExpiresAfterTTL(t *testing.T) {
	t.Parallel()
	env := NewTestEnv(t)

	// Insert a row with created set to 2 hours ago.
	res, err := env.db.Exec(
		`INSERT INTO outbound_mail (created, recipient, subject, body) VALUES (strftime('%s','now') - 7200, ?, ?, ?)`,
		"old@example.com", "Old", "Body")
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)

	// The worker's GetItem also filters out items older than 3600s. Confirm that
	// the row will not be picked up by a worker tick.
	require.False(t, drainEmailOnce(t, env), "expired row should be invisible to the worker")

	// Verify the row still exists prior to cleanup (drain leaves it alone).
	var pre int
	require.NoError(t, env.db.QueryRow(`SELECT COUNT(*) FROM outbound_mail WHERE id = ?`, id).Scan(&pre))
	assert.Equal(t, 1, pre)

	// Run the hourly cleanup query (mirrors AttachWorkers configuration).
	_, err = env.db.Exec(`DELETE FROM outbound_mail WHERE unixepoch() - created > 3600`)
	require.NoError(t, err)

	var post int
	require.NoError(t, env.db.QueryRow(`SELECT COUNT(*) FROM outbound_mail WHERE id = ?`, id).Scan(&post))
	assert.Equal(t, 0, post, "expired row should be cleaned up")
}

// silence unused linter warnings for stdlib helpers used elsewhere in this file
var _ = time.Second
