package core

import (
	"database/sql"
	"testing"

	"github.com/TheLab-ms/conway/engine/db"
)

// NewTestDB creates a test database with the core schema applied.
func NewTestDB(t *testing.T) *sql.DB {
	d := db.OpenTest(t)
	db.MustMigrate(d, Migration)
	return d
}
