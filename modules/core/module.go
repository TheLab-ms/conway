// Package core provides the base database schema for Conway.
// This module must be registered first to ensure the core tables
// exist before other modules attempt to use them.
package core

import (
	"database/sql"

	"github.com/TheLab-ms/conway/engine/db"
)

// Module provides the core database schema.
type Module struct {
	db *sql.DB
}

// New creates a new core module and applies the base schema migration.
func New(d *sql.DB) *Module {
	db.MustMigrate(d, Migration)
	return &Module{db: d}
}
