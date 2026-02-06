CREATE TABLE IF NOT EXISTS waivers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    version INTEGER NOT NULL,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    name TEXT NOT NULL,
    email TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS waivers_email_idx ON waivers (email);

CREATE TABLE IF NOT EXISTS members (
    /* Identifiers */
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    email TEXT NOT NULL DEFAULT '',
    confirmed INTEGER NOT NULL DEFAULT false,

    /* Metadata */
    name TEXT NOT NULL DEFAULT '',
    name_override TEXT,
    admin_notes TEXT NOT NULL DEFAULT '',
    identifier TEXT GENERATED ALWAYS AS (CASE WHEN (name IS NOT NULL AND name != '') THEN name ELSE email END) VIRTUAL,

    /* Building Access */
    waiver INTEGER REFERENCES waivers(id) ON DELETE SET NULL,
    fob_id INTEGER,
    fob_last_seen INTEGER,
    access_status TEXT NOT NULL GENERATED ALWAYS AS ( CASE
            WHEN (confirmed IS NOT TRUE AND non_billable IS NOT TRUE) THEN 'UnconfirmedEmail'
            WHEN (waiver IS NULL AND non_billable IS NOT TRUE) THEN 'MissingWaiver'
            WHEN (payment_status IS NULL AND non_billable IS NOT TRUE) THEN 'PaymentInactive'
            WHEN (fob_id IS NULL OR fob_id = 0) THEN 'MissingKeyFob'
            WHEN (root_family_member IS NOT NULL AND root_family_member_active = 0) THEN 'FamilyInactive'
        ELSE 'Ready' END) VIRTUAL,

    /* Designations */
    leadership INTEGER NOT NULL DEFAULT false,
    non_billable INTEGER NOT NULL DEFAULT false,

    /* Payment and Discounts */
    discount_type TEXT,
    bill_annually INTEGER NOT NULL DEFAULT false,
    root_family_member INTEGER REFERENCES members(id) ON DELETE SET NULL CHECK (root_family_member != id),
    root_family_member_active INTEGER,
    payment_status TEXT GENERATED ALWAYS AS ( CASE
            WHEN (confirmed != 1) THEN NULL
            WHEN (paypal_subscription_id IS NOT NULL) THEN 'ActivePaypal'
            WHEN (stripe_subscription_state = 'active') THEN 'ActiveStripe'
            WHEN (non_billable = 1) THEN 'ActiveNonBillable'
        ELSE NULL END) VIRTUAL,

    /* Stripe */
    stripe_customer_id TEXT,
    stripe_subscription_id TEXT,
    stripe_subscription_state TEXT,
    paypal_subscription_id TEXT,
    paypal_price REAL,

	/* Discord */
	discord_user_id TEXT,
	discord_last_synced INTEGER,
	discord_username TEXT,
	discord_email TEXT,
	discord_avatar BLOB
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS members_email_idx ON members (email);

CREATE UNIQUE INDEX IF NOT EXISTS members_fob_idx ON members (fob_id);

CREATE INDEX IF NOT EXISTS members_fob_last_seen_idx ON members (fob_last_seen);

CREATE INDEX IF NOT EXISTS members_pending_idx ON members (confirmed, created);

CREATE INDEX IF NOT EXISTS members_root_family_idx ON members (root_family_member);

CREATE TRIGGER IF NOT EXISTS members_family_relationship_update AFTER UPDATE ON members
BEGIN
  UPDATE members SET root_family_member_active = NEW.payment_status IS NOT NULL WHERE root_family_member = NEW.id;

  UPDATE members SET root_family_member_active = (SELECT payment_status IS NOT NULL FROM members WHERE id = NEW.root_family_member) WHERE id = NEW.id AND root_family_member IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS members_family_relationship_insert AFTER INSERT ON members
BEGIN
  UPDATE members SET root_family_member_active = (SELECT payment_status IS NOT NULL FROM members WHERE id = NEW.root_family_member) WHERE id = NEW.id AND root_family_member IS NOT NULL;
END;

CREATE TRIGGER IF NOT EXISTS members_family_relationship_delete BEFORE DELETE ON members
BEGIN
  UPDATE members SET root_family_member_active = 0, root_family_member = NULL WHERE root_family_member = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS members_accept_signed_waiver AFTER INSERT ON waivers
BEGIN
UPDATE members SET waiver = NEW.id WHERE email = NEW.email;
END;

CREATE TRIGGER IF NOT EXISTS members_resolve_waiver AFTER INSERT ON members
BEGIN
UPDATE members SET waiver = (SELECT id FROM waivers WHERE email = NEW.email) WHERE email = NEW.email AND EXISTS (SELECT 1 FROM waivers WHERE email = NEW.email);
END;

CREATE VIEW IF NOT EXISTS active_keyfobs AS SELECT fob_id FROM members WHERE access_status = 'Ready';

CREATE TABLE IF NOT EXISTS fob_swipes (
    uid TEXT PRIMARY KEY,
    timestamp INTEGER NOT NULL,
    fob_id INTEGER NOT NULL,
	member INTEGER
) STRICT;

CREATE INDEX IF NOT EXISTS fob_swipes_fob_id_idx ON fob_swipes (fob_id);

CREATE TRIGGER IF NOT EXISTS no_discount_after_cancelation AFTER UPDATE ON members WHEN OLD.payment_status IS NOT NULL AND NEW.payment_status IS NULL
BEGIN
UPDATE members SET discount_type = NULL WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS fob_swipe_to_member AFTER INSERT ON fob_swipes
BEGIN
UPDATE members SET fob_last_seen = MAX(COALESCE(fob_last_seen, 0), NEW.timestamp) WHERE fob_id = NEW.fob_id;
END;

CREATE INDEX IF NOT EXISTS fob_swipes_timestamp ON fob_swipes (timestamp);
CREATE UNIQUE INDEX IF NOT EXISTS fob_swipes_uniq ON fob_swipes (fob_id, timestamp);

CREATE TABLE IF NOT EXISTS member_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    member INTEGER REFERENCES members(id) ON DELETE SET NULL,
    important INTEGER NOT NULL DEFAULT true,
    event TEXT NOT NULL DEFAULT '',
    details TEXT NOT NULL DEFAULT ''
);

