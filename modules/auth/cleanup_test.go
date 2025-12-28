package auth

import (
	"testing"
	"time"

	"github.com/TheLab-ms/conway/modules/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupExpiredLoginCodes(t *testing.T) {
	ctx := t.Context()
	db := core.NewTestDB(t)
	m := New(db, nil, nil, nil)

	// Insert expired code (5 minutes ago)
	_, err := db.ExecContext(ctx, `INSERT INTO login_codes (code, token, email, expires_at) VALUES ('12345', 'tok1', 'a@b.com', ?)`, time.Now().Add(-5*time.Minute).Unix())
	require.NoError(t, err)

	// Insert valid code (5 minutes in future)
	_, err = db.ExecContext(ctx, `INSERT INTO login_codes (code, token, email, expires_at) VALUES ('67890', 'tok2', 'c@d.com', ?)`, time.Now().Add(5*time.Minute).Unix())
	require.NoError(t, err)

	// Run cleanup
	result := m.cleanupExpiredLoginCodes(ctx)
	assert.False(t, result)

	// Verify only valid code remains
	var count int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM login_codes").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	var code string
	err = db.QueryRowContext(ctx, "SELECT code FROM login_codes").Scan(&code)
	require.NoError(t, err)
	assert.Equal(t, "67890", code)
}
