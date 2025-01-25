package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDB(t *testing.T) {
	NewTest(t)
	NewTest(t)
}

func TestMemberActive(t *testing.T) {
	db := NewTest(t)

	_, err := db.Exec("INSERT INTO members (email, confirmed) VALUES ('inactive', 1)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO members (email, stripe_subscription_id, confirmed) VALUES ('unconfirmed', 'foo', 0)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO members (email, stripe_subscription_id, confirmed) VALUES ('stripe_active', 'foo', 1)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO members (email, paypal_subscription_id, confirmed) VALUES ('paypal_active', 'foo', 1)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO members (email, non_billable, confirmed) VALUES ('non_billable_active', 1, 1)")
	require.NoError(t, err)

	results, err := db.Query("SELECT email, active FROM members")
	require.NoError(t, err)
	defer results.Close()

	for results.Next() {
		var email string
		var active bool
		err = results.Scan(&email, &active)
		require.NoError(t, err)

		if email == "inactive" || email == "unconfirmed" {
			assert.False(t, active, email)
		} else {
			assert.True(t, active, email)
		}
	}
}

func TestMemberIdentifier(t *testing.T) {
	db := NewTest(t)

	t.Run("no name", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email) VALUES ('foo@bar.com')")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT identifier FROM members WHERE email = 'foo@bar.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "foo@bar.com", actual)
	})

	t.Run("name", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, name) VALUES ('baz@bar.com', 'Foo Bar')")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT identifier FROM members WHERE email = 'baz@bar.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "Foo Bar", actual)
	})
}
