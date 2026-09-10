# Legacy SQLite to Workers D1

This is an **offline, fail-closed, membership-core migration**, not a live sync or
an in-place upgrade. `scripts/migrate_sqlite.py` uses Python's standard `sqlite3`
module and the exact `migrations/0001_core.sql` schema. It does not import Go
modules, run legacy triggers, load SQLite extensions, call HTTP, invoke Wrangler,
or contact Stripe, Discord, or the edge. The TypeScript application is unchanged.

An export with no blockers means **verified in local SQLite**, not approved for
production. A real D1 rehearsal, identity verification, financial reconciliation,
and operator sign-off are still required. Do not treat `AUTOMATION_ENABLED=false`
as a maintenance mode: login, checkout and inbound routes can still have effects.

## Prerequisites

- Python 3.10+ with SQLite 3.37+ (STRICT tables, generated columns and JSON support).
- A closed, standalone, consistent SQLite backup on an encrypted, access-controlled
  archive volume. Never point this tool at the running Go application's database.
- Enough RAM for selected source rows, generated SQL, and a complete in-memory
  target. Images and excluded table bodies are not loaded into migration data.
- An existing output parent directory. Each run creates a new private directory,
  refuses overwrite, and never deletes existing outputs or source files.
- A matching deployed schema and an **empty dedicated D1 database**, inaccessible
  to the public Worker, queue consumer, cron, webhooks, interactions and edge sync.
- At least one independently verified leadership account already linked to its
  own Discord ID in the source backup. Prefer two, with tested recovery access.

The root disk on the development host is almost full. Use `TMPDIR=/dev/shm` for
tests, and `/dev/shm` output paths for rehearsals. The script uses an in-memory
validation DB and does not create a second SQLite copy on disk. `/dev/shm` is
ephemeral, is not an archive, and may be backed by swap. Check capacity first;
move final artifacts to approved durable encrypted storage before shutdown.
Never remove another person's temporary files to free space.

## Source Backup

1. For a rehearsal, obtain a consistent backup using the existing operational
   backup procedure or SQLite's online backup API. Do not copy only a live main
   database file: committed records may still be in its WAL.
2. For cutover, first stop **all** legacy writers and background workers, including
   mail delivery, timed SQL, Discord, payment handling and swipe ingestion. Block
   mutations and incoming webhooks or retain them durably for later reconciliation.
   Obtain the final backup only after this boundary.
3. Close the backup writer, verify integrity, and archive the standalone database
   plus application version, deployment configuration, signing material and
   backup metadata under the existing secret-retention policy. Keep the original
   source/archive unchanged. Do not perform cleanup or schema migrations on it.
4. The importer refuses `-wal`, `-shm` or `-journal` sidecars. Their presence is a
   reason to create a proper standalone backup, **not** to delete them. It opens
   `mode=ro&immutable=1`, enables `query_only`, uses memory for SQLite temporary
   storage, checks integrity/FKs and hashes source bytes before and after the run.
   `immutable=1` is safe only because the operator guarantees the file is closed
   and unchanging. A concurrent writer violates this guarantee; hash checks are
   an additional check, not snapshot isolation for a live file.

There is deliberately no automatic production backup or cleanup command. Backup
paths, encryption, live WAL consistency and retention are operational decisions.
The exporter consumes that backup read-only; it does not archive another copy.

## Preserved Data

