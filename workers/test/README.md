# Workers Runtime Tests

Use Node 22.12 or newer. From `workers/` on a machine with sufficient disk space:

```sh
npm ci
npm test
npm run typecheck
npm run test:integrations
npx wrangler deploy --dry-run
```

`npm test` runs inside workerd through `@cloudflare/vitest-pool-workers`, with a
real local D1 binding, not a SQLite or D1 mock. Every test resets local bindings
and applies `migrations/0001_core.sql`. No remote Cloudflare database or external
service is contacted. Test files are sequential because their D1 state is reset.
The separately owned `src/integrations.test.mjs` suite runs in Node and uses mocks.

The runtime tests cover schema constraints, legacy payment/access status rules,
waiver resolution, import automation suppression, family payment propagation and
deletion protection, last linked leader protection, transactional outbox jobs,
swipe deduplication, session authorization, CSRF, and bounded streaming JSON bodies.
Router tests call the actual default Worker through `SELF.fetch`, using the
Wrangler D1, assets, queue, and Durable Object bindings. OAuth tests mock only
Discord's external HTTP responses. A signup streaming-race test invokes the real
default handler directly with a Workers execution context to coordinate the
session-expiration boundary without mocking D1 or application code.

Request-level coverage includes public/private routing, session creation and
rotation, all browser mutation CSRF boundaries, profile allowlists, directory
privacy, admin settings conflicts and CRUD, concurrent last-leader protection,
waiver publication/signing/version races, stale discount approvals, family
approval, kiosk IP gates, enrollment replay/conflicts/concurrency, and signup
`changes()` transaction behavior with real member-created triggers. Login-error
tests verify safe non-cached HTML GET responses, no reflection of malicious
callback input or provider secrets, recovery links and security headers, and
continued JSON responses for API and non-GET login errors.

Admin PATCH and DELETE tests send the required JSON `version`. Routine CRUD uses
`adminMutation()` to fetch the current version; CAS tests call `api()` directly
with explicit stale/missing versions. `test/coordinator.test.ts` exercises the
real Wrangler-bound `MembershipCoordinator` through both router requests and
`mutateMember`/`mutateDiscount`, without mocking D1, the DO, or its storage. It
checks concurrent/stale writes, conditional audits, post-trigger return versions,
family eligibility and payment propagation, discount outbox jobs, and authorization.
Inactive-family regressions cover notes-only saves and editor-style saves that
resend unchanged billing fields, preserving family state without external billing
calls or extra outbox jobs. Router coverage includes legacy `firstResponder`
discount-ID validation.

## Constrained Workspace Runtime

This session installed Node 22.22.0 at
`/dev/shm/conway-workers-test/package/bin/node`. npm is available from
`/snap/node/current/bin/npm`. The project's ignored `node_modules` symlink targets
`/dev/shm/conway-workers-test/node_modules`.

```sh
export PATH=/dev/shm/conway-workers-test/package/bin:/snap/node/current/bin:$PATH
export TMPDIR=/dev/shm/conway-workers-test/tmp
export npm_config_cache=/dev/shm/conway-workers-test/cache
export WRANGLER_SEND_METRICS=false
npm test
npm run typecheck
npm run test:integrations
node node_modules/wrangler/bin/wrangler.js deploy --dry-run --outdir /dev/shm/conway-workers-test/bundle
node node_modules/wrangler/bin/wrangler.js d1 migrations apply conway --local --persist-to /dev/shm/conway-workers-test/d1-versioned-final
```

Shared-memory files are temporary and disappear on reboot. Do not run `npm ci`
against the project symlink on this full root filesystem: npm replaces the link
with a directory. Install into the shared-memory prefix instead. Regenerate the
project lockfile with the dependency symlink detached to avoid recording local
links; the checked-in-path `workers/package-lock.json` contains registry URLs only.

## Integration Findings

- Latest complete runtime run: all 160 tests pass across six files (12.82 seconds).
  No skipped tests or expected failures. No remaining blockers were observed in
  the requested runtime, typecheck, integration, and dry-run checks.
- Pool 0.22.0 requires Vitest 4.1, not the scaffold's Vitest 3.2. The lockfile pins
  Vitest 4.1.11, Wrangler 4.124.0, TypeScript 5.9.3, and Workers types 5.20260908.1.
- The pool's workerd supports compatibility date 2026-08-15, so the test config
  uses that date rather than the application's newer 2026-09-01 date.
- Fresh local D1 migration succeeds (45 commands), including member revision,
  identity-session invalidation, and consumed-claim deletion triggers. The prior
  `family_link` splitter workaround is removed. Use a fresh persistence directory
  when validating edits to an already-applied `0001_core.sql`.
- Full typecheck passes. Production dry-run bundle passes: 94.38 KiB uploaded,
  22.60 KiB gzip, including assets and D1/queue/Durable Object bindings. No remote
  deployment or live-provider validation was performed.
- Coordinator conflict mapping is fixed: duplicate email/Discord/fob edits and
  protected last-leader mutations return 409. All four prior regressions pass.
- Profile PATCH now returns the post-trigger D1 revision. Its prior stale-version
  regression passes alongside coordinator response-version and CAS checks.
- The consumed-claim replay and successful Discord relink/unlink invalidation
  regressions now pass. CAS, conditional actor audit, post-trigger coordinator
  responses, and family-trigger revision checks pass against real bindings.
- The separately owned Node integration suite passes all 44 tests, including
  rejected-deletion lock cleanup, inactive-family billing portal access, and
  value-based billing-change detection. Real D1 scheduled cleanup also passes
  and confirms disabled automation preserves jobs.
- Pool `reset()` can print `Application called deleteAllDurableObjects()` stack
  messages when discarding used DOs between tests; the run still completes with
  the assertion results above. Graceful eviction was tested but hung with this
  pool/runtime version, so setup retains the documented reset API.
