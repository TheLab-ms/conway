# Browser Checks

Run `node workers/browser-tests/run.mjs`. Requires Node 18+ and Chrome, or set
`CHROME=/path/to/chromium`. No npm packages, backend, or build step required.
Worker-aligned API mocks are isolated from the real database and integrations.
The runner uses Chrome's DevTools pipe and removes its temporary browser profile.
It reads the exact CSP from `src/index.ts` and serves it on every response,
along with the Worker's no-referrer, nosniff, and Permissions-Policy headers.
Any browser security/CSP error fails the suite. No `unsafe-inline` exception,
injected inline script, or inline style is needed for the app or canvas QR.

Chrome runs without its sandbox for container compatibility; use only this
trusted local fixture. Actual OAuth, Stripe, trusted-IP enforcement, QR camera
scans, database constraints, and deployment deep-link fallback need integration
checks. `TMPDIR` may point to a writable temporary filesystem.

## Integration Handoff

The frontend owns only `workers/public/` and these isolated tests. Serve
`public/index.html` as the fallback for frontend history routes, with JS/MJS
served as JavaScript; API and `/login/discord` routes must reach the Worker.
There is no build step, external asset request, or member imagery.

- Member JSON is a direct Member object for `/api/member` and admin detail.
  Admin PATCH sends editable fields plus the required `version` read from that
  member. Admin DELETE sends JSON `{version: member.version}`. Stale revisions
  return 409 without writing stale privileges or deleting the record; local
  edits remain until leadership explicitly reloads the latest record.
- Admin config GET is a flat settings object with `version`; PUT sends the
  edited nonsecret fields plus that same version. A 409 keeps the local edits.
- Waiver editing reads public `/api/config` and publishes with
  `PUT /api/admin/waiver {content,agreements}`, implemented in `src/core.ts`.
  Public `waiver: null` renders an unpublished state, not a signing form.
- New member POST expects `{id}` (or a direct Member); all editable fields are
  limited to the membership/profile/Discord/family fields in the contract.
- Anonymous kiosk sessions need a CSRF token from `/api/session` before claim
  POSTs. Trusted kiosk IP validation remains entirely server-side.
- Enrollment URLs use `/fobs/bind?token=...`. OAuth/signup must preserve this
  local return path until binding. Tokens are 64 lowercase hexadecimal digits.
  Signup follows the server's returned `return_to`, then refreshes the session
  to obtain the rotated CSRF token.
- Checkout accepts only HTTPS `checkout.stripe.com` or `billing.stripe.com`
  redirects. No credentials, card data, or deployment secrets are collected.
- Waivers, bios, audit details, and templates are rendered as plain text.
- Members, audit, and swipes use offset pagination. Next advances by the
  actual returned row count; previous offsets stay in the local route URL.
- Only `dead` jobs offer retry; `pending`, `processing`, and `completed` do not.
- Family approval requires `root_family_member`; discount requests cannot be
  replaced while pending. Billing conflicts are not described as edit conflicts.
- Any Stripe subscription history blocks physical deletion, including canceled
  subscriptions. Retain the member record and its billing mapping. Other records
  still require billing cancellation/reconciliation and deletion safeguards.

The browser suite uses mocks aligned against `src/{core,auth,http,index}.ts`
and `migrations/0001_core.sql`, not a running Worker or D1. It covers strict
config/member payload shapes, mandatory PATCH/DELETE revisions and stale-write
rejection, Stripe-history deletion refusal, nullable waivers, all access/job status values,
CSRF rotation, 400/401/403/409/429/503 scenarios, consent, safe return paths,
and desktop/mobile rendering. Live database, OAuth, Stripe, and deployment
checks remain separate. `wrangler.jsonc` already configures SPA asset fallback.

OAuth navigation errors are owned by the main agent's safe server-rendered
HTML fallback. No separate frontend `/login?error=...` route is introduced.
