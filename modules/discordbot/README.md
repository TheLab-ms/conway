# discordbot

Notifies leadership when a member requests a membership discount and lets any authorized leader approve it from Discord with a single Approve button click. Leadership is **only** notified on a discount request — never on signup or on unrelated status changes — and there is intentionally no deny/cancel control (members remove their own requests; leadership can only approve).

## Functionality

- AFTER UPDATE OF `discount_status` trigger on `members` enqueues a member ID into `discordbot_discount_request_queue` whenever `discount_status` transitions into `'requested'`. Nothing is enqueued on signup, on unrelated updates, or on the `requested`→`approved` transition.
- A 15s polling worker drains the queue, builds a rich Discord payload (an embed describing the request plus a single **Approve** button), and forwards it via the `discordwebhook` module's `MessageQueuer` for rate-limited delivery.
- `POST /discord/interactions` receives Discord's signed callbacks. The handler verifies the Ed25519 signature, then atomically runs `UPDATE members SET discount_status='approved' WHERE id=? AND discount_status='requested'`, logs a `DiscountApprovedViaDiscord` audit event, and replies with `UPDATE_MESSAGE` (empty `components`) recording who approved and removing the button. If the request is no longer pending (the member withdrew it, or another leader already approved), it instead shows a "Discount request closed" message and changes nothing.
- No Conway authentication is required: the route is unauthenticated and identity is established purely by Discord's request signature against the configured `ApplicationPublicKey`.

## Discount lifecycle

`members.discount_status` is `NULL | 'requested' | 'approved'`:

- A member self-requests a discount on their dashboard → `discount_type` set and `discount_status='requested'`. This fires the notification.
- A discount is **usable** (coupon applies at Stripe checkout, and the member's "Set Up Payment" button reappears) when `discount_type IS NOT NULL AND discount_status IS NOT 'requested'`.
- Leadership approves either via the Discord **Approve** button or the admin member page → `discount_status='approved'`.
- Admins who set a discount directly from the admin page leave it **status-less** (`discount_status = NULL`), which counts as usable immediately.
- Members may remove their discount at any time, pending or approved (`discount_type` and `discount_status` both cleared).

## Setup

1. Create a Discord application at <https://discord.com/developers/applications>.
2. Copy the application's **Public Key** (hex) into the Conway admin UI under **Integrations → discordbot → Application Public Key**.
3. In the leadership channel, create a webhook and copy its URL into **Leadership Channel Webhook URL** (stored as a secret).
4. In the Discord application's **General Information** page, set **Interactions Endpoint URL** to `https://<your-conway-host>/discord/interactions`. Discord will immediately probe the endpoint with a signed PING; saving succeeds only if signature verification passes.
5. Toggle **Enabled** on.

## Behavioral details

- The Discord application's bot account does not need to be invited to the server; webhook delivery posts the message, and interaction callbacks are routed by Discord's infrastructure based on the application's configured endpoint URL.
- Inbound interactions must be acknowledged within 3 seconds, so the entire happy path (signature verify, DB lookup, UPDATE, response build) runs inline on the request goroutine.
- Approval is atomic via the `WHERE ... AND discount_status='requested'` clause, so two leaders clicking Approve at once cannot double-approve; the loser sees "Discount request closed".
- **Family** requests can be approved from Discord like any other tier, but the root-account linkage must still be completed in the admin panel; the request and approval messages call this out.
- When the bot is disabled or unconfigured at the time a queued request is processed, the queue row is dropped rather than retained, to avoid a backlog accumulating before configuration arrives.
- Permanent webhook-delivery failures are logged as `DiscountRequestNotifyError` events; the queue row is then deleted to prevent unbounded retry.
- The custom_id format is `conway:approve_discount:<memberID>`, embedding the target member ID directly so the handler is stateless.
- `member_id` in `discordbot_discount_request_queue` declares a foreign key to `members(id)`; Conway does not enable SQLite's `PRAGMA foreign_keys`, so the reference is declarative. `GetItem`'s `JOIN` against `members` filters out any orphaned row so it is not retried in a tight loop.

## Security

- Signature verification is `ed25519.Verify(publicKey, timestamp || body, signature)` using the stdlib. Bodies are capped at 64 KiB before verification.
- `Application Public Key` is validated on save: must be a 64-character hex string decoding to exactly 32 bytes.
- Any request that fails signature verification — including missing/empty headers, malformed hex, or a mismatched signature — receives a 401 and logs a `Warn`. Discord will then mark the endpoint as failing and the app cannot be saved until the key is correct.
