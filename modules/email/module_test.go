package email

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMailDispatch(t *testing.T) {
	ctx := context.Background()
	db := db.OpenTest(t)

	messages := []string{}
	m := New(db, func(ctx context.Context, to, subj string, msg []byte) error {
		messages = append(messages, fmt.Sprintf("to=%s subj=%s msg=%s", to, subj, msg))
		return nil
	})

	pollFunc := engine.PollWorkqueue(m)

	// Test no messages - should return false (no work to do)
	result := pollFunc(ctx)
	assert.False(t, result)
	assert.Equal(t, []string{}, messages)

	_, err := db.Exec("INSERT INTO outbound_mail (recipient, subject, body) VALUES ('foo@bar.com', 'Test!', 'hello world');")
	require.NoError(t, err)

	// Test processing a message - should return true (work was done)
	result = pollFunc(ctx)
	assert.True(t, result)
	assert.Equal(t, []string{"to=foo@bar.com subj=Test! msg=hello world"}, messages)

	// Test no more messages after completion - should return false
	result = pollFunc(ctx)
	assert.False(t, result)
	assert.Equal(t, []string{"to=foo@bar.com subj=Test! msg=hello world"}, messages)
}

func TestExponentialBackoffOnFailure(t *testing.T) {
	ctx := context.Background()
	db := db.OpenTest(t)

	failCount := 0
	m := New(db, func(ctx context.Context, to, subj string, msg []byte) error {
		failCount++
		if failCount <= 2 {
			return fmt.Errorf("simulated send failure")
		}
		return nil
	})

	pollFunc := engine.PollWorkqueue(m)

	baseTime := time.Now().Unix() - 100
	_, err := db.Exec("INSERT INTO outbound_mail (recipient, subject, body, created, send_at) VALUES ('test@example.com', 'Test Backoff', 'test message', $1, $2);", baseTime, baseTime+10)
	require.NoError(t, err)

	originalSendAt := baseTime + 10

	// First attempt - should fail and return true (work was attempted)
	result := pollFunc(ctx)
	assert.True(t, result)

	var newSendAt int64
	err = db.QueryRow("SELECT send_at FROM outbound_mail WHERE id = 1").Scan(&newSendAt)
	require.NoError(t, err)

	assert.True(t, newSendAt > originalSendAt, "send_at should be delayed after failure")

	_, err = db.Exec("UPDATE outbound_mail SET send_at = unixepoch() WHERE id = 1")
	require.NoError(t, err)

	// Second attempt - should fail again and return true (work was attempted)
	result = pollFunc(ctx)
	assert.True(t, result)

	var finalSendAt int64
	err = db.QueryRow("SELECT send_at FROM outbound_mail WHERE id = 1").Scan(&finalSendAt)
	require.NoError(t, err)

	assert.True(t, finalSendAt > newSendAt, "send_at should increase exponentially on repeated failures")
}

func TestCleanupStaleOutboundMail(t *testing.T) {
	ctx := context.Background()
	db := db.OpenTest(t)
	m := New(db, nil)

	// Insert stale email (2 hours ago)
	_, err := db.Exec("INSERT INTO outbound_mail (recipient, subject, body, created) VALUES ('old@test.com', 'Old', 'body', ?)", time.Now().Add(-2*time.Hour).Unix())
	require.NoError(t, err)

	// Insert fresh email (5 minutes ago)
	_, err = db.Exec("INSERT INTO outbound_mail (recipient, subject, body, created) VALUES ('new@test.com', 'New', 'body', ?)", time.Now().Add(-5*time.Minute).Unix())
	require.NoError(t, err)

	// Run cleanup
	result := m.cleanupStaleOutboundMail(ctx)
	assert.False(t, result)

	// Verify only fresh email remains
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM outbound_mail").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var recipient string
	err = db.QueryRow("SELECT recipient FROM outbound_mail").Scan(&recipient)
	require.NoError(t, err)
	assert.Equal(t, "new@test.com", recipient)
}
