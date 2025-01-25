PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS members (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    name TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    confirmed INTEGER NOT NULL DEFAULT false,
    admin_notes TEXT NOT NULL DEFAULT '',

    identifier TEXT GENERATED ALWAYS AS (CASE WHEN (name IS NOT NULL AND name != '') THEN name ELSE email END) VIRTUAL,
    active INTEGER GENERATED ALWAYS AS (CASE WHEN ((paypal_subscription_id IS NOT NULL OR stripe_subscription_id IS NOT NULL OR non_billable = 1) AND confirmed = 1) THEN 1 ELSE 0 END) VIRTUAL,

    leadership INTEGER NOT NULL DEFAULT false,
    non_billable INTEGER NOT NULL DEFAULT false,
    discount_type TEXT,
    root_family_email TEXT,
    building_access_approver INTEGER REFERENCES members(id),

    waiver_signed INTEGER,
    waiver_id INTEGER REFERENCES waivers(id),
    fob_id INTEGER,

    stripe_customer_id TEXT,
    stripe_subscription_id TEXT,
    stripe_subscription_state TEXT,

    paypal_subscription_id TEXT,
    paypal_price REAL,
    paypal_last_payment INTEGER
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS members_email_idx ON members (email);
CREATE UNIQUE INDEX IF NOT EXISTS members_fob_idx ON members (fob_id);
CREATE INDEX IF NOT EXISTS members_pending_idx ON members (confirmed, created);

CREATE TABLE IF NOT EXISTS waivers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    pdf TEXT
) STRICT;

CREATE TABLE IF NOT EXISTS logins (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    send_email_at INTEGER DEFAULT (strftime('%s', 'now')),
    member INTEGER,
    code INTEGER NOT NULL DEFAULT 0,
    UNIQUE(code),
    FOREIGN KEY(member) REFERENCES members(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX IF NOT EXISTS logins_send_at_idx ON logins (send_email_at);
CREATE INDEX IF NOT EXISTS logins_created_idx ON logins (created);
CREATE UNIQUE INDEX IF NOT EXISTS logins_code_idx ON logins (code);

CREATE TABLE IF NOT EXISTS keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    label TEXT NOT NULL DEFAULT '',
    key_pem TEXT NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS api_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    label TEXT NOT NULL DEFAULT '',
    token TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS api_tokens_idx ON api_tokens (token);
