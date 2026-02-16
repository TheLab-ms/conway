package triggers

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// triggerRow represents a row from the triggers table.
type triggerRow struct {
	ID           int64
	Name         string
	Enabled      bool
	TriggerTable string
	TriggerOp    string
	WhenClause   string
	ActionSQL    string
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

// tableColumns returns the column names for the given table.
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

// placeholderPattern matches {placeholder} tokens in message templates.
var placeholderPattern = regexp.MustCompile(`\{(\w+)\}`)

// buildDiscordActionSQL constructs a self-contained action SQL string for
// inserting into discord_webhook_queue with template placeholder substitution.
// This is used both during migration and by the UI preset.
func buildDiscordActionSQL(webhookURL, messageTemplate, table, op string, db *sql.DB) string {
	op = strings.ToUpper(op)
	rowRef := "NEW"
	if op == "DELETE" {
		rowRef = "OLD"
	}

	// Build column set for this table.
	var colSet map[string]bool
	if db != nil {
		cols, err := tableColumns(db, table)
		if err == nil {
			colSet = make(map[string]bool, len(cols))
			for _, c := range cols {
				colSet[c] = true
			}
		}
	}

	// Build nested REPLACE() calls.
	matches := placeholderPattern.FindAllStringSubmatch(messageTemplate, -1)
	expr := fmt.Sprintf("'%s'", strings.ReplaceAll(messageTemplate, "'", "''"))
	for _, m := range matches {
		placeholder := m[0]
		colName := m[1]

		if colSet == nil || colSet[colName] {
			escaped := strings.ReplaceAll(placeholder, "'", "''")
			expr = fmt.Sprintf("REPLACE(%s, '%s', COALESCE(CAST(%s.%s AS TEXT), ''))",
				expr, escaped, rowRef, colName)
		}
	}

	// Escape the webhook URL for SQL.
	escapedURL := strings.ReplaceAll(webhookURL, "'", "''")

	return fmt.Sprintf(`INSERT INTO discord_webhook_queue (webhook_url, payload)
    VALUES ('%s', json_object('content', %s, 'username', 'Conway'));`, escapedURL, expr)
}
