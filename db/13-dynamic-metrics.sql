CREATE TABLE IF NOT EXISTS metrics_samplings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    query TEXT NOT NULL,
    interval_seconds INTEGER NOT NULL,
    created_at REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec'))
) STRICT;

INSERT OR IGNORE INTO metrics_samplings (name, query, interval_seconds) VALUES
    ('active-members', 'SELECT COUNT(*) FROM members WHERE access_status = ''Ready''', 86400),
    ('daily-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 86400),
    ('weekly-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 604800),
    ('monthly-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 2592000);

CREATE INDEX IF NOT EXISTS metrics_samplings_name_idx ON metrics_samplings (name);
