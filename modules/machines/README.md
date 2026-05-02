# machines

Bambu 3D printer integration. Polls configured printers via MQTT, persists status to SQLite, renders a `/machines` dashboard with live MJPEG camera feeds, and exposes a stop-print action.

## Routes

- `GET /machines` — dashboard of printers (auth required).
- `GET /machines/stream/{serial}` — MJPEG camera feed multiplexed via `engine.StreamMux` (one upstream `ffmpeg` process per printer, fan-out to subscribers).
- `POST /machines/{serial}/stop` — sets a `stop_requested` flag in the DB; the next poll cycle issues the actual MQTT stop and clears the flag.

## Configuration

Loaded via `config.Loader[Config]` under module key `bambu`. Schema in `config.go`:
- `Printers[]`: `Name`, `Host`, `AccessCode`, `SerialNumber` (key field).
- `PollIntervalSeconds`: 1–60, default 5.

Config changes are detected by version comparison at the start of every poll. On change, all existing MQTT clients are disconnected and rebuilt; `StreamMux` instances are preserved for serials still present, and torn down for removed ones.

## Polling

`poll` runs on `engine.DynamicPoll` at `pollInterval`. For each printer it:
1. Checks/executes pending stop request.
2. Calls `printer.GetState()` (blocking MQTT roundtrip with 10s timeout).
3. Computes `JobFinishedTimestamp` from `RemainingPrintTime` (nil when ≤1 minute).
4. Upserts into `bambu_printer_state`.
5. Logs a `Poll`/`PollError` event via `EventLogger`.

State rows older than `3 * pollInterval` are treated as stale and excluded from `loadPrinterStates`. A separate hourly worker hard-deletes rows older than 24h (handles printers removed from config).

## Bambu MQTT client (`bambu/`)

- TLS MQTT on port 8883, username `bblp`, password = access code, `InsecureSkipVerify: true`.
- Subscribes to `device/{serial}/report`, publishes commands to `device/{serial}/request`.
- `GetState` issues a `pushing.pushall` and waits on a single-shot response channel; only messages with non-empty `gcode_state` are accepted (filters out partial pushes).
- `StopPrint` publishes `print.stop`.
- `CameraStream` shells out to `ffmpeg` to transcode the printer's RTSPS stream (`rtsps://bblp:{accessCode}@{host}:322/streaming/live/1`) to MJPEG on stdout. Requires `ffmpeg` on PATH.

## Discord mentions

`PrinterStatus.OwnerDiscordHandle()` extracts the first `@handle` (regex `@([a-zA-Z0-9_.]+)`) from `SubtaskName` (the user-editable plate name from Bambu Studio). Note: this matches inside email addresses — `email@example.com` returns `example.com`.

## Schema notes

Migration creates `bambu_config` and `bambu_printer_state`. `New()` also runs an unconditional `ALTER TABLE ... ADD COLUMN stop_requested` to upgrade pre-existing DBs (error ignored). The migration explicitly drops legacy `bambu_print_completed` / `bambu_print_failed` triggers — notifications are now handled in Go, not SQL triggers.