| Source                     | Target and behavior                                                                                                                                                                                                                                                                                                                    |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `members`                  | All contract writable fields and original IDs/creation times, names/overrides, notes/referral source, confirmation, waiver pointer, fobs/last seen, leadership, billing/discount/family/PayPal/Stripe state, Discord linkage, and profile/privacy/notification fields                                                                  |
| Target `members.version`   | Initialized explicitly to `1` and verified for every imported member. The legacy source normally has no revision column; its absence is not a blocker.                                                                                                                                                                                 |
| Directory additive columns | `bio`/`pronouns` NULL or absent become `''`; nullable/absent privacy and notification flags become `0`. Every normalization is recorded. No other missing required member column is guessed.                                                                                                                                           |
| `waivers`                  | Every signature, including pre-signup signatures, original ID/version/time/name/email and exact existing member links. No email-based relinking or newest-waiver substitution during import.                                                                                                                                           |
| `waiver_content`           | Every version becomes `waiver_versions`, retaining content and created time verbatim. `agreements` is the ordered JSON list extracted with the legacy `modules/waiver/markdown.go` checkbox rules. Latest version becomes `settings.waiver_version`.                                                                                   |
| `member_events`            | Original IDs, times, member, event and details; `actor` retained if present, otherwise NULL. The target has no `important` flag: this requires explicit archive-only review, not silent loss.                                                                                                                                          |
| `fob_swipes`               | Original UID/time/fob/member/allowed. Existing `controller` retained if present; otherwise `fob_client` becomes `legacy-fob-client:<id>`, or `''` without a client. Client references are checked. Client IP/door-name configuration remains in source. Historical member attribution is not rewritten based on current fob ownership. |
| `sqlite_sequence`          | Member, waiver and event high-water marks retained so deleted historical IDs are not reused.                                                                                                                                                                                                                                           |
| `members_config`           | Latest version's `referral_sources_json` labels become `referral_sources`.                                                                                                                                                                                                                                                             |
| `stripe_config`            | Latest `donation_items_json` maps `name` to `label` and retains `price_id`. API/signing keys are never settings.                                                                                                                                                                                                                       |
| `discord_config`           | Latest guild/role/leadership/badge channel IDs and badge/access-denied enable flags map to contract settings.                                                                                                                                                                                                                          |

The source layout is intentionally discovered with `table_xinfo`: the base member
SQL is not the whole schema. Relevant additive migrations live in members,
directory, payment, fobapi and Discord Go modules. Missing required fields block;
unknown columns, unsupported config and excluded tables require review. All
historical config versions remain intact in the source archive; only the current
nonsecret configuration maps to target singleton `settings(version=1)`.

**Images are omitted entirely from D1/SQL.** Neither `discord_avatar` nor
`profile_picture` is selected or encoded. No image files or base64 exports are
created. Column names may appear in the source inventory.

Everything outside the core stays **only in the unchanged source archive**:
images, email/login codes, sessions/tokens, OAuth provider data, SQL/timed/custom
triggers, webhook URLs/queues, metrics, machines, printers/signs, elections, phone
inbox, fob-client configuration and module event/config histories. No email,
Discord notification, payment or other legacy queue is replayed. Target sessions,
OAuth state, claims, jobs, Stripe webhook events, donations, notification state and
rate limits start empty. Do not infer historical donation records from event text.

## Blocking Preflight

There is no force flag for data blockers and no automatic merge, email identity
linking, Discord lookup, fob normalization, orphan deletion or family repair.

- Duplicate email identities, including trim/casefold collisions; invalid/empty
  email identities; duplicate or malformed Discord IDs; Discord email pointing
  at another member's email; and absence of a linked leadership account block.
  Unlinked ordinary members are counted and preserved, not linked by email.
- Duplicate/malformed Stripe customer or subscription IDs, unknown subscription
  states and inconsistent customer/subscription/state combinations block. A
  customer with no subscription is valid. Customer/subscription ownership and
  real payment state still require independent Stripe reconciliation.
- Orphan waivers, waiver versions, family roots, history members/actors and
  controller references block. Linked waiver email mismatches and duplicate
  `(email,version)` signatures block. Anonymous signatures are valid.
- Cycles, nested family roots, stale `root_family_member_active` and family flags
  without a root block. Membership state is not silently recalculated to fix them.
- Duplicate, zero, negative or out-of-range fobs block (valid assigned range is
  `1..4294967295`). Zero is a legacy missing-fob sentinel but is not silently
  changed to NULL. Malformed swipe values, booleans, IDs and timestamps block.
- Payment/access/identifier values are independently derived with legacy rules,
  compared with source generated values, then checked against target generated
  values. The exact source `active_keyfobs` set is compared with independently
  derived membership eligibility and the loaded target set. Reports contain the
  set count and SHA-256; SQL verification asserts every eligible fob.
- Target constraints/FKs, unsupported scalar values, NUL-containing text and SQL
  statements exceeding the conservative 95,000-byte D1 limit block. No truncation
  or silent dropping of long waiver/history records is performed.

Resolve data blockers through a separately authorized, audited process in the
legacy application, then take a **new** backup. Never alter the archived backup.
If new source variants require a mapping, update and test this tooling instead of
acknowledging away data inconsistencies.

## Settings And Secrets

Create a reviewed **nonsecret** settings JSON using
`scripts/migration-settings.example.json` as the shape reference. Required fields
are `site_name`, `monthly_price_id`, `yearly_price_id`, `discounts`,
`signup_notify_enabled`, and `notification_templates`. The example's `REPLACE`
price values intentionally block. Empty prices are allowed for intentionally
disabled checkout, not evidence of a completed billing configuration.

