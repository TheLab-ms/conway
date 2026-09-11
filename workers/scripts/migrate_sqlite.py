#!/usr/bin/env python3
"""Offline, fail-closed legacy SQLite -> empty Workers D1 export. No dependencies."""

import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import re
import sqlite3
import sys


SCHEMA = Path(__file__).resolve().parents[1] / "migrations/0001_core.sql"
MEMBER_FIELDS = """id created email confirmed name name_override admin_notes
waiver fob_id fob_last_seen leadership non_billable discount_type discount_status
discount_request_id bill_annually root_family_member root_family_member_active
stripe_customer_id stripe_subscription_id stripe_subscription_state
stripe_cancellation_reason stripe_last_payment_error paypal_subscription_id paypal_price
discord_user_id discord_username discord_email discord_last_synced""".split()
IMAGES = {"discord_avatar", "profile_picture"}
GENERATED = {"identifier", "payment_status", "access_status"}
DISCOUNTS = [("military", "Military"), ("retired", "Retired"),
             ("unemployed", "Unemployed"), ("firstResponder", "First Responder"),
             ("student", "Student"), ("educator", "Educator"),
             ("emeritus", "Emeritus"), ("family", "Family")]
CONFIG_MAP = {
    "discord_config": {
        "guild_id": "DISCORD_GUILD_ID", "role_id": "DISCORD_ROLE_ID",
        "leadership_channel_id": "DISCORD_LEADERSHIP_CHANNEL_ID",
        "access_denied_enabled": "DISCORD_ACCESS_DENIED_ENABLED",
    },
}
SECRET_BINDINGS = {
    "STRIPE_SECRET_KEY": "stripe_config.api_key",
    "STRIPE_WEBHOOK_SECRET": "stripe_config.webhook_key",
    "DISCORD_CLIENT_ID": "discord_config.client_id",
    "DISCORD_CLIENT_SECRET": "discord_config.client_secret",
    "DISCORD_BOT_TOKEN": "discord_config.bot_token",
    "DISCORD_PUBLIC_KEY": "discord_config.application_public_key",
    "EDGE_TOKEN": "provision separately",
}
SECRET_COLUMN = re.compile(r"secret|token|password|api_key|webhook_key|webhook_url|credential|private_key", re.I)
SECRET_TEXT = re.compile(
    r"(?:[sr]k_(?:live|test)_[A-Za-z0-9]+|whsec_[A-Za-z0-9]+|"
    r"https?://[^\s/]*discord(?:app)?\.com/api/webhooks/[^\s]+|"
    r"-----BEGIN (?:[A-Z ]*PRIVATE KEY)-----|"
    r"[A-Za-z0-9_-]{23,28}\.[A-Za-z0-9_-]{6}\.[A-Za-z0-9_-]{27,})"
)
REQUIRED_TABLES = {"members", "waivers", "waiver_content", "member_events", "fob_swipes"}
MAX_STATEMENT_BYTES = 95000  # Below D1's 100 KB SQL statement limit.


def digest_file(path):
    h = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def encode(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=True, separators=(",", ":"))


def ident(name):
    return '"' + name.replace('"', '""') + '"'


def literal(value):
    if value is None:
        return "NULL"
    if isinstance(value, str):
        if "\x00" in value:
            raise ValueError("nul_text")
        return "'" + value.replace("'", "''") + "'"
    if isinstance(value, (int, float)) and math.isfinite(value):
        return repr(value)
    raise ValueError("unsupported_scalar")


def columns(db, table):
    return {row[1] for row in db.execute(f"PRAGMA table_xinfo({ident(table)})")}


def read_rows(db, table, fields, order):
    return [dict(row) for row in db.execute(
        f"SELECT {','.join(map(ident, fields))} FROM {ident(table)} ORDER BY {ident(order)}")]


def statuses(member):
    payment = None
    if member["confirmed"] == 1:
        if member["paypal_subscription_id"] is not None:
            payment = "ActivePaypal"
        elif member["stripe_subscription_state"] in ("active", "trialing"):
            payment = "ActiveStripe"
        elif member["non_billable"] == 1:
            payment = "ActiveNonBillable"
    if member["confirmed"] != 1 and member["non_billable"] != 1:
        access = "UnconfirmedEmail"
    elif member["waiver"] is None and member["non_billable"] != 1:
        access = "MissingWaiver"
    elif payment is None and member["non_billable"] != 1:
        access = "PaymentInactive"
    elif member["fob_id"] in (None, 0):
        access = "MissingKeyFob"
    elif member["root_family_member"] is not None and member["root_family_member_active"] == 0:
        access = "FamilyInactive"
    else:
        access = "Ready"
    return payment, access


