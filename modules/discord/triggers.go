package discord

import (
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/TheLab-ms/conway/engine"
)

// triggerName returns the SQLite trigger name for a given webhook ID.
func triggerName(webhookID int64) string {
	return fmt.Sprintf("discord_webhook_%d", webhookID)
}

// validOps is the set of allowed trigger operations.
var validOps = map[string]bool{
	"INSERT": true,
	"UPDATE": true,
	"DELETE": true,
}

// placeholderPattern matches {placeholder} tokens in message templates.
var placeholderPattern = regexp.MustCompile(`\{(\w+)\}`)

// columnInfo holds the name and type of a database column.
type columnInfo struct {
	Name string
	Type string
}

// tableColumnsInfo returns column names and types for the given table, using PRAGMA table_info.
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

// tableColumns returns the column names for the given table, using PRAGMA table_info.
func tableColumns(db *sql.DB, table string) ([]string, error) {
	infos, err := tableColumnsInfo(db, table)
	if err != nil {
		return nil, err
	}
	cols := make([]string, len(infos))
	for i, c := range infos {
		cols[i] = c.Name
	}
	return cols, nil
}

// availableTables returns tables suitable for webhook triggers, filtering out
// discord-internal tables from the shared engine.AvailableTables helper.
func availableTables(db *sql.DB) ([]string, error) {
	all, err := engine.AvailableTables(db)
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{
		"discord_webhook_queue":    true,
		"discord_webhooks":         true,
		"_discord_migration_check": true,
	}
	var tables []string
	for _, name := range all {
		if !skip[name] {
			tables = append(tables, name)
		}
	}
	return tables, nil
}