CREATE TRIGGER IF NOT EXISTS members_confirmed_email AFTER UPDATE OF confirmed ON members WHEN OLD.confirmed = 0 AND NEW.confirmed = 1
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'EmailConfirmed', 'Email address confirmed');
END;

CREATE TRIGGER IF NOT EXISTS members_discount_type_update AFTER UPDATE OF discount_type ON members
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'DiscountTypeModified', 'Discount changed from "' || COALESCE(OLD.discount_type, 'NULL') || '" to "' || COALESCE(NEW.discount_type, 'NULL') || '"');
END;

CREATE TRIGGER IF NOT EXISTS members_access_status_update AFTER UPDATE ON members WHEN OLD.access_status != NEW.access_status
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'AccessStatusChanged', 'Building access status changed from "' || COALESCE(OLD.access_status, 'NULL') || '" to "' || COALESCE(NEW.access_status, 'NULL') || '"');
END;

CREATE TRIGGER IF NOT EXISTS members_leadership_set AFTER UPDATE OF leadership ON members WHEN NEW.leadership = 1 AND OLD.leadership = 0
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'LeadershipStatusAdded', 'Designated as leadership');
END;

CREATE TRIGGER IF NOT EXISTS members_leadership_unset AFTER UPDATE OF leadership ON members WHEN NEW.leadership = 0 AND OLD.leadership = 1
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'LeadershipStatusRemoved', 'No longer designated as leadership');
END;

CREATE TRIGGER IF NOT EXISTS members_non_billable_added AFTER UPDATE OF non_billable ON members WHEN NEW.non_billable IS true AND OLD.non_billable IS false
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'NonBillableStatusAdded', 'The member has been marked as non-billable');
END;

CREATE TRIGGER IF NOT EXISTS members_non_billable_removed AFTER UPDATE OF non_billable ON members WHEN NEW.non_billable IS false AND OLD.non_billable IS true
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'NonBillableStatusRemoved', 'The member is no longer marked as non-billable');
END;

CREATE TRIGGER IF NOT EXISTS members_fob_changed AFTER UPDATE OF fob_id ON members WHEN OLD.fob_id != NEW.fob_id
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'FobChanged', 'The fob ID changed from ' || COALESCE(OLD.fob_id, 'NULL') || ' to ' || COALESCE(NEW.fob_id, 'NULL'));
END;

CREATE TRIGGER IF NOT EXISTS waiver_signed AFTER UPDATE OF waiver ON members WHEN OLD.waiver IS NULL AND NEW.waiver IS NOT NULL
BEGIN
INSERT INTO member_events (member, event, details) VALUES (NEW.id, 'WaiverSigned', 'Waiver signed');
END;

CREATE UNIQUE INDEX IF NOT EXISTS waivers_email_version_uidx ON waivers(email, version);

CREATE TRIGGER IF NOT EXISTS discord_sync_on_user_id_change AFTER UPDATE OF discord_user_id ON members WHEN NEW.discord_user_id IS NOT NULL AND (OLD.discord_user_id IS NULL OR OLD.discord_user_id != NEW.discord_user_id)
BEGIN
    UPDATE members SET discord_last_synced = NULL WHERE id = NEW.id;
END;

CREATE TRIGGER IF NOT EXISTS discord_sync_on_payment_affecting_change AFTER UPDATE ON members
WHEN NEW.discord_user_id IS NOT NULL AND (
    (OLD.confirmed != NEW.confirmed) OR
    (OLD.stripe_subscription_state != NEW.stripe_subscription_state) OR
    (OLD.paypal_subscription_id != NEW.paypal_subscription_id) OR
    (OLD.non_billable != NEW.non_billable)
)
BEGIN
    UPDATE members SET discord_last_synced = NULL WHERE id = NEW.id;
END;

CREATE TABLE IF NOT EXISTS waiver_content (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    content TEXT NOT NULL,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
) STRICT;

INSERT OR IGNORE INTO waiver_content (version, content) VALUES (
    1,
    '# Liability Waiver

This is a sample liability waiver. Please customize this content to match your organization''s requirements.

Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris.

1. I acknowledge that participation in activities may involve inherent risks and I voluntarily assume all such risks.

2. I understand that I am personally responsible for my safety and actions while on the premises.

3. I affirm that I am at least 18 years of age and mentally competent to sign this liability waiver.

- [ ] By checking here, you are consenting to the use of your electronic signature in lieu of an original signature on paper.
- [ ] By checking this box, I agree and acknowledge to be bound by this waiver and release.'
);

CREATE TABLE IF NOT EXISTS discord_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    client_id TEXT NOT NULL DEFAULT '',
    client_secret TEXT NOT NULL DEFAULT '',
    bot_token TEXT NOT NULL DEFAULT '',
    guild_id TEXT NOT NULL DEFAULT '',
    role_id TEXT NOT NULL DEFAULT '',
    print_webhook_url TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE IF NOT EXISTS stripe_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    api_key TEXT NOT NULL DEFAULT '',
    webhook_key TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE IF NOT EXISTS google_config (
    version INTEGER PRIMARY KEY AUTOINCREMENT,
    created INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    client_id TEXT NOT NULL DEFAULT '',
    client_secret TEXT NOT NULL DEFAULT ''
) STRICT;
