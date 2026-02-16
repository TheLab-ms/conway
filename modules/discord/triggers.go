package discord

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

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