def assertion(query):
    return "INSERT INTO _migration_guard SELECT CASE WHEN (" + query + ") THEN 1 ELSE 0 END;"


def insert(table, row):
    return (f"INSERT INTO {ident(table)} ({','.join(map(ident, row))}) VALUES ("
            + ",".join(literal(v) for v in row.values()) + ");")


def migrate(source, output, settings_path=None, review_path=None):
    """Create a new private artifact directory; return 0 only for a verified export."""
    source = Path(source).resolve(strict=True)
    output = Path(output)
    # immutable avoids even SQLite's creation of WAL/SHM files beside the source.
    # It is safe ONLY for a closed, standalone backup, never a live database.
    if any(Path(str(source) + suffix).exists() for suffix in ("-wal", "-shm", "-journal")):
        raise ValueError("source_has_sidecars_use_standalone_backup")
    output.mkdir(mode=0o700, parents=False, exist_ok=False)
    report = {"format": 1, "source_sha256": digest_file(source),
              "schema_sha256": digest_file(SCHEMA), "blockers": [], "reviews": [],
              "inventory": [], "normalizations": [], "counts": {}}
    secrets = set()
    secret_locations = set()
    approvals = set()
    if review_path:
        review = json.loads(Path(review_path).read_text())
        if (set(review) != {"source_sha256", "approvals"}
                or review["source_sha256"] != report["source_sha256"]
                or not isinstance(review["approvals"], list)
                or not all(isinstance(x, str) for x in review["approvals"])):
            raise ValueError("invalid_or_stale_review")
        approvals = set(review["approvals"])

    def block(code, table=None, row=None, field=None):
        item = {"code": code}
        if table is not None:
            item["table"] = table
        if row is not None:
            # Never log a source string value (including IDs, errors or SQL).
            item["row"] = row if type(row) is int else "noninteger_id"
        if field is not None:
            item["field"] = field
        report["blockers"].append(item)

    def needs_review(key, reason):
        approved = key in approvals
        report["reviews"].append({"id": key, "reason": reason, "approved": approved})
        if not approved:
            block("unsupported_needs_review", field=key)

    def sensitive(value):
        if isinstance(value, dict):
            return any(sensitive(k) or sensitive(v) for k, v in value.items())
        if isinstance(value, list):
            return any(sensitive(v) for v in value)
        return isinstance(value, str) and (SECRET_TEXT.search(value) or any(s in value for s in secrets))

    src = sqlite3.connect(source.as_uri() + "?mode=ro&immutable=1", uri=True)
    src.row_factory = sqlite3.Row
    target = sqlite3.connect(":memory:")
    target.row_factory = sqlite3.Row
    try:
        src.execute("PRAGMA query_only=ON")
        src.execute("PRAGMA trusted_schema=OFF")
        src.execute("PRAGMA temp_store=MEMORY")
        src.execute("BEGIN")
        if [r[0] for r in src.execute("PRAGMA integrity_check")] != ["ok"]:
            block("source_integrity_check")
        tables = {r[0] for r in src.execute("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'")}
        for table in sorted(REQUIRED_TABLES - tables):
            block("missing_table", table)
        # Inventory schemas, not row bodies, trigger SQL or configuration values.
        for table in sorted(tables):
            cols = columns(src, table)
            report["inventory"].append({"table": table, "columns": sorted(cols),
                                        "rows": src.execute(f"SELECT count(*) FROM {ident(table)}").fetchone()[0]})
            for col in sorted(cols):
                if SECRET_COLUMN.search(col) or f"{table}.{col}" in SECRET_BINDINGS.values():
                    secret_locations.add(f"{table}.{col}")
                    for row in src.execute(f"SELECT DISTINCT {ident(col)} FROM {ident(table)} WHERE {ident(col)} IS NOT NULL"):
                        if isinstance(row[0], str) and row[0]:
                            secrets.add(row[0])
            if table not in REQUIRED_TABLES | set(CONFIG_MAP) | {"fob_clients"}:
                needs_review("archive:" + table, "Excluded table remains only in source archive; review behavior and retention")
        report["triggers"] = []
        for row in src.execute("SELECT name,tbl_name,sql FROM sqlite_schema WHERE type='trigger' ORDER BY name"):
            report["triggers"].append({"name": row[0], "table": row[1],
                                       "sql_sha256": hashlib.sha256(row[2].encode()).hexdigest()})
            needs_review("trigger:" + row[0], "Legacy SQL is never installed or executed; review replacement or deliberate retirement")
        # Include disabled and timed rules, which may have no sqlite_schema trigger.
        for table in ("triggers", "discord_webhooks"):
            if table in tables:
                for row in src.execute(f"SELECT id FROM {ident(table)} ORDER BY id"):
                    if type(row[0]) is int:
                        needs_review(f"rule:{table}:{row[0]}", "Review archived custom/timed/notification rule in source; no SQL, URL or template is exported")
                    else:
                        block("invalid_rule_id", table)
        for row in src.execute("PRAGMA foreign_key_check"):
            block("source_orphan", row[0], row[1])
        target.executescript(SCHEMA.read_text())
        target_tables = [r[0] for r in target.execute("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
        schema_objects = [tuple(r) for r in target.execute("SELECT type,name,sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY type,name")]
        suspended = [tuple(r) for r in target.execute("SELECT name,sql FROM sqlite_schema WHERE type='trigger' AND (sql NOT LIKE '%runtime_control%' OR name='swipe_received') ORDER BY name")]
        data = {"waiver_versions": [], "waivers": [], "members": [], "member_events": [], "fob_swipes": []}
        original_status = {}
        for table, fields, order in (
            ("members", MEMBER_FIELDS, "id"),
            ("waivers", ["id", "version", "created", "name", "email"], "id"),
            ("waiver_content", ["version", "created", "content"], "version"),
            ("member_events", ["id", "created", "member", "event", "details"], "id"),
            ("fob_swipes", ["uid", "timestamp", "fob_id", "member", "allowed"], "uid"),
        ):
            if table not in tables:
                continue
            cols = columns(src, table)
            missing = set(fields) - cols
            if missing:
                for field in sorted(missing):
                    block("missing_column", table, field=field)
                continue
            extras = {"members": IMAGES | GENERATED, "member_events": {"important", "actor"},
                      "fob_swipes": {"fob_client", "controller"}}.get(table, set())
            for field in sorted(cols - set(fields) - extras):
                needs_review(f"column:{table}.{field}", "No target mapping; source value archived only")
            selected = [f for f in fields if f in cols]
            if table == "members":
                selected += sorted(GENERATED & cols)
            if table == "member_events" and "actor" in cols:
                selected += ["actor"]
            if table == "fob_swipes":
                selected += sorted({"fob_client", "controller"} & cols)
            for n, row in enumerate(read_rows(src, table, selected, order), 1):
                if table == "members":
                    original_status[row["id"]] = {f: row.pop(f) for f in GENERATED if f in row}
                    row = {f: row[f] for f in fields}
                if table == "waiver_content":
                    row["agreements"] = encode([m.group(1).strip() for line in row["content"].split("\n")
                                                if (m := re.match(r"^-\s*\[\s*\]\s*(.+)$", line.strip(), flags=re.ASCII))])
                if table == "member_events":
                    row.setdefault("actor", None)
                if table == "fob_swipes":
                    client = row.pop("fob_client", None)
                    row.setdefault("controller", "" if client is None else f"legacy-fob-client:{client}")
                    if client is not None and ("fob_clients" not in tables or not src.execute("SELECT 1 FROM fob_clients WHERE id=?", (client,)).fetchone()):
                        block("orphan_controller", table, n)
                dest = "waiver_versions" if table == "waiver_content" else table
                data[dest].append(row)
            if table == "member_events" and "important" in cols:
                needs_review("column:member_events.important", "Events retained with IDs and details; target has no importance flag, archived in source")

        settings = json.loads(target.execute("SELECT data FROM settings WHERE id=1").fetchone()[0])
        settings["discounts"] = [{"id": key, "label": label, "coupon_id": ""} for key, label in DISCOUNTS]
        settings["waiver_version"] = max((r["version"] for r in data["waiver_versions"]), default=0)
        for table, mapping in CONFIG_MAP.items():
            if table not in tables:
                needs_review("missing-config:" + table, "Module config absent; review target defaults")
                continue
            cols = columns(src, table)
            if not {"version", "created"} <= cols:
                block("invalid_config_schema", table)
                continue
            allowed = set(mapping) & cols
            rows = read_rows(src, table, ["version"] + sorted(allowed), "version")
            if not rows:
                needs_review("empty-config:" + table, "Module config empty; review target defaults")
            for col in sorted(cols - allowed - {"version", "created"}):
                if f"{table}.{col}" not in SECRET_BINDINGS.values():
                    needs_review(f"config:{table}.{col}", "Unsupported config field; all versions archived, not silently translated")
            if rows:
                report.setdefault("config_versions", {})[table] = rows[-1]["version"]
                for col in sorted(allowed):
                    val = rows[-1][col]
                    try:
                        if col.endswith("enabled"):
                            if val not in (0, 1):
                                raise ValueError()
                            val = "true" if val else "false"
                        elif not isinstance(val, str):
                            raise ValueError()
                        report.setdefault("deployment_vars", {})[mapping[col]] = val
                    except (ValueError, TypeError, KeyError):
                        block("invalid_config_value", table, field=col)
        # These settings were flags, Stripe lookup keys/metadata or Go templates,
        # not durable equivalent values in the legacy DB. Require explicit input.
        required = {"site_name", "monthly_price_id", "yearly_price_id", "discounts"}
        supplied = json.loads(Path(settings_path).read_text()) if settings_path else {}
        if not isinstance(supplied, dict):
            block("settings_not_object")
            supplied = {}
        for field in sorted(required - supplied.keys()):
            block("deployment_setting_required", field=field)
        for field, val in supplied.items():
            if field not in settings or field == "waiver_version":
                block("unsupported_deployment_setting")
            else:
                if field not in required and settings[field] != val:
                    needs_review("override:" + field, "Explicit nonsecret input differs from mapped DB setting")
                settings[field] = val
        try:
            string_fields = [k for k, v in settings.items() if k not in
                             {"discounts", "waiver_version"}]
            valid = all(isinstance(settings[k], str) for k in string_fields)
            valid &= isinstance(settings["discounts"], list) and all(set(v) == {"id", "label", "coupon_id"} and all(isinstance(x, str) for x in v.values()) for v in settings["discounts"])
            ids = [d["id"] for d in settings["discounts"]]
            valid &= len(ids) == len(set(ids)) and set(k for k, _ in DISCOUNTS) <= set(ids)
            valid &= all(not settings[k] or re.fullmatch(r"price_[A-Za-z0-9]+", settings[k])
                         for k in ("monthly_price_id", "yearly_price_id"))
            valid &= all("REPLACE" not in settings[k] for k in ("monthly_price_id", "yearly_price_id"))
            if not valid:
                raise ValueError()
        except (ValueError, TypeError, KeyError, AttributeError):
            block("invalid_settings_shape")

        members = {r["id"]: r for r in data["members"]}
        waivers = {r["id"]: r for r in data["waivers"]}
        versions = {r["version"] for r in data["waiver_versions"]}
        if not versions:
            block("missing_waiver_versions")
        seen = {f: {} for f in ("email", "discord_user_id", "stripe_customer_id", "stripe_subscription_id", "fob_id", "discount_request_id")}
        for row in data["members"]:
            mid = row["id"]
            for field in seen:
                value = row[field]
                if value is None:
                    continue
                # casefold intentionally catches more than SQLite ASCII NOCASE:
                # these identities need a human decision, never auto-link/merge.
                key = value.strip().casefold() if field == "email" and isinstance(value, str) else value
                if key in seen[field]:
                    block("duplicate_identity" if field != "fob_id" else "duplicate_fob", "members", mid, field)
                    block("conflicting_row", "members", seen[field][key], field)
                seen[field][key] = mid
            email = row["email"]
            if not isinstance(email, str) or email != email.strip() or not re.fullmatch(r"[^\s@]+@[^\s@]+", email):
                block("invalid_email_identity", "members", mid, "email")
            did = row["discord_user_id"]
            if did is not None and (not isinstance(did, str) or not re.fullmatch(r"[1-9][0-9]{4,24}", did)):
                block("invalid_discord_identity", "members", mid, "discord_user_id")
            for field, prefix in (("stripe_customer_id", "cus_"), ("stripe_subscription_id", "sub_")):
                value = row[field]
                if value is not None and (not isinstance(value, str) or not re.fullmatch(prefix + r"[A-Za-z0-9]+", value)):
                    block("invalid_stripe_identity", "members", mid, field)
            state = row["stripe_subscription_state"]
            if ((row["stripe_subscription_id"] is not None and (row["stripe_customer_id"] is None or state is None))
                    or (state is not None and row["stripe_subscription_id"] is None)
                    or (state is not None and state not in {"active", "trialing", "canceled", "past_due", "unpaid", "incomplete", "incomplete_expired", "paused"})):
                block("inconsistent_stripe_subscription", "members", mid)
            fob = row["fob_id"]
            if fob is not None and (type(fob) is not int or not 1 <= fob <= 4294967295):
                block("invalid_fob", "members", mid, "fob_id")
            for field in ("confirmed", "leadership", "non_billable", "bill_annually"):
                if type(row[field]) is not int or row[field] not in (0, 1):
                    block("invalid_boolean", "members", mid, field)
            for field, refs in (("waiver", waivers), ("root_family_member", members)):
                if row[field] is not None and row[field] not in refs:
                    block("orphan_reference", "members", mid, field)
            if row["waiver"] in waivers and waivers[row["waiver"]]["email"] != email:
                block("waiver_email_mismatch", "members", mid, "waiver")
            root = members.get(row["root_family_member"])
            if root:
                if root["id"] == mid or root["root_family_member"] is not None:
                    block("family_cycle_or_nested_root", "members", mid)
                if row["root_family_member_active"] != int(statuses(root)[0] is not None):
                    block("family_active_inconsistent", "members", mid)
            elif row["root_family_member"] is None and row["root_family_member_active"] is not None:
                block("family_active_without_root", "members", mid)
            payment, access = statuses(row)
            expected = {"identifier": row["name"] or email, "payment_status": payment, "access_status": access}
            if original_status[mid] != expected:
                block("source_status_parity", "members", mid)
            if row["discount_status"] is not None and (row["discount_type"] is None or not row["discount_request_id"]):
                block("inconsistent_discount", "members", mid)
            if row["discount_type"] is not None and row["discount_type"] not in {d.get("id") for d in settings["discounts"] if isinstance(d, dict)}:
                block("unknown_discount", "members", mid)
        for row in data["members"]:
            discord_email = row["discord_email"]
            if isinstance(discord_email, str):
                other = seen["email"].get(discord_email.strip().casefold())
                if other is not None and other != row["id"]:
                    block("discord_email_identity_conflict", "members", row["id"], "discord_email")
        linked = sorted(r["id"] for r in data["members"] if r["leadership"] == 1 and isinstance(r["discord_user_id"], str) and re.fullmatch(r"[1-9][0-9]{4,24}", r["discord_user_id"]))
        report["linked_leadership_member_ids"] = linked
        report["unlinked_members"] = sum(r["discord_user_id"] is None for r in data["members"])
        if not linked:
            block("no_linked_leadership")
        for row in data["waivers"]:
            if row["version"] not in versions:
                block("orphan_waiver_version", "waivers", row["id"])
        waiver_keys = set()
        for row in data["waivers"]:
            key = (row["email"], row["version"])
            if key in waiver_keys:
                block("duplicate_waiver_email_version", "waivers", row["id"])
            waiver_keys.add(key)
        for table in ("member_events", "fob_swipes"):
            for n, row in enumerate(data[table], 1):
                for field in ("member", "actor") if table == "member_events" else ("member",):
                    if row[field] is not None and row[field] not in members:
                        block("orphan_reference", table, n, field)
                if table == "fob_swipes" and (type(row["fob_id"]) is not int or not 1 <= row["fob_id"] <= 4294967295 or row["allowed"] not in (0, 1)):
                    block("invalid_swipe", table, n)
        fobs = sorted(r["fob_id"] for r in data["members"] if statuses(r)[1] == "Ready" and type(r["fob_id"]) is int)
        try:
            source_fobs = sorted(r[0] for r in src.execute("SELECT fob_id FROM active_keyfobs"))
            if source_fobs != fobs:
                block("source_authorized_fob_set_mismatch")
        except (sqlite3.Error, TypeError):
            block("source_authorized_fob_view_unavailable")
        report["authorized_fobs"] = {"count": len(fobs), "sha256": hashlib.sha256(encode(fobs).encode()).hexdigest()}
        # Preserve AUTOINCREMENT high-water marks, including IDs deleted years ago.
        sequences = {}
        if src.execute("SELECT 1 FROM sqlite_schema WHERE name='sqlite_sequence'").fetchone():
            for row in src.execute("SELECT name,seq FROM sqlite_sequence ORDER BY name"):
                if row[0] in {"members", "waivers", "member_events"}:
                    sequences[row[0]] = row[1]
        if sensitive(settings):
            block("secret_in_preserved_data", "settings")
        if sensitive(report.get("deployment_vars", {})):
            block("secret_in_preserved_data", "deployment_vars")
        # Revision counters belong to the new application, not legacy identity.
        for row in data["members"]:
            row["version"] = 1
        data["settings"] = [{"id": 1, "version": 1, "data": encode(settings)}]
        for table, rows in data.items():
            report["counts"][table] = len(rows)
            for n, row in enumerate(rows, 1):
                for field, value in row.items():
                    if sensitive(value):
                        block("secret_in_preserved_data", table, n, field)
                    if field in {"id", "version", "created", "timestamp", "fob_last_seen", "discord_last_synced", "member", "actor", "waiver", "root_family_member"}:
                        if value is not None and (type(value) is not int or value < 0 or value > 9007199254740991):
                            block("invalid_integer_or_timestamp", table, n, field)
                        if field in {"id", "version", "member", "actor", "waiver", "root_family_member"} and value == 0:
                            block("zero_identifier", table, n, field)
                try:
                    if len(insert(table, row).encode()) > MAX_STATEMENT_BYTES:
                        block("d1_statement_too_large", table, n)
                except ValueError:
                    block("unrepresentable_value", table, n)

        import_sql = verify_sql = None
        if not report["blockers"]:
            guard = ["CREATE TABLE _migration_guard (ok INTEGER NOT NULL CHECK(ok=1));"]
            schema_checks = [assertion("EXISTS(SELECT 1 FROM sqlite_schema WHERE type=" + literal(typ) + " AND name=" + literal(name) + " AND sql=" + literal(sql) + ")") for typ, name, sql in schema_objects]
            schema_checks.append(assertion("(SELECT count(*) FROM sqlite_schema WHERE type='trigger')=" + str(sum(typ == "trigger" for typ, _, _ in schema_objects))))
            start = guard + schema_checks
            for table in target_tables:
                if table not in {"runtime_control", "settings"}:
                    start.append(assertion(f"(SELECT count(*) FROM {ident(table)})=0"))
            start += [assertion("(SELECT count(*) FROM runtime_control WHERE id=1 AND automation_enabled=1)=1"),
                      assertion("(SELECT count(*) FROM settings)=1 AND (SELECT version FROM settings WHERE id=1)=1"),
                      "UPDATE runtime_control SET automation_enabled=0 WHERE id=1;"]
            load = [f"DROP TRIGGER {ident(name)};" for name, _ in suspended]
            load += ["DELETE FROM settings;"]
            # Roots precede dependents even though maintenance triggers are suspended.
            data["members"].sort(key=lambda r: (r["root_family_member"] is not None, r["id"]))
            for table, rows in data.items():
                load += [insert(table, row) for row in rows]
            for table, seq in sorted(sequences.items()):
                load += ["DELETE FROM sqlite_sequence WHERE name=" + literal(table) + ";",
                         "INSERT INTO sqlite_sequence(name,seq) VALUES(" + literal(table) + "," + literal(seq) + ");"]
            load += [sql + ";" for _, sql in suspended]
            load += ["UPDATE runtime_control SET automation_enabled=1 WHERE id=1;"]
            checks = schema_checks + [assertion("NOT EXISTS(SELECT 1 FROM pragma_foreign_key_check)"),
                                      assertion("(SELECT automation_enabled FROM runtime_control WHERE id=1)=1")]
            for table in target_tables:
                if table != "runtime_control":
                    checks.append(assertion(f"(SELECT count(*) FROM {ident(table)})={len(data.get(table, []))}"))
            for table, rows in data.items():
                for row in rows:
                    comparison = dict(row)
                    if table == "members":
                        comparison.update(original_status[row["id"]])
                    checks.append(assertion(f"EXISTS(SELECT 1 FROM {ident(table)} WHERE " + " AND ".join(ident(k) + " COLLATE BINARY IS " + literal(v) for k, v in comparison.items()) + ")"))
            for table, seq in sorted(sequences.items()):
                checks.append(assertion("(SELECT seq FROM sqlite_sequence WHERE name=" + literal(table) + ")=" + literal(seq)))
            checks.append(assertion(f"(SELECT count(*) FROM active_keyfobs)={len(fobs)}"))
            checks += [assertion(f"EXISTS(SELECT 1 FROM active_keyfobs WHERE fob_id={fob})") for fob in fobs]
            finish = ["DROP TABLE _migration_guard;", "SELECT 'migration_verified' AS result;"]
            import_sql = "\n".join(start + load + checks + finish) + "\n"
            verify_sql = "\n".join(guard + checks + finish) + "\n"
            if any(len(statement.encode()) > MAX_STATEMENT_BYTES for statement in start + load + checks + finish):
                block("d1_statement_too_large")
            # Exercise exactly the emitted SQL against the authoritative schema,
            # with foreign keys enabled. No HTTP functions/extensions are loaded.
            try:
                target.executescript(import_sql)
                target.executescript(verify_sql)
                if target.execute("PRAGMA integrity_check").fetchone()[0] != "ok":
                    block("target_integrity_check")
            except sqlite3.Error:
                # SQLite errors can contain private values or trigger bodies.
                block("target_validation_failed")
        if digest_file(source) != report["source_sha256"]:
            block("source_changed_during_export")
        if digest_file(SCHEMA) != report["schema_sha256"]:
            block("target_schema_changed_during_export")
        report["result"] = "blocked" if report["blockers"] else "ready"
        report["limits"] = ["Offline SQLite checks only; no Stripe/Discord/edge calls or D1 execution",
                            "All mapped rows and the validation target must fit in memory",
                            "No source secret values or images intentionally emitted; free text requires operator secret review",
                            "Fresh empty exact-schema target only; not an incremental sync"]
        # Schema/rule names are usually identifiers but are still source-controlled.
        # Do not let a credential embedded in a custom identifier leak in reports.
        def safe_metadata(value):
            if isinstance(value, dict):
                return {("[REDACTED]" if sensitive(k) else k): safe_metadata(v) for k, v in value.items()}
            if isinstance(value, list):
                return [safe_metadata(v) for v in value]
            return "[REDACTED]" if sensitive(value) else value

        report = safe_metadata(report)
        if not report["blockers"]:
            (output / "import.sql").write_text(import_sql, encoding="utf-8")
            (output / "verify.sql").write_text(verify_sql, encoding="utf-8")
            report["import_sha256"] = digest_file(output / "import.sql")
            report["verify_sha256"] = digest_file(output / "verify.sql")
        (output / "report.json").write_text(json.dumps(report, sort_keys=True, indent=2) + "\n")
        (output / "review-template.json").write_text(json.dumps({"source_sha256": report["source_sha256"], "approvals": []}, indent=2) + "\n")
        (output / "secret-names.json").write_text(json.dumps(safe_metadata({"worker_bindings": SECRET_BINDINGS,
                                                                           "source_locations": sorted(secret_locations)}), sort_keys=True, indent=2) + "\n")
        return 2 if report["blockers"] else 0
    finally:
        src.close()
        target.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path, help="Closed standalone SQLite backup, never live/WAL DB")
    parser.add_argument("--output", required=True, type=Path, help="New directory; parent must exist, prefer /dev/shm")
    parser.add_argument("--settings", type=Path, help="Reviewed nonsecret deployment settings JSON")
    parser.add_argument("--review", type=Path, help="Source-hash-bound explicit unsupported-feature approvals")
    args = parser.parse_args()
    os.umask(0o077)
    try:
        result = migrate(args.source, args.output, args.settings, args.review)
    except (OSError, ValueError, sqlite3.Error, TypeError, KeyError):
        print("Migration failed safely: invalid input, schema, or filesystem. No exception values are logged.", file=sys.stderr)
        return 1
    print("Migration blocked; inspect report.json." if result else "Migration verified offline; artifacts ready for isolated D1 rehearsal.")
    return result


if __name__ == "__main__":
    sys.exit(main())
