PRAGMA foreign_keys = ON;
CREATE TABLE runtime_control (id INTEGER PRIMARY KEY CHECK(id=1), automation_enabled INTEGER NOT NULL);
INSERT INTO runtime_control VALUES(1,1);
CREATE TABLE waiver_versions (version INTEGER PRIMARY KEY, created INTEGER NOT NULL DEFAULT(unixepoch()), content TEXT NOT NULL, agreements TEXT NOT NULL CHECK(json_valid(agreements)));
CREATE TABLE waivers (id INTEGER PRIMARY KEY AUTOINCREMENT, version INTEGER NOT NULL, created INTEGER NOT NULL DEFAULT(unixepoch()), name TEXT NOT NULL, email TEXT NOT NULL, UNIQUE(email,version));
CREATE INDEX waivers_email ON waivers(email);
CREATE TABLE members (
 id INTEGER PRIMARY KEY AUTOINCREMENT, version INTEGER NOT NULL DEFAULT 1, created INTEGER NOT NULL DEFAULT(unixepoch()),
 email TEXT NOT NULL COLLATE NOCASE UNIQUE, confirmed INTEGER NOT NULL DEFAULT 0 CHECK(confirmed IN(0,1)),
 name TEXT NOT NULL DEFAULT '', name_override TEXT, admin_notes TEXT NOT NULL DEFAULT '',
 identifier TEXT GENERATED ALWAYS AS(CASE WHEN name != '' THEN name ELSE email END) VIRTUAL,
 waiver INTEGER REFERENCES waivers(id), fob_id INTEGER UNIQUE CHECK(fob_id IS NULL OR fob_id BETWEEN 1 AND 4294967295), fob_last_seen INTEGER,
 leadership INTEGER NOT NULL DEFAULT 0 CHECK(leadership IN(0,1)), non_billable INTEGER NOT NULL DEFAULT 0 CHECK(non_billable IN(0,1)),
 discount_type TEXT, discount_status TEXT CHECK(discount_status IN('requested','approved')), discount_request_id TEXT UNIQUE,
 bill_annually INTEGER NOT NULL DEFAULT 0 CHECK(bill_annually IN(0,1)),
 root_family_member INTEGER REFERENCES members(id) ON DELETE RESTRICT CHECK(root_family_member != id), root_family_member_active INTEGER,
 stripe_customer_id TEXT UNIQUE, stripe_subscription_id TEXT UNIQUE, stripe_subscription_state TEXT,
 stripe_cancellation_reason TEXT, stripe_last_payment_error TEXT, paypal_subscription_id TEXT, paypal_price REAL,
 discord_user_id TEXT UNIQUE CHECK(discord_user_id IS NULL OR (length(discord_user_id) BETWEEN 5 AND 25 AND discord_user_id NOT GLOB '*[^0-9]*')),
 discord_username TEXT, discord_email TEXT, discord_last_synced INTEGER,
 payment_status TEXT GENERATED ALWAYS AS(CASE WHEN confirmed != 1 THEN NULL WHEN paypal_subscription_id IS NOT NULL THEN 'ActivePaypal' WHEN stripe_subscription_state IN('active','trialing') THEN 'ActiveStripe' WHEN non_billable=1 THEN 'ActiveNonBillable' ELSE NULL END) VIRTUAL,
 access_status TEXT GENERATED ALWAYS AS(CASE WHEN confirmed != 1 AND non_billable != 1 THEN 'UnconfirmedEmail' WHEN waiver IS NULL AND non_billable != 1 THEN 'MissingWaiver' WHEN payment_status IS NULL AND non_billable != 1 THEN 'PaymentInactive' WHEN fob_id IS NULL THEN 'MissingKeyFob' WHEN root_family_member IS NOT NULL AND root_family_member_active=0 THEN 'FamilyInactive' ELSE 'Ready' END) VIRTUAL
) STRICT;
CREATE INDEX members_family ON members(root_family_member);
CREATE TRIGGER member_revision AFTER UPDATE ON members WHEN NEW.version=OLD.version
BEGIN UPDATE members SET version=OLD.version+1 WHERE id=NEW.id; END;
CREATE INDEX members_access ON members(access_status);
CREATE VIEW active_keyfobs AS SELECT fob_id FROM members WHERE access_status='Ready';
CREATE TABLE settings (id INTEGER PRIMARY KEY CHECK(id=1), version INTEGER NOT NULL, data TEXT NOT NULL CHECK(json_valid(data)));
INSERT INTO settings VALUES(1,1,'{"site_name":"Conway","discounts":[{"id":"student","label":"Student","coupon_id":""},{"id":"family","label":"Family","coupon_id":""}],"monthly_price_id":"","yearly_price_id":"","waiver_version":0}');
CREATE TABLE sessions (token_hash TEXT PRIMARY KEY, member INTEGER REFERENCES members(id) ON DELETE CASCADE, csrf_token TEXT NOT NULL, expires INTEGER NOT NULL);
CREATE INDEX sessions_expires ON sessions(expires);
CREATE TRIGGER identity_sessions AFTER UPDATE OF discord_user_id ON members WHEN OLD.discord_user_id IS NOT NEW.discord_user_id
BEGIN DELETE FROM sessions WHERE member=NEW.id; END;
CREATE TABLE oauth_states (state_hash TEXT PRIMARY KEY, browser_hash TEXT NOT NULL, return_to TEXT NOT NULL, expires INTEGER NOT NULL);
CREATE TABLE enrollment_claims (token_hash TEXT PRIMARY KEY, fob_id INTEGER NOT NULL CHECK(fob_id BETWEEN 1 AND 4294967295), created INTEGER NOT NULL, expires INTEGER NOT NULL, claimed_by INTEGER REFERENCES members(id) ON DELETE SET NULL);
CREATE TRIGGER consumed_claim_delete BEFORE DELETE ON members
BEGIN DELETE FROM enrollment_claims WHERE claimed_by=OLD.id; END;
CREATE TABLE member_events (id INTEGER PRIMARY KEY AUTOINCREMENT, created INTEGER NOT NULL DEFAULT(unixepoch()), member INTEGER REFERENCES members(id) ON DELETE SET NULL, actor INTEGER REFERENCES members(id) ON DELETE SET NULL, event TEXT NOT NULL, details TEXT NOT NULL DEFAULT '');
CREATE INDEX events_member ON member_events(member,id);
CREATE TABLE fob_swipes (uid TEXT PRIMARY KEY, timestamp INTEGER NOT NULL, fob_id INTEGER NOT NULL, member INTEGER REFERENCES members(id) ON DELETE SET NULL, allowed INTEGER NOT NULL, controller TEXT NOT NULL DEFAULT '');
CREATE INDEX swipes_time ON fob_swipes(timestamp);
CREATE TABLE jobs (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, payload TEXT NOT NULL CHECK(json_valid(payload)), status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN('pending','processing','completed','dead')), attempts INTEGER NOT NULL DEFAULT 0, available_at INTEGER NOT NULL DEFAULT(unixepoch()), lease_until INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL DEFAULT(unixepoch()), completed INTEGER, dedupe_key TEXT UNIQUE);
CREATE INDEX jobs_due ON jobs(status,available_at);
CREATE TABLE stripe_events (id TEXT PRIMARY KEY, created INTEGER NOT NULL, payload TEXT NOT NULL, processed INTEGER);

