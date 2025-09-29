CREATE TABLE IF NOT EXISTS printer_events (
    uid TEXT PRIMARY KEY,
    timestamp INTEGER NOT NULL,
    printer_name TEXT NOT NULL,
    job_finished_at INTEGER,
    error_code TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS printer_events_timestamp_idx ON printer_events (timestamp);
