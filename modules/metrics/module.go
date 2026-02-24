package metrics

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
)

const defaultTTL = 2 * 365 * 24 * 60 * 60 // 2 years in seconds

const migration = `
CREATE TABLE IF NOT EXISTS metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec')),
    series TEXT NOT NULL,
    value REAL NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS metrics_timestamp_idx ON metrics (series, timestamp);

CREATE TABLE IF NOT EXISTS metrics_samplings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    query TEXT NOT NULL,
    interval_seconds INTEGER NOT NULL,
    target_table TEXT NOT NULL,
    created_at REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec'))
) STRICT;

CREATE INDEX IF NOT EXISTS metrics_samplings_name_idx ON metrics_samplings (name);

CREATE TABLE IF NOT EXISTS metrics_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    charts_json TEXT NOT NULL DEFAULT '[]'
) STRICT;
`

type Module struct {
	db *sql.DB
}

func New(d *sql.DB) *Module {
	engine.MustMigrate(d, migration)
	m := &Module{db: d}
	m.seedDefaults()
	return m
}

// seedDefaults inserts the default chart configuration if no config exists yet.
// This ensures the metrics dashboard has entries for the standard metric series
// without relying on a fallback that auto-discovers series from the metrics table.
func (m *Module) seedDefaults() {
	var count int
	if err := m.db.QueryRow("SELECT COUNT(*) FROM metrics_config").Scan(&count); err != nil {
		slog.Error("failed to check metrics config", "error", err)
		return
	}
	if count > 0 {
		return
	}

	const defaultChartsJSON = `[{"title":"Active Members","series":"active-members","color":""},{"title":"Daily Unique Visitors","series":"daily-unique-visitors","color":""},{"title":"Weekly Unique Visitors","series":"weekly-unique-visitors","color":""},{"title":"Monthly Unique Visitors","series":"monthly-unique-visitors","color":""}]`

	if _, err := m.db.Exec("INSERT INTO metrics_config (charts_json) VALUES (?)", defaultChartsJSON); err != nil {
		slog.Error("failed to seed default metrics config", "error", err)
	}
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Hour*24, engine.Cleanup(m.db, "old metrics",
		"DELETE FROM metrics WHERE timestamp < unixepoch('subsec') - ?", defaultTTL)))
}
