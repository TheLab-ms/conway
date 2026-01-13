package engine

import (
	"context"
	"database/sql"
	"log/slog"
)

const integrationEventsMigration = `
CREATE TABLE IF NOT EXISTS integration_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    source TEXT NOT NULL,
    member INTEGER REFERENCES members(id) ON DELETE SET NULL,
    event_type TEXT NOT NULL,
    external_id TEXT,
    external_name TEXT,
    success INTEGER NOT NULL DEFAULT 1,
    details TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS integration_events_source_created_idx
    ON integration_events (source, created);
CREATE INDEX IF NOT EXISTS integration_events_source_type_success_idx
    ON integration_events (source, event_type, success);
CREATE INDEX IF NOT EXISTS integration_events_member_idx
    ON integration_events (member);
`

// EventLogger provides centralized logging for integration events.
type EventLogger struct {
	db *sql.DB
}

// NewEventLogger creates an EventLogger and applies the integration_events table migration.
func NewEventLogger(db *sql.DB) *EventLogger {
	MustMigrate(db, integrationEventsMigration)
	return &EventLogger{db: db}
}

// LogEvent inserts an integration event into the database.
// Parameters:
//   - source: the integration source (e.g., "discord", "stripe", "bambu")
//   - memberID: the member ID (0 for no member association)
//   - eventType: the type of event
//   - externalID: external identifier (discord_user_id, stripe_customer_id, printer_serial)
//   - externalName: optional display name (e.g., printer_name for bambu)
//   - success: whether the operation succeeded
//   - details: additional details about the event
func (e *EventLogger) LogEvent(ctx context.Context, source string, memberID int64, eventType, externalID, externalName string, success bool, details string) {
	if e == nil || e.db == nil {
		return
	}

	successInt := 0
	if success {
		successInt = 1
	}

	var memberPtr interface{} = nil
	if memberID > 0 {
		memberPtr = memberID
	}

	var extIDPtr interface{} = nil
	if externalID != "" {
		extIDPtr = externalID
	}

	var extNamePtr interface{} = nil
	if externalName != "" {
		extNamePtr = externalName
	}

	_, err := e.db.ExecContext(ctx,
		`INSERT INTO integration_events (source, member, event_type, external_id, external_name, success, details)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		source, memberPtr, eventType, extIDPtr, extNamePtr, successInt, details)
	if err != nil {
		slog.Error("failed to log integration event", "error", err, "source", source, "eventType", eventType)
	}
}
