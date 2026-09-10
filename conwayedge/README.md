# conwayedge

A standalone Go HTTP service for LAN access controllers and Bambu printer status/cameras. No Conway imports, database, cloud polling, or membership business logic. A Cloudflare Worker computes the authorized fob IDs and POSTs the complete list here. Edge durably spools swipes for the Worker to ingest and optionally signs controller responses with the existing Conway key.

## Run

Requires Go 1.25+ to build and `ffmpeg` on PATH for cameras. Run these commands from this directory:

```sh
go build .
export CONWAYEDGE_TOKEN='a-long-random-worker-secret'
export CONWAYEDGE_ADMIN_PASSWORD='a-different-long-random-admin-secret'
./conwayedge -lan 192.168.1.10:8080 -tunnel 127.0.0.1:8081 -data ./data
```

Both secrets are required. Defaults are `-lan :8080`, `-tunnel 127.0.0.1:8081`, and `-data data`. Run one instance per data directory, as an unprivileged user. The directory must be on a local filesystem supporting atomic rename and file/directory sync. New data directories use mode 0700; files use 0600. Protect an existing directory equivalently.

Point cloudflared **only** at `http://127.0.0.1:8081`. Every tunnel request requires `Authorization: Bearer <CONWAYEDGE_TOKEN>`. The Worker must authenticate/authorize its own callers before proxying printer endpoints, and must not expose the token to browsers. Configure the tunnel ingress hostname with a catch-all rejection rule. No Cloudflare API credentials are needed here.

Point controllers at the machine's LAN IPv4 address and port 8080. Firewall that port to trusted controllers/admins; controllers do not authenticate. Never expose the LAN listener through the tunnel or public Internet. Configuration uses Basic authentication over plain HTTP, so use a trusted management network or an SSH port forward for administration.

**Signing:** set `CONWAYEDGE_SIGNING_SEED=/secure/path/fob-signing.ed25519` to retain existing controller key pins. This is the legacy engine's **exactly 32 raw binary bytes**, not PEM, hex, base64, a 64-byte private key, or a newline-terminated file. Use the existing seed and protect it with mode 0600. A configured missing, unreadable, or wrong-sized file prevents startup; edge never generates or replaces a key. The key is loaded once at startup. With the variable unset, responses remain unsigned, so controllers with pinned keys must have those pins cleared through their physical-confirmation procedure. Edge does not manage controller keys.

## API

| Listener | Endpoint | Contract |
| --- | --- | --- |
| Tunnel | `POST /api/goal` | Full JSON array of fob IDs; 204 after persistence. |
| Tunnel | `POST /api/goal/versioned` | `{ "version": 123, "fobs": [7,42] }`; durable ordering, 204 accepted/idempotent, 409 older/conflicting. |
| Tunnel | `GET /api/swipes?limit=100` | `{ "events": [{ "id": "...", "time": "2026-09-09T12:00:00Z", "controller": "192.168.1.20", "fob": 7, "allowed": true }] }`; oldest pending events, no removal. |
| Tunnel | `POST /api/swipes/ack` | `{ "ids": ["..."] }`; 204 after durable removal; unknown/already-acked/duplicate IDs are harmless. |
| Tunnel | `GET /api/printers` | JSON array of current printer status. |
| Tunnel | `GET /machines/stream/{serial}` | Shared MJPEG camera stream, boundary `frame`; 404 unknown printer, 502 startup failure. |
| LAN | `POST /api/fobs` | Controller swipe array in; authorized ID array or 304 out. |
| LAN | `GET /config`, `POST /config` | Printer configuration page; Basic username `admin`, password from environment. |

Example versioned Worker push:

```js
const response = await fetch(env.EDGE_URL + "/api/goal/versioned", {
  method: "POST",
  headers: {
    Authorization: "Bearer " + env.EDGE_TOKEN,
    "Content-Type": "application/json",
  },
  body: JSON.stringify({ version, fobs: authorizedFobIDs }),
});
if (!response.ok) throw new Error(`Edge update failed: ${response.status}`);
```

