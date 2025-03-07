CREATE TABLE IF NOT EXISTS metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec')),
    series TEXT NOT NULL,
    value REAL NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS metrics_timestamp_idx ON metrics (series, timestamp);
