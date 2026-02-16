// Package triggers provides a unified SQL trigger management system.
// It supports two types of triggers:
//   - Event triggers: SQLite triggers that fire on INSERT/UPDATE/DELETE operations
//   - Timed triggers: SQL statements executed on a configurable interval
//
// Timed triggers replace the former metrics sampling system, providing a
// general-purpose mechanism for periodic SQL execution.
package triggers

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/config"
	"github.com/a-h/templ"
)

const migration = `
CREATE TABLE IF NOT EXISTS triggers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    trigger_table TEXT NOT NULL DEFAULT '',
    trigger_op TEXT NOT NULL DEFAULT '',
    when_clause TEXT NOT NULL DEFAULT '',
    action_sql TEXT NOT NULL DEFAULT '',
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
) STRICT;
`

// Module manages user-configurable SQL triggers (both event and timed).
type Module struct {
	db *sql.DB
}

// New creates a new triggers module, applying migrations and seeding defaults.
func New(db *sql.DB) *Module {
	engine.MustMigrate(db, migration)

	// Add columns for timed trigger support.
	db.Exec("ALTER TABLE triggers ADD COLUMN trigger_type TEXT NOT NULL DEFAULT 'event'")
	db.Exec("ALTER TABLE triggers ADD COLUMN interval_seconds INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE triggers ADD COLUMN last_run REAL NOT NULL DEFAULT 0")

	m := &Module{db: db}

	m.migrateMetricsSamplings()
	m.seedDefaults()
	m.recreateAllTriggers()

	return m
}

// AttachWorkers registers the timed trigger polling worker.
func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Minute, m.visitTimedTriggers))
}

// ConfigSpec returns the config specification for the triggers page.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:   "triggers",
		Title:    "Triggers",
		ReadOnly: true,
		ExtraContent: func(ctx context.Context) templ.Component {
			rows, err := m.loadAll()
			if err != nil {
				slog.Error("failed to load triggers for config page", "error", err)
				rows = nil
			}
			tables, err := availableTables(m.db)
			if err != nil {
				slog.Error("failed to load tables for triggers page", "error", err)
				tables = nil
			}
			return renderTriggersCard(rows, tables)
		},
		Order: 5,
	}
}

// AttachRoutes registers the trigger CRUD and helper API routes.
func (m *Module) AttachRoutes(router *engine.Router) {
	router.HandleFunc("POST /admin/triggers/new", router.WithLeadership(m.handleCreate))
	router.HandleFunc("POST /admin/triggers/{id}/edit", router.WithLeadership(m.handleUpdate))
	router.HandleFunc("POST /admin/triggers/{id}/delete", router.WithLeadership(m.handleDelete))
	router.HandleFunc("GET /admin/triggers/columns", router.WithLeadership(m.handleTableColumns))
}

// migrateMetricsSamplings converts rows from the metrics_samplings table
// into timed triggers. Each sampling's query is wrapped in an INSERT INTO
// statement targeting the sampling's configured target table. This is
// idempotent: rows that already exist (by name) are skipped.
func (m *Module) migrateMetricsSamplings() {
	// Check if the metrics_samplings table exists.
	var tableExists int
	err := m.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='metrics_samplings'").Scan(&tableExists)
	if err != nil || tableExists == 0 {
		return
	}

	rows, err := m.db.Query("SELECT name, query, interval_seconds, target_table FROM metrics_samplings")
	if err != nil {
		slog.Error("failed to read metrics_samplings for migration", "error", err)
		return
	}

	// Collect all rows first so we release the database connection before
	// executing further queries (SQLite is limited to a single connection).
	type sampling struct {
		name, query, targetTable string
		intervalSeconds          int64
	}
	var samplings []sampling
	for rows.Next() {
		var s sampling
		if err := rows.Scan(&s.name, &s.query, &s.intervalSeconds, &s.targetTable); err != nil {
			slog.Error("failed to scan metrics_sampling row", "error", err)
			continue
		}
		samplings = append(samplings, s)
	}
	rows.Close()

	migrated := 0
	for _, s := range samplings {
		// Skip if a trigger with this name already exists.
		var exists int
		if err := m.db.QueryRow("SELECT COUNT(*) FROM triggers WHERE name = ?", s.name).Scan(&exists); err != nil {
			slog.Error("failed to check for existing trigger during migration", "error", err, "name", s.name)
			continue
		}
		if exists > 0 {
			continue
		}

		// Build the action SQL: wrap the sampling query in an INSERT INTO the target table.
		actionSQL := "INSERT INTO " + s.targetTable + " (series, value) VALUES ('" + s.name + "', (" + s.query + "));"

		_, err := m.db.Exec(
			`INSERT INTO triggers (name, enabled, trigger_type, interval_seconds, action_sql) VALUES (?, 1, 'timed', ?, ?)`,
			s.name, s.intervalSeconds, actionSQL)
		if err != nil {
			slog.Error("failed to migrate metrics sampling to trigger", "error", err, "name", s.name)
			continue
		}
		migrated++
	}

	if migrated > 0 {
		slog.Info("migrated metrics samplings to timed triggers", "count", migrated)
	}
}

