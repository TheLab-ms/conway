# signs

Member-facing sign printer. Active members pick a template at `/signs/{slug}`,
fill out the dynamic fields, and a worker delivers a rendered PDF to a network
printer over IPP.

## Pieces

- `module.go` — module wiring, HTTP routes, workqueue (`GetItem`/`ProcessItem`/
  `UpdateItem`), submission handlers, and the `signs_config` /
  `signs_print_queue` SQLite migration.
- `config.go` — typed `Config` (printer host/port/queue + `[]Template`) and
  `FieldDef`/`Template` types. Includes the seed `DefaultMaintenanceTemplate`.
- `render.go` — `RenderSign` executes a Go `text/template` body and renders a
  small markdown subset to a Letter-size PDF via `go-pdf/fpdf`.
- `ipp.go` — `Printer` interface, `NewIPPPrinter` (raw IPP Print-Job request),
  and the `noopPrinter` fallback.
- `admin.go` — leadership-only template editor at `/admin/signs/templates/...`
  with a live PDF preview endpoint, plus the templates panel embedded in the
  signs config page.
- `signs.templ` / `admin.templ` — UI.

## Behavior worth knowing

- **Auth gate.** All `/signs` routes require an active member; non-active users
  get 403. Template editing requires leadership.
- **Worker.** Polled at 1 Hz, rate-limited to `printRPS = 1` job/sec. A
  separate hourly cleanup deletes rows older than `printQueueTTL` (1 hour).
- **Retry semantics.** `ProcessItem` returns `*RenderError` for non-retryable
  failures (missing template, template execute error, PDF too large). The
  engine doesn't pass the error to `UpdateItem`, so failures currently bump
  `attempts` with exponential backoff (`min(maxBackoffSeconds, 2^attempts)`,
  cap 1 h) and rely on the TTL cleanup to eventually drop bad rows.
- **Printer fallback.** If `printer_host` or `printer_queue` is empty the
  module installs `noopPrinter`, which errors on every job so rows back off
  in-queue rather than being silently dropped. `SetPrinter(nil)` reinstalls
  the noop. `reloadConfig` will not overwrite a test-injected printer with a
  real IPP one unless real config is present.
- **Brother quirks (`ipp.go`).** A custom `brotherAdapter` overrides the
  go-ipp library's hardcoded `/printers/<queue>` URL so the configured queue
  path is used verbatim (Brother exposes `/ipp/print`). `printer-uri` is set
  to the actual printer URI rather than `localhost`. `job-priority` and
  `copies` are deliberately not sent — Brother firmware silently drops jobs
  that include `job-priority`. IPP statuses in `0x0000–0x00FF` are treated as
  success (RFC 8011 §15.1) even though go-ipp surfaces them as errors.
- **Submission limits.** Per-member: ≤5 submits/min and ≤20 outstanding
  prints (HTTP 429 otherwise). Body capped at 64 KiB; per-field length
  ≤2000 chars; rendered PDF capped at 10 MiB.
- **Field storage.** New rows store form values in `fields_json`; legacy
  `machine_name`/`issue` columns are still written empty for back-compat and
  read as a fallback by `buildSignData` / `printRecord.FieldSummary`.
- **Always-available template vars.** `{{.DiscordHandle}}` (member's Discord
  handle, falling back to email local-part, then `"unknown"`) and `{{.Date}}`
  (formatted print-creation time). Other variables come from the template's
  `FieldDef`s.
- **Markdown subset.** `# / ## / ###` headings, `---`/`***` rules, `-`/`*`
  bullets, `**bold**` inline. Long unbreakable tokens are
  character-soft-wrapped to avoid right-margin overflow. Page has a
  Conway-green accent bar at the top.
- **Default template seeding.** On first run (no `signs_config` row) the
  `DefaultMaintenanceTemplate` is inserted. Subsequent saves are honored
  verbatim, including an empty templates list — clearing the picker is
  treated as an intentional admin choice, not an error.
- **Preview endpoint** (`POST /admin/signs/preview`) accepts both
  `application/x-www-form-urlencoded` and `multipart/form-data`; the editor's
  JS uses `FormData`, which is multipart. Sample values come from
  `preview_<FieldName>`, falling back to placeholder, then `(<Label>)`.
- **Generated files.** `*_templ.go` are produced by `templ generate`
  (`go:generate` directive in `module.go`); don't edit by hand.
