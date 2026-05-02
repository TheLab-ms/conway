# payment

Stripe integration for membership subscriptions and one-time donations.

## Routes

- `POST /webhooks/stripe` — receives `customer.subscription.{created,updated,deleted}` events. All other event types return 204 without processing.
- `GET /payment/checkout` (authn) — redirects active members to the Stripe billing portal; redirects everyone else into a new subscription Checkout Session.
- `GET /donations/checkout?price_id=...` (authn) — creates a one-time payment Checkout Session for a configured donation item.

## Configuration

Stored in the `stripe_config` table (versioned, append-only; the latest row wins). Managed through the generic `engine/config` UI under the "stripe" module. Fields:

- `api_key` — Stripe secret key. Set as the global `stripe.Key` on each request that needs the API.
- `webhook_key` — webhook signing secret used by `webhook.ConstructEvent`.
- `donation_items_json` — JSON array of `{name, price_id}` entries. The `price_id` allow-list gates `/donations/checkout`.

The `donation_items_json` column is added to existing DBs via an idempotent `ALTER TABLE` whose error is intentionally ignored.

## Subscription state reconciliation

`applySubscriptionUpdate` (module.go:193) is the only writer of `stripe_subscription_*` columns from webhooks. It guards against out-of-order Stripe events using a single UPDATE with a WHERE clause that accepts the event only if:

1. The event's subscription ID matches the member's currently-tracked one, OR
2. The member has no subscription on file, OR
3. The event status is `active` or `trialing` (a new subscription legitimately taking over).

Any other event (notably a non-active status for a subscription ID that doesn't match what's tracked) is dropped as stale. This prevents a delayed `customer.subscription.deleted` for a replaced subscription from clobbering a newer active one and deactivating the member. See `webhook_test.go` for the full matrix and the regression case.

Member lookup is by lowercased email. Unknown emails are a silent no-op.

## Checkout behavior

- Subscription price is selected by `lookup_key` = `monthly` or `yearly`, controlled by the member's `bill_annually` flag.
- Discounts: iterates all Stripe coupons and applies the first valid one whose `metadata.discountTypes` (comma-separated, case-insensitive) contains the member's `discount_type`.
- Existing `stripe_customer_id` is reused on Checkout/Portal sessions to avoid duplicate Stripe customer objects. Donation flow lazily creates a customer if none exists and persists the new ID.
- Donation Checkout sets `payment_intent_data.setup_future_usage=on_session` so the payment method is saved on the customer for prefill on subsequent sessions.

## Logging

All meaningful outcomes (webhook receipt, ignored stale events, API errors, checkout/portal/donation creation) go through `engine.EventLogger` with the member ID resolved from the customer email when available.
