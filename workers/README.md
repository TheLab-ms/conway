# Conway Membership Core

Cloudflare Workers TypeScript backend with D1, Queues, and SQLite-backed Durable
Objects. The frontend is static native JavaScript custom elements: no framework,
asset build, external fonts, avatar requests, image uploads, or member images.
The only generated graphic is a locally rendered enrollment QR code.

## Scope

- Discord-only signup/login, opaque sessions, CSRF protection, and leadership-assisted identity linking.
- Billing home with membership and fob access status, versioned waivers, and member profiles.
- Stripe subscriptions, billing portal, configured donations, discounts and family relationships.
- Kiosk fob enrollment, ordered edge access snapshots, deduplicated swipe history.
- Leadership member administration, audit, CSV export, settings, waiver publishing and failed-job retry.
- Discord roles, signup/discount notifications, signed leadership approvals and denied-access messages.

Legacy PayPal entitlements are retained as data, not a new PayPal integration.
Email login/delivery, Google login, OAuth-provider services, member images, SQL
console/automation, printers UI, signs, elections, phone inbox and general metrics
are intentionally excluded. The Go application remains separate; neither it nor
the access-controller firmware is replaced by this directory.

## Local Development

Requires Node **22.22+ or 24**, npm, and Python **3.10+** with SQLite **3.37+** for
offline migration/auth tests. Browser fixtures use `node:sqlite` and `globSync`;
install Chromium with `npx playwright install chromium` after `npm ci`, or set
`CHROME=/path/to/chromium`.
Run commands in `workers/`:

```sh
npm ci
npm run db:local
npm run dev
```

Populate ignored `.dev.vars` using the keys in `.dev.vars.example`, using only
development credentials. Register `http://localhost:8787/login/discord/callback`
as an exact redirect in a development Discord application. Open
`http://localhost:8787`; there is deliberately no fake-login or development
authentication bypass. `AUTOMATION_ENABLED=false` prevents background external
delivery, **not** interactive login, checkout, webhook ingestion or approvals.
Use test Stripe keys and an isolated Discord guild, never production credentials.

For a fresh development database, sign up with Discord first. Verify the numeric
Discord ID independently, then promote only that exact account via local D1:

```sh
npx wrangler d1 execute conway --local --command "UPDATE members SET leadership=1 WHERE discord_user_id='VERIFIED_DISCORD_ID';"
```

Replace the placeholder; confirm exactly one intended account was updated.
This is an operator bootstrap, not a web API. Existing production installs must
instead use the migration runbook, which requires verified linked leadership.
Never claim an existing account from an email address or a Discord display name.

## Configuration

`wrangler.jsonc` defines the application bindings and safe inactive defaults.
Secrets belong in Wrangler secrets, not D1 settings, frontend files or git.

| Name                                         | Purpose                                                                                             |
| -------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| `DB`                                         | Dedicated D1 database; replace the database ID placeholder                                          |
| `COORDINATOR`                                | `MembershipCoordinator` Durable Object binding; preserve its storage across upgrades                |
| `JOBS`                                       | `conway-jobs` Queue, producer and consumer                                                          |
| `SITE_URL`                                   | Exact canonical origin, no trailing slash; used for OAuth, CSRF and return links                    |
| `AUTOMATION_ENABLED`                         | String `true` enables background provider delivery and edge sync; default `false`                   |
| `KIOSK_IPS`                                  | Comma-separated exact public makerspace egress IPs, including IPv6 if used; empty denies enrollment |
| `EDGE_URL`                                   | HTTPS tunnel origin for the edge; empty disables edge synchronization                               |
| `DISCORD_CLIENT_ID`, `DISCORD_CLIENT_SECRET` | Discord OAuth application credentials                                                               |
| `DISCORD_PUBLIC_KEY`                         | Hex application key for interaction signature verification                                          |
| `DISCORD_BOT_TOKEN`                          | Bot credential for roles and messages                                                               |
| `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET` | Matching account/mode API and endpoint signing secrets                                              |
| `EDGE_TOKEN`                                 | Bearer token matching the edge tunnel configuration                                                 |

