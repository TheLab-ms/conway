package engine

import (
	"context"
	"database/sql"
	"log/slog"
)

const moduleEventsMigration = `
CREATE TABLE IF NOT EXISTS module_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    module TEXT NOT NULL,
    member INTEGER REFERENCES members(id) ON DELETE SET NULL,
    event_type TEXT NOT NULL,
    entity_id TEXT,
    entity_name TEXT,
    success INTEGER NOT NULL DEFAULT 1,
    details TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS module_events_module_created_idx ON module_events (module, created);
CREATE INDEX IF NOT EXISTS module_events_module_type_success_idx ON module_events (module, event_type, success);
`

// EventLogger provides centralized event logging for all modules.
type EventLogger struct {
	db *sql.DB
}

// NewEventLogger creates a new EventLogger and applies the required migration.
func NewEventLogger(db *sql.DB) *EventLogger {
	MustMigrate(db, moduleEventsMigration)
	return &EventLogger{db: db}
}

// LogEvent records an event to the module_events table.
//
// Parameters:
//   - module: the module name (e.g., "stripe", "discord", "bambu")
//   - memberID: the member ID if applicable, 0 for no member association
//   - eventType: the type of event (e.g., "WebhookReceived", "RoleSync")
//   - entityID: module-specific external ID (e.g., stripe_customer_id, discord_user_id, printer_serial)
//   - entityName: optional display name (e.g., printer_name)
//   - success: whether the operation succeeded
//   - details: additional details about the event
func (e *EventLogger) LogEvent(ctx context.Context, module string, memberID int64, eventType, entityID, entityName string, success bool, details string) {
	successInt := 0
	if success {
		successInt = 1
	}

	var memberPtr interface{} = nil
	if memberID > 0 {
		memberPtr = memberID
	}

	var entityIDPtr interface{} = nil
	if entityID != "" {
		entityIDPtr = entityID
	}

	var entityNamePtr interface{} = nil
	if entityName != "" {
		entityNamePtr = entityName
	}

	_, err := e.db.ExecContext(ctx,
		`INSERT INTO module_events (module, member, event_type, entity_id, entity_name, success, details)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		module, memberPtr, eventType, entityIDPtr, entityNamePtr, successInt, details)
	if err != nil {
		slog.Error("failed to log module event", "error", err, "module", module, "eventType", eventType)
	}
}