The old application discovers Stripe prices by `monthly`/`yearly` lookup keys and
coupons by `metadata.discountTypes`. These mappings cannot be recovered from
SQLite. An authorized operator must verify them separately in the correct Stripe
account/mode. Preserve all eight legacy discount IDs/labels, including
`firstResponder`; fill each coupon ID or explicitly choose no coupon (`''`).
Additional discount IDs are permitted. Test requested/approved/admin-set discounts
and existing customer billing-portal behavior before enabling billing.

Legacy Go signup templates (`{{ ... }}`), arbitrary Discord webhooks, approval-bot
enable behavior and configurable timed SQL do not have equivalent target
semantics. Translate templates explicitly into contract placeholders
(`{name}`, `{discount_type}`, `{access_status}`, `{site_url}`), review notification
destinations/enablement and retire or reimplement unsupported rules separately.
DB-mapped settings can be overridden only with a source-bound review item when
their value changes. `waiver_version` is derived, not operator-overridable.

`secret-names.json` lists **names/locations only**, separately from the report:
`STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`, `DISCORD_CLIENT_ID`,
`DISCORD_CLIENT_SECRET`, `DISCORD_BOT_TOKEN`, `DISCORD_PUBLIC_KEY`, `EDGE_TOKEN`,
and detected source secret columns. Provision values through the deployment's
secret manager or interactive `wrangler secret put`, never this JSON, SQL, shell
command arguments, reports or source control. Webhook URLs are credentials, not
channel IDs. `SITE_URL`, `EDGE_URL`, `KIOSK_IPS`, and `AUTOMATION_ENABLED` also need
explicit deployment review; they are not migrated DB settings. Preserve the edge
signing seed separately if existing controllers verify legacy signatures.

The exporter never copies config secret columns, rule SQL, webhook URLs or
excluded table bodies. It scans all retained text/settings for known source
credential values and common credential formats and **blocks instead of
redacting retained history**. Diagnostics contain field names/row numbers, not
values, SQL bodies or SQLite exception text. This cannot recognize every arbitrary
secret pasted into prose, encoded credential or unknown secret column. A private
operator review of free text is mandatory; artifacts also contain member PII and
waiver signatures and must be protected accordingly. Do not post SQL in CI logs.

## Dry Run

Run from the repository root. The source/settings paths below are examples on an
approved archive volume; the output path must not already exist.

```sh
TMPDIR=/dev/shm PYTHONDONTWRITEBYTECODE=1 python3 workers/scripts/migrate_sqlite.py \
  --source /secure/archive/conway-final.sqlite \
  --settings /secure/migration/nonsecret-settings.json \
  --output /dev/shm/conway-preflight-01
```

Exit codes: `0` = offline verified, `2` = preflight blocked with `report.json`,
`1` = invalid input/schema/filesystem error with deliberately generic diagnostics.
On exit 1, consider the output directory incomplete and never import anything
from it. Inputs are never overwritten. Use a new output name for every attempt.

The first run normally blocks because **every actual legacy SQL trigger** needs
explicit review, even stock ones. This avoids trusting a familiar trigger name
whose production body was customized. The report inventories table names,
columns/counts, trigger names/body hashes, latest mapped config versions and
review IDs. Disabled and timed rules in `triggers`, plus `discord_webhooks` rows,
are inventoried separately; they may have no installed SQLite trigger.

Review each unsupported item in the private source using read-only tooling.
Record its replacement or deliberate retirement and retention owner in the change
ticket. Starting from `review-template.json`, add only reviewed IDs to
`approvals`, retaining the exact `source_sha256`. Example shape:

```json
{
    "source_sha256": "the exact hash from report.json",
    "approvals": ["trigger:members_family_relationship_update", "column:member_events.important", "archive:outbound_mail", "rule:triggers:7"]
}
```

Do not automatically approve every item. Review approval accepts unsupported
behavior/archive exclusions, **not** data inconsistencies. A changed source hash
invalidates the whole review. Re-run with a new directory:

```sh
TMPDIR=/dev/shm PYTHONDONTWRITEBYTECODE=1 python3 workers/scripts/migrate_sqlite.py \
  --source /secure/archive/conway-final.sqlite \
  --settings /secure/migration/nonsecret-settings.json \
  --review /secure/migration/review.json \
  --output /dev/shm/conway-approved-01
```

