// Package triggers provides a unified SQL trigger management system.
// It replaces the hardcoded member_events triggers, consolidating all
// user-configurable triggers into a single settings page with generic
// SQL action bodies.
package triggers

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"log/slog"

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

// Module manages user-configurable SQL triggers.
type Module struct {
	db *sql.DB
}

// New creates a new triggers module, applying migrations and seeding defaults.
func New(db *sql.DB) *Module {
	engine.MustMigrate(db, migration)

	m := &Module{db: db}

	m.seedDefaults()
	m.recreateAllTriggers()

	return m
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

// seedDefaults ensures the default member_events triggers exist. Each default
// is inserted only if no trigger with the same name already exists, so this
// is safe to call on every startup.
func (m *Module) seedDefaults() {
	defaults := []triggerRow{
		{
			Name:         "Log: Email confirmed",
			Enabled:      true,
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "OLD.confirmed = 0 AND NEW.confirmed = 1",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'EmailConfirmed', 'Email address confirmed');",
		},
		{
			Name:         "Log: Discount changed",
			Enabled:      true,
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "COALESCE(OLD.discount_type, '') != COALESCE(NEW.discount_type, '')",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'DiscountTypeModified', 'Discount changed from \"' || COALESCE(OLD.discount_type, 'NULL') || '\" to \"' || COALESCE(NEW.discount_type, 'NULL') || '\"');",
		},
		{
			Name:         "Log: Access status changed",
			Enabled:      true,
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "OLD.access_status != NEW.access_status",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'AccessStatusChanged', 'Building access status changed from \"' || COALESCE(OLD.access_status, 'NULL') || '\" to \"' || COALESCE(NEW.access_status, 'NULL') || '\"');",
		},
		{
			Name:         "Log: Leadership added",
			Enabled:      true,
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "NEW.leadership = 1 AND OLD.leadership = 0",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'LeadershipStatusAdded', 'Designated as leadership');",
		},
		{
			Name:         "Log: Leadership removed",
			Enabled:      true,
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "NEW.leadership = 0 AND OLD.leadership = 1",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'LeadershipStatusRemoved', 'No longer designated as leadership');",
		},
		{
			Name:         "Log: Non-billable added",
			Enabled:      true,
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "NEW.non_billable IS true AND OLD.non_billable IS false",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'NonBillableStatusAdded', 'The member has been marked as non-billable');",
		},
		{
			Name:         "Log: Non-billable removed",
			Enabled:      true,
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "NEW.non_billable IS false AND OLD.non_billable IS true",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'NonBillableStatusRemoved', 'The member is no longer marked as non-billable');",
		},
		{
			Name:         "Log: Fob changed",
			Enabled:      true,
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "OLD.fob_id != NEW.fob_id",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'FobChanged', 'The fob ID changed from ' || COALESCE(OLD.fob_id, 'NULL') || ' to ' || COALESCE(NEW.fob_id, 'NULL'));",
		},
		{
			Name:         "Log: Waiver signed",
			Enabled:      true,
			TriggerTable: "members",
			TriggerOp:    "UPDATE",
			WhenClause:   "OLD.waiver IS NULL AND NEW.waiver IS NOT NULL",
			ActionSQL:    "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'WaiverSigned', 'Waiver signed');",
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

		_, err := m.db.Exec(
			`INSERT INTO triggers (name, enabled, trigger_table, trigger_op, when_clause, action_sql) VALUES (?, 1, ?, ?, ?, ?)`,
			d.Name, d.TriggerTable, d.TriggerOp, d.WhenClause, d.ActionSQL)
		if err != nil {
			slog.Error("failed to seed default trigger", "error", err, "name", d.Name)
		}
		seeded++
	}

	if seeded > 0 {
		slog.Info("seeded default triggers", "count", seeded)
	}
}