CREATE TABLE notification_state (member INTEGER REFERENCES members(id) ON DELETE CASCADE, kind TEXT NOT NULL, last_sent INTEGER NOT NULL, PRIMARY KEY(member,kind));
CREATE TABLE rate_limits (key TEXT PRIMARY KEY, count INTEGER NOT NULL, expires INTEGER NOT NULL);

CREATE TRIGGER family_cycle_insert BEFORE INSERT ON members WHEN NEW.root_family_member IS NOT NULL
BEGIN SELECT RAISE(ABORT,'Family root must be a primary member') WHERE EXISTS(SELECT 1 FROM members WHERE id=NEW.root_family_member AND root_family_member IS NOT NULL); END;
CREATE TRIGGER family_cycle_update BEFORE UPDATE OF root_family_member ON members WHEN NEW.root_family_member IS NOT NULL
BEGIN
 SELECT RAISE(ABORT,'Family root must be a primary member') WHERE EXISTS(SELECT 1 FROM members WHERE id=NEW.root_family_member AND root_family_member IS NOT NULL);
 SELECT RAISE(ABORT,'Family root has dependents') WHERE EXISTS(SELECT 1 FROM members WHERE root_family_member=NEW.id);
END;
CREATE TRIGGER family_insert AFTER INSERT ON members WHEN NEW.root_family_member IS NOT NULL
BEGIN UPDATE members SET root_family_member_active=(SELECT payment_status IS NOT NULL FROM members WHERE id=NEW.root_family_member) WHERE id=NEW.id; END;
CREATE TRIGGER family_link AFTER UPDATE OF root_family_member ON members
BEGIN UPDATE members SET root_family_member_active=IIF(NEW.root_family_member IS NULL,NULL,(SELECT payment_status IS NOT NULL FROM members WHERE id=NEW.root_family_member)) WHERE id=NEW.id; END;
CREATE TRIGGER family_payment AFTER UPDATE OF confirmed,non_billable,stripe_subscription_state,paypal_subscription_id ON members
BEGIN UPDATE members SET root_family_member_active=NEW.payment_status IS NOT NULL WHERE root_family_member=NEW.id; END;
CREATE TRIGGER discount_lapse AFTER UPDATE ON members WHEN OLD.payment_status IS NOT NULL AND NEW.payment_status IS NULL AND NEW.discount_status IS NOT 'requested'
BEGIN UPDATE members SET discount_type=NULL,discount_status=NULL,discount_request_id=NULL WHERE id=NEW.id; END;
CREATE TRIGGER waiver_signed AFTER INSERT ON waivers
BEGIN UPDATE members SET waiver=NEW.id WHERE email=NEW.email; END;
CREATE TRIGGER waiver_resolve AFTER INSERT ON members WHEN NEW.waiver IS NULL
BEGIN UPDATE members SET waiver=(SELECT id FROM waivers WHERE email=NEW.email ORDER BY version DESC,id DESC LIMIT 1) WHERE id=NEW.id; END;
CREATE TRIGGER leader_update BEFORE UPDATE OF leadership,discord_user_id ON members WHEN OLD.leadership=1 AND OLD.discord_user_id IS NOT NULL AND (NEW.leadership=0 OR NEW.discord_user_id IS NULL) AND (SELECT automation_enabled FROM runtime_control WHERE id=1)=1
BEGIN SELECT RAISE(ABORT,'Cannot remove last linked leader') WHERE NOT EXISTS(SELECT 1 FROM members WHERE leadership=1 AND discord_user_id IS NOT NULL AND id!=OLD.id); END;
CREATE TRIGGER leader_delete BEFORE DELETE ON members WHEN OLD.leadership=1 AND OLD.discord_user_id IS NOT NULL AND (SELECT automation_enabled FROM runtime_control WHERE id=1)=1
BEGIN SELECT RAISE(ABORT,'Cannot remove last linked leader') WHERE NOT EXISTS(SELECT 1 FROM members WHERE leadership=1 AND discord_user_id IS NOT NULL AND id!=OLD.id); END;
CREATE TRIGGER member_created AFTER INSERT ON members WHEN (SELECT automation_enabled FROM runtime_control WHERE id=1)=1
BEGIN
 INSERT INTO member_events(member,event) VALUES(NEW.id,'MemberCreated');
 INSERT INTO jobs(kind,payload) VALUES('signup',json_object('member_id',NEW.id));
 INSERT INTO jobs(kind,payload) VALUES('discord_sync',json_object('member_id',NEW.id));
