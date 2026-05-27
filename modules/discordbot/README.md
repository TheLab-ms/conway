# discordbot

Posts a Discord message announcing each new Conway member and lets anyone in the channel assign a discount type by picking from a string-select component. The picker locks itself after the first successful assignment.

## Functionality

- AFTER INSERT trigger on `members` enqueues new member IDs into `discordbot_signup_queue`.
- A 15s polling worker drains the queue, builds a rich Discord payload (embed + string-select listing every `memberdb.DiscountTypes` value), and forwards it via the `discordwebhook` module's `MessageQueuer` for rate-limited delivery.
- `POST /discord/interactions` receives Discord's signed callbacks. The handler verifies the Ed25519 signature, updates `members.discount_type` (mapping the `_none` sentinel back to NULL), logs a `DiscountSetViaDiscord` audit event, and replies with `UPDATE_MESSAGE` containing an empty `components` array — which removes the picker so the discount cannot be changed a second time.
- No Conway authentication is required: the route is unauthenticated and identity is established purely by Discord's request signature against the configured `ApplicationPublicKey`.

## Setup

1. Create a Discord application at <https://discord.com/developers/applications>.
2. Copy the application's **Public Key** (hex) into the Conway admin UI under **Integrations → discordbot → Application Public Key**.
3. In the target Discord channel, create a webhook and copy its URL into **Signup Channel Webhook URL** (stored as a secret).
4. In the Discord application's **General Information** page, set **Interactions Endpoint URL** to `https://<your-conway-host>/discord/interactions`. Discord will immediately probe the endpoint with a signed PING; saving succeeds only if signature verification passes.
5. Toggle **Enabled** on.

## Behavioral details

- The Discord application's bot account does not need to be invited to the server; webhook delivery is what posts the message, and interaction callbacks are routed by Discord's infrastructure based on the application's configured endpoint URL.
- Inbound interactions must be acknowledged within 3 seconds, so the entire happy path (signature verify, DB lookup, UPDATE, response build) runs inline on the request goroutine.
- The `_none` option value is a sentinel: Discord rejects empty option values, so the `None` discount is sent as `_none` and the interaction handler maps it back to the empty string (which the SQL `CASE` then stores as NULL — matching the convention used by the admin discount form).
- When the bot is disabled or unconfigured at the time a queued signup is processed, the queue row is dropped rather than retained. This avoids a backlog accumulating before configuration arrives; historical signups will not retroactively notify when the bot is later enabled.
- Permanent webhook-delivery failures are logged as `SignupNotifyError` events; the queue row is then deleted to prevent unbounded retry.
- All discount labels and values in the picker are sourced from `modules/members/memberdb.DiscountTypes` — adding a new discount tier there automatically makes it selectable from Discord.
- The custom_id format is `conway:set_discount:<memberID>`, embedding the target member ID directly so the handler is stateless.
- `member_id` in `discordbot_signup_queue` references `members(id) ON DELETE CASCADE`, so deleting a member before the notification is delivered cleanly removes the pending notification.

## Security

- Signature verification is `ed25519.Verify(publicKey, timestamp || body, signature)` using the stdlib. Bodies are capped at 64 KiB before verification.
- `Application Public Key` is validated on save: must be a 64-character hex string decoding to exactly 32 bytes.
- Any request that fails signature verification — including missing/empty headers, malformed hex, or a mismatched signature — receives a 401 and logs a `Warn`. Discord will then mark the endpoint as failing and the app cannot be saved until the key is correct.
