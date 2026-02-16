package triggers

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/TheLab-ms/conway/engine"
)

// triggerRow represents a row from the triggers table.
type triggerRow struct {
	ID              int64
	Name            string
	Enabled         bool
	TriggerType     string // "event" or "timed"
	TriggerTable    string // event triggers only
	TriggerOp       string // event triggers only
	WhenClause      string // event triggers only
	ActionSQL       string
	IntervalSeconds int64   // timed triggers only
	Interval        string  // display string (e.g. "24h0m0s"), derived from IntervalSeconds
	LastRun         float64 // timed triggers only (unix timestamp)
}

// triggerName returns the SQLite trigger name for a given trigger ID.
// Uses a "user_trigger_" prefix to avoid collisions with hardcoded triggers.
func triggerName(id int64) string {
	return fmt.Sprintf("user_trigger_%d", id)
}

// validOps is the set of allowed trigger operations.
var validOps = map[string]bool{
	"INSERT": true,
	"UPDATE": true,
	"DELETE": true,
}

// columnInfo holds the name and type of a database column.
type columnInfo struct {
	Name string
	Type string
}

// tableColumnsInfo returns column names and types for the given table.
func tableColumnsInfo(db *sql.DB, table string) ([]columnInfo, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, fmt.Errorf("querying table_info for %s: %w", table, err)
	}
	defer rows.Close()

	var cols []columnInfo
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt *string
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, columnInfo{Name: name, Type: typ})
	}
	return cols, rows.Err()
}

// availableTables returns tables suitable for triggers, filtering out
// internal/trigger-related tables.
func availableTables(db *sql.DB) ([]string, error) {
	all, err := engine.AvailableTables(db)
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{
		"triggers": true,
	}
	var tables []string
	for _, name := range all {
		if !skip[name] {
			tables = append(tables, name)
		}
	}
	return tables, nil
}

