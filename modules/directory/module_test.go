package directory

import (
	"context"
	"testing"

	"github.com/TheLab-ms/conway/modules/members"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryMembersNameOverride(t *testing.T) {
	db := members.NewTestDB(t)
	db.Exec(`ALTER TABLE members ADD COLUMN profile_picture BLOB`)
	db.Exec(`ALTER TABLE members ADD COLUMN bio TEXT`)

	// Insert a member with only billing name (no override)
	_, err := db.Exec(`INSERT INTO members (email, name, confirmed, non_billable, waiver, fob_id, profile_picture)
		VALUES ('billing@test.com', 'Billing Name', 1, 1, 1, 1, X'89504E47')`)
	require.NoError(t, err)

	// Insert a member with name_override set
	_, err = db.Exec(`INSERT INTO members (email, name, name_override, confirmed, non_billable, waiver, fob_id, profile_picture)
		VALUES ('override@test.com', 'Billing Name', 'Display Name', 1, 1, 1, 2, X'89504E47')`)
	require.NoError(t, err)

	// Insert a member with only name_override (empty billing name)
	_, err = db.Exec(`INSERT INTO members (email, name, name_override, confirmed, non_billable, waiver, fob_id, profile_picture)
		VALUES ('onlyoverride@test.com', '', 'Only Override', 1, 1, 1, 3, X'89504E47')`)
	require.NoError(t, err)

	m := &Module{db: db}
	results, err := m.queryMembers(context.Background())
	require.NoError(t, err)

	// Build a map for easier assertions
	byEmail := make(map[string]DirectoryMember)
	for _, member := range results {
		// We need to look up by ID since we don't have email in DirectoryMember
		var email string
		db.QueryRow("SELECT email FROM members WHERE id = ?", member.ID).Scan(&email)
		byEmail[email] = member
	}

	assert.Equal(t, "Billing Name", byEmail["billing@test.com"].DisplayName, "should use billing name when no override")
	assert.Equal(t, "Display Name", byEmail["override@test.com"].DisplayName, "should use name_override when set")
	assert.Equal(t, "Only Override", byEmail["onlyoverride@test.com"].DisplayName, "should use name_override even when billing name is empty")
}

func TestQueryMembersExcludesEmptyNames(t *testing.T) {
	db := members.NewTestDB(t)
	db.Exec(`ALTER TABLE members ADD COLUMN profile_picture BLOB`)
	db.Exec(`ALTER TABLE members ADD COLUMN bio TEXT`)

	// Member with empty name and no override should be excluded
	_, err := db.Exec(`INSERT INTO members (email, name, confirmed, non_billable, waiver, fob_id, profile_picture)
		VALUES ('empty@test.com', '', 1, 1, 1, 1, X'89504E47')`)
	require.NoError(t, err)

	// Member with NULL name and no override should be excluded
	_, err = db.Exec(`INSERT INTO members (email, confirmed, non_billable, waiver, fob_id, profile_picture)
		VALUES ('null@test.com', 1, 1, 1, 2, X'89504E47')`)
	require.NoError(t, err)

	// Member with valid name should be included
	_, err = db.Exec(`INSERT INTO members (email, name, confirmed, non_billable, waiver, fob_id, profile_picture)
		VALUES ('valid@test.com', 'Valid Name', 1, 1, 1, 3, X'89504E47')`)
	require.NoError(t, err)

	m := &Module{db: db}
	results, err := m.queryMembers(context.Background())
	require.NoError(t, err)

	assert.Len(t, results, 1)
	assert.Equal(t, "Valid Name", results[0].DisplayName)
}
