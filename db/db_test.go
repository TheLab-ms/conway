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
			assert.Contains(t, *status, "Active", email)
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
		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, fob_id) VALUES ('1@test.com', 1, 1, 1, 1)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '1@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "Ready", actual)
	})

	t.Run("unconfirmed", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, fob_id) VALUES ('2@test.com', 0, 0, 1, 2)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '2@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "UnconfirmedEmail", actual)
	})

	t.Run("unconfirmed non-billable", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, fob_id) VALUES ('2.5@test.com', 1, 0, 1, 20)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '2.5@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "Ready", actual)
	})

	t.Run("missing waiver", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, fob_id) VALUES ('3@test.com', 0, 1, NULL, 3)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '3@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "MissingWaiver", actual)
	})

	t.Run("missing waiver non-billable", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, fob_id) VALUES ('4@test.com', TRUE, 1, NULL, 4)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '4@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "Ready", actual)
	})

	t.Run("missing fob", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, fob_id) VALUES ('5@test.com', 1, 1, 1, NULL)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '5@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "MissingKeyFob", actual)
	})

	t.Run("inactive membership", func(t *testing.T) {
		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, fob_id) VALUES ('6@test.com', 0, 1, 1, 5)")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '6@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "PaymentInactive", actual)
	})

	t.Run("inactive root family member", func(t *testing.T) {
		_, err = db.Exec("INSERT INTO members (email, confirmed) VALUES ('root@family.com', 1)")
		require.NoError(t, err)

		_, err := db.Exec("INSERT INTO members (email, non_billable, confirmed, waiver, fob_id, root_family_member) VALUES ('7@test.com', 1, 1, 1, 6, (SELECT id FROM members WHERE email = 'root@family.com'))")
		require.NoError(t, err)

		var actual string
		err = db.QueryRow("SELECT access_status FROM members WHERE email = '7@test.com'").Scan(&actual)
		require.NoError(t, err)
		assert.Equal(t, "FamilyInactive", actual)
	})
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

	_, err = db.Exec("UPDATE members SET discount_type = 'anything', leadership = 1, confirmed = 1, non_billable = 1 WHERE id = 2")
	require.NoError(t, err)

	_, err = db.Exec("UPDATE members SET leadership = 0, non_billable = 0 WHERE id = 2")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO waivers (name, email, version) VALUES ('foo', 'foo@bar.com', 1)")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"NonBillableStatusAdded - The member has been marked as non-billable",
		"LeadershipStatusAdded - Designated as leadership",
		"AccessStatusChanged - Building access status changed from \"UnconfirmedEmail\" to \"MissingKeyFob\"",
		`DiscountTypeModified - Discount changed from "NULL" to "anything"`,
		"EmailConfirmed - Email address confirmed",
		"NonBillableStatusRemoved - The member is no longer marked as non-billable",
		"LeadershipStatusRemoved - No longer designated as leadership",
		"AccessStatusChanged - Building access status changed from \"MissingKeyFob\" to \"MissingWaiver\"",
		"DiscountTypeModified - Discount changed from \"anything\" to \"NULL\"",
		"WaiverSigned - Waiver signed",
		"AccessStatusChanged - Building access status changed from \"MissingWaiver\" to \"PaymentInactive\"",
	}, eventsToStrings(t, db))
}

func TestMemberWaiverRelation(t *testing.T) {
	t.Run("signed after signup", func(t *testing.T) {
		db := NewTest(t)

		_, err := db.Exec("INSERT INTO members (id, email, confirmed) VALUES (1, 'foo@bar.com', 1)")
		require.NoError(t, err)

		_, err = db.Exec("INSERT INTO waivers (name, email, version) VALUES ('foo', 'foo@bar.com', 1)")
		require.NoError(t, err)

		var waiverID int
		err = db.QueryRow("SELECT waiver FROM members WHERE email = 'foo@bar.com'").Scan(&waiverID)
		require.NoError(t, err)
		assert.Equal(t, 2, waiverID)
	})

	t.Run("signed before signup", func(t *testing.T) {
		db := NewTest(t)

		_, err := db.Exec("INSERT INTO waivers (name, email, version) VALUES ('foo', 'foo@bar.com', 1)")
		require.NoError(t, err)

		_, err = db.Exec("INSERT INTO members (id, email, confirmed) VALUES (1, 'foo@bar.com', 1)")
		require.NoError(t, err)

		var waiverID int
		err = db.QueryRow("SELECT waiver FROM members WHERE email = 'foo@bar.com'").Scan(&waiverID)
		require.NoError(t, err)
		assert.Equal(t, 2, waiverID)
	})
}

