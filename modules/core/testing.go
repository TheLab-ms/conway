package core

import (
	"database/sql"
	"testing"

	"github.com/TheLab-ms/conway/engine"
)

// NewTestDB creates a test database with the core schema applied.
func NewTestDB(t *testing.T) *sql.DB {
	d := engine.OpenTest(t)
	engine.MustMigrate(d, Migration)
	return d
}
