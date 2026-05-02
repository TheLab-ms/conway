# kiosk

In-space kiosk for binding RFID key fobs to member accounts.

## Routes

- `GET /kiosk` - Kiosk UI. Listens for keypress input (HID-style fob reader); buffered keys are submitted as `fobid` after Enter or 1s of inactivity. With a `fobid`, renders a QR code containing a signed JWT linking to `/keyfob/bind`.
- `GET /keyfob/bind` - Authenticated. Verifies the JWT from the QR and sets `members.fob_id` for the current user.
- `GET /keyfob/status/{id}` - Returns JSON `true`/`false` indicating whether any member already has that fob ID. Polled by the kiosk page to auto-redirect once the bind completes.

## Behavioral notes

- `/kiosk` and `/keyfob/status/{id}` are gated by `atPhysicalSpace`: the request's IP (preferring the `CF-Connecting-IP` header, falling back to `RemoteAddr`) must equal the resolved IP of `trustedHostname`. The trusted IP is refreshed every minute via a UDP dial (no packet sent; just used to discover the outbound route's resolved address).
- `/keyfob/bind` is **not** IP-gated - the QR is scanned on the member's phone, which is typically on cellular. Trust comes from the short-lived JWT (5 minute TTL, see `qrTTL`) signed by `engine.TokenIssuer`.
- The bind UPDATE is a no-op when the fob is already assigned to that member (`fob_id != $1` guard). It does not prevent reassigning a fob already bound to a different member; the kiosk JS uses `/keyfob/status/{id}` only to detect completion, not to block conflicts.
- The kiosk page auto-redirects back to `/kiosk` 5 minutes after a QR is shown, and full-reloads hourly to pick up template changes.
- `kiosk_templ.go` is generated from `kiosk.templ` via `go generate` (templ).