func TestActiveFobsLogic(t *testing.T) {
	db := NewTest(t)

	// Signup
	_, err := db.Exec("INSERT INTO members (id, email, confirmed, fob_id) VALUES (1, 'foo@bar.com', TRUE, 123)")
	require.NoError(t, err)
	assert.Equal(t, []int64{}, activeFobs(t, db))

	// Sign waiver
	_, err = db.Exec("INSERT INTO waivers (name, email, version) VALUES ('foo', 'foo@bar.com', 1)")
	require.NoError(t, err)
	assert.Equal(t, []int64{}, activeFobs(t, db))

	// Glider cache hasn't been invalidated yet
	var gliderRev int
	err = db.QueryRow("SELECT revision FROM glider_state").Scan(&gliderRev)
	require.NoError(t, err)
	assert.Equal(t, 1, gliderRev)

	// Stripe
	_, err = db.Exec("UPDATE members SET stripe_subscription_state = 'active' WHERE email = 'foo@bar.com'")
	require.NoError(t, err)
	assert.Equal(t, []int64{123}, activeFobs(t, db))

	// Glider cache has been invalidated
	err = db.QueryRow("SELECT revision FROM glider_state").Scan(&gliderRev)
	require.NoError(t, err)
	assert.Equal(t, 2, gliderRev)

	// Updating some other field doesn't invalidate the Glider cache
	_, err = db.Exec("UPDATE members SET admin_notes = 'v cool dood' WHERE email = 'foo@bar.com'")
	require.NoError(t, err)
	assert.Equal(t, []int64{123}, activeFobs(t, db))

	err = db.QueryRow("SELECT revision FROM glider_state").Scan(&gliderRev)
	require.NoError(t, err)
	assert.Equal(t, 2, gliderRev)

	// Deleting the member invalidates the cache again
	_, err = db.Exec("DELETE FROM members")
	require.NoError(t, err)

	err = db.QueryRow("SELECT revision FROM glider_state").Scan(&gliderRev)
	require.NoError(t, err)
	assert.Equal(t, 3, gliderRev)
}

func TestFobSwipes(t *testing.T) {
	db := NewTest(t)

	_, err := db.Exec("INSERT INTO members (id, email, fob_id) VALUES (1, 'foo@bar.com', 123)")
	require.NoError(t, err)

	_, err = db.Exec("INSERT INTO fob_swipes (uid, fob_id, timestamp) VALUES ('yeet', 123, 9001)")
	require.NoError(t, err)

	var lastSwipe int
	err = db.QueryRow("SELECT fob_last_seen FROM members").Scan(&lastSwipe)
	require.NoError(t, err)
	assert.Equal(t, 9001, lastSwipe)
}

func TestDiscountCancelation(t *testing.T) {
	db := NewTest(t)

	_, err := db.Exec("INSERT INTO members (id, email, confirmed, discount_type, stripe_subscription_state) VALUES (1, 'foo@bar.com', TRUE, 'anything', 'active')")
	require.NoError(t, err)

	// Unrelated write to prove that the discount wasn't incorrectly removed
	_, err = db.Exec("UPDATE members SET fob_id = 123")
	require.NoError(t, err)

	var discount *string
	err = db.QueryRow("SELECT discount_type FROM members").Scan(&discount)
	require.NoError(t, err)
	assert.Equal(t, "anything", *discount)

	// Cancel, prove that discount was removed
	_, err = db.Exec("UPDATE members SET stripe_subscription_state = NULL")
	require.NoError(t, err)

	err = db.QueryRow("SELECT discount_type FROM members").Scan(&discount)
	require.NoError(t, err)
	assert.Nil(t, discount)
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

func activeFobs(t *testing.T, db *sql.DB) []int64 {
	results, err := db.Query("SELECT fob_id FROM active_keyfobs")
	require.NoError(t, err)
	defer results.Close()

	all := []int64{}
	for results.Next() {
		var val int64
		err = results.Scan(&val)
		require.NoError(t, err)
		all = append(all, val)
	}
	require.NoError(t, results.Err())
	return all
}
