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

	_, err = db.Exec("INSERT INTO members (email, stripe_subscription_state, confirmed) VALUES ('unconfirmed', 'active', 0)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO members (email, stripe_subscription_state, confirmed) VALUES ('stripe_active', 'active', 1)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO members (email, stripe_subscription_state, confirmed) VALUES ('stripe_inactive', 'foobar', 1)")
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

		if email == "inactive" || email == "stripe_inactive" || email == "unconfirmed" {
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

func TestMemberAccessStatus(t *testing.T) {
	db := NewTest(t)

	_, err := db.Exec("INSERT INTO waivers (id) VALUES (1)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO members (id) VALUES (1)")
	require.NoError(t, err)

	t.Run("happy path", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, fob_id, building_access_approver) VALUES ('1@test.com', 1, 1, 1, 1, 1)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '1@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "Ready", actual)
	})

	t.Run("unconfirmed", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, non_billable, waiver, fob_id, building_access_approver) VALUES ('2@test.com', 1, 1, 2, 1)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '2@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "Unconfirmed Email", actual)
	})

	t.Run("missing waiver", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, fob_id, building_access_approver) VALUES ('3@test.com', 1, 1, 3, 1)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '3@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "Missing Waiver", actual)
	})

	t.Run("missing fob", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, building_access_approver) VALUES ('4@test.com', 1, 1, 1, 1)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '4@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "Key Fob Not Assigned", actual)
	})

	t.Run("missing access approver", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, fob_id) VALUES ('5@test.com', 1, 1, 1, 4)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '5@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "Access Not Approved", actual)
	})

	t.Run("inactive membership", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, confirmed, waiver, fob_id, building_access_approver) VALUES ('6@test.com', 1, 1, 5, 1)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '6@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "Membership Inactive", actual)
	})
}

func TestMemberFamilyDiscountPropagation(t *testing.T) {
	db := NewTest(t)

	_, err := db.Exec("INSERT INTO members (email, confirmed, non_billable) VALUES ('root@family.com', 1, 1)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO members (email) VALUES ('leaf@family.com')")
	require.NoError(t, err)

	_, err = db.Exec("UPDATE members SET root_family_member = (SELECT id FROM members WHERE email = 'root@family.com') WHERE email = 'leaf@family.com'")
	require.NoError(t, err)

	var actual bool
	err = db.QueryRow("SELECT root_family_member_active FROM members WHERE email = 'leaf@family.com'").Scan(&actual)
	require.NoError(t, err)
	assert.True(t, actual)

	// root becomes inactive
	_, err = db.Exec("UPDATE members SET non_billable = 0 WHERE email = 'root@family.com'")
	require.NoError(t, err)

	err = db.QueryRow("SELECT root_family_member_active FROM members WHERE email = 'leaf@family.com'").Scan(&actual)
	require.NoError(t, err)
	assert.False(t, actual)

	// root becomes active
	_, err = db.Exec("UPDATE members SET non_billable = 1 WHERE email = 'root@family.com'")
	require.NoError(t, err)

	err = db.QueryRow("SELECT root_family_member_active FROM members WHERE email = 'leaf@family.com'").Scan(&actual)
	require.NoError(t, err)
	assert.True(t, actual)

	// root is deleted
	_, err = db.Exec("DELETE FROM members WHERE email = 'root@family.com'")
	require.NoError(t, err)

	var id *int64
	err = db.QueryRow("SELECT root_family_member, root_family_member_active FROM members WHERE email = 'leaf@family.com'").Scan(&id, &actual)
	require.NoError(t, err)
	assert.False(t, actual)
	assert.Nil(t, id)
}
