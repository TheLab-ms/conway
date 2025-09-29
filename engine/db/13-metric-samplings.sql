CREATE TABLE IF NOT EXISTS metrics_samplings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    query TEXT NOT NULL,
    interval_seconds INTEGER NOT NULL,
    target_table TEXT NOT NULL,
    created_at REAL NOT NULL DEFAULT (strftime('%s', 'now', 'subsec'))
) STRICT;

INSERT OR IGNORE INTO metrics_samplings (name, query, interval_seconds, target_table) VALUES
    ('active-members', 'SELECT COUNT(*) FROM members WHERE access_status = ''Ready''', 86400, 'metrics'),
    ('daily-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 86400, 'metrics'),
    ('weekly-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 604800, 'metrics'),
    ('monthly-unique-visitors', 'SELECT COUNT(DISTINCT fob_id) FROM fob_swipes WHERE member IS NOT NULL AND timestamp > :last', 2592000, 'metrics');

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