// seedDefaults ensures the default triggers exist. Each default is inserted
// only if no trigger with the same name already exists, so this is safe to
// call on every startup.
func (m *Module) seedDefaults() {
	defaults := []triggerRow{
		// Event triggers: member activity logging
		{
			Name:         "Log: Email confirmed",
			Enabled:      true,
			TriggerType:  "event",
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "OLD.confirmed = 0 AND NEW.confirmed = 1",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'EmailConfirmed', 'Email address confirmed');",
		},
		{
			Name:         "Log: Discount changed",
			Enabled:      true,
			TriggerType:  "event",
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "COALESCE(OLD.discount_type, '') != COALESCE(NEW.discount_type, '')",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'DiscountTypeModified', 'Discount changed from \"' || COALESCE(OLD.discount_type, 'NULL') || '\" to \"' || COALESCE(NEW.discount_type, 'NULL') || '\"');",
		},
		{
			Name:         "Log: Access status changed",
			Enabled:      true,
			TriggerType:  "event",
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "OLD.access_status != NEW.access_status",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'AccessStatusChanged', 'Building access status changed from \"' || COALESCE(OLD.access_status, 'NULL') || '\" to \"' || COALESCE(NEW.access_status, 'NULL') || '\"');",
		},
		{
			Name:         "Log: Leadership added",
			Enabled:      true,
			TriggerType:  "event",
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "NEW.leadership = 1 AND OLD.leadership = 0",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'LeadershipStatusAdded', 'Designated as leadership');",
		},
		{
			Name:         "Log: Leadership removed",
			Enabled:      true,
			TriggerType:  "event",
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "NEW.leadership = 0 AND OLD.leadership = 1",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'LeadershipStatusRemoved', 'No longer designated as leadership');",
		},
		{
			Name:         "Log: Non-billable added",
			Enabled:      true,
			TriggerType:  "event",
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "NEW.non_billable IS true AND OLD.non_billable IS false",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'NonBillableStatusAdded', 'The member has been marked as non-billable');",
		},
		{
			Name:         "Log: Non-billable removed",
			Enabled:      true,
			TriggerType:  "event",
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "NEW.non_billable IS false AND OLD.non_billable IS true",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'NonBillableStatusRemoved', 'The member is no longer marked as non-billable');",
		},
		{
			Name:         "Log: Fob changed",
			Enabled:      true,
			TriggerType:  "event",
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "OLD.fob_id != NEW.fob_id",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'FobChanged', 'The fob ID changed from ' || COALESCE(OLD.fob_id, 'NULL') || ' to ' || COALESCE(NEW.fob_id, 'NULL'));",
		},
		{
			Name:         "Log: Waiver signed",
			Enabled:      true,
			TriggerType:  "event",
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "OLD.waiver IS NULL AND NEW.waiver IS NOT NULL",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'WaiverSigned', 'Waiver signed');",
		},
		// Timed triggers: metric samplings
		{
			Name:            "active-members",
			Enabled:         true,
			TriggerType:     "timed",
			IntervalSeconds: 86400,
			ActionSQL:       "INSERT INTO metrics (series, value) VALUES ('active-members', (SELECT COUNT(*) FROM members WHERE access_status = 'Ready'));",
		},
		{
			Name:            "daily-unique-visitors",
			Enabled:         true,
			TriggerType:     "timed",
			IntervalSeconds: 86400,
			ActionSQL:       "INSERT INTO metrics (series, value) VALUES ('daily-unique-visitors', (SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last));",
		},
		{
			Name:            "weekly-unique-visitors",
			Enabled:         true,
			TriggerType:     "timed",
			IntervalSeconds: 604800,
			ActionSQL:       "INSERT INTO metrics (series, value) VALUES ('weekly-unique-visitors', (SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last));",
		},
		{
			Name:            "monthly-unique-visitors",
			Enabled:         true,
			TriggerType:     "timed",
			IntervalSeconds: 2592000,
			ActionSQL:       "INSERT INTO metrics (series, value) VALUES ('monthly-unique-visitors', (SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last));",
		},
	}

	seeded := 0
	for _, d := range defaults {
		// Skip if a trigger with this name already exists.
		var exists int
		if err := m.db.QueryRow("SELECT COUNT(*) FROM triggers WHERE name = ?", d.Name).Scan(&exists); err != nil {
			slog.Error("failed to check for existing trigger", "error", err, "name", d.Name)
			continue
		}
		if exists > 0 {
			continue
		}

		enabled := 0
		if d.Enabled {
			enabled = 1
		}

		_, err := m.db.Exec(
			`INSERT INTO triggers (name, enabled, trigger_type, trigger_table, trigger_op, when_clause, action_sql, interval_seconds) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			d.Name, enabled, d.TriggerType, d.TriggerTable, d.TriggerOp, d.WhenClause, d.ActionSQL, d.IntervalSeconds)
		if err != nil {
			slog.Error("failed to seed default trigger", "error", err, "name", d.Name)
		}
		seeded++
	}

	if seeded > 0 {
		slog.Info("seeded default triggers", "count", seeded)
	}
}