A successful export contains deterministic `import.sql`, `verify.sql`,
`report.json`, `review-template.json` and `secret-names.json`. SQL files are not
emitted if blocked. Source/schema/SQL hashes, counts, linked-leader IDs,
normalizations and fob-set digest are reported. The same source, schema, settings
and approvals produce byte-identical artifacts. There is no run-time timestamp in
the export. Retain input settings and review alongside artifact hashes.

## D1 Rehearsal

For a synthetic **local-only** rehearsal, run from the repository root:

```sh
TMPDIR=/dev/shm PYTHONDONTWRITEBYTECODE=1 python3 workers/scripts/rehearse_local_d1.py \
  --node /dev/shm/conway-workers-test/package/bin/node
```

This harness invokes the installed `workers/node_modules/wrangler/bin/wrangler.js`
with `d1 execute conway --local --persist-to <unique /dev/shm directory>`, applies
the authoritative schema first, imports generated synthetic SQL, and executes its
verification SQL. It then checks the snapshot and exercises revision, identity
session invalidation and consumed-claim deletion triggers. No public Worker is
started, telemetry is disabled, and no remote D1 command is issued. Only the
harness's own temporary artifacts/persistence/logs are removed. Its automatic
review approvals are strictly for synthetic test data, never real backups.

Validated with Node `v22.22.0` and Wrangler `4.124.0`: all commands succeeded;
both import and verification returned `migration_verified`. Snapshot: 3 members,
all at revision 1, 2 authorized fobs, 1 preserved member event, 0 jobs and runtime
automation restored to 1. Subsequent identity change increased revision and
deleted the session; deleting the member removed the consumed enrollment claim.
The Python regression suite contains 28 passing tests against this schema.
This is local D1/workerd evidence, not remote-service or production-volume proof.

Use the installed project Wrangler, not an unpinned download. From `workers/`,
against a disposable **unbound and isolated** D1 database, apply the authoritative
schema and import the approved files. These example commands intentionally name
a rehearsal database, not production:

```sh
TMPDIR=/dev/shm npx --no-install wrangler d1 execute conway-migration-rehearsal \
  --remote --file migrations/0001_core.sql
TMPDIR=/dev/shm npx --no-install wrangler d1 execute conway-migration-rehearsal \
  --remote --file /dev/shm/conway-approved-01/import.sql
TMPDIR=/dev/shm npx --no-install wrangler d1 execute conway-migration-rehearsal \
  --remote --file /dev/shm/conway-approved-01/verify.sql
```

Use the team's migration ledger/deployment procedure for the real target; do not
apply `0001_core.sql` a second time to an already initialized database. The import
asserts exact core schema SQL and trigger count, empty application tables, initial
settings version and enabled runtime singleton before touching imported data.
Wrangler's own bookkeeping tables do not need to be empty. Any schema drift,
including normalization by an untested D1 version, requires investigation, not
removal of the guard. Rehearse `sqlite_sequence` updates, schema introspection,
trigger DDL, statement limits and `pragma_foreign_key_check` on actual D1.

During loading the SQL sets `runtime_control.automation_enabled=0` to suppress
outbox/audit triggers. It temporarily drops the authoritative family/waiver/
discount/swipe maintenance triggers, as well as `member_revision`,
`identity_sessions` and `consumed_claim_delete`, because they are **not all gated**
by that flag. Otherwise import would silently relink waivers or overwrite last-seen
history. It loads roots before dependents and waivers before members, restores
the exact maintenance triggers and runtime flag to `1`, then asserts every
mapped value, initial member revision, generated status, fob membership, count, sequence, FK and empty
effect table. `_migration_guard` CHECK failures abort verification; success ends
with `migration_verified`. `verify.sql` repeats these checks against the import
snapshot, including case-sensitive text equality. It is a cutover snapshot check,
not a monitor to run after intentional member changes.

The file does **not** use `BEGIN`/`COMMIT`, disable foreign keys, or assume a whole
Wrangler file is one atomic transaction. A failed/chunked import can leave partial
rows, dropped maintenance triggers, runtime `0`, or `_migration_guard` behind.
**Never bind or serve a failed target.** Discard/recreate that dedicated database
through the approved infrastructure procedure, apply schema, and start over. Do
not try `INSERT OR REPLACE`, resume a tail of SQL, manually toggle runtime, or
re-run into a nonempty target. Export is repeatable; import is fresh-target only.

