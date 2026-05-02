# email

Outbound email queue backed by SQLite. Other modules enqueue messages by inserting rows into `outbound_mail`; a background worker drains the queue via a pluggable `Sender`.

## Sender

`Sender` is `func(ctx, to, subj, msg) error`. Two implementations are provided:

- `NewGoogleSmtpSender(from, senderName)` — sends via `smtp.gmail.com:587` using Google application-default credentials with the `https://mail.google.com/` scope and domain-wide delegation (`Subject: from`). Messages are sent as `text/html; charset=UTF-8`. Internally rate-limited to 1 send per 5 seconds. Panics at construction if credentials cannot be loaded.
- Noop fallback (used when `New` is called with a nil sender) — prints the message to stdout.

## Worker behavior

`AttachWorkers` registers two pollers:

- Workqueue poller, ticked every 1s, rate-limited to `maxRPS = 1` send/sec at the module level (in addition to any limits inside the `Sender`).
- Hourly cleanup that deletes any `outbound_mail` row older than 1 hour (by `created`), regardless of send state.

## Queue semantics

- Selection: oldest `send_at <= now` where `created` is within the last hour. Messages older than 1 hour are never sent — they will be cleaned up.
- Success: row is deleted.
- Failure: `send_at` is pushed out using exponential backoff: `send_at = now + 2 * (send_at - created)`. Combined with the 1-hour `created` cutoff, this caps total retry duration to ~1 hour.
- Schema is auto-migrated on `New`.