Leadership settings contain only nonsecrets: site/referral labels, membership
price IDs, donation allowlist, discount-to-coupon mappings, Discord guild/role/
channel IDs, notification switches and templates. Settings and member edits use
optimistic revisions; a 409 requires reviewing the latest state, not blindly
replaying a stale form. Publish waiver text and required agreements before signup
onboarding is opened. Existing waiver evidence remains valid after publication.

Template placeholders are `{name}`, `{site_name}`, `{site_url}`, `{member_id}`,
`{discount}`, `{discount_type}`, `{request_id}`, `{reason}`, and `{access_status}`.
They are simple text substitutions, not Go templates, Markdown rendering or SQL.
Discord messages disable automatic mentions. Denied-access DMs are throttled for
one hour; all swipes remain in the access audit regardless of notification delivery.

## Provider Setup

**Discord:** register the exact production redirect
`https://YOUR_HOST/login/discord/callback` and interaction endpoint
`https://YOUR_HOST/discord/interactions`. Login requests `identify email`; a
verified contact email is required, but only the immutable Discord ID resolves
an account. Invite the bot to the configured guild with Manage Roles and the
necessary channel View/Send permissions. Its highest role must be above the
membership role. No avatar or image access is used. Approvals require a currently
linked Conway leader in the configured guild and leadership channel. Test role
addition/removal, approval, and DMs with actual test guild accounts; members may
disable DMs in Discord.

**Stripe:** configure explicit active monthly/yearly recurring prices and
allowlisted one-time donation prices in the correct account/mode. Configure each
discount's coupon explicitly; an approved discount without a valid coupon fails
closed instead of silently charging full price. Enable the customer Billing
Portal, including cancellation. Register `https://YOUR_HOST/webhooks/stripe` with
event payload API version `2024-06-20`, matching the REST client, for:

- `customer.subscription.created`, `customer.subscription.updated`, `customer.subscription.deleted`
- `checkout.session.completed`, `checkout.session.async_payment_succeeded`, `checkout.session.async_payment_failed`
- `invoice.paid`, `invoice.payment_failed`

Existing customer/subscription IDs remain the ownership mapping; email does not
select a billing account. Event receipts and jobs are durable and deduplicated.
Reconciliation retrieves current subscription state, rather than trusting event
ordering. Periodic reconciliation rotates through five customers per minute;
webhooks provide the normal fast path. Validate test/live mode, customer mapping,
old subscription replacement, cancellation and asynchronous donations before
cutover. Retain member records with any Stripe subscription history, even after
cancellation; physical deletion deliberately refuses to orphan billing history.

## Edge And Kiosk

Deploy the updated [`conwayedge`](../conwayedge/README.md) before enabling sync.
The Worker sends `POST /api/goal/versioned` and ingests `GET /api/swipes` before
acknowledging durable IDs with `POST /api/swipes/ack`. Keep its tunnel private
except for bearer-authenticated Worker calls; never put its token in the browser.
Do not run competing legacy goal writers once the Worker takes over.

If controllers pin signatures, install the existing **32-byte raw** legacy
`fob-signing.ed25519` seed at the edge's `CONWAYEDGE_SIGNING_SEED` path. Do not
regenerate it or modify firmware. Preserve its permissions and backup policy.
The first versioned snapshot changes the on-disk goal format; old edge binaries
cannot read it. Read the edge rollback and storage-failure guidance first.

`/kiosk` accepts a keyboard-wedge reader and creates a five-minute, one-use QR
claim. The phone authenticates and explicitly binds the fob at `/fobs/bind`.
Only claim creation/status require the trusted public egress IP; phones can use
cellular data. Trust `CF-Connecting-IP` only at Cloudflare's real ingress, not a
public origin/proxy that accepts caller-supplied headers. Local tests must supply
a test IP explicitly. There is no firmware or edge enrollment API change.

Access snapshots normally update each minute, but this is not a hard revocation
deadline. During outages, controllers retain the edge's last-good access set
indefinitely. Monitor freshness and establish an onsite response for urgent
revocations. The 10,000-event edge spool applies backpressure when full; central
deduplication covers replay of edge IDs, not every repeated physical swipe from
firmware that does not provide a stable event ID.

## Deployment

No command below has been run against a production account. Provision a dedicated
staging environment first. Commands below create remote resources:

```sh
npx wrangler login
npx wrangler d1 create conway
npx wrangler queues create conway-jobs
```