END;
CREATE TRIGGER member_changed AFTER UPDATE ON members WHEN (SELECT automation_enabled FROM runtime_control WHERE id=1)=1 AND (OLD.email IS NOT NEW.email OR OLD.name IS NOT NEW.name OR OLD.confirmed IS NOT NEW.confirmed OR OLD.access_status IS NOT NEW.access_status OR OLD.payment_status IS NOT NEW.payment_status OR OLD.leadership IS NOT NEW.leadership OR OLD.non_billable IS NOT NEW.non_billable OR OLD.fob_id IS NOT NEW.fob_id OR OLD.discount_type IS NOT NEW.discount_type OR OLD.discount_status IS NOT NEW.discount_status OR OLD.root_family_member IS NOT NEW.root_family_member OR OLD.discord_user_id IS NOT NEW.discord_user_id)
BEGIN INSERT INTO member_events(member,event,details) VALUES(NEW.id,'MemberUpdated',json_object('access_before',OLD.access_status,'access_after',NEW.access_status,'payment_before',OLD.payment_status,'payment_after',NEW.payment_status)); END;
CREATE TRIGGER discord_changed AFTER UPDATE ON members WHEN (SELECT automation_enabled FROM runtime_control WHERE id=1)=1 AND (OLD.payment_status IS NOT NEW.payment_status OR OLD.discord_user_id IS NOT NEW.discord_user_id)
BEGIN INSERT INTO jobs(kind,payload) VALUES('discord_sync',json_object('member_id',NEW.id)); END;
CREATE TRIGGER discord_deleted AFTER DELETE ON members WHEN (SELECT automation_enabled FROM runtime_control WHERE id=1)=1
BEGIN INSERT INTO jobs(kind,payload) VALUES('discord_sync',json_object('member_id',OLD.id)); END;
CREATE TRIGGER discount_requested AFTER UPDATE ON members WHEN (SELECT automation_enabled FROM runtime_control WHERE id=1)=1 AND NEW.discount_status='requested' AND NEW.discount_request_id IS NOT OLD.discount_request_id
BEGIN INSERT INTO jobs(kind,payload,dedupe_key) VALUES('discount',json_object('member_id',NEW.id,'request_id',NEW.discount_request_id),'discount:'||NEW.discount_request_id); END;
CREATE TRIGGER swipe_received AFTER INSERT ON fob_swipes
BEGIN
 UPDATE members SET fob_last_seen=MAX(COALESCE(fob_last_seen,0),NEW.timestamp) WHERE fob_id=NEW.fob_id;
 INSERT INTO jobs(kind,payload,dedupe_key) SELECT 'denied',json_object('member_id',id,'swipe_id',NEW.uid),'swipe:'||NEW.uid FROM members WHERE fob_id=NEW.fob_id AND NEW.allowed=0 AND (SELECT automation_enabled FROM runtime_control WHERE id=1)=1;
END;
