# triggers

User-configurable SQL trigger management. Stores trigger definitions in the
`triggers` table and exposes CRUD via the admin config page (`/admin/config/triggers`).

## Trigger types

- **Event**: backed by a real SQLite `CREATE TRIGGER ... AFTER INSERT|UPDATE|DELETE`
  on a table, with an optional `WHEN` clause. Created/dropped in SQLite as rows
  are written. The underlying SQLite trigger is named `user_trigger_<id>`.
- **Timed**: an SQL statement executed on an interval (Go duration string) by a
  background poller. Replaces the prior `metrics_samplings` system.

## Behavioral details

- `New` runs the migration, then `ALTER TABLE` adds for `trigger_type`,
  `interval_seconds`, `last_run` (errors ignored — they are idempotent on restart).
- On startup: legacy `metrics_samplings` rows are converted to timed triggers
  (idempotent by name), default triggers are seeded (idempotent by name), and
  all event triggers are dropped+recreated in SQLite to sync with the DB rows.
- Disabling an event trigger drops the SQLite trigger; re-enabling recreates it.
  Changing an event trigger to timed drops the SQLite trigger.
- Timed triggers are polled every minute (`AttachWorkers`). A trigger fires when
  `now - last_run >= interval_seconds`, or when `last_run == 0` (first run).
  `last_run` is stored as a Unix timestamp (float).
- Timed trigger SQL may reference `:last` (named parameter) — bound to the
  previous `last_run` as an int64. This lets samplings query "since last run".
- Operation validation: only `INSERT`, `UPDATE`, `DELETE` are accepted for event
  triggers. Trigger names and table names are interpolated unsanitized into SQL —
  only leadership users can hit the routes (`router.WithLeadership`).
- The `triggers` table itself is filtered out of the table picker.
- All routes redirect to `/admin/config/triggers` on success. Empty required
  fields silently redirect without saving (no error shown).
