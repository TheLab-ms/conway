package payment

import (
	"context"
	"database/sql"
	"net/url"
	"testing"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/modules/members"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestModule constructs a payment.Module backed by a fresh in-memory DB
// with the members + payment + module_events migrations applied.
func newTestModule(t *testing.T) *Module {
	t.Helper()
	db := members.NewTestDB(t)
	engine.MustMigrate(db, migration)
	self, err := url.Parse("http://localhost")
	require.NoError(t, err)
	logger := engine.NewEventLogger(db, "payment-test")
	return New(db, self, logger)
}

func seedTestMember(t *testing.T, db *sql.DB, email, custID, subID, state string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO members (email, confirmed, stripe_customer_id, stripe_subscription_id, stripe_subscription_state)
		VALUES (?, 1, ?, ?, ?)`,
		email,
		nullStr(custID), nullStr(subID), nullStr(state))
	require.NoError(t, err)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func subState(t *testing.T, db *sql.DB, email string) (custID, subID, state, name string) {
	t.Helper()
	var c, s, st, n sql.NullString
	err := db.QueryRow(
		`SELECT stripe_customer_id, stripe_subscription_id, stripe_subscription_state, name
		   FROM members WHERE email = ?`, email).Scan(&c, &s, &st, &n)
	require.NoError(t, err)
	return c.String, s.String, st.String, n.String
}

// TestApplySubscriptionUpdate_BugRegression is the headline regression test.
// Reproduces the reported bug:
//
//   - Member has subscription A (status=active, scheduled to cancel at period end).
//   - A new subscription B is created and becomes active (webhook updates the
//     member to track B).
//   - Stripe later fires customer.subscription.deleted for the OLD A at the end
//     of its billed period.
//
// Before the fix, the third event would clobber the member's tracking back to
// the canceled A, deactivating them. After the fix, the stale event must be
// ignored.
func TestApplySubscriptionUpdate_BugRegression(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	// 1. Initial state: member has subscription A, active.
	seedTestMember(t, m.db, "user@example.com", "cus_1", "sub_A", "active")

	// 2. New subscription B comes in active. Should take over.
	applied, err := m.applySubscriptionUpdate(ctx, "user@example.com", "cus_1", "sub_B", "active", "User")
	require.NoError(t, err)
	assert.True(t, applied, "new active subscription should take over the canceling one")

	_, subID, state, _ := subState(t, m.db, "user@example.com")
	assert.Equal(t, "sub_B", subID)
	assert.Equal(t, "active", state)

	// 3. Stale delete for A arrives later. Must NOT clobber B.
	applied, err = m.applySubscriptionUpdate(ctx, "user@example.com", "cus_1", "sub_A", "canceled", "User")
	require.NoError(t, err)
	assert.False(t, applied, "stale cancel event for replaced subscription must be ignored")

	_, subID, state, _ = subState(t, m.db, "user@example.com")
	assert.Equal(t, "sub_B", subID, "currently-tracked subscription must remain B")
	assert.Equal(t, "active", state, "member must remain active")
}

func TestApplySubscriptionUpdate_Scenarios(t *testing.T) {
	type setup struct {
		custID, subID, state string // initial member row state ("" → NULL)
	}
	type event struct {
		custID, subID, status, name string
	}
	type expect struct {
		applied              bool
		subID, state, custID string
	}

	cases := []struct {
		name  string
		setup setup
		event event
		want  expect
	}{
		{
			name:  "first ever subscription created",
			setup: setup{},
			event: event{custID: "cus_1", subID: "sub_A", status: "active", name: "Alice"},
			want:  expect{applied: true, subID: "sub_A", state: "active", custID: "cus_1"},
		},
		{
			name:  "update to currently tracked sub: trialing -> active",
			setup: setup{custID: "cus_1", subID: "sub_A", state: "trialing"},
			event: event{custID: "cus_1", subID: "sub_A", status: "active", name: "Alice"},
			want:  expect{applied: true, subID: "sub_A", state: "active", custID: "cus_1"},
		},
		{
			name:  "cancel of currently tracked sub applied",
			setup: setup{custID: "cus_1", subID: "sub_A", state: "active"},
			event: event{custID: "cus_1", subID: "sub_A", status: "canceled", name: "Alice"},
			want:  expect{applied: true, subID: "sub_A", state: "canceled", custID: "cus_1"},
		},
		{
			name:  "past_due of currently tracked sub applied (intentional deactivation)",
			setup: setup{custID: "cus_1", subID: "sub_A", state: "active"},
			event: event{custID: "cus_1", subID: "sub_A", status: "past_due", name: "Alice"},
			want:  expect{applied: true, subID: "sub_A", state: "past_due", custID: "cus_1"},
		},
		{
			name:  "new active subscription replaces tracked active one",
			setup: setup{custID: "cus_1", subID: "sub_A", state: "active"},
			event: event{custID: "cus_1", subID: "sub_B", status: "active", name: "Alice"},
			want:  expect{applied: true, subID: "sub_B", state: "active", custID: "cus_1"},
		},
		{
			name:  "trialing of new sub replaces tracked active one",
			setup: setup{custID: "cus_1", subID: "sub_A", state: "active"},
			event: event{custID: "cus_1", subID: "sub_B", status: "trialing", name: "Alice"},
			want:  expect{applied: true, subID: "sub_B", state: "trialing", custID: "cus_1"},
		},
		{
			// THE BUG: stale cancel for an old/replaced subscription
			name:  "stale cancel of non-current sub is ignored",
			setup: setup{custID: "cus_1", subID: "sub_B", state: "active"},
			event: event{custID: "cus_1", subID: "sub_A", status: "canceled", name: "Alice"},
			want:  expect{applied: false, subID: "sub_B", state: "active", custID: "cus_1"},
		},
		{
			name:  "stale past_due of non-current sub is ignored",
			setup: setup{custID: "cus_1", subID: "sub_B", state: "active"},
			event: event{custID: "cus_1", subID: "sub_A", status: "past_due", name: "Alice"},
			want:  expect{applied: false, subID: "sub_B", state: "active", custID: "cus_1"},
		},
		{
			name:  "stale incomplete_expired of non-current sub is ignored",
			setup: setup{custID: "cus_1", subID: "sub_B", state: "trialing"},
			event: event{custID: "cus_1", subID: "sub_A", status: "incomplete_expired", name: "Alice"},
			want:  expect{applied: false, subID: "sub_B", state: "trialing", custID: "cus_1"},
		},
		{
			// If the member was already canceled (no current sub_id should not be the case
			// here because it remains set to the canceled sub's id), but for completeness:
			// a delete event for the same canceled sub still applies (idempotent).
			name:  "duplicate delete for currently-tracked canceled sub is idempotent",
			setup: setup{custID: "cus_1", subID: "sub_A", state: "canceled"},
			event: event{custID: "cus_1", subID: "sub_A", status: "canceled", name: "Alice"},
			want:  expect{applied: true, subID: "sub_A", state: "canceled", custID: "cus_1"},
		},
		{
			// After full cancellation, member's stripe_subscription_id is still
			// the old A. A brand new active subscription C must still take over.
			name:  "new active subscription after previous cancellation takes over",
			setup: setup{custID: "cus_1", subID: "sub_A", state: "canceled"},
			event: event{custID: "cus_1", subID: "sub_C", status: "active", name: "Alice"},
			want:  expect{applied: true, subID: "sub_C", state: "active", custID: "cus_1"},
		},
		{
			// Email casing must not matter — webhook lowercases on lookup.
			name:  "case-insensitive email lookup applies",
			setup: setup{custID: "cus_1", subID: "sub_A", state: "active"},
			// event email handled below — set in a sub-block
			event: event{custID: "cus_1", subID: "sub_A", status: "canceled", name: "Alice"},
			want:  expect{applied: true, subID: "sub_A", state: "canceled", custID: "cus_1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModule(t)
			email := "user@example.com"
			seedTestMember(t, m.db, email, tc.setup.custID, tc.setup.subID, tc.setup.state)

			// For the case-insensitive test, send mixed-case email.
			eventEmail := email
			if tc.name == "case-insensitive email lookup applies" {
				eventEmail = "USER@Example.COM"
			}

			applied, err := m.applySubscriptionUpdate(context.Background(),
				eventEmail, tc.event.custID, tc.event.subID, tc.event.status, tc.event.name)
			require.NoError(t, err)
			assert.Equal(t, tc.want.applied, applied, "applied")

			cust, sub, state, _ := subState(t, m.db, email)
			assert.Equal(t, tc.want.custID, cust, "stripe_customer_id")
			assert.Equal(t, tc.want.subID, sub, "stripe_subscription_id")
			assert.Equal(t, tc.want.state, state, "stripe_subscription_state")
		})
	}
}

// TestApplySubscriptionUpdate_NamePreservedWhenIgnored verifies the incidental
// benefit that ignored stale events don't overwrite the member's name with
// stale data from the customer object snapshot at that point in time.
func TestApplySubscriptionUpdate_NamePreservedWhenIgnored(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	seedTestMember(t, m.db, "user@example.com", "cus_1", "sub_B", "active")
	_, err := m.db.Exec(`UPDATE members SET name = 'Current Name' WHERE email = ?`, "user@example.com")
	require.NoError(t, err)

	// Stale event for old sub_A with a stale name must not overwrite.
	applied, err := m.applySubscriptionUpdate(ctx, "user@example.com", "cus_1", "sub_A", "canceled", "Stale Old Name")
	require.NoError(t, err)
	assert.False(t, applied)

	_, _, _, name := subState(t, m.db, "user@example.com")
	assert.Equal(t, "Current Name", name, "name must not be clobbered by stale event")
}

// TestApplySubscriptionUpdate_UnknownEmailIsNoop verifies behavior when no
// member matches the customer's email — no error, no rows affected.
func TestApplySubscriptionUpdate_UnknownEmailIsNoop(t *testing.T) {
	m := newTestModule(t)
	applied, err := m.applySubscriptionUpdate(context.Background(),
		"nobody@example.com", "cus_x", "sub_x", "active", "Nobody")
	require.NoError(t, err)
	assert.False(t, applied)
}

// TestApplySubscriptionUpdate_AccessStatusTransition proves the fix preserves
// the derived access_status (which drives building access via the
// active_keyfobs view) across the buggy event sequence.
func TestApplySubscriptionUpdate_AccessStatusTransition(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	// Set up a fully access-Ready member with subscription A.
	_, err := m.db.Exec(`
		INSERT INTO members (email, confirmed, waiver, fob_id,
		                     stripe_customer_id, stripe_subscription_id, stripe_subscription_state)
		VALUES ('user@example.com', 1, NULL, 1234, 'cus_1', 'sub_A', 'active')`)
	require.NoError(t, err)
	_, err = m.db.Exec(`INSERT INTO waivers (name, email, version) VALUES ('User', 'user@example.com', 1)`)
	require.NoError(t, err)

	mustAccess := func(want string) {
		t.Helper()
		var got string
		require.NoError(t, m.db.QueryRow(
			`SELECT access_status FROM members WHERE email = 'user@example.com'`).Scan(&got))
		assert.Equal(t, want, got)
	}
	mustAccess("Ready")

	// New active sub B takes over.
	_, err = m.applySubscriptionUpdate(ctx, "user@example.com", "cus_1", "sub_B", "active", "User")
	require.NoError(t, err)
	mustAccess("Ready")

	// Stale delete for A arrives. Member must remain Ready.
	_, err = m.applySubscriptionUpdate(ctx, "user@example.com", "cus_1", "sub_A", "canceled", "User")
	require.NoError(t, err)
	mustAccess("Ready")

	// Sanity: a delete for the *current* B does deactivate.
	_, err = m.applySubscriptionUpdate(ctx, "user@example.com", "cus_1", "sub_B", "canceled", "User")
	require.NoError(t, err)
	mustAccess("PaymentInactive")
}
