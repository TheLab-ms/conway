# members

Owns the core `members` and `waivers` schemas and serves the authenticated member dashboard at `GET /`.

## Responsibilities

- Defines and migrates the canonical `members`, `waivers`, `fob_swipes`, and `member_events` tables (see `schema.sql`).
- Renders the member-facing dashboard (`member.templ`): onboarding checklist (waiver, payment, key fob, Discord), status banner, and an optional one-time donation card.
- Runs hourly cleanup workers that prune `fob_swipes` and `member_events` rows older than `defaultTTL` (2 years).

## Behavioral details

- `access_status` is a generated column with strict precedence: `UnconfirmedEmail` > `MissingWaiver` > `PaymentInactive` > `MissingKeyFob` > `FamilyInactive` > `Ready`. `non_billable` members bypass the email-confirmation, waiver, and payment gates but still require a fob.
- `payment_status` is generated and returns `NULL` for unconfirmed members regardless of subscription state. Order of preference: PayPal subscription, active/trialing Stripe, then `non_billable`.
- A trigger nulls `discount_type` whenever `payment_status` transitions from non-NULL to NULL (cancellation), so discounts cannot persist across lapses.
- Family plans: `root_family_member` cannot reference self (CHECK constraint). `root_family_member_active` is maintained by triggers on insert/update/delete of the root member; deleting a root nulls the link on dependents.
- Waiver linkage is bidirectional via triggers: inserting a waiver back-fills the matching member's `waiver` FK by email, and inserting a member resolves any pre-existing waiver for that email. `(email, version)` is unique on `waivers`.
- `fob_swipes` insertion updates the owning member's `fob_last_seen` to the max of the existing value and the new timestamp (out-of-order swipes won't regress it). `(fob_id, timestamp)` is unique.
- The `active_keyfobs` view exposes only fobs belonging to members with `access_status = 'Ready'`; this is what downstream access-control modules should query.
- Discord re-sync is triggered (by nulling `discord_last_synced`) whenever `discord_user_id` changes or any payment-affecting field changes on a Discord-linked member.
- The legacy hardcoded `member_events` triggers are explicitly dropped here; event emission is now owned by the `triggers` module, which must be initialized for events to be recorded.
- The donation card on the dashboard only renders when Stripe is configured, the member has a fob, and `donation_items_json` (from `stripe_config`) is non-empty.
- `NewTestDB(t)` opens an isolated test DB with this schema applied; tests that rely on `member_events` must also call `triggers.New(db)`.
