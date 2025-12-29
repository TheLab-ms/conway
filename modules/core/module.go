// Package core provides the base database schema for Conway.
// This module must be registered first to ensure the core tables
// exist before other modules attempt to use them.
package core

import (
	"database/sql"
	"time"

	"github.com/TheLab-ms/conway/engine"
)

const defaultTTL = 2 * 365 * 24 * 60 * 60 // 2 years in seconds

// Module provides the core database schema.
type Module struct {
	db *sql.DB
}

// New creates a new core module and applies the base schema migration.
func New(d *sql.DB) *Module {
	engine.MustMigrate(d, Migration)
	return &Module{db: d}
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "fob swipes",
		"DELETE FROM fob_swipes WHERE timestamp < unixepoch() - ?", defaultTTL)))
	mgr.Add(engine.Poll(time.Hour, engine.Cleanup(m.db, "member events",
		"DELETE FROM member_events WHERE created < unixepoch() - ?", defaultTTL)))
}