Put the resulting D1 ID and canonical vars into the reviewed deployment config.
Use separate bindings, queue names, credentials and domains for staging and
production. Provision each configured secret interactively, for example:

```sh
npx wrangler secret put DISCORD_CLIENT_ID
npx wrangler secret put DISCORD_CLIENT_SECRET
npx wrangler secret put DISCORD_PUBLIC_KEY
npx wrangler secret put DISCORD_BOT_TOKEN
npx wrangler secret put STRIPE_SECRET_KEY
npx wrangler secret put STRIPE_WEBHOOK_SECRET
npx wrangler secret put EDGE_TOKEN
```

For a **new, empty installation** only, apply the schema before deployment:

```sh
npx wrangler d1 migrations apply conway --remote
npx wrangler deploy --dry-run
npm run deploy
```

For existing Conway data, follow [MIGRATION.md](MIGRATION.md) instead, including
isolated target, reviewed snapshot/automation inventory, verified Discord owners,
Stripe reconciliation, and exact authorized-fob parity. An automation flag is not
a maintenance boundary: restrict ingress and stop every legacy writer during
cutover. There is no automatic production deploy workflow. The root repository's
legacy workflow still deploys Go; disable that deployment through the reviewed
cutover procedure before making Workers authoritative.

Configure the canonical custom domain, test deep links and authenticated routes,
then enable background automation only after provider/edge validation. Preserve
Durable Object data: it includes Stripe idempotency/revocation state, delivery
receipts and access-snapshot versions. Restoring D1 alone is not a consistent
application rollback. Never run both backends as billing/access writers.

## Operations

Use leadership Audit, Swipes and Jobs views plus Cloudflare observability and
edge logs. Alert on dead jobs, queue backlog, repeated provider failures, missing
edge sync, stale goals and spool growth. `wrangler tail` can assist diagnosis;
restrict logs and database exports as membership/billing personal data.

Jobs lease for 180 seconds, back off with jitter, and become `dead` after at most
10 attempts or a permanent failure. Correct the provider/config problem before
Retry. Queue transport retries and application job attempts are separate; cron
republishes due D1 jobs. Do not delete dedupe tombstones or DO receipts as a retry
mechanism. Discord delivery has a residual send-before-receipt crash window;
stable nonces and durable receipts reduce but do not promise exactly-once sends.

Ambiguous Stripe writes fail closed after 23 hours rather than reuse an expired
idempotency guarantee. Investigate the exact customer, open checkout sessions,
subscriptions and coordinator operation before operator recovery; never clear
coordinator storage blindly. Discount changes expire open sessions, but completed
payments are reconciled, not automatically refunded or canceled. Portal access
remains available for existing subscriptions when a family primary is inactive.

Keep at least two verified linked leaders. Leadership linking is an account
recovery operation: verify the person and numeric Discord ID independently, review
the audit, and remember that changes invalidate all of that member's sessions.
Non-billable door-access exceptions and the distinction between payment-active
and access-Ready follow legacy policy; do not assume they are interchangeable.

## Verification

```sh
npm run typecheck
npm test
npm run test:integrations
node --test src/auth.test.mjs
npm run test:browser
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/rehearse_local_d1.py --node "$(command -v node)"
npx wrangler deploy --dry-run
```

The synthetic local D1 rehearsal uses Linux `/dev/shm` for isolated temporary
state. Run edge checks separately in `conwayedge/`: `go test -race ./...` and
`go vet ./...`. The scoped CI workflow runs these checks without deployment
credentials. See [test/README.md](test/README.md) and
[browser-tests/README.md](browser-tests/README.md) for harness boundaries.
Browser tests are isolated in `browser-tests/` and use Playwright with real local
Wrangler APIs, assets, D1 and Durable Objects. `npm run test:browser` automatically
starts a fresh server at `http://127.0.0.1:8799`, with no server reuse, one
Playwright worker and no retries. Provider calls are stubbed in the test Worker;
OAuth consent redirects and hosted Stripe pages are intercepted. Fixtures use
real hashed sessions and reset D1 in isolated `.state/`; no production secrets
or development `.dev.vars` are needed or loaded. Backend tests likewise use real
workerd/D1/DO bindings with mocked providers. Neither proves live Discord OAuth,
Stripe settlement, remote D1 capacity, production migration, or physical hardware.
