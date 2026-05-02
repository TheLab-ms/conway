# discordwebhook

Persistent, rate-limited delivery queue for Discord webhook messages.

## Functionality

- `QueueMessage(ctx, webhookURL, payload)` enqueues a raw JSON payload for delivery to a Discord webhook URL.
- `QueueTemplateMessage(ctx, webhookURL, tmpl, replacements)` substitutes `{key}` placeholders in `tmpl` from `replacements`, wraps the result as a Discord webhook JSON payload (`content` + `username`), and enqueues it.
- `RenderMessage` exposes the template rendering used by `QueueTemplateMessage`.
- `NewHTTPSender()` returns the production `Sender` (POSTs JSON to the webhook URL with a 10s timeout). Passing `nil` to `New` installs a noop sender that prints payloads to stdout (useful for dev).
- The `MessageQueuer` interface is what other modules should depend on.

## Behavioral details

- Backed by a SQLite table `discord_webhook_queue` created via migration on `New`. Also writes audit rows to a shared `discord_events` table (must exist; not created here).
- `AttachWorkers` registers two background workers:
  - Hourly cleanup deleting any queue rows older than 1 hour (`created` age > 3600s), regardless of delivery status.
  - 1Hz workqueue poller, rate-limited to 5 sends/sec globally (`maxRPS = 5`).
- Delivery picks the row with the earliest `send_at` that is due and not yet stale (created < 1 hour ago). Items older than 1 hour are never sent and will be reaped by cleanup, so undeliverable messages are silently dropped after ~1 hour.
- On send failure, `send_at` is rescheduled with exponential backoff: `send_at = now + 2 * (send_at - created)`. Note that for a fresh row (`send_at == created`) the first failure produces a delta of 0, so the next retry is effectively immediate; backoff only grows after `send_at` has drifted past `created`.
- On success, the row is deleted and a `WebhookSent` event is logged. On failure, a `WebhookError` event is logged with the error string.
- `RenderMessage` returns an error for empty rendered content. Unknown `{placeholder}` tokens are left in the output verbatim (no error).
- The bot username is hardcoded to `"Conway"` in `template.go`.
- HTTP sender treats any non-2xx response as an error (including Discord's 429 rate limits); the response body is included in the error and retried via the backoff path. There is no special handling of Discord's `Retry-After` header.
