package triggers

import (
	"strings"
	"testing"

	"github.com/TheLab-ms/conway/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildTriggerSQL_Basic(t *testing.T) {
	sql, err := buildTriggerSQL(1, "members", "INSERT", "", "INSERT INTO log (msg) VALUES ('new member');")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "AFTER INSERT ON members") {
		t.Errorf("expected AFTER INSERT ON members, got:\n%s", sql)
	}
	if !strings.Contains(sql, "user_trigger_1") {
		t.Errorf("expected trigger name user_trigger_1, got:\n%s", sql)
	}
	if strings.Contains(sql, "WHEN") {
		t.Errorf("expected no WHEN clause, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_WithWhenClause(t *testing.T) {
	sql, err := buildTriggerSQL(42, "members", "UPDATE", "OLD.confirmed = 0 AND NEW.confirmed = 1", "INSERT INTO member_events (member, event) VALUES (NEW.id, 'EmailConfirmed');")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "WHEN OLD.confirmed = 0 AND NEW.confirmed = 1") {
		t.Errorf("expected WHEN clause, got:\n%s", sql)
	}
	if !strings.Contains(sql, "AFTER UPDATE ON members") {
		t.Errorf("expected AFTER UPDATE ON members, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_EmptyWhenClause(t *testing.T) {
	sql, err := buildTriggerSQL(1, "members", "INSERT", "   ", "INSERT INTO log (msg) VALUES ('test');")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(sql, "WHEN") {
		t.Errorf("expected no WHEN clause for whitespace-only when_clause, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_DeleteOperation(t *testing.T) {
	sql, err := buildTriggerSQL(3, "members", "DELETE", "OLD.status = 'active'", "INSERT INTO log (msg) VALUES ('deleted');")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "AFTER DELETE ON members") {
		t.Errorf("expected AFTER DELETE, got:\n%s", sql)
	}
	if !strings.Contains(sql, "WHEN OLD.status = 'active'") {
		t.Errorf("expected WHEN clause with OLD reference, got:\n%s", sql)
	}
}

func TestBuildTriggerSQL_InvalidOperation(t *testing.T) {
	_, err := buildTriggerSQL(1, "members", "TRUNCATE", "", "SELECT 1;")
	if err == nil {
		t.Fatal("expected error for invalid operation")
	}
}

func TestBuildTriggerSQL_ActionSQLPreserved(t *testing.T) {
	action := "INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'FobChanged', 'fob changed from ' || OLD.fob_id || ' to ' || NEW.fob_id);"
	sql, err := buildTriggerSQL(10, "members", "UPDATE", "OLD.fob_id != NEW.fob_id", action)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sql, "INSERT INTO member_events") {
		t.Errorf("expected action SQL to be preserved, got:\n%s", sql)
	}
}

func TestTriggerName(t *testing.T) {
	name := triggerName(42)
	if name != "user_trigger_42" {
		t.Errorf("expected user_trigger_42, got %s", name)
	}
}

func TestTimedTriggerExecution(t *testing.T) {
	db := engine.OpenTestDB(t)

	// Create a target table for the timed trigger to write into.
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS test_metrics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec')),
			series TEXT NOT NULL,
			value REAL NOT NULL
		) STRICT;
	`)
	require.NoError(t, err)

	// Create the triggers table (simulating module init without seeding).
	engine.MustMigrate(db, migration)
	db.Exec("ALTER TABLE triggers ADD COLUMN trigger_type TEXT NOT NULL DEFAULT 'event'")
	db.Exec("ALTER TABLE triggers ADD COLUMN interval_seconds INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE triggers ADD COLUMN last_run REAL NOT NULL DEFAULT 0")

	m := &Module{db: db}

	// Insert a timed trigger.
	_, err = db.Exec(
		`INSERT INTO triggers (name, enabled, trigger_type, interval_seconds, action_sql)
		 VALUES ('test-metric', 1, 'timed', 60, 'INSERT INTO test_metrics (series, value) VALUES (''test-metric'', (SELECT 42));')`)
	require.NoError(t, err)

	// First call should execute the trigger (last_run is 0).
	assert.False(t, m.visitTimedTriggers(t.Context()))

	// Verify the metric was inserted.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM test_metrics WHERE series = 'test-metric'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Verify the value.
	var value float64
	err = db.QueryRow("SELECT value FROM test_metrics WHERE series = 'test-metric'").Scan(&value)
	require.NoError(t, err)
	assert.Equal(t, 42.0, value)

	// Verify last_run was updated.
	var lastRun float64
	err = db.QueryRow("SELECT last_run FROM triggers WHERE name = 'test-metric'").Scan(&lastRun)
	require.NoError(t, err)
	assert.Greater(t, lastRun, 0.0)

	// Second call should NOT execute (interval hasn't elapsed).
	assert.False(t, m.visitTimedTriggers(t.Context()))

	err = db.QueryRow("SELECT COUNT(*) FROM test_metrics WHERE series = 'test-metric'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "should not have inserted another row")
}

func TestTimedTriggerWithLastParam(t *testing.T) {
	db := engine.OpenTestDB(t)

	// Create target table.
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS test_metrics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec')),
			series TEXT NOT NULL,
			value REAL NOT NULL
		) STRICT;
	`)
	require.NoError(t, err)

	// Create a source table to query against :last.
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS test_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec'))
		) STRICT;
	`)
	require.NoError(t, err)

	// Insert some test events.
	_, err = db.Exec("INSERT INTO test_events (timestamp) VALUES (100), (200), (300)")
	require.NoError(t, err)

	// Create triggers table.
	engine.MustMigrate(db, migration)
	db.Exec("ALTER TABLE triggers ADD COLUMN trigger_type TEXT NOT NULL DEFAULT 'event'")
	db.Exec("ALTER TABLE triggers ADD COLUMN interval_seconds INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE triggers ADD COLUMN last_run REAL NOT NULL DEFAULT 0")

	m := &Module{db: db}

	// Insert a timed trigger using :last parameter.
	// last_run starts at 0, so all events (timestamp > 0) should be counted.
	_, err = db.Exec(
		`INSERT INTO triggers (name, enabled, trigger_type, interval_seconds, action_sql)
		 VALUES ('events-since-last', 1, 'timed', 60,
		 'INSERT INTO test_metrics (series, value) VALUES (''events-since-last'', (SELECT COUNT(*) FROM test_events WHERE timestamp > :last));')`)
	require.NoError(t, err)

	assert.False(t, m.visitTimedTriggers(t.Context()))

	// All 3 events should have been counted (since :last was 0).
	var value float64
	err = db.QueryRow("SELECT value FROM test_metrics WHERE series = 'events-since-last'").Scan(&value)
	require.NoError(t, err)
	assert.Equal(t, 3.0, value)
}

func TestTimedTriggerDisabled(t *testing.T) {
	db := engine.OpenTestDB(t)

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS test_metrics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec')),
			series TEXT NOT NULL,
			value REAL NOT NULL
		) STRICT;
	`)
	require.NoError(t, err)

	engine.MustMigrate(db, migration)
	db.Exec("ALTER TABLE triggers ADD COLUMN trigger_type TEXT NOT NULL DEFAULT 'event'")
	db.Exec("ALTER TABLE triggers ADD COLUMN interval_seconds INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE triggers ADD COLUMN last_run REAL NOT NULL DEFAULT 0")

	m := &Module{db: db}

	// Insert a disabled timed trigger.
	_, err = db.Exec(
		`INSERT INTO triggers (name, enabled, trigger_type, interval_seconds, action_sql)
		 VALUES ('disabled-metric', 0, 'timed', 60, 'INSERT INTO test_metrics (series, value) VALUES (''disabled-metric'', 1);')`)
	require.NoError(t, err)

	assert.False(t, m.visitTimedTriggers(t.Context()))

	// Should not have executed since the trigger is disabled.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM test_metrics WHERE series = 'disabled-metric'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestEventTriggerDoesNotRunOnSchedule(t *testing.T) {
	db := engine.OpenTestDB(t)

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS test_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			msg TEXT NOT NULL
		) STRICT;
	`)
	require.NoError(t, err)

	engine.MustMigrate(db, migration)
	db.Exec("ALTER TABLE triggers ADD COLUMN trigger_type TEXT NOT NULL DEFAULT 'event'")
	db.Exec("ALTER TABLE triggers ADD COLUMN interval_seconds INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE triggers ADD COLUMN last_run REAL NOT NULL DEFAULT 0")

	m := &Module{db: db}

	// Insert an event trigger (should NOT be picked up by visitTimedTriggers).
	_, err = db.Exec(
		`INSERT INTO triggers (name, enabled, trigger_type, trigger_table, trigger_op, action_sql)
		 VALUES ('event-trigger', 1, 'event', 'test_log', 'INSERT', 'INSERT INTO test_log (msg) VALUES (''should not run'');')`)
	require.NoError(t, err)

	assert.False(t, m.visitTimedTriggers(t.Context()))

	// The event trigger's action SQL should NOT have been executed by the timed runner.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM test_log").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}
