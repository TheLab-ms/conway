PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS members (
    /* Identifiers */
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    email TEXT NOT NULL DEFAULT '',
    confirmed INTEGER NOT NULL DEFAULT false,

    /* Metadata */
    name TEXT NOT NULL DEFAULT '',
    admin_notes TEXT NOT NULL DEFAULT '',
    identifier TEXT GENERATED ALWAYS AS (CASE WHEN (name IS NOT NULL AND name != '') THEN name ELSE email END) VIRTUAL,

    /* Payment and Discounts */
    active INTEGER GENERATED ALWAYS AS (CASE WHEN ((paypal_subscription_id IS NOT NULL OR stripe_subscription_state = 'active' OR non_billable = 1) AND confirmed = 1) THEN 1 ELSE 0 END) VIRTUAL,
    discount_type TEXT CHECK (discount_type != 'family' OR root_family_member IS NOT NULL),
    root_family_member INTEGER REFERENCES members(id) CHECK (root_family_member != id),
    root_family_member_active INTEGER,

    /* Building Access */
    waiver INTEGER REFERENCES waivers(id),
    building_access_approver INTEGER REFERENCES members(id),
    fob_id INTEGER,
    access_status TEXT GENERATED ALWAYS AS ( CASE 
            WHEN (confirmed IS NOT 1) THEN "Unconfirmed Email"
            WHEN (waiver IS NULL) THEN "Missing Waiver"
            WHEN (fob_id IS NULL OR fob_id = 0) THEN "Key Fob Not Assigned"
            WHEN (building_access_approver IS NULL) THEN "Access Not Approved"
            WHEN (root_family_member IS NOT NULL AND root_family_member_active = 0) THEN "Root Family Member Inactive"
            WHEN (active IS NOT 1) THEN "Membership Inactive"
        ELSE "Ready" END) VIRTUAL,

    /* Designations */
    leadership INTEGER NOT NULL DEFAULT false,
    non_billable INTEGER NOT NULL DEFAULT false,

    /* Stripe */
    stripe_customer_id TEXT,
    stripe_subscription_id TEXT,
    stripe_subscription_state TEXT,

    /* Paypal */
    paypal_subscription_id TEXT,
    paypal_price REAL,
    paypal_last_payment INTEGER
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS members_email_idx ON members (email);
CREATE UNIQUE INDEX IF NOT EXISTS members_fob_idx ON members (fob_id);
CREATE INDEX IF NOT EXISTS members_pending_idx ON members (confirmed, created);
CREATE INDEX IF NOT EXISTS members_root_family_idx ON members (root_family_member);

CREATE TRIGGER IF NOT EXISTS members_family_relationship_update AFTER UPDATE ON members
BEGIN
  UPDATE members SET root_family_member_active = NEW.active WHERE root_family_member = NEW.id;
  UPDATE members SET root_family_member_active = (SELECT active FROM members WHERE id = NEW.root_family_member) WHERE id = NEW.id AND root_family_member IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS members_family_relationship_insert AFTER INSERT ON members
BEGIN
  UPDATE members SET root_family_member_active = (SELECT active FROM members WHERE id = NEW.root_family_member) WHERE id = NEW.id AND root_family_member IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS members_family_relationship_delete BEFORE DELETE ON members
BEGIN
  UPDATE members SET root_family_member_active = 0, root_family_member = NULL WHERE root_family_member = OLD.id;
END;

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
