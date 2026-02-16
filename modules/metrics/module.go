package metrics

import (
	"database/sql"
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
	return &Module{db: d}
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Hour*24, engine.Cleanup(m.db, "old metrics",
		"DELETE FROM metrics WHERE timestamp < unixepoch('subsec') - ?", defaultTTL)))
}
