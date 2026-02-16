// Package triggers provides a unified SQL trigger management system.
// It replaces the hardcoded member_events triggers and the Discord-specific
// webhook trigger CRUD, consolidating all user-configurable triggers into
// a single settings page with generic SQL action bodies.
package triggers

//go:generate go run github.com/a-h/templ/cmd/templ generate

import (
	"context"
	"database/sql"
	"fmt"
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

	m.migrateDiscordWebhooks()
	m.seedDefaults()
	m.recreateAllTriggers()

	return m
}

// ConfigSpec returns the config specification for the triggers page.
func (m *Module) ConfigSpec() config.Spec {
	return config.Spec{
		Module:      "triggers",
		Title:       "Triggers",
		ReadOnly:    true,
		Description: triggersDescription(),
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

// seedDefaults inserts the default member_events triggers if the triggers
// table is empty and the old hardcoded triggers still exist. This runs once
// on a fresh migration from the old system.
func (m *Module) seedDefaults() {
	var count int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM triggers").Scan(&count); err != nil {
		slog.Error("failed to count triggers", "error", err)
		return
	}
	if count > 0 {
		return // Already have triggers, nothing to seed.
	}

	// Check whether the old hardcoded triggers are present (they will be
	// until the members schema.sql edit removes them on next startup).
	// We seed defaults regardless — if the hardcoded ones still exist,
	// we'll name ours differently and they'll coexist until the old ones
	// are dropped by the schema change.

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

	for _, d := range defaults {
		_, err := m.db.Exec(
			`INSERT INTO triggers (name, enabled, trigger_table, trigger_op, when_clause, action_sql) VALUES (?, 1, ?, ?, ?, ?)`,
			d.Name, d.TriggerTable, d.TriggerOp, d.WhenClause, d.ActionSQL)
		if err != nil {
			slog.Error("failed to seed default trigger", "error", err, "name", d.Name)
		}
	}

	slog.Info("seeded default triggers", "count", len(defaults))
}

// migrateDiscordWebhooks converts existing discord_webhooks rows (that have
// trigger_table set) into rows in the unified triggers table. Each webhook
// becomes a self-contained trigger whose action SQL inserts directly into
// discord_webhook_queue with the webhook URL and rendered message inlined.
func (m *Module) migrateDiscordWebhooks() {
	// Check if the discord_webhooks table exists.
	var tableName string
	err := m.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='discord_webhooks'").Scan(&tableName)
	if err != nil {
		return // Table doesn't exist, nothing to migrate.
	}

	// Check if we already migrated (look for a marker).
	var marker string
	err = m.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='_triggers_discord_migrated'").Scan(&marker)
	if err == nil {
		return // Already migrated.
	}

	// Load all discord webhooks that have SQL triggers configured.
	rows, err := m.db.Query("SELECT id, webhook_url, message_template, enabled, trigger_table, trigger_op, when_clause FROM discord_webhooks WHERE trigger_table != ''")
	if err != nil {
		slog.Error("failed to load discord webhooks for migration", "error", err)
		return
	}

	type webhook struct {
		id              int64
		webhookURL      string
		messageTemplate string
		enabled         bool
		triggerTable    string
		triggerOp       string
		whenClause      string
	}
	var webhooks []webhook
	for rows.Next() {
		var wh webhook
		var enabledInt int
		if err := rows.Scan(&wh.id, &wh.webhookURL, &wh.messageTemplate, &enabledInt, &wh.triggerTable, &wh.triggerOp, &wh.whenClause); err != nil {
			slog.Error("failed to scan discord webhook for migration", "error", err)
			continue
		}
		wh.enabled = enabledInt == 1
		webhooks = append(webhooks, wh)
	}
	rows.Close()

	for _, wh := range webhooks {
		// Build self-contained action SQL that inlines the webhook URL and
		// message template with placeholder substitution.
		actionSQL := buildDiscordActionSQL(wh.webhookURL, wh.messageTemplate, wh.triggerTable, wh.triggerOp, m.db)

		enabledInt := 0
		if wh.enabled {
			enabledInt = 1
		}

		name := fmt.Sprintf("Discord: %s on %s", wh.triggerOp, wh.triggerTable)

		_, err := m.db.Exec(
			`INSERT INTO triggers (name, enabled, trigger_table, trigger_op, when_clause, action_sql) VALUES (?, ?, ?, ?, ?, ?)`,
			name, enabledInt, wh.triggerTable, wh.triggerOp, wh.whenClause, actionSQL)
		if err != nil {
			slog.Error("failed to migrate discord webhook to trigger", "error", err, "webhookID", wh.id)
			continue
		}

		// Drop the old per-webhook trigger.
		m.db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS discord_webhook_%d", wh.id))
	}

	// Mark migration as complete.
	m.db.Exec("CREATE TABLE IF NOT EXISTS _triggers_discord_migrated (id INTEGER PRIMARY KEY)")
	m.db.Exec("INSERT OR IGNORE INTO _triggers_discord_migrated (id) VALUES (1)")

	if len(webhooks) > 0 {
		slog.Info("migrated discord webhooks to unified triggers", "count", len(webhooks))
	}
}