Send at most 512 nonzero unsigned 32-bit IDs (16 KiB request limit on both goal endpoints). IDs are sorted and deduplicated; version equality compares this canonical set, not array order or duplicates. An empty fob array explicitly revokes all remote access; missing/null/malformed bodies are rejected. Versions are integers from 0 through 9007199254740991 (JavaScript's largest safe integer). A newer version replaces the complete goal; an equal version with equal contents is an idempotent 204; older versions and equal versions with different contents return 409, including after restart. New object APIs reject unknown fields and trailing JSON values.

The Worker coordinator must allocate `version` durably and monotonically and read the current D1 authorized set immediately before pushing. Serialize pushes/reconciliation and never replay stale snapshots. There is no freshness expiry: cloud outages retain the last good goal indefinitely. Cloud automation should audit successful pushes, alert on prolonged lack of successful reconciliation, 409s, and 5xx responses, and monitor swipe backlog age. Edge does not decide membership freshness or expire access itself.

**Legacy compatibility:** `POST /api/goal` still accepts an array and unconditionally replaces the active list with last-arrival-wins semantics, even after versioned pushes. It does not erase or advance the version watermark. An equal-version replay is a no-op and does not undo a later legacy push; the versioned producer must send a higher version to replace it. Do not run competing legacy and versioned producers in steady state. This behavior keeps shipped legacy consumers usable for migration/rollback without silently weakening ordering between versioned requests.

`goal.json` atomically stores the active goal and last versioned snapshot together, so a crash cannot separate contents from the ordering watermark. Existing array caches load unchanged and stay arrays until first versioned use; afterward the file contains `{version,fobs,goal}`. Back up the data directory before migration: older edge binaries cannot read the new object format. Do not manually discard the watermark when rolling back the cloud producer; use the legacy HTTP endpoint instead. A corrupt goal or spool prevents startup.

State replacement writes a same-directory temporary file, syncs it, renames it, then syncs the directory before publishing memory and acknowledging. Concurrent goal, spool, and ack operations are serialized. Pre-rename failures leave live state unchanged and can be retried. A post-rename failure may leave disk ahead of memory: edge returns an error and blocks further goal/spool mutations and spool reads until storage is repaired and the process restarted. Restart validates state and syncs the directory before accepting traffic. Empty controller polls still serve the in-memory last-good goal during that fault. Before the first goal, controller requests return 503 so controllers retain their own cache.

Controllers POST `[{"fob":123,"allowed":true}]` or `[]` (16 KiB, at most 512 swipes). Responses remain sorted JSON arrays with a trailing newline, explicit `Content-Length` (no chunked encoding), and the existing unquoted SHA-256 ETag (hash of each decimal ID followed by a comma). An exact `If-None-Match` returns 304 **after durably storing swipes**. When signing is enabled, `X-Fob-Signature` is standard padded base64 of the Ed25519 signature over the exact response body, including its newline. There is no timestamp, nonce, envelope, prehash, or signature on 304/error responses, matching `engine/ed25519_signer.go` and `modules/fobapi/module.go`. Local controller credentials remain independent of this list.

## Swipe Delivery

Swipes still append to `swipes.jsonl` as one JSON object per event with `time` (UTC RFC3339), `controller` (peer IP, never a forwarding header), `fob`, and `allowed`. The local log format is unchanged. Every accepted controller batch is also stored in `swipe-spool.json`, with an edge-assigned random ID per event. Both stores are synced before 200/304. A log or spool write failure returns an error, not an acknowledgment. No events are acknowledged until a goal exists. Local logs and spool are separate: a failed/unacknowledged batch may appear only in the log; the controller must retry it.

`GET /api/swipes` returns pending events in ingestion order without consuming them. `limit` defaults to 100 and must be 1 through 100. The Worker must commit ingestion and deduplication by event ID before acknowledging those IDs. Ack accepts at most 100 nonempty IDs (128 bytes each, 16 KiB body limit), including an empty array for a no-op. Only named events are removed, not a prefix or high-water mark; repeated, overlapping, and out-of-order acknowledgments are safe. Ack never removes local log records. Lost GET/ack responses can be retried; IDs survive restarts. Controller retries can produce new IDs for the same physical swipe because the legacy controller request has no event identifier. Delivery is at least once, not exactly once.

The pending spool is bounded to 10,000 events and rewritten atomically per batch/ack. A batch exceeding available capacity is rejected in full with 503 without eviction; empty polls still work and cloud acks free capacity. Monitor backlog age, HTTP failures, and disk space to avoid filling controller-side retry buffers. Old local logs are not automatically backfilled into the spool. There is no automatic log rotation: archive/rotate `swipes.jsonl` externally by renaming it (each request reopens it). An interrupted final log line is truncated before the next append. Do not rotate/delete the live spool or goal files.

Enrollment stays in the cloud: Worker kiosk claims use the configured trusted kiosk IP gate. No edge enrollment API is provided.

## Printers

Open `http://<LAN-address>:8080/config`, authenticate as `admin`, and edit the JSON list:

```json
[
  {
    "name": "Workshop X1C",
    "host": "192.168.1.50",
    "access_code": "12345678",
    "serial_number": "YOUR_PRINTER_SERIAL"
  }
]
```

The page includes printer passwords; do not share it. Up to 32 printers are supported, with literal IP addresses and unique serials. Saving uses a CSRF-protected form and an atomic replacement of `printers.json`. Changed/removed printers have their MQTT connections and cameras stopped; unchanged printers stay connected. Goal POSTs never modify printer configuration.

Enable Bambu LAN access. MQTT uses TLS on port 8883 with `bblp` and the access code; certificate verification is disabled for Bambu's self-signed certificates, so the printer network must be trusted. Status is requested every five seconds and partial reports are merged. JSON fields are `serial_number`, `name`, `gcode_file`, `subtask_name`, `gcode_state`, `print_error_code` (printer-supplied string/number or null), `remaining_print_time` (minutes), `print_percent_done`, `updated_at` (Unix seconds, initially 0), and `error`. Disconnected/stale status is retained with an error; consumers should also check `updated_at`. Credentials and IP addresses are not returned.

Cameras require the RTSPS endpoint on port 322, `/streaming/live/1`, used by the existing Conway Bambu integration. Models with a different camera protocol are not supported. FFmpeg transcodes to 15 fps MJPEG, with one process per viewed printer. Slow viewers skip complete frames; the last viewer leaving stops the process. Startup waits up to 15 seconds, writes have five-second deadlines, frames are capped at 8 MiB, and 20 seconds without a complete upstream frame terminates a stalled camera. Camera credentials are hidden from HTTP errors/logs but are present in FFmpeg process arguments; restrict local process visibility accordingly. No printer-control endpoints or dashboard are included.

## Verify

```sh
go test -race ./...
go vet ./...
go build .
```

Tests cover goal ordering/restarts/legacy interoperation, spool replay/ack/concurrent ingestion, request bounds and listener/auth isolation, pre- and post-rename failures, corrupt-state startup, and exact legacy signing. Existing printer tests use simulated camera child processes; no FFmpeg, printers, Cloudflare, or root Conway module is required. Live printer, controller, and tunnel behavior must still be checked on the target network. This nested module is intentionally not built/tested by Conway's root Go commands.
