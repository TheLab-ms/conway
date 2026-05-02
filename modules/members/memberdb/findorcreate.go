// Package memberdb contains low-level helpers for reading and writing the
// members table that need to be callable from packages which themselves
// expose request-context primitives the parent members package depends on.
//
// It is intentionally a leaf package with no other internal dependencies so
// that auth, discord, google, admin, etc. can use it without creating an
// import cycle through modules/members → modules/auth.
package memberdb

import (
	"context"
	"database/sql"
)

// RowQuerier is the narrow interface satisfied by *sql.DB, *sql.Tx, and
// *sql.Conn. We accept it so callers can run inside a transaction if they
// have one, but the common case is a bare *sql.DB.
type RowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// FindOrCreateByEmail returns the id of the member with the given email,
// creating a row if none exists. It is the canonical implementation of the
// "INSERT ... ON CONFLICT (email) DO UPDATE SET email=email RETURNING id"
// pattern that was previously copy-pasted across the auth, discord, google,
// and admin modules.
//
// The email is stored verbatim (no normalization). Other parts of the
// codebase do mix-case lookups via LOWER(email) or strings.ToLower; consider
// fixing that inconsistency in a separate change.
func FindOrCreateByEmail(ctx context.Context, db RowQuerier, email string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx,
		`INSERT INTO members (email) VALUES ($1)
		   ON CONFLICT (email) DO UPDATE SET email = email
		 RETURNING id`,
		email).Scan(&id)
	return id, err
}
