# Browser Checks

Playwright runs Chromium against real local Wrangler APIs, static assets, D1,
and Durable Objects. All browser test code, fixtures, and configuration live in
`browser-tests/`, separate from the application and backend test suite.

## Run

Requires Node **22.22+ or 24** for the fixtures' `node:sqlite` and `globSync`.
Run from `workers/`:

```sh
npm ci
npx playwright install chromium
npm run test:browser
```

Alternatively, skip the browser install and set `CHROME=/path/to/chromium`
when running the suite. Playwright automatically starts and stops Wrangler at
`http://127.0.0.1:8799` (localhost); port 8799 must be free, since existing
servers are never reused. Tests use one Playwright worker and no retries.

```sh
npm run test:browser -- -g 'Discord'  # Run a subset by test name
npm run test:browser -- --headed    # Watch the browser while debugging
npm run test:browser -- --trace=retain-on-failure  # Record diagnostic traces
npx playwright show-trace browser-tests/test-results/<test-directory>/trace.zip
```

Failures retain screenshots under `browser-tests/test-results/`. Tracing is
opt-in to keep ordinary runs lightweight; use the reported trace path with
`show-trace` when enabled. On disk-constrained hosts, `--output=/path/to/results`
can move artifacts to a filesystem with space available.

## Isolation

`run.mjs` recreates isolated `browser-tests/.state/` and applies real migrations
at startup. Fixtures reset and seed D1 before each test; DO state lasts for the
run, so billing tests use distinct member IDs. The `login` fixture inserts a
real SHA-256-hashed session into D1 and sets its matching browser cookie, rather
than bypassing application authentication.

`worker.ts` stubs outbound Discord and Stripe provider calls, not application
APIs. OAuth tests execute the real login handler, intercept its consent redirect,
and return to the real callback. Hosted Stripe pages are also intercepted.
The harness uses fake credentials and an empty generated `.dev.vars`, with
dotenv loading disabled; no production secrets or local development `.dev.vars`
are needed or loaded. Never deploy the test Worker.

## Coverage And Limits

- Login/signup, session rotation and expiry, safe return paths, leadership authorization, CSRF and Origin checks.
- Profile persistence and directory privacy; waiver consent/versioning; discount requests and family approval.
- Member CRUD, identity conflicts, stale-write protection, deletion safeguards, session revocation, settings, CSV, pagination, audit/swipes and dead-job retry.
- Checkout reuse/replacement, billing portal, coupons and fail-closed configuration/provider errors through real D1/DO coordination.
- Online Discord signup to waiver, optional Student request, pending-payment block, direct-entry leadership approval, coupon/standard checkout, and real kiosk binding to Ready. Stripe settlement alone is explicitly simulated by updating D1 subscription state; returning from checkout does not mark payment active.
- Slim membership pages without global navigation or leaked admin links, collapsed pre-payment discount requests, pending withdrawal, and pending/active mobile layouts.
- Kiosk IP checks with supplied test headers, QR decoding, clipboard, separate kiosk/phone contexts, one-use claims, expiration, rate limits and polling cleanup.
- SPA deep links/history, mobile layout checks, security headers and text-only rendering; uncaught browser exceptions and CSP/security console errors fail tests.

This does not prove live Discord consent or delivery, real Stripe settlement,
production ingress/IP trust, remote D1 capacity, production migration, camera
scanning, fob readers or door-controller hardware. Those require separate checks.