The tool has no outgoing code path, but SQL cannot stop an already-running Worker
from processing a message or doing checkout. Isolation, disabled automation,
stopped consumers/cron and no inbound/public traffic remain mandatory throughout.
Keep all legacy outboxes archived, never copy/retry them into target jobs.

## Cutover

1. Complete the isolated D1 rehearsal and record source/schema hashes, counts,
   full field checks, exact authorized fob comparison, configuration reviews and
   linked-leadership sign-off. Confirm the firmware/edge contract and controller
   last-good set behavior; do not change firmware in this migration.
2. Announce a write freeze; stop legacy writers/workers and inbound effects.
   Ensure Stripe deliveries and edge swipes across the gap are retained/retriable
   with a documented replay boundary. Keep the edge serving its last-good access
   set, not an empty/new cloud snapshot. Record the final source time and backup.
3. Export the final backup, renew source-bound reviews, import into a fresh
   isolated production D1 target with automation disabled, and run `verify.sql`.
   Repeat external Stripe reconciliation for changes during the freeze; do not
   infer correctness from matching SQLite state alone.
4. Before public access, independently verify the actual Discord ID ownership of
   every linked leader and test leader login in a controlled environment. SQL
   validates ID format and uniqueness, not ownership or Discord availability.
   Unlinked accounts require leadership-assisted manual ID linkage following
   identity verification; email equality must never authorize linking or merging.
   Block public signup while resolving preexisting unlinked identities.
5. Bind the verified target; review SITE/EDGE/KIOSK vars, secrets, OAuth callback,
   interactions and webhook routes. Enable each route only when its outgoing
   implications are approved. Reconcile/ingest retained Stripe/edge events using
   stable event IDs and the application's deduplication. Do not blindly replay
   historic signup, badge or denial notifications or webhook queues.
6. Compare the live edge authorized set with D1 immediately before its first
   push. Confirm monotonic version allocation exceeds existing edge state;
   do not reuse a stale version or force a lower one. Provision signing seed
   separately where required. Explicitly approve the first access snapshot.
7. Enable public writes and external automation only after sign-off, then watch
   login/admin access, family/payment transitions, job failures, Stripe state,
   access set and swipe ingestion. Existing members do not get synthetic signup
   jobs; initial Discord reconciliation must be a separately approved operation.

## Rollback Boundaries

**Before Workers accepts writes or external effects:** leave the failed/new D1
isolated, restore original routing, and restart the unchanged legacy system with
its original database/config/signing material. Handle retained webhook/swipe
events once. Do not delete the archive or overwrite source from D1.

**After Workers accepts writes or produces effects:** DNS rollback alone is not
safe. Freeze both systems and reconcile new members/waivers/fobs/admin changes,
Stripe payments/cancellations, Discord effects, edge snapshots and event replay
state. There is no automatic reverse migration or dual-write synchronization.
Payments/messages cannot be undone by restoring SQLite. A separately reviewed
reverse-data reconciliation and compensating-action plan is required before
resuming Go. Keep durable edge versions monotonic on rollback too. Never run both
membership writers or automation stacks simultaneously.

Define who may authorize this boundary in the change ticket. Retain the complete
legacy archive, excluded-feature retention schedule, production artifact hashes,
pre/post-cutover D1 backups and operational event logs for the agreed rollback and
legal-retention period. This tooling never deletes any of them.

## Verification And Limits

```sh
TMPDIR=/dev/shm PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s workers/scripts -p 'test_*.py' -v
```

Tests synthesize the real base member schema plus distributed additive columns
and configuration tables. They test source byte equality, deterministic output,
profile/waiver/history/settings/ID preservation, trigger restoration and outbox
silence, status matrices, fob parity, nonempty/schema-drift rejection, secret
blocking and all principal identity/family/orphan preflights. Their temporary
directories live in `TMPDIR` (default `/dev/shm`) and only those directories are
removed by test cleanup.

These tests do not access a production backup, execute remote D1, verify real
Discord ownership, reconcile Stripe, deliver/undo effects, test firmware, or
benchmark production volume. Source archive creation/retention and the real
cutover are operator tasks. Exact-schema D1 compatibility and capacity must be
proven in the isolated rehearsal; large records currently block rather than being
split. Preserving all retained fields while guaranteeing arbitrary prose contains
no unknown secret is not automatically solvable; review privately and resolve
any contamination under a separately authorized retention/security process.
