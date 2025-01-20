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
	w, err := New(db, &url.URL{}, nil)
	require.NoError(t, err)

	// Create a member+login
	_, err = w.db.Exec("INSERT INTO members (email) VALUES ('foobar');")
	require.NoError(t, err)

	_, err = w.db.Exec("INSERT INTO logins (member, code) VALUES (1, 666);")
	require.NoError(t, err)

	// Prove it isn't removed immediately
	assert.True(t, w.cleanupLogins(context.Background()))

	var count int
	err = w.db.QueryRow("SELECT count(*) FROM logins").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Move creation time into the past, prove it's deleted
	_, err = w.db.Exec("UPDATE logins SET created = strftime('%s', 'now') - 600 WHERE member = 1")
	require.NoError(t, err)

	assert.True(t, w.cleanupLogins(context.Background()))

	err = w.db.QueryRow("SELECT count(*) FROM logins").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestPruneSpamMembers(t *testing.T) {
	db := db.NewTest(t)
	w, err := New(db, &url.URL{}, nil)
	require.NoError(t, err)

	// Member that meets threshold but has been confirmed
	_, err = w.db.Exec("INSERT INTO members (email, created, confirmed) VALUES ('foobar', strftime('%s', 'now') - 90000, true);")
	require.NoError(t, err)

	// Member that meets threshold but has not been confirmed
	_, err = w.db.Exec("INSERT INTO members (email, created, confirmed) VALUES ('barbaz', strftime('%s', 'now') - 90000, false);")
	require.NoError(t, err)

	// Member that does not meet threshold
	_, err = w.db.Exec("INSERT INTO members (email, created, confirmed) VALUES ('foobaz', strftime('%s', 'now') - 82800, false);")
	require.NoError(t, err)

	// This login should be removed when the member is deleted
	_, err = w.db.Exec("INSERT INTO logins (member, code) VALUES (2, 666);")
	require.NoError(t, err)

	assert.True(t, w.pruneSpamMembers(context.Background()))

	// Prove one member was pruned
	var count int
	err = w.db.QueryRow("SELECT count(*) FROM members WHERE email = 'barbaz'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Prove other members were not
	err = w.db.QueryRow("SELECT count(*) FROM members").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count)

	// Prove the login was deleted
	err = w.db.QueryRow("SELECT count(*) FROM logins").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}
