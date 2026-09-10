"""Synthetic legacy modules -> exact target schema, using only stdlib SQLite."""

import json
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest

import migrate_sqlite as migration


ROOT = Path(__file__).resolve().parents[2]


class MigrationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="conway-migration-", dir=os.environ.get("TMPDIR", "/dev/shm"))
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.source = self.root / "legacy.sqlite"
        self.settings = self.root / "settings.json"
        self.settings.write_text(json.dumps({
            "site_name": "The Lab", "monthly_price_id": "price_monthly", "yearly_price_id": "price_yearly",
            "discounts": [{"id": k, "label": v, "coupon_id": "coupon_" + k} for k, v in migration.DISCOUNTS],
        }))
        with sqlite3.connect(self.source) as db:
            db.executescript((ROOT / "modules/members/schema.sql").read_text())
            # Exercise additive migrations from directory, payment, and fobapi.
            db.executescript("""
                ALTER TABLE members ADD COLUMN profile_picture BLOB;
                ALTER TABLE members ADD COLUMN discord_checkin_notify INTEGER DEFAULT 0;
                CREATE TABLE waiver_content(version INTEGER PRIMARY KEY,created INTEGER,content TEXT);
                CREATE TABLE fob_clients(id INTEGER PRIMARY KEY,ip_address TEXT,door_name TEXT,last_seen INTEGER);
                ALTER TABLE fob_swipes ADD COLUMN fob_client INTEGER REFERENCES fob_clients(id);
                CREATE TABLE discord_config(version INTEGER PRIMARY KEY,created INTEGER,client_id TEXT,client_secret TEXT,bot_token TEXT,
                    guild_id TEXT,role_id TEXT,leadership_channel_id TEXT,badge_notify_channel_id TEXT,
                    badge_notify_enabled INTEGER,access_denied_enabled INTEGER,signup_message_template TEXT,approval_bot_enabled INTEGER);
                CREATE TABLE triggers(id INTEGER PRIMARY KEY,name TEXT,enabled INTEGER,trigger_type TEXT,action_sql TEXT);
                CREATE TABLE outbound_mail(id INTEGER PRIMARY KEY,body TEXT);
                INSERT INTO waiver_content VALUES(1,100,'# Old waiver\n- [ ] First agreement');
                INSERT INTO waiver_content VALUES(2,200,'# Current waiver\n -  [ ]  New agreement\n- [] Second agreement');
                INSERT INTO waivers(id,version,created,name,email) VALUES
                    (11,1,101,'Leader','leader@example.org'),(12,2,201,'Leader','leader@example.org'),
                    (13,2,202,'Anonymous','pre-signup@example.org');
                INSERT INTO members(id,created,email,name,confirmed,non_billable,leadership,discord_user_id,fob_id,waiver)
                    VALUES(40,102,'leader@example.org','Leader',1,1,1,'123456789012345678',123,11);
                INSERT INTO members(id,created,email,name,confirmed,non_billable,fob_id,root_family_member)
                    VALUES(2,103,'family@example.org','Family',1,1,456,40);
                UPDATE members SET waiver=11,discord_avatar=X'CAFEBABE',profile_picture=X'DEADBEEF',
                    admin_notes='O''Brien\nAdmin note',name_override='Display',discord_checkin_notify=1 WHERE id=40;
                INSERT INTO members(id,created,email,name,confirmed) VALUES(100,104,'unlinked@example.org','Unlinked',0);
                INSERT INTO fob_clients VALUES(9,'192.0.2.10','Front door',300);
                INSERT INTO fob_swipes(uid,timestamp,fob_id,member,allowed,fob_client) VALUES('legacy-swipe',300,123,40,1,9);
                INSERT INTO member_events(id,created,member,important,event,details) VALUES(88,301,40,0,'LegacyEvent','preserve details');
                INSERT INTO discord_config VALUES(1,100,'999999999999999999','SYNTHETIC-CLIENT-SECRET','SYNTHETIC-BOT-SECRET',
                    '222222222222222222','333333333333333333','444444444444444444','555555555555555555',1,1,'{{ .Name }}',1);
                INSERT INTO triggers VALUES(7,'Timed rule',1,'timed','SELECT remote_effect(''SYNTHETIC-BOT-SECRET'')');
                INSERT INTO triggers VALUES(8,'Disabled rule',0,'event','SELECT 1');
                INSERT INTO outbound_mail VALUES(1,'Excluded private email');
                UPDATE sqlite_sequence SET seq=1000 WHERE name='members';
                UPDATE sqlite_sequence SET seq=900 WHERE name='member_events';
            """)
        self.serial = 0

    def execute_source(self, sql):
        with sqlite3.connect(self.source) as db:
            db.executescript(sql)

    def run_export(self, approve=True):
        self.serial += 1
        output = self.root / f"output-{self.serial}"
        code = migration.migrate(self.source, output, self.settings)
        report = json.loads((output / "report.json").read_text())
        if approve:
            review = self.root / f"review-{self.serial}.json"
            review.write_text(json.dumps({"source_sha256": report["source_sha256"],
                                          "approvals": [r["id"] for r in report["reviews"]]}))
            output = self.root / f"approved-{self.serial}"
            code = migration.migrate(self.source, output, self.settings, review)
            report = json.loads((output / "report.json").read_text())
        return code, output, report

    def target(self, output):
        db = sqlite3.connect(":memory:")
        self.addCleanup(db.close)
        db.executescript(migration.SCHEMA.read_text())
        db.executescript((output / "import.sql").read_text())
        return db

    def assert_blocked(self, code):
        status, output, report = self.run_export()
        self.assertEqual(status, 2, report)
        self.assertIn(code, {r["code"] for r in report["blockers"]}, report)
        self.assertFalse((output / "import.sql").exists())
        self.assertFalse((output / "verify.sql").exists())

    def test_repeatable_source_readonly_preserves_data_and_no_effects(self):
        before = self.source.read_bytes()
        status, output, report = self.run_export()
        self.assertEqual(status, 0, report)
        status2, output2, report2 = self.run_export()
        self.assertEqual(status2, 0, report2)
        for name in ("import.sql", "verify.sql", "report.json", "secret-names.json"):
            self.assertEqual((output / name).read_bytes(), (output2 / name).read_bytes())
        self.assertEqual(before, self.source.read_bytes())
        self.assertFalse(Path(str(self.source) + "-wal").exists())
        self.assertEqual(output.stat().st_mode & 0o777, 0o700)
        db = self.target(output)
        self.assertEqual(db.execute("SELECT id FROM members ORDER BY id").fetchall(), [(2,), (40,), (100,)])
        self.assertEqual(db.execute("SELECT waiver,fob_last_seen,discord_checkin_notify,admin_notes FROM members WHERE id=40").fetchone(),
                         (11, 300, 1, "O'Brien\nAdmin note"))
        self.assertEqual(db.execute("SELECT root_family_member,root_family_member_active,access_status FROM members WHERE id=2").fetchone(), (40, 1, "Ready"))
        self.assertEqual(db.execute("SELECT discord_user_id FROM members WHERE id=100").fetchone(), (None,))
        self.assertEqual(db.execute("SELECT id FROM waivers ORDER BY id").fetchall(), [(11,), (12,), (13,)])
        self.assertEqual(json.loads(db.execute("SELECT agreements FROM waiver_versions WHERE version=2").fetchone()[0]), ["New agreement", "Second agreement"])
        self.assertEqual(db.execute("SELECT id,event,details FROM member_events").fetchall(), [(88, "LegacyEvent", "preserve details")])
        self.assertEqual(db.execute("SELECT controller FROM fob_swipes").fetchone()[0], "legacy-fob-client:9")
        self.assertEqual(db.execute("SELECT count(*) FROM jobs").fetchone()[0], 0)
        self.assertEqual(db.execute("SELECT automation_enabled FROM runtime_control").fetchone()[0], 1)
        self.assertEqual(db.execute("PRAGMA foreign_key_check").fetchall(), [])
        settings = json.loads(db.execute("SELECT data FROM settings").fetchone()[0])
        self.assertEqual(settings["waiver_version"], 2)
        db.executescript((output / "verify.sql").read_text())
        db.execute("INSERT INTO members(email) VALUES('new@example.org')")
        self.assertEqual(db.execute("SELECT id FROM members WHERE email='new@example.org'").fetchone()[0], 1001)
        self.assertEqual(db.execute("SELECT count(*) FROM jobs").fetchone()[0], 2)
        self.assertEqual(db.execute("SELECT max(id) FROM member_events").fetchone()[0], 901)
        emitted = "\n".join(p.read_text() for p in output.iterdir())
        for value in ("SYNTHETICSECRET", "SYNTHETIC-CLIENT-SECRET", "SYNTHETIC-BOT-SECRET", "Excluded private email", "CAFEBABE", "DEADBEEF", "remote_effect"):
            self.assertNotIn(value, emitted)
        sql = (output / "import.sql").read_text()
        self.assertNotIn("profile_picture", sql)
        self.assertNotIn("discord_avatar", sql)

    def test_unreviewed_triggers_config_timed_and_disabled_rules_block(self):
        status, output, report = self.run_export(approve=False)
        self.assertEqual(status, 2)
        reviews = {r["id"] for r in report["reviews"]}
        self.assertIn("rule:triggers:7", reviews)
        self.assertIn("rule:triggers:8", reviews)
        self.assertIn("trigger:members_family_relationship_update", reviews)
        self.assertIn("config:discord_config.approval_bot_enabled", reviews)
        self.assertIn("column:member_events.important", reviews)
        self.assertFalse((output / "import.sql").exists())

    def test_duplicate_case_email(self):
        self.execute_source("UPDATE members SET email='LEADER@example.org' WHERE id=100;")
        self.assert_blocked("duplicate_identity")

    def test_duplicate_discord_identity(self):
        self.execute_source("UPDATE members SET discord_user_id='123456789012345678' WHERE id=100;")
        self.assert_blocked("duplicate_identity")

    def test_invalid_discord_and_linked_leader(self):
        self.execute_source("UPDATE members SET discord_user_id='' WHERE id=40;")
        self.assert_blocked("invalid_discord_identity")
        self.assert_blocked("no_linked_leadership")

    def test_no_email_identity_linking(self):
        self.execute_source("UPDATE members SET discord_email='UNLINKED@example.org' WHERE id=40;")
        self.assert_blocked("discord_email_identity_conflict")

    def test_duplicate_stripe_and_incomplete_stripe(self):
        self.execute_source("UPDATE members SET stripe_customer_id='cus_same' WHERE id IN(40,100);")
        self.assert_blocked("duplicate_identity")
        self.execute_source("UPDATE members SET stripe_subscription_state='active' WHERE id=100;")
        self.assert_blocked("inconsistent_stripe_subscription")

    def test_orphans_in_waivers_history_and_controller(self):
        self.execute_source("UPDATE waivers SET version=99 WHERE id=13; UPDATE member_events SET member=9999; UPDATE fob_swipes SET fob_client=999;")
        self.assert_blocked("orphan_waiver_version")
        self.assert_blocked("orphan_reference")
        self.assert_blocked("orphan_controller")

    def test_family_cycle_and_stale_active(self):
        self.execute_source("DROP TRIGGER members_family_relationship_update; UPDATE members SET root_family_member_active=0 WHERE id=2;")
        self.assert_blocked("family_active_inconsistent")
        self.execute_source("UPDATE members SET root_family_member=2 WHERE id=40;")
        self.assert_blocked("family_cycle_or_nested_root")

    def test_zero_and_duplicate_fobs_block_not_normalized(self):
        self.execute_source("UPDATE members SET fob_id=0 WHERE id=100;")
        self.assert_blocked("invalid_fob")
        self.execute_source("DROP INDEX members_fob_idx; UPDATE members SET fob_id=123 WHERE id=100;")
        self.assert_blocked("duplicate_fob")

    def test_authorized_view_mismatch(self):
        self.execute_source("DROP VIEW active_keyfobs; CREATE VIEW active_keyfobs AS SELECT 999 AS fob_id;")
        self.assert_blocked("source_authorized_fob_set_mismatch")

    def test_secrets_in_preserved_text_block_without_leaking(self):
        self.execute_source("UPDATE member_events SET details='error using SYNTHETIC-BOT-SECRET';")
        status, output, report = self.run_export()
        self.assertEqual(status, 2)
        self.assertIn("secret_in_preserved_data", {r["code"] for r in report["blockers"]})
        for path in output.iterdir():
            self.assertNotIn("SYNTHETIC-BOT-SECRET", path.read_text())

    def test_nul_and_large_history_block(self):
        with sqlite3.connect(self.source) as db:
            db.execute("UPDATE member_events SET details=?", ("nul\x00text",))
        self.assert_blocked("unrepresentable_value")
        with sqlite3.connect(self.source) as db:
            db.execute("UPDATE member_events SET details=?", ("x" * 100000,))
        self.assert_blocked("d1_statement_too_large")

    def test_verify_detects_drift_and_reimport_refuses_nonempty_target(self):
        status, output, report = self.run_export()
        self.assertEqual(status, 0, report)
        db = self.target(output)
        with self.assertRaises(sqlite3.IntegrityError):
            db.executescript((output / "import.sql").read_text())
        self.assertEqual(db.execute("SELECT count(*) FROM members").fetchone()[0], 3)
        self.assertEqual(db.execute("SELECT automation_enabled FROM runtime_control").fetchone()[0], 1)
        db2 = self.target(output)
        db2.execute("UPDATE members SET email='LEADER@example.org' WHERE id=40")
        with self.assertRaises(sqlite3.IntegrityError):
            db2.executescript((output / "verify.sql").read_text())

    def test_extra_target_trigger_refused_before_import(self):
        status, output, report = self.run_export()
        self.assertEqual(status, 0, report)
        db = sqlite3.connect(":memory:")
        self.addCleanup(db.close)
        db.executescript(migration.SCHEMA.read_text())
        db.executescript("CREATE TRIGGER unexpected AFTER INSERT ON members BEGIN SELECT 1; END;")
        with self.assertRaises(sqlite3.IntegrityError):
            db.executescript((output / "import.sql").read_text())
        self.assertEqual(db.execute("SELECT count(*) FROM members").fetchone()[0], 0)

    def test_new_revision_and_identity_triggers_restored(self):
        with sqlite3.connect(self.source) as source:
            self.assertNotIn("version", migration.columns(source, "members"))
        status, output, report = self.run_export()
        self.assertEqual(status, 0, report)
        db = self.target(output)
        self.assertEqual(db.execute("SELECT version FROM members ORDER BY id").fetchall(), [(1,), (1,), (1,)])
        for name in ("member_revision", "identity_sessions", "consumed_claim_delete"):
            sql = (output / "import.sql").read_text()
            self.assertIn(f'DROP TRIGGER "{name}";', sql)
            self.assertIn(f"CREATE TRIGGER {name}", sql)
        db.executescript("""
            INSERT INTO sessions VALUES('synthetic-session',100,'csrf',9999999999,NULL);
            UPDATE members SET discord_user_id='987654321012345678' WHERE id=100;
        """)
        self.assertGreater(db.execute("SELECT version FROM members WHERE id=100").fetchone()[0], 1)
        self.assertEqual(db.execute("SELECT count(*) FROM sessions").fetchone()[0], 0)
        db.executescript("""
            INSERT INTO enrollment_claims VALUES('synthetic-claim',999,100,9999999999,100);
            DELETE FROM members WHERE id=100;
        """)
        self.assertEqual(db.execute("SELECT count(*) FROM enrollment_claims").fetchone()[0], 0)

    def test_verify_detects_revision_drift(self):
        status, output, report = self.run_export()
        self.assertEqual(status, 0, report)
        db = self.target(output)
        db.execute("UPDATE members SET version=99 WHERE id=100")
        with self.assertRaises(sqlite3.IntegrityError):
            db.executescript((output / "verify.sql").read_text())

    def test_wal_source_and_existing_output_refused(self):
        sidecar = Path(str(self.source) + "-wal")
        sidecar.write_bytes(b"not-a-standalone-backup")
        with self.assertRaisesRegex(ValueError, "source_has_sidecars"):
            migration.migrate(self.source, self.root / "rejected", self.settings)
        sidecar.unlink()
        with self.assertRaises(FileExistsError):
            migration.migrate(self.source, self.root, self.settings)

    def test_review_bound_to_source_hash(self):
        review = self.root / "stale.json"
        review.write_text(json.dumps({"source_sha256": "wrong", "approvals": []}))
        with self.assertRaisesRegex(ValueError, "invalid_or_stale_review"):
            migration.migrate(self.source, self.root / "stale", self.settings, review)

    def test_explicit_settings_required(self):
        self.settings.write_text("{}")
        self.assert_blocked("deployment_setting_required")

    def test_duplicate_waiver_reported(self):
        self.execute_source("DROP INDEX waivers_email_version_uidx; INSERT INTO waivers(id,version,created,name,email) VALUES(14,2,202,'Anonymous','pre-signup@example.org');")
        self.assert_blocked("duplicate_waiver_email_version")

    def test_negative_history_timestamp_blocked(self):
        self.execute_source("UPDATE member_events SET created=-1;")
        self.assert_blocked("invalid_integer_or_timestamp")

    def test_json_escaped_secret_settings_block(self):
        with sqlite3.connect(self.source) as db:
            db.execute("UPDATE discord_config SET bot_token=?", ('private"quoted\\credential',))
        self.assert_blocked("secret_in_preserved_data")

    def test_legacy_effect_trigger_is_only_inventoried(self):
        self.execute_source("CREATE TRIGGER outgoing_effect AFTER INSERT ON members BEGIN SELECT unavailable_http_function(NEW.id); END;")
        before = self.source.read_bytes()
        status, _, report = self.run_export()
        self.assertEqual(status, 0, report)
        self.assertEqual(self.source.read_bytes(), before)
        self.assertIn("trigger:outgoing_effect", {r["id"] for r in report["reviews"]})

    def test_stale_swipe_does_not_change_preserved_member_last_seen(self):
        self.execute_source("UPDATE members SET fob_last_seen=200 WHERE id=40;")
        status, output, report = self.run_export()
        self.assertEqual(status, 0, report)
        db = self.target(output)
        self.assertEqual(db.execute("SELECT fob_last_seen FROM members WHERE id=40").fetchone()[0], 200)

    def test_source_generated_status_difference_blocks(self):
        self.execute_source("ALTER TABLE members DROP COLUMN identifier; ALTER TABLE members ADD COLUMN identifier TEXT GENERATED ALWAYS AS ('different') VIRTUAL;")
        self.assert_blocked("source_status_parity")

    def test_profile_columns_absent_supported(self):
        for field in migration.PROFILE_DEFAULTS:
            self.execute_source(f"ALTER TABLE members DROP COLUMN {field};")
        status, _, report = self.run_export()
        self.assertEqual(status, 0, report)

    def test_status_matrix(self):
        self.execute_source("""
            INSERT INTO members(id,created,email,confirmed,non_billable,fob_id) VALUES
                (110,100,'unconfirmed@example.org',0,0,110),
                (111,100,'waiver@example.org',1,0,111),
                (112,100,'inactive@example.org',1,0,112),
                (113,100,'missingfob@example.org',1,0,NULL),
                (114,100,'paypal@example.org',1,0,114),
                (115,100,'stripe@example.org',1,0,115),
                (116,100,'familyinactive@example.org',1,1,116),
                (117,100,'nonbillable@example.org',0,1,117);
            INSERT INTO waivers(id,version,created,name,email) VALUES
                (112,1,100,'Inactive','inactive@example.org'),(113,1,100,'No fob','missingfob@example.org'),
                (114,1,100,'Paypal','paypal@example.org'),(115,1,100,'Stripe','stripe@example.org');
            UPDATE members SET paypal_subscription_id='I-OLD' WHERE id IN(113,114);
            UPDATE members SET stripe_customer_id='cus_test',stripe_subscription_id='sub_test',stripe_subscription_state='trialing' WHERE id=115;
            UPDATE members SET root_family_member=100 WHERE id=116;
        """)
        status, output, report = self.run_export()
        self.assertEqual(status, 0, report)
        db = self.target(output)
        self.assertEqual(db.execute("SELECT payment_status,access_status FROM members WHERE id>=110 ORDER BY id").fetchall(), [
            (None, "UnconfirmedEmail"), (None, "MissingWaiver"), (None, "PaymentInactive"),
            ("ActivePaypal", "MissingKeyFob"), ("ActivePaypal", "Ready"), ("ActiveStripe", "Ready"),
            ("ActiveNonBillable", "FamilyInactive"), (None, "Ready"),
        ])


if __name__ == "__main__":
    unittest.main()
