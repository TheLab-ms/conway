CREATE TABLE IF NOT EXISTS pruning_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    table_name TEXT NOT NULL,
    column TEXT NOT NULL DEFAULT 'timestamp',
    ttl INTEGER NOT NULL DEFAULT (2 * 365 * 86400) -- 2 years
);

INSERT INTO pruning_jobs (table_name, column, ttl) VALUES ('outbound_mail', 'created', 86400); -- 1 day
INSERT INTO pruning_jobs (table_name) VALUES ('fob_swipes');
INSERT INTO pruning_jobs (table_name) VALUES ('metrics');
INSERT INTO pruning_jobs (table_name) VALUES ('printer_events');
