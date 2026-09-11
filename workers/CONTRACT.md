# Conway Workers Implementation Contract

Membership core only. Discord ID is the only authentication identity. Discord login creates members directly (no signup form). No email delivery, images, OAuth provider, SQL console, machines UI, signs, elections, or phone inbox. Existing Go application remains available for migration rollback; firmware is untouched.

## Shared conventions

- Backend TypeScript in `src/`, static zero-build ES modules in `public/`, SQL in `migrations/`.
- API timestamps are Unix seconds (the edge wire protocol uses RFC3339). Discord IDs are strings. API fields use snake_case. JSON mutations require same-origin `Origin` and `X-CSRF-Token` from `/api/session`. Errors: `{error: string}` with non-2xx status; GET login navigation errors use escaped, non-cached HTML with a retry link.
- `GET /api/session`: `{member: Member|null, csrf_token: string|null}`. Member includes public membership/billing status, never secrets. Session `conway_session` cookie is HttpOnly; opaque token hash stored in D1.
- `GET /login/discord?return_to=/path`, `GET /login/discord/callback`; `POST /api/logout`.
- `GET /api/config`: public `{site_name, discounts:[{id,label}], waiver:{version,content,agreements}|null, stripe_enabled}`.
- `GET /api/member`; `PATCH /api/profile {name}`.
- `POST /api/waiver {version,name,agreements:true[]}` requires login; existing pre-signup waivers migrate, no new anonymous waivers.
- `POST /api/discount {discount_type}`; `DELETE /api/discount`.
- `POST /api/billing/checkout {}` returns `{url}` using stored `member.bill_annually`, editable only by admins; member checkout selections are rejected. `POST /webhooks/stripe`; `POST /discord/interactions`.
- `GET /api/admin/members?search=&status=&offset=0` -> `{members,total}`; `POST /api/admin/members`; `GET/PATCH/DELETE /api/admin/members/:id`; `POST /api/admin/members/:id/approve {request_id,root_family_member?}`. PATCH requires `version` plus explicitly allowlisted fields and supports manual Discord ID linkage. DELETE requires JSON `{version}`. Stale versions return 409. Identity changes revoke sessions. DELETE protects the last linked leader, family roots with dependents, PayPal and all Stripe subscription history. Family approval requires a primary member.
- `GET /api/admin/events?member=&offset=0` -> `{events}`; `GET /api/admin/swipes?offset=0` -> `{swipes}`; `GET /api/admin/jobs` -> `{jobs}`; `POST /api/admin/jobs/:id/retry`.
- `GET/PUT /api/admin/config`: flat editable nonsecret settings object (site_name, monthly_price_id, yearly_price_id, discounts), optimistic `version`. `PUT /api/admin/waiver {content,agreements}` publishes a new version. `GET /api/admin/export` CSV. Job retries accept only `dead` jobs.
- `POST /api/kiosk/claims {fob_id}` gated by configured exact trusted kiosk IPs in Worker; response `{token,url,expires}`. `GET /api/kiosk/claims/:token` same IP gate -> `{claimed,expires}`. `POST /api/fobs/bind {token}` login + CSRF. QR frontend may use a small vendored native JS library, never third-party QR network services.

## Backend integration interfaces

`src/types.ts`: Env includes DB:D1Database, ASSETS:Fetcher, JOBS:Queue<{id:number}>, COORDINATOR:DurableObjectNamespace; secrets DISCORD_CLIENT_ID, DISCORD_CLIENT_SECRET, DISCORD_BOT_TOKEN, DISCORD_PUBLIC_KEY, STRIPE_SECRET_KEY, STRIPE_WEBHOOK_SECRET, EDGE_TOKEN; vars SITE_URL, EDGE_URL, KIOSK_IPS, AUTOMATION_ENABLED, DISCORD_GUILD_ID, DISCORD_ROLE_ID, DISCORD_LEADERSHIP_CHANNEL_ID, DISCORD_SIGNUP_NOTIFY_ENABLED, DISCORD_ACCESS_DENIED_ENABLED. `Member`, `Settings` exported.

`src/http.ts`: `HttpError(status,message)`, `json(value,status=200)`, `body<T>(request)` bounded JSON parsing, `settings(env): Promise<Settings>`, `now()`, `hash(string):Promise<string>`, `randomToken()`, `requireMember(request,env):Promise<Member>`, `requireLeader(request,env):Promise<Member>`. Main router enforces CSRF for all browser API mutations; integrations authenticate webhooks themselves. `memberView` sanitizes full DB records.

