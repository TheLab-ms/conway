package db

import (
	"database/sql"
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

	results, err := db.Query("SELECT email, payment_status FROM members")
	require.NoError(t, err)
	defer results.Close()

	for results.Next() {
		var email string
		var status *string
		err = results.Scan(&email, &status)
		require.NoError(t, err)

		if email == "inactive" || email == "stripe_inactive" || email == "unconfirmed" || email == "cto@thelab.ms" {
			assert.Nil(t, status, email)
		} else {
			assert.Contains(t, *status, "Active ", email)
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

	_, err := db.Exec("INSERT INTO members (id) VALUES (1)")
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

	t.Run("inactive root family member", func(t *testing.T) {
		_, err = db.Exec("INSERT INTO members (email, confirmed) VALUES ('root@family.com', 1)")
		require.NoError(t, err)

		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, fob_id, building_access_approver, root_family_member) VALUES ('7@test.com', 1, 1, 1, 7, 1, (SELECT id FROM members WHERE email = 'root@family.com'))")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '7@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "Root Family Member Inactive", actual)
	})

	assert.Equal(t, []string{
		`AccessStatusChanged - Building access status changed from "Ready" to "Root Family Member Inactive"`,
	}, eventsToStrings(t, db))
}

func TestMemberFamilyDiscountPropagation(t *testing.T) {
	db := NewTest(t)

	_, err := db.Exec("INSERT INTO members (email, confirmed, non_billable) VALUES ('root@family.com', 1, 1)")
	require.NoError(t, err)

	// can't become your own root family member
	_, err = db.Exec("UPDATE members SET root_family_member = 1 WHERE email = 'root@family.com'")
	require.Error(t, err)

	_, err = db.Exec("INSERT INTO members (email, root_family_member) VALUES ('leaf@family.com', (SELECT id FROM members WHERE email = 'root@family.com'))")
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

func TestMemberEvents(t *testing.T) {
	db := NewTest(t)

	_, err := db.Exec("INSERT INTO members (id, email, confirmed, non_billable) VALUES (1, 'root@family.com', 1, 1)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO members (id, email) VALUES (2, 'foo@bar.com')")
	require.NoError(t, err)

	_, err = db.Exec("UPDATE members SET name = 'foobar', discount_type = 'anything', leadership = 1, building_access_approver = 9001, confirmed = 1, non_billable = 1 WHERE id = 2")
	require.NoError(t, err)

	_, err = db.Exec("UPDATE members SET leadership = 0, building_access_approver = NULL, non_billable = 0 WHERE id = 2")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"NonBillableStatusAdded - The member has been marked as non-billable",
		`BuildingAccessApproved - Building access was approved by "Legacy Building Access Approver"`,
		"LeadershipStatusAdded - Designated as leadership",
		`AccessStatusChanged - Building access status changed from "Unconfirmed Email" to "Missing Waiver"`,
		`DiscountTypeModified - Discount changed from "NULL" to "anything"`,
		`NameModified - Name changed from "" to "foobar"`,
		"NonBillableStatusRemoved - The member is no longer marked as non-billable",
		"BuildingAccessRevoked - Building access was revoked",
		"LeadershipStatusRemoved - No longer designated as leadership",
	}, eventsToStrings(t, db))
}

func eventsToStrings(t *testing.T, db *sql.DB) []string {
	results, err := db.Query("SELECT event, details FROM member_events")
	require.NoError(t, err)
	defer results.Close()

	all := []string{}
	for results.Next() {
		var event, details string
		err = results.Scan(&event, &details)
		require.NoError(t, err)
		all = append(all, event+" - "+details)
	}
	require.NoError(t, results.Err())
	return all
}