// buildTriggerSQL generates the CREATE TRIGGER statement for a trigger row.
func buildTriggerSQL(id int64, table, op, whenClause, actionSQL string) (string, error) {
	op = strings.ToUpper(op)
	if !validOps[op] {
		return "", fmt.Errorf("invalid trigger operation: %s", op)
	}

	name := triggerName(id)

	// Format the optional WHEN clause.
	whenLine := ""
	if wc := strings.TrimSpace(whenClause); wc != "" {
		whenLine = "WHEN " + wc + "\n"
	}

	// Ensure actionSQL doesn't have trailing semicolons that would break
	// the trigger body (SQLite doesn't want the final semicolon after END).
	action := strings.TrimSpace(actionSQL)

	trigSQL := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %s AFTER %s ON %s
%sBEGIN
    %s
END`, name, op, table, whenLine, action)

	return trigSQL, nil
}

// createTrigger creates (or recreates) the SQLite trigger for a trigger row.
// Only applicable to event triggers.
func (m *Module) createTrigger(tr *triggerRow) error {
	if tr.TriggerType == "timed" {
		return nil // timed triggers don't use SQLite triggers
	}

	if tr.TriggerTable == "" || tr.TriggerOp == "" || tr.ActionSQL == "" {
		return nil
	}

	if !tr.Enabled {
		// Disabled triggers should not have SQL triggers active.
		m.dropTrigger(tr.ID)
		return nil
	}

	trigSQL, err := buildTriggerSQL(tr.ID, tr.TriggerTable, tr.TriggerOp, tr.WhenClause, tr.ActionSQL)
	if err != nil {
		return err
	}

	// Drop existing trigger first (in case it was updated).
	m.dropTrigger(tr.ID)

	_, err = m.db.Exec(trigSQL)
	if err != nil {
		return fmt.Errorf("creating trigger %s: %w", triggerName(tr.ID), err)
	}
	return nil
}

// dropTrigger drops the SQLite trigger for a trigger row.
func (m *Module) dropTrigger(id int64) {
	_, err := m.db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s", triggerName(id)))
	if err != nil {
		slog.Error("failed to drop trigger", "error", err, "triggerName", triggerName(id))
	}
}

// recreateAllTriggers drops and recreates SQL triggers for all enabled event
// trigger rows. Called on startup to ensure triggers are in sync with the database.
func (m *Module) recreateAllTriggers() {
	rows, err := m.db.Query("SELECT id, name, enabled, trigger_type, trigger_table, trigger_op, when_clause, action_sql, interval_seconds, last_run FROM triggers")
	if err != nil {
		slog.Error("failed to load triggers for recreation", "error", err)
		return
	}

	var triggers []triggerRow
	for rows.Next() {
		var tr triggerRow
		var enabled int
		if err := rows.Scan(&tr.ID, &tr.Name, &enabled, &tr.TriggerType, &tr.TriggerTable, &tr.TriggerOp, &tr.WhenClause, &tr.ActionSQL, &tr.IntervalSeconds, &tr.LastRun); err != nil {
			slog.Error("failed to scan trigger for recreation", "error", err)
			continue
		}
		tr.Enabled = enabled == 1
		triggers = append(triggers, tr)
	}
	rows.Close()

	for i := range triggers {
		if err := m.createTrigger(&triggers[i]); err != nil {
			slog.Error("failed to recreate trigger", "error", err, "triggerID", triggers[i].ID, "name", triggers[i].Name)
		}
	}
}

// loadAll returns all trigger rows ordered by type (event first) then ID.
func (m *Module) loadAll() ([]triggerRow, error) {
	rows, err := m.db.Query("SELECT id, name, enabled, trigger_type, trigger_table, trigger_op, when_clause, action_sql, interval_seconds, last_run FROM triggers ORDER BY trigger_type, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var triggers []triggerRow
	for rows.Next() {
		var tr triggerRow
		var enabled int
		if err := rows.Scan(&tr.ID, &tr.Name, &enabled, &tr.TriggerType, &tr.TriggerTable, &tr.TriggerOp, &tr.WhenClause, &tr.ActionSQL, &tr.IntervalSeconds, &tr.LastRun); err != nil {
			return nil, err
		}
		tr.Enabled = enabled == 1
		if tr.IntervalSeconds > 0 {
			tr.Interval = (time.Duration(tr.IntervalSeconds) * time.Second).String()
		}
		triggers = append(triggers, tr)
	}
	return triggers, rows.Err()
}

// visitTimedTriggers checks all enabled timed triggers and executes those
// whose interval has elapsed since their last run.
func (m *Module) visitTimedTriggers(ctx context.Context) bool {
	rows, err := m.db.QueryContext(ctx, "SELECT id, name, action_sql, interval_seconds, last_run FROM triggers WHERE trigger_type = 'timed' AND enabled = 1")
	if err != nil {
		slog.Error("failed to load timed triggers", "error", err)
		return false
	}
	defer rows.Close()

	type timedTrigger struct {
		ID              int64
		Name            string
		ActionSQL       string
		IntervalSeconds int64
		LastRun         float64
	}

	var triggers []timedTrigger
	for rows.Next() {
		var t timedTrigger
		if err := rows.Scan(&t.ID, &t.Name, &t.ActionSQL, &t.IntervalSeconds, &t.LastRun); err != nil {
			slog.Error("failed to scan timed trigger", "error", err)
			continue
		}
		triggers = append(triggers, t)
	}

	now := float64(time.Now().Unix())
	for _, t := range triggers {
		elapsed := now - t.LastRun
		if t.LastRun > 0 && elapsed < float64(t.IntervalSeconds) {
			continue // not ready yet
		}

		_, err := m.db.ExecContext(ctx, t.ActionSQL, sql.Named("last", int64(t.LastRun)))
		if err != nil {
			slog.Error("failed to execute timed trigger", "error", err, "trigger", t.Name, "id", t.ID)
			continue
		}

		_, err = m.db.ExecContext(ctx, "UPDATE triggers SET last_run = ? WHERE id = ?", now, t.ID)
		if err != nil {
			slog.Error("failed to update last_run for timed trigger", "error", err, "trigger", t.Name, "id", t.ID)
		}

		slog.Info("executed timed trigger", "trigger", t.Name, "id", t.ID)
	}

	return false
}