`src/integrations.ts` exports `integrationRoute(request,env):Promise<Response|null>`, `runJobs(env):Promise<void>`, `processJob(env,id):Promise<void>`, `scheduled(env):Promise<void>`, `MembershipCoordinator` Durable Object. It owns Stripe/Discord jobs and edge sync. Main entry calls route then core routes, queues call processJob, cron calls scheduled. Imports shared interfaces from types/http. DB triggers insert outbox jobs; runJobs publishes due jobs. Durable Object serializes Stripe member reconciliation/checkout and edge snapshot pushes via named instances. External effects disabled unless AUTOMATION_ENABLED='true'; manual checkout/login still operate.

Trusted core mutations call `mutateMember(env,memberID,fields|null,actorID,expectedVersion)` or `mutateDiscount(env,memberID,discountType|null,expectedVersion)`. Billing-sensitive changes share checkout's coordinator, revoke open sessions, reconcile, and conditionally commit with actor audit. Definitive rejections must not leave billing locked; ambiguous external writes fail closed until reconciled.

## Data schema (authoritative migrations/0001_core.sql)

members retains legacy writable membership fields, without BLOBs: id,created,email,confirmed,name,name_override,admin_notes,waiver,fob_id,fob_last_seen,leadership,non_billable,discount_type,discount_status,discount_request_id,bill_annually,root_family_member,root_family_member_active,stripe_customer_id,stripe_subscription_id,stripe_subscription_state,stripe_cancellation_reason,stripe_last_payment_error,paypal_subscription_id,paypal_price,discord_user_id,discord_username,discord_email,discord_last_synced. Generated identifier/payment_status/access_status mirror legacy. Required version INTEGER defaults to 1 and advances after updates; mutation responses read the post-trigger version.

waivers(id,version,created,name,email); waiver_versions(version,created,content,agreements TEXT JSON).
settings(id=1,version,data TEXT JSON). Settings: site_name,discounts:{id,label,coupon_id}[],monthly_price_id,yearly_price_id,waiver_version.
sessions(token_hash PK,member FK nullable,csrf_token,expires).
oauth_states(state_hash PK,browser_hash,return_to,expires).
enrollment_claims(token_hash PK,fob_id,created,expires,claimed_by FK nullable).
member_events(id,created,member FK nullable,actor FK nullable,event,details).
fob_swipes(uid TEXT PK,timestamp,fob_id,member FK nullable,allowed,controller TEXT).
jobs(id INTEGER PK,kind TEXT,payload TEXT JSON,status TEXT default 'pending',attempts default 0,available_at,lease_until default 0,last_error TEXT default '',created,completed nullable,dedupe_key TEXT UNIQUE nullable).
stripe_events(id TEXT PK,created,payload TEXT JSON,processed nullable).
notification_state(member FK,kind TEXT,last_sent INTEGER,PRIMARY KEY(member,kind)) throttles denied-access DMs.

Job kinds: `signup`/`discord_sync` payload `{member_id}`, `discount` `{member_id,request_id}`, `denied` `{member_id,swipe_id}`, `stripe_event` `{event_id}`. All swipes remain audited and update last-seen timestamps; only denied swipes enqueue notifications. SQL triggers enqueue membership changes; no HTTP effects during SQL execution. Audit triggers capture membership updates. Schema must expose a runtime_control singleton with automation_enabled INTEGER; importer sets 0 to suppress outbox/audit triggers, then restores 1 (deployment AUTOMATION_ENABLED independently gates external delivery).

## Edge contract extensions

- Keep old POST /api/goal array contract for existing consumers. Add POST /api/goal/versioned `{version:integer,fobs:number[]}` ordered persistently. Equal version/equal contents idempotent; older/conflicting versions rejected with 409. Worker coordinator allocates monotonically increasing versions durably, reads current D1 authorized set immediately before push, never replays stale snapshots.
- Add bearer-auth tunnel GET /api/swipes?limit=100 -> `{events:[{id,time,controller,fob,allowed}]}` and POST /api/swipes/ack `{ids:string[]}`. Event ID edge-assigned, durable spool persisted before controller acknowledgment; at-least-once ingestion, Worker dedups ID. Time RFC3339. Preserve existing local logs; bound batches.
- Add optional CONWAYEDGE_SIGNING_SEED path using legacy fob-signing.ed25519 format; sign exact controller response as legacy protocol. Do not modify firmware or printer features.
- Worker enrollment is trusted kiosk IP gated, no edge enrollment endpoint required. Keep edge stale last-good access behavior with cloud audit/health signals.

## Ownership

Main agent: scaffolding, types/http, schema, core/auth API, tests and assembly.
Frontend agent: public/ only plus frontend tests if isolated.
Integrations agent: src/integrations.ts and isolated integration tests only.
Edge agent: conwayedge/ only.
Migration agent: scripts/ and migration docs only after schema available.
