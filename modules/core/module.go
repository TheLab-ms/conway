// Package core provides the base database schema for Conway.
// This module must be registered first to ensure the core tables
// exist before other modules attempt to use them.
package core

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/db"
)

const defaultTTL = 2 * 365 * 24 * 60 * 60 // 2 years in seconds

// Module provides the core database schema.
type Module struct {
	db *sql.DB
}

// New creates a new core module and applies the base schema migration.
func New(d *sql.DB) *Module {
	db.MustMigrate(d, Migration)
	return &Module{db: d}
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Hour, m.cleanupFobSwipes))
	mgr.Add(engine.Poll(time.Hour, m.cleanupMemberEvents))
}

func (m *Module) cleanupFobSwipes(ctx context.Context) bool {
	start := time.Now()
	result, err := m.db.ExecContext(ctx, "DELETE FROM fob_swipes WHERE timestamp < unixepoch() - ?", defaultTTL)
	if err != nil {
		slog.Error("failed to cleanup fob swipes", "error", err)
		return false
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		slog.Info("cleaned up fob swipes", "duration", time.Since(start), "rows", rowsAffected)
	}
	return false
}

func (m *Module) cleanupMemberEvents(ctx context.Context) bool {
	start := time.Now()
	result, err := m.db.ExecContext(ctx, "DELETE FROM member_events WHERE created < unixepoch() - ?", defaultTTL)
	if err != nil {
		slog.Error("failed to cleanup member events", "error", err)
		return false
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		slog.Info("cleaned up member events", "duration", time.Since(start), "rows", rowsAffected)
	}
	return false
}
