# discord

Discord integration for Conway: OAuth2 login/account linking, asynchronous guild
role reconciliation, and configurable webhook notifications driven by SQL
triggers.

## Functionality

- **Account linking** (`GET /discord/login`, `/discord/callback`): authenticated
  users link their Discord account; stores `discord_user_id`, `discord_email`,
  `discord_avatar` on the member row.
- **Discord login** (`GET /login/discord`, `/login/discord/callback`):
  unauthenticated sign-in. Resolves the member by `discord_user_id` first, then
  by email. If no account exists, delegates to `SignupConfirmFunc` (set via
  `SetSignupConfirm`); falls back to direct member creation if unset.
  Login completion is delegated to `LoginCompleteFunc` (set via
  `SetLoginCompleter`). Both must be wired before routes are attached for the
  respective flows to work. Discord accounts without a verified email are
  rejected.
- **Role sync**: a paid `payment_status` maps to membership of the configured
  `RoleID` in the configured guild. Reconciliation is driven by the workqueue
  pattern in `engine` and rate-limited to `maxRPS = 3` API requests/second.
- **Webhooks**: `discord_webhooks` rows define URL, message template
  (`{placeholder}` syntax), username, and a SQL trigger (`trigger_table` +
  `trigger_op` + optional `when_clause`). Trigger creation/dispatch lives
  outside this file set; this module owns the schema and migrations.

## Behavioral details

- Configuration is loaded dynamically per request via `config.Loader[Config]`
  (`SetConfigLoader` must be called at startup). Workers always run and no-op
  when bot/OAuth config is incomplete.
- `scheduleFullReconciliation` runs every minute, marking up to 10 members
  whose `discord_last_synced` is older than `SyncIntervalHours` (default 24,
  clamped 1-168) as needing sync by setting `discord_last_synced = NULL`.
- `GetItem` atomically claims a member needing sync via `UPDATE ... RETURNING`,
  setting `discord_last_synced = unixepoch()` to prevent double-claiming.
- On sync **success**, `discord_username` and `discord_avatar` are updated.
  On **failure**, `discord_last_synced` is pushed forward with exponential
  backoff (starting at 300s, capped at 86400s).
- Display name resolution priority: guild `nick` > `global_name` > `username`.
  Avatar priority: guild-specific avatar > user avatar. Avatars are fetched as
  raw PNG bytes and stored on the member row.
- OAuth state is a JWT signed by `engine.TokenIssuer`. Account-linking state
  expires in 1 minute and binds to the user ID; login state expires in 5
  minutes and carries the post-login `callback_uri` in the `Issuer` claim.
- Login flow stores Discord email lowercased and clears `discord_last_synced`
  to trigger an async avatar/role refresh.
- Schema migrations in `module.go` use `ALTER TABLE ADD COLUMN` and ignore
  errors (idempotent on existing DBs). `triggers.go` migrates legacy
  `trigger_event`-based webhooks to the new SQL-trigger model and folds the
  removed `discord_webhook_conditions` table into `discord_webhooks.when_clause`
  before dropping it. Old metrics samplings and the legacy
  `members_signup_notification` / `discord_webhook_on_member_event` triggers
  are dropped on startup.
- All Discord API errors and OAuth callbacks are recorded via
  `engine.EventLogger`.
