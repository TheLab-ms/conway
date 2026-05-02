# fobapi

LAN-only HTTP API consumed by ESP32 access controllers that gate building doors. Controllers poll for the current authorized fob list and report swipe events back for audit logging.

## Endpoints

- `POST /api/fobs` — controller poll. Restricted to LAN via `auth.OnlyLAN` (internet requests get 403).
- `POST /admin/doors/{id}` — leader-only admin form submit to assign a human-readable door name to a tracked controller.

## Poll request/response

Request body: JSON array of swipe events (may be empty), e.g. `[{"fob": 12345678, "allowed": true}]`.

Response: JSON array of currently authorized fob IDs (sourced from the `active_keyfobs` view), e.g. `[12345678, 23456789]`.

## Behavioral notes

- **ETag caching.** Response carries an `ETag` computed as `sha256` of the comma-joined fob IDs in sort order. Clients sending a matching `If-None-Match` get `304` with no body and no `ETag` header.
- **Client tracking.** Every poll upserts a row in `fob_clients` keyed by `RemoteAddr` IP (port stripped). `last_seen` is rate-limited to update at most once per 30 seconds via a conditional `ON CONFLICT DO UPDATE ... WHERE last_seen < now - 30`.
- **Swipe ingestion.** Each posted event is inserted into `fob_swipes` with a fresh UUID, the server's current time (the client-provided timestamp is ignored), the resolved member ID via subquery on `members.fob_id`, and the originating `fob_client.id`. Duplicate inserts are suppressed by `ON CONFLICT DO NOTHING` (relies on the `fob_swipes` unique index defined elsewhere).
- **Member resolution.** If no member matches the `fob_id`, the swipe is still recorded with `member = 0` / NULL.
- **Schema migration.** `New` creates `fob_clients` and best-effort adds a `fob_client` FK column to the pre-existing `fob_swipes` table; the `ALTER TABLE` error is intentionally ignored so the call is idempotent.
- **Config page.** Registers a read-only entry in the admin config UI listing all known controllers (IP, assigned door name, last-seen). The door-name form posts to the admin endpoint above.

## Reference client

An ESP32 implementation lives in `access-controller/` at the repo root.
