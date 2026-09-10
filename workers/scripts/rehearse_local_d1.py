#!/usr/bin/env python3
"""Generate synthetic artifacts and verify them with Wrangler's local D1 only."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

from test_migrate_sqlite import MigrationTests
from migrate_sqlite import SCHEMA


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--node", required=True, type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    workers = SCHEMA.parents[1]
    env = dict(os.environ, TMPDIR="/dev/shm", WRANGLER_SEND_METRICS="false",
               CI="true", BROWSER="none")
    # This fixture intentionally auto-approves SYNTHETIC rules only. Never use
    # its review-generation shortcut on a real backup.
    fixture = MigrationTests()
    fixture.setUp()
    try:
        code, output, report = fixture.run_export()
        if code:
            raise RuntimeError("synthetic export blocked")
        with tempfile.TemporaryDirectory(prefix="conway-migration-d1-", dir="/dev/shm") as directory:
            env["WRANGLER_LOG_PATH"] = str(Path(directory) / "wrangler.log")
            command = [str(args.node.resolve()), str(workers / "node_modules/wrangler/bin/wrangler.js"),
                       "d1", "execute", "conway", "--local", "--persist-to", directory,
                       "--config", str(workers / "wrangler.jsonc"), "--json"]
            results = {}

            def execute(label, *options):
                run = subprocess.run(command + list(options), env=env, cwd=workers,
                                     text=True, capture_output=True, timeout=120)
                if run.returncode:
                    # The input here contains synthetic data only.
                    print(run.stdout)
                    print(run.stderr)
                    raise RuntimeError(f"local D1 {label} failed: exit {run.returncode}")
                response = json.loads(run.stdout)
                if not isinstance(response, list) or any(not item.get("success") for item in response):
                    raise RuntimeError(f"local D1 {label} returned unsuccessful results")
                results[label] = {"result_entries": len(response), "success": True}
                print(f"{label}: {len(response)} successful result entries", flush=True)
                return response

            execute("schema", "--file", str(SCHEMA))
            imported = execute("import", "--file", str(output / "import.sql"))
            verified = execute("verify", "--file", str(output / "verify.sql"))
            for response in (imported, verified):
                if not any(row.get("result") == "migration_verified" for item in response for row in item["results"]):
                    raise RuntimeError("missing migration verification sentinel")
            snapshot = execute("snapshot", "--command", """
                SELECT (SELECT count(*) FROM members) AS members,
                       (SELECT count(*) FROM members WHERE version=1) AS initial_revisions,
                       (SELECT count(*) FROM jobs) AS jobs,
                       (SELECT count(*) FROM member_events) AS member_events,
                       (SELECT count(*) FROM active_keyfobs) AS authorized_fobs,
                       (SELECT automation_enabled FROM runtime_control) AS automation_enabled;
            """)[0]["results"][0]
            if snapshot != {"members": 3, "initial_revisions": 3, "jobs": 0, "member_events": 1,
                            "authorized_fobs": 2, "automation_enabled": 1}:
                raise RuntimeError("unexpected local D1 snapshot")
            restored = execute("restored_triggers", "--command", """
                INSERT INTO sessions VALUES('synthetic-session',100,'csrf',9999999999,NULL);
                UPDATE members SET discord_user_id='987654321012345678' WHERE id=100;
                SELECT version>1 AS revised, (SELECT count(*) FROM sessions)=0 AS sessions_cleared FROM members WHERE id=100;
                INSERT INTO enrollment_claims VALUES('synthetic-claim',999,100,9999999999,100);
                DELETE FROM members WHERE id=100;
                SELECT count(*)=0 AS claims_cleared FROM enrollment_claims;
            """)
            if restored[2]["results"] != [{"revised": 1, "sessions_cleared": 1}] or restored[5]["results"] != [{"claims_cleared": 1}]:
                raise RuntimeError("restored trigger behavior failed")
            print(json.dumps({"mode": "local-only", "snapshot": snapshot, "results": results,
                              "schema_sha256": report["schema_sha256"],
                              "import_sha256": report["import_sha256"]}, sort_keys=True, indent=2))
    finally:
        fixture.doCleanups()


if __name__ == "__main__":
    main()
