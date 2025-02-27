PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS migrations (
    name TEXT PRIMARY KEY
) STRICT;

INSERT INTO migrations (name) VALUES ('01-init.sql');