// buildTriggerSQL generates the CREATE TRIGGER statement for a webhook.
// The trigger fires on the given table+operation and substitutes {column} placeholders
// in the message template with NEW.column or OLD.column values, then inserts the
// rendered JSON payload into discord_webhook_queue.
//
// For INSERT/UPDATE triggers, column references use NEW.<col>.
// For DELETE triggers, column references use OLD.<col>.
//
// If whenClause is non-empty, it is inserted as a WHEN expression so the trigger
// only fires when the expression evaluates to true. The whenClause should not
// include the "WHEN" keyword itself (it is added automatically).
func buildTriggerSQL(webhookID int64, table, op, messageTemplate string, tableCols []string, whenClause string) (string, error) {
	op = strings.ToUpper(op)
	if !validOps[op] {
		return "", fmt.Errorf("invalid trigger operation: %s", op)
	}

	name := triggerName(webhookID)

	// Determine the row reference prefix.
	rowRef := "NEW"
	if op == "DELETE" {
		rowRef = "OLD"
	}

	// Build the column set available in this table.
	colSet := make(map[string]bool, len(tableCols))
	for _, c := range tableCols {
		colSet[c] = true
	}

	// Find all placeholders used in the template.
	matches := placeholderPattern.FindAllStringSubmatch(messageTemplate, -1)

	// Build nested REPLACE() calls in SQL.
	// Start with the raw template string and wrap successive REPLACE calls around it.
	expr := fmt.Sprintf("w.message_template")
	for _, m := range matches {
		placeholder := m[0] // e.g. "{email}"
		colName := m[1]     // e.g. "email"

		if colSet[colName] {
			// Column exists in the table: substitute with the row value.
			expr = fmt.Sprintf("REPLACE(%s, '%s', COALESCE(CAST(%s.%s AS TEXT), ''))",
				expr, placeholder, rowRef, colName)
		}
		// If the column doesn't exist, leave the placeholder as-is (it might be
		// handled by the Go notifier or be a typo the admin can fix).
	}

	// Format the optional WHEN clause.
	whenLine := ""
	if wc := strings.TrimSpace(whenClause); wc != "" {
		whenLine = "WHEN " + wc + "\n"
	}

	triggerSQL := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %s AFTER %s ON %s
%sBEGIN
    INSERT INTO discord_webhook_queue (webhook_url, payload)
    SELECT
        w.webhook_url,
        json_object(
            'content', %s,
            'username', 'Conway'
        )
    FROM discord_webhooks w
    WHERE w.id = %d AND w.enabled = 1;
END`, name, op, table, whenLine, expr, webhookID)

	return triggerSQL, nil
}

// createTrigger creates (or recreates) the SQLite trigger for a webhook.
func (m *Module) createTrigger(wh *webhookRow) error {
	if wh.TriggerTable == "" || wh.TriggerOp == "" {
		return nil // App-level event (Signup, Print*), no SQL trigger needed.
	}

	cols, err := tableColumns(m.db, wh.TriggerTable)
	if err != nil {
		return fmt.Errorf("getting columns for %s: %w", wh.TriggerTable, err)
	}
	if len(cols) == 0 {
		return fmt.Errorf("table %s has no columns or does not exist", wh.TriggerTable)
	}

	trigSQL, err := buildTriggerSQL(wh.ID, wh.TriggerTable, wh.TriggerOp, wh.MessageTemplate, cols, wh.WhenClause)
	if err != nil {
		return err
	}

	// Drop existing trigger first (in case the webhook was updated).
	m.dropTrigger(wh.ID)

	_, err = m.db.Exec(trigSQL)
	if err != nil {
		return fmt.Errorf("creating trigger %s: %w", triggerName(wh.ID), err)
	}
	return nil
}

// dropTrigger drops the SQLite trigger for a webhook.
func (m *Module) dropTrigger(webhookID int64) {
	_, err := m.db.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s", triggerName(webhookID)))
	if err != nil {
		slog.Error("failed to drop webhook trigger", "error", err, "triggerName", triggerName(webhookID))
	}
}

// recreateAllTriggers drops and recreates SQL triggers for all webhooks that have
// a trigger_table configured. Called on startup to ensure triggers are in sync.
func (m *Module) recreateAllTriggers() {
	rows, err := m.db.Query("SELECT id, webhook_url, message_template, enabled, trigger_table, trigger_op, when_clause FROM discord_webhooks WHERE trigger_table != ''")
	if err != nil {
		slog.Error("failed to load webhooks for trigger recreation", "error", err)
		return
	}
	defer rows.Close()

	// Collect all webhooks first so the rows iterator is fully consumed before
	// createTrigger issues additional queries. With MaxOpenConns=1 (SQLite),
	// interleaving queries while rows is open causes a deadlock.
	var webhooks []webhookRow
	for rows.Next() {
		var wh webhookRow
		var enabled int
		if err := rows.Scan(&wh.ID, &wh.WebhookURL, &wh.MessageTemplate, &enabled, &wh.TriggerTable, &wh.TriggerOp, &wh.WhenClause); err != nil {
			slog.Error("failed to scan webhook for trigger recreation", "error", err)
			continue
		}
		wh.Enabled = enabled == 1
		webhooks = append(webhooks, wh)
	}
	if err := rows.Err(); err != nil {
		slog.Error("error iterating webhooks for trigger recreation", "error", err)
		return
	}
	rows.Close()

	for i := range webhooks {
		if err := m.createTrigger(&webhooks[i]); err != nil {
			slog.Error("failed to recreate webhook trigger", "error", err, "webhookID", webhooks[i].ID)
		}
	}
}

// migrateLegacyWebhooks converts webhooks that used the old trigger_event system
// (which relied on the discord_webhook_on_member_event trigger) to the new
// per-webhook SQL trigger system. Legacy member event webhooks become SQL triggers
// on the member_events table with INSERT operation.
func migrateLegacyWebhooks(db *sql.DB) {
	// Only migrate rows that have a trigger_event set for member events
	// but no trigger_table yet.
	memberEvents := []string{
		"EmailConfirmed", "AccessStatusChanged", "WaiverSigned",
		"DiscountTypeModified", "LeadershipStatusAdded", "LeadershipStatusRemoved",
		"NonBillableStatusAdded", "NonBillableStatusRemoved", "FobChanged",
	}

	for _, event := range memberEvents {
		_, err := db.Exec(`UPDATE discord_webhooks SET trigger_table = 'member_events', trigger_op = 'INSERT' WHERE trigger_event = ? AND trigger_table = ''`, event)
		if err != nil {
			slog.Error("failed to migrate legacy webhook", "error", err, "event", event)
		}
	}
}

// migrateConditionsToWhenClause migrates data from the legacy discord_webhook_conditions
// table into the when_clause column on discord_webhooks, then drops the conditions table.
func migrateConditionsToWhenClause(db *sql.DB) {
	// Check if the conditions table still exists.
	var tableName string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='discord_webhook_conditions'").Scan(&tableName)
	if err != nil {
		return // Table doesn't exist, nothing to migrate.
	}

	// Load all conditions grouped by webhook_id.
	rows, err := db.Query("SELECT webhook_id, column_name, operator, value, logic FROM discord_webhook_conditions ORDER BY webhook_id, id")
	if err != nil {
		slog.Error("failed to load conditions for migration", "error", err)
		return
	}

	type cond struct {
		col, op, val, logic string
	}
	grouped := map[int64][]cond{}
	for rows.Next() {
		var webhookID int64
		var c cond
		if err := rows.Scan(&webhookID, &c.col, &c.op, &c.val, &c.logic); err != nil {
			slog.Error("failed to scan condition for migration", "error", err)
			continue
		}
		grouped[webhookID] = append(grouped[webhookID], c)
	}
	rows.Close()

	// Build WHEN clause strings and update each webhook.
	for webhookID, conds := range grouped {
		var parts []string
		for i, c := range conds {
			op := strings.ToUpper(c.op)
			isUnary := op == "IS NULL" || op == "IS NOT NULL"
			var part string
			if isUnary {
				part = fmt.Sprintf("NEW.%s %s", c.col, op)
			} else {
				escaped := strings.ReplaceAll(c.val, "'", "''")
				part = fmt.Sprintf("NEW.%s %s '%s'", c.col, op, escaped)
			}
			if i > 0 {
				logic := strings.ToUpper(c.logic)
				if logic != "OR" {
					logic = "AND"
				}
				part = logic + " " + part
			}
			parts = append(parts, part)
		}
		whenClause := strings.Join(parts, " ")

		// Only set when_clause if it's currently empty (don't overwrite manual edits).
		_, err := db.Exec("UPDATE discord_webhooks SET when_clause = ? WHERE id = ? AND when_clause = ''", whenClause, webhookID)
		if err != nil {
			slog.Error("failed to migrate conditions to when_clause", "error", err, "webhookID", webhookID)
		}
	}

	// Drop the conditions table now that data has been migrated.
	if _, err := db.Exec("DROP TABLE IF EXISTS discord_webhook_conditions"); err != nil {
		slog.Error("failed to drop discord_webhook_conditions table", "error", err)
	}
}
