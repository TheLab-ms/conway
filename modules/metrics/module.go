package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/TheLab-ms/conway/engine"
	"github.com/TheLab-ms/conway/engine/db"
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

CREATE TRIGGER IF NOT EXISTS validate_metrics_sampling_target_table_insert
BEFORE INSERT ON metrics_samplings
FOR EACH ROW
BEGIN
    -- Check if the table exists
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM sqlite_master 
            WHERE type='table' AND name = NEW.target_table
        ) THEN RAISE(ABORT, 'Target table does not exist')
    END;

    
    -- Check if the table has the required 'series' column of type TEXT
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'series' AND type = 'TEXT'
        ) THEN RAISE(ABORT, 'Target table must have a series column of type TEXT')
    END;

    
    -- Check if the table has the required 'value' column of type REAL
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'value' AND type = 'REAL'
        ) THEN RAISE(ABORT, 'Target table must have a value column of type REAL')
    END;

    
    -- Check if the table has the required 'timestamp' column of type REAL
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'timestamp' AND type = 'REAL'
        ) THEN RAISE(ABORT, 'Target table must have a timestamp column of type REAL')
    END;
END;

CREATE TRIGGER IF NOT EXISTS validate_metrics_sampling_target_table_update
BEFORE UPDATE OF target_table ON metrics_samplings
FOR EACH ROW
BEGIN
    -- Check if the table exists
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM sqlite_master 
            WHERE type='table' AND name = NEW.target_table
        ) THEN RAISE(ABORT, 'Target table does not exist')
    END;

    
    -- Check if the table has the required 'series' column of type TEXT
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'series' AND type = 'TEXT'
        ) THEN RAISE(ABORT, 'Target table must have a series column of type TEXT')
    END;

    
    -- Check if the table has the required 'value' column of type REAL
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'value' AND type = 'REAL'
        ) THEN RAISE(ABORT, 'Target table must have a value column of type REAL')
    END;

    
    -- Check if the table has the required 'timestamp' column of type REAL
    SELECT CASE
        WHEN NOT EXISTS (
            SELECT 1 FROM pragma_table_info(NEW.target_table)
            WHERE name = 'timestamp' AND type = 'REAL'
        ) THEN RAISE(ABORT, 'Target table must have a timestamp column of type REAL')
    END;
END;

INSERT OR IGNORE INTO metrics_samplings (name, query, interval_seconds, target_table) VALUES
    ('active-members', 'SELECT COUNT(*) FROM members WHERE access_status = ''Ready''', 86400, 'metrics'),
    ('daily-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 86400, 'metrics'),
    ('weekly-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 604800, 'metrics'),
    ('monthly-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 2592000, 'metrics');
`

type Module struct {
	db *sql.DB
}

func New(d *sql.DB) *Module {
	db.MustMigrate(d, migration)
	return &Module{db: d}
}

func (m *Module) AttachWorkers(mgr *engine.ProcMgr) {
	mgr.Add(engine.Poll(time.Minute, m.visitSamplings))
	mgr.Add(engine.Poll(time.Hour*24, engine.Cleanup(m.db, "old metrics",
		"DELETE FROM metrics WHERE timestamp < unixepoch('subsec') - ?", defaultTTL)))
}

func (m *Module) visitSamplings(ctx context.Context) bool {
	samplings, err := m.getSamplings(ctx)
	if err != nil {
		slog.Error("failed to get metric samplings", "error", err)
		return false
	}

	for _, sample := range samplings {
		m.evalSampling(ctx, sample)
	}
	return false
}

func (m *Module) getSamplings(ctx context.Context) ([]*sampling, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT name, query, interval_seconds, target_table FROM metrics_samplings`)
	if err != nil {
		return nil, fmt.Errorf("querying samplings: %w", err)
	}
	defer rows.Close()

	var samplings []*sampling
	for rows.Next() {
		var sample sampling
		var intervalSeconds int64
		if err := rows.Scan(&sample.Name, &sample.Query, &intervalSeconds, &sample.TargetTable); err != nil {
			return nil, fmt.Errorf("scanning sampling: %w", err)
		}
		sample.Interval = time.Duration(intervalSeconds) * time.Second
		samplings = append(samplings, &sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating samplings: %w", err)
	}

	return samplings, nil
}

func (m *Module) evalSampling(ctx context.Context, sample *sampling) bool {
	var since *float64
	var start float64
	query := fmt.Sprintf("SELECT unixepoch('subsec') - MAX(timestamp), COALESCE(MAX(timestamp), 0.0) FROM %s WHERE series = $1", sample.TargetTable)
	err := m.db.QueryRowContext(ctx, query, sample.Name).Scan(&since, &start)
	if err != nil && err != sql.ErrNoRows {
		slog.Error("failed to check for metric", "metric", sample.Name, "error", err)
		return false
	}
	if err == nil && since != nil && *since < sample.Interval.Seconds() {
		return true // not ready to be sampled yet
	}

	insertQuery := fmt.Sprintf("INSERT INTO %s (series, value) VALUES ($1, (%s))", sample.TargetTable, sample.Query)
	_, err = m.db.ExecContext(ctx, insertQuery, sample.Name, sql.Named("last", int64(start)))
	if err != nil {
		slog.Error("failed to insert sampled metric", "metric", sample.Name, "target", sample.TargetTable, "error", err)
		return false
	}

	slog.Info("sampled metric", "metric", sample.Name, "target", sample.TargetTable)
	return true
}

type sampling struct {
	Name        string
	Query       string
	Interval    time.Duration
	TargetTable string
}
