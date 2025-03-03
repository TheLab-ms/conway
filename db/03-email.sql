DROP TABLE logins;

CREATE TABLE IF NOT EXISTS outbound_mail (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    send_at INTEGER DEFAULT (strftime('%s', 'now')),
    recipient TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS outbound_mail_send_at_idx ON outbound_mail (send_at);
