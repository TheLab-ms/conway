# admin

Server-rendered admin web UI for Conway. Mounts under `/admin` and renders templ-generated HTML.

## Functionality

- **List views** (`meta.go`): paginated, searchable, filterable tables. Currently `/admin/members` and `/admin/events`. Each view supplies its own `BuildQuery`/`BuildRows`. Pages are 20 rows; `/admin/search/...` (POST) returns the first page, `/admin/more/...` (GET) returns subsequent pages for HTMX-style infinite scroll.
- **Member detail** (`member.templ`): `/admin/members/{id}` shows member info plus paginated event history (10/page). `POST /admin/members/new` upserts by email and redirects to the detail page. `POST /admin/members/{id}/delete` removes the member. `POST /admin/members/{id}/stripe-customer` creates a Stripe customer using the API key from `stripe_config` (latest version) and stores `stripe_customer_id`.
- **Login QR** (`/admin/members/{id}/logincode`): issues a 5-minute JWT via the injected `TokenIssuer` and returns a PNG QR code pointing at `{self}/login?t=...`.
- **CSV export** (`/admin/export/{table}`): whitelist-only — `members`, `waivers`, `fob_swipes`, `member_events`. `SELECT *` is interpolated as the table name (safe due to whitelist).
- **Metrics** (`/admin/metrics`, `/admin/chart`): renders charts configured via the `metrics` config module. Chart definitions are read from the config store using reflection on a `Charts` field to avoid importing the metrics package. Default time window is 60 days (`1440h`); chart data window defaults to 7 days.
- **Generic config UI** (`configui.templ`): `/admin/config` redirects to the first registered module. `/admin/config/{module}` GET/POST is driven entirely by `engine/config.Registry` + `Store`. POST uses `ParseFormIntoConfig` and `Save(..., preserveSecrets=true)` so blank secret fields keep existing values. `ReadOnly` specs skip the `Load` call (no backing table). Each config page also lists the 20 most recent rows from `module_events` for that module, joined to `members` for display names.
- **DB console** (`/admin/config/dev/db`): raw SQL execution. Distinguishes read queries (`SELECT`/`PRAGMA`/`EXPLAIN`/`WITH`) from writes by uppercase prefix; reads render a result table, writes report `RowsAffected`.

## Behavioral notes

- All routes are wrapped with `router.WithLeadership` — only the leader process serves admin traffic.
- List queries run inside a read-only transaction. Pagination uses named params `:limit`/`:offset` appended to the args produced by `BuildQuery`.
- The navbar (`m.nav`) is built once at `New()`. Config pages are *not* in the navbar; they use a separate sidebar built from the registry in `getConfigSections`.
- `SetConfigRegistry` must be called after all modules register their config specs; it lazily constructs the `Store` and is what gates registration of the `/admin/config/{module}` routes (see `AttachRoutes`). If `AttachRoutes` runs before `SetConfigRegistry`, generic config routes will not exist.
- `AttachWorkers` runs an hourly cleanup that keeps only the 100 most recent `module_events` rows per module (FIFO via `ROW_NUMBER() OVER (PARTITION BY module ORDER BY created DESC)`).
- `formHandlers` is a package-level slice populated by `handlePostForm` at init time from other files; currently empty but the wiring in `AttachRoutes` will register any that get added.
- Templ files (`*.templ`) are the source; `*_templ.go` are generated — do not edit by hand. Run `go generate ./modules/admin` after changes.
