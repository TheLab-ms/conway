package auth

import (
	"context"
	"net/url"
	"testing"

	"github.com/TheLab-ms/conway/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoginCleanup(t *testing.T) {
	db := db.NewTest(t)
	w, err := New(db, &url.URL{}, nil, nil, nil)
	require.NoError(t, err)

	// Create a member+login
	_, err = w.db.Exec("INSERT INTO members (email) VALUES ('foobar');")
	require.NoError(t, err)

	_, err = w.db.Exec("INSERT INTO logins (member, code) VALUES (1, 666);")
	require.NoError(t, err)

	// Prove it isn't removed immediately
	assert.False(t, w.cleanupLogins(context.Background()))

	var count int
	err = w.db.QueryRow("SELECT count(*) FROM logins").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Move creation time into the past, prove it's deleted
	_, err = w.db.Exec("UPDATE logins SET created = strftime('%s', 'now') - 600 WHERE member = 1")
	require.NoError(t, err)

	assert.False(t, w.cleanupLogins(context.Background()))

	err = w.db.QueryRow("SELECT count(*) FROM logins").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}
