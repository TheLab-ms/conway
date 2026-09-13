import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import test from 'node:test';
import vm from 'node:vm';

// node --test src/integrations.test.mjs; requires the project's TypeScript dev dependency.
// Transpile the real module in memory, with isolated HTTP and Cloudflare bindings.
const require = createRequire(import.meta.url);
let ts;
if (process.env.TYPESCRIPT_SOURCE_STDIN === '1') {
    let source = '';
    for await (const chunk of process.stdin) source += chunk;
    const compiler = { exports: {} };
    vm.runInNewContext(source, { module: compiler, exports: compiler.exports }, { filename: 'typescript.js' });
    ts = compiler.exports;
} else ts = require(process.env.TYPESCRIPT_PATH || 'typescript');
const compiled = ts.transpileModule(await readFile(new URL('./integrations.ts', import.meta.url), 'utf8'), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
    reportDiagnostics: true,
});
assert.equal(compiled.diagnostics?.length || 0, 0);
const time = 1_800_000_000;
const config = {
    site_name: 'Lab',
    monthly_price_id: 'price_month',
    yearly_price_id: 'price_year',
    discounts: [
        { id: 'student', label: 'Student', coupon_id: 'coupon_student' },
        { id: 'family', label: 'Family', coupon_id: 'coupon_family' },
    ],
};
const member = (extra = {}) => ({
    id: 1,
    version: 1,
    root_family_member: null,
    paypal_subscription_id: null,
    email: 'member@example.com',
    name: 'Member',
    discord_user_id: '555',
    discord_username: 'member',
    stripe_customer_id: 'cus_member',
    stripe_subscription_id: null,
    stripe_subscription_state: null,
    bill_annually: 0,
    discount_type: null,
    discount_status: null,
    discount_request_id: null,
    payment_status: null,
    access_status: 'PaymentInactive',
    ...extra,
});
const response = (value, status = 200) => new Response(JSON.stringify(value), { status });

function database(handler = () => undefined) {
    const calls = [];
    return {
        calls,
        prepare(sql) {
            return {
                sql: sql.replace(/\s+/g, ' ').trim(),
                values: [],
                bind(...values) {
                    this.values = values;
                    return this;
                },
                async first() {
                    calls.push({ ...this, method: 'first' });
                    return (await handler(this, 'first')) ?? null;
                },
                async all() {
                    calls.push({ ...this, method: 'all' });
                    return (await handler(this, 'all')) ?? { results: [] };
                },
                async run() {
                    calls.push({ ...this, method: 'run' });
                    return (await handler(this, 'run')) ?? { success: true, meta: { changes: 1 } };
                },
            };
        },
        async batch(statements) {
            const result = await handler({ sql: 'BATCH', statements }, 'batch');
            return result ?? Promise.all(statements.map((statement) => statement.run()));
        },
    };
}

function harness({
    db = database(),
    fetch: external = async () => {
        throw new Error('Unexpected fetch');
    },
    enabled = 'true',
} = {}) {
    const storage = new Map();
    const state = {
        storage: {
            async get(key) {
                return structuredClone(storage.get(key));
            },
            async put(key, value) {
                storage.set(key, structuredClone(value));
            },
            async delete(key) {
                return storage.delete(key);
            },
        },
    };
    class HttpError extends Error {
        constructor(status, message) {
            super(message);
            this.status = status;
        }
    }
    const http = {
        HttpError,
        now: () => time,
        randomToken: () => webcrypto.randomUUID(),
        hash: async (value) => Buffer.from(await webcrypto.subtle.digest('SHA-256', new TextEncoder().encode(value))).toString('hex'),
        json: (value, status = 200) => response(value, status),
        body: (request) => request.json(),
        settings: async () => config,
        requireMember: async () => member(),
    };
    const context = {
        exports: {},
        require: (name) => {
            assert.equal(name, './http');
            return http;
        },
        crypto: webcrypto,
        Request,
        Response,
        URL,
        URLSearchParams,
        TextEncoder,
        TextDecoder,
        AbortSignal,
        Uint8Array,
        fetch: external,
        console,
    };
    vm.runInNewContext(compiled.outputText, context, { filename: 'integrations.ts' });
    const api = context.exports;
    const env = {
        DB: db,
        AUTOMATION_ENABLED: enabled,
        SITE_URL: 'https://members.example.com',
        STRIPE_SECRET_KEY: 'sk_test',
        STRIPE_WEBHOOK_SECRET: 'whsec_test',
        DISCORD_BOT_TOKEN: 'bot_token',
        DISCORD_GUILD_ID: '111',
        DISCORD_ROLE_ID: '222',
        DISCORD_LEADERSHIP_CHANNEL_ID: '333',
        DISCORD_SIGNUP_NOTIFY_ENABLED: 'true',
        DISCORD_ACCESS_DENIED_ENABLED: 'true',
        EDGE_URL: 'https://edge.example.com',
        EDGE_TOKEN: 'edge_token',
        JOBS: { send: async () => {} },
    };
    const coordinator = new api.MembershipCoordinator(state, env);
    env.COORDINATOR = {
        idFromName: (name) => {
            assert.equal(name, 'stripe');
            return name;
        },
        get: () => ({ fetch: (url, init) => coordinator.fetch(new Request(url, init)) }),
    };
    const internal = (path, value = {}) => coordinator.fetch(new Request(`https://internal${path}`, { method: 'POST', body: JSON.stringify(value) }));
    return { api, env, db, storage, internal };
}

async function signedStripe(event, { corrupt = false, timestamp = time } = {}) {
    const body = JSON.stringify(event);
    const key = await webcrypto.subtle.importKey('raw', new TextEncoder().encode('whsec_test'), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
    const signature = Buffer.from(await webcrypto.subtle.sign('HMAC', key, new TextEncoder().encode(`${timestamp}.${body}`))).toString('hex');
    return new Request('https://members.example.com/webhooks/stripe', {
        method: 'POST',
        body,
        headers: { 'Stripe-Signature': `t=${timestamp},v1=${corrupt ? '0'.repeat(64) : signature}` },
    });
}
async function signedDiscord(env, value, { corrupt = false, timestamp = time } = {}) {
    const keys = await webcrypto.subtle.generateKey('Ed25519', true, ['sign', 'verify']);
    env.DISCORD_PUBLIC_KEY = Buffer.from(await webcrypto.subtle.exportKey('raw', keys.publicKey)).toString('hex');
    const body = JSON.stringify(value);
    const signature = Buffer.from(await webcrypto.subtle.sign('Ed25519', keys.privateKey, new TextEncoder().encode(String(timestamp) + body))).toString('hex');
    return new Request('https://members.example.com/discord/interactions', {
        method: 'POST',
        body,
        headers: {
            'X-Signature-Timestamp': String(timestamp),
            'X-Signature-Ed25519': corrupt ? '0'.repeat(128) : signature,
        },
    });
}
function jobDB(job = {}, who = member(), extra = () => undefined) {
    let claimed = false;
    return database((statement, method) => {
        const result = extra(statement, method);
        if (result !== undefined) return result;
        if (statement.sql.startsWith("UPDATE jobs SET status = 'processing'")) {
            if (claimed) return null;
            claimed = true;
            return {
                id: 1,
                kind: 'signup',
                payload: '{"member_id":1}',
                attempts: 1,
                lease_until: time + 180,
                ...job,
            };
        }
        if (statement.sql === 'SELECT * FROM members WHERE id = ?') return who;
    });
}

test('Stripe authentication rejects invalid and stale signatures before DB access', async () => {
    const h = harness();
    for (const options of [{ corrupt: true }, { timestamp: time - 301 }, { timestamp: time + 301 }]) {
        await assert.rejects(h.api.integrationRoute(await signedStripe({ id: 'evt_test', type: 'test', data: { object: {} } }, options), h.env), (error) => error.status === 401);
    }
    assert.equal(h.db.calls.length, 0);
});
test('Stripe intake atomically deduplicates event and outbox while automation disabled', async () => {
    const batches = [];
    const h = harness({
        enabled: 'false',
        db: database((statement, method) => {
            if (method === 'batch') {
                batches.push(statement.statements);
                return [];
            }
        }),
    });
    const event = {
        id: 'evt_test',
        type: 'checkout.session.completed',
        data: { object: { id: 'cs_test' } },
    };
    for (let i = 0; i < 2; i++) assert.equal((await h.api.integrationRoute(await signedStripe(event), h.env)).status, 200);
    assert.equal(batches.length, 2);
    for (const batch of batches) {
        assert.equal(batch.length, 2);
        assert.match(batch[0].sql, /ON CONFLICT\(id\) DO NOTHING/);
        assert.match(batch[1].sql, /ON CONFLICT\(dedupe_key\) DO NOTHING/);
        assert.equal(batch[1].values[3], 'stripe:evt_test');
    }
});
test('Discord Ed25519 ping succeeds; tampering and stale timestamps fail', async () => {
    const h = harness();
    assert.deepEqual(await (await h.api.integrationRoute(await signedDiscord(h.env, { type: 1 }), h.env)).json(), { type: 1 });
    for (const options of [{ corrupt: true }, { timestamp: time - 301 }]) {
        await assert.rejects(h.api.integrationRoute(await signedDiscord(h.env, { type: 1 }, options), h.env), (error) => error.status === 401);
    }
});
const approval = {
    type: 3,
    guild_id: '111',
    channel_id: '333',
    member: { user: { id: '555' } },
    data: { custom_id: 'approve:1:request-123' },
};
test('Discord approval requires local leadership and configured guild/channel', async () => {
    for (const change of [{}, { guild_id: 'wrong' }, { channel_id: 'wrong' }]) {
        const h = harness();
        const result = await h.api.integrationRoute(await signedDiscord(h.env, { ...approval, ...change }), h.env);
        assert.equal((await result.json()).data.flags, 64);
        assert.equal(
            h.db.calls.some((call) => call.sql.startsWith('UPDATE members')),
            false,
        );
    }
});
test('approval write rechecks exact pending request and leadership, rejecting stale buttons', async () => {
    const h = harness({
        db: database((statement) => (statement.sql.startsWith('SELECT id FROM members') ? { id: 9 } : undefined)),
    });
    const result = await h.api.integrationRoute(await signedDiscord(h.env, approval), h.env);
    assert.match((await result.json()).data.content, /no longer pending/);
    const write = h.db.calls.find((call) => call.sql.startsWith('UPDATE members'));
    assert.match(write.sql, /discount_request_id = \?/);
    assert.match(write.sql, /EXISTS.*leadership = 1/);
    assert.deepEqual(write.values, [1, 'request-123', 9, '555']);
});
test('successful approval clears components and audits the local leader actor', async () => {
    const h = harness({
        db: database((statement) => {
            if (statement.sql.startsWith('SELECT id FROM members')) return { id: 9 };
            if (statement.sql.startsWith('UPDATE members')) return { id: 1, discount_type: 'family' };
        }),
    });
    const result = await (await h.api.integrationRoute(await signedDiscord(h.env, approval), h.env)).json();
    assert.equal(result.type, 7);
    assert.deepEqual(result.data.components, []);
    assert.match(result.data.content, /primary family/);
    assert.equal(h.db.calls.find((call) => call.sql.startsWith('INSERT INTO member_events')).values[2], 9);
});
test('disabled automation preserves jobs, suppresses effects, but cleans expired sessions', async () => {
    const h = harness({ enabled: 'false' });
    await h.api.runJobs(h.env);
    await h.api.processJob(h.env, 1);
    for (const path of ['/job', '/edge', '/reconcile']) await h.internal(path, { id: 1 });
    assert.equal(h.db.calls.length, 0);
    await h.api.scheduled(h.env);
    assert.equal(h.db.calls.length, 4);
    assert.ok(h.db.calls.every((call) => /DELETE FROM (sessions|oauth_states|enrollment_claims|rate_limits)/.test(call.sql)));
});
test('outbox requeues expired processing jobs and releases failed publication leases', async () => {
    const h = harness({
        db: database((_statement, method) => (method === 'all' ? { results: [{ id: 7 }] } : undefined)),
    });
    h.env.JOBS.send = async () => {
        throw new Error('queue unavailable');
    };
    await assert.rejects(h.api.runJobs(h.env), /queue unavailable/);
    assert.match(h.db.calls[0].sql, /SET status = 'pending', lease_until = \?/);
    assert.match(h.db.calls[0].sql, /status IN \('pending', 'processing'\)/);
    assert.match(h.db.calls[1].sql, /lease_until = 0 WHERE id = \? AND lease_until = \?/);
});
test('Discord 429 retries durably; permanent and exhausted failures enter DLQ', async () => {
    for (const [status, attempts, expected] of [
        [429, 1, 'pending'],
        [403, 1, 'dead'],
        [503, 10, 'dead'],
    ]) {
        const h = harness({
            db: jobDB({ attempts }),
            fetch: async () => response({ retry_after: 120 }, status),
        });
        assert.equal((await h.internal('/job', { id: 1 })).status, 200);
        const write = h.db.calls.find((call) => call.sql.startsWith('UPDATE jobs SET status = ?'));
        assert.equal(write.values[0], expected);
        if (status === 429) assert.equal(write.values[1], time + 120);
        assert.equal(
            h.db.calls.some((call) => call.sql.startsWith("UPDATE jobs SET status = 'completed'")),
            false,
        );
    }
});
test('malformed job JSON dead-letters without network effects', async () => {
    const h = harness({ db: jobDB({ payload: 'invalid' }) });
    await h.internal('/job', { id: 1 });
    assert.equal(h.db.calls.find((call) => call.sql.startsWith('UPDATE jobs SET status = ?')).values[0], 'dead');
});
for (const [payment, roles, nick, method] of [
    ['ActiveStripe', [], 'Guild Nickname', 'PUT'],
    [null, ['222'], null, 'DELETE'],
    ['ActiveStripe', ['222'], 'Guild Nickname', null],
])
    test(`Discord sync stores username, not nickname/global name, with role action ${method}`, async () => {
        const requests = [];
        const h = harness({
            db: jobDB({ kind: 'discord_sync' }, member({ payment_status: payment, access_status: 'MissingWaiver', discord_username: 'Old Nickname' })),
            fetch: async (url, init) => {
                requests.push({ url, init });
                return init.method === 'GET'
                    ? response({ roles, nick, user: { username: 'actual.handle', global_name: 'Global Display Name', avatar: 'ignored' } })
                    : new Response(null, { status: 204 });
            },
        });
        await h.internal('/job', { id: 1 });
        assert.equal(requests.length, method ? 2 : 1);
        if (method) {
            assert.equal(requests[1].init.method, method);
            assert.ok(requests[1].url.endsWith('/guilds/111/members/555/roles/222'));
        }
        assert.ok(requests.every((request) => request.url.startsWith('https://discord.com/api/v10/')));
        const update = h.db.calls.find((call) => call.sql.startsWith('UPDATE members SET discord_last_synced'));
        assert.deepEqual(update.values, [time, 'actual.handle', 1, '555', payment]);
        assert.match(update.sql, /WHERE id = \? AND discord_user_id = \? AND payment_status IS \?/);
    });
test('messages disable mentions, use stable nonce, and omit image payloads', async () => {
    let sent;
    const h = harness({
        db: jobDB(),
        fetch: async (_url, init) => {
            sent = JSON.parse(init.body);
            return response({ id: 'message' });
        },
    });
    await h.internal('/job', { id: 1 });
    assert.deepEqual(sent.allowed_mentions, { parse: [] });
    assert.equal(sent.nonce, '1');
    assert.equal(sent.enforce_nonce, true);
    assert.equal(sent.embeds, undefined);
    assert.equal(sent.attachments, undefined);
});
test('durable message receipt suppresses redelivery after a later D1 failure', async () => {
    const h = harness({ db: jobDB() });
    h.storage.set('message:1', time - 3600);
    await h.internal('/job', { id: 1 });
    assert.ok(h.db.calls.some((call) => call.sql.startsWith("UPDATE jobs SET status = 'completed'")));
});
test('denied notifications require a denied swipe and respect the one-hour throttle', async () => {
    for (const [allowed, last_sent] of [
        [1, 0],
        [0, time - 3599],
        [null, 0],
    ]) {
        const h = harness({
            db: jobDB({ kind: 'denied', payload: '{"member_id":1,"swipe_id":"swipe"}' }, member(), (statement) => {
                if (statement.sql.startsWith('SELECT allowed')) {
                    assert.deepEqual(statement.values, ['swipe', 1]);
                    return allowed === null ? null : { allowed };
                }
                if (statement.sql.startsWith('SELECT last_sent')) return { last_sent };
            }),
        });
        await h.internal('/job', { id: 1 });
        assert.ok(h.db.calls.some((call) => call.sql.startsWith("UPDATE jobs SET status = 'completed'")));
    }
});
test('denied notifications send a DM and persist the throttle only after delivery', async () => {
    for (const last_sent of [null, time - 3600]) {
        const requests = [];
        const h = harness({
            db: jobDB({ kind: 'denied', payload: '{"member_id":1,"swipe_id":"swipe"}' }, member(), (statement) => {
                if (statement.sql.startsWith('SELECT allowed')) return { allowed: 0 };
                if (statement.sql.startsWith('SELECT last_sent')) return last_sent === null ? null : { last_sent };
            }),
            fetch: async (url, init) => {
                requests.push({ url, body: JSON.parse(init.body) });
                return response({ id: 'dm' });
            },
        });
        await h.internal('/job', { id: 1 });
        assert.equal(requests.length, 2);
        assert.ok(requests[0].url.endsWith('/users/@me/channels'));
        assert.deepEqual(requests[0].body, { recipient_id: '555' });
        assert.ok(requests[1].url.endsWith('/channels/dm/messages'));
        assert.match(requests[1].body.content, /your fob was denied access/);
        assert.deepEqual(requests[1].body.allowed_mentions, { parse: [] });
        assert.deepEqual(h.db.calls.find((call) => call.sql.startsWith('INSERT INTO notification_state')).values, [1, 'denied', time]);
    }
    const h = harness({
        db: jobDB({ kind: 'denied', payload: '{"member_id":1,"swipe_id":"swipe"}' }, member(), (statement) => {
            if (statement.sql.startsWith('SELECT allowed')) return { allowed: 0 };
        }),
        fetch: async (url) => (url.endsWith('/users/@me/channels') ? response({ id: 'dm' }) : response({}, 503)),
    });
    await h.internal('/job', { id: 1 });
    assert.equal(
        h.db.calls.some((call) => call.sql.startsWith('INSERT INTO notification_state')),
        false,
    );
    assert.equal(h.db.calls.find((call) => call.sql.startsWith('UPDATE jobs SET status = ?')).values[0], 'pending');
});
test('coordinator serializes across awaited network calls and durably increases edge versions', async () => {
    let release;
    const gate = new Promise((resolve) => {
        release = resolve;
    });
    const pushes = [];
    const h = harness({
        db: database((_statement, method) => (method === 'all' ? { results: [{ fob_id: 42 }] } : undefined)),
        fetch: async (url, init) => {
            if (!url.includes('/goal/')) return response({ events: [] });
            pushes.push(JSON.parse(init.body));
            if (pushes.length === 1) await gate;
            return response({});
        },
    });
    const first = h.internal('/edge');
    while (!pushes.length) await new Promise((resolve) => setTimeout(resolve, 1));
    const second = h.internal('/edge');
    await new Promise((resolve) => setTimeout(resolve, 10));
    assert.equal(pushes.length, 1);
    release();
    assert.equal((await first).status, 200);
    assert.equal((await second).status, 200);
    assert.ok(pushes[1].version > pushes[0].version);
    assert.deepEqual(pushes[0].fobs, [42]);
});
test('failed edge pushes consume versions and retry reads fresh authorized set', async () => {
    let fob = 7;
    const pushes = [];
    const h = harness({
        db: database((_statement, method) => (method === 'all' ? { results: [{ fob_id: fob }] } : undefined)),
        fetch: async (url, init) => {
            if (!url.includes('/goal/')) return response({ events: [] });
            pushes.push(JSON.parse(init.body));
            return response({}, pushes.length === 1 ? 503 : 200);
        },
    });
    assert.equal((await h.internal('/edge')).status, 502);
    fob = 9;
    assert.equal((await h.internal('/edge')).status, 200);
    assert.ok(pushes[1].version > pushes[0].version);
    assert.deepEqual(pushes[1].fobs, [9]);
});
test('swipes are acknowledged only after durable D1 batch succeeds', async () => {
    for (const fail of [true, false]) {
        const order = [];
        const h = harness({
            db: database((_statement, method) => {
                if (method === 'batch') {
                    order.push('commit');
                    if (fail) throw new Error('D1 unavailable');
                }
            }),
            fetch: async (url, init) => {
                if (url.includes('/goal/')) return response({});
                if (url.includes('/ack')) {
                    order.push('ack');
                    assert.deepEqual(JSON.parse(init.body).ids, ['edge-1']);
                    return response({});
                }
                return response({
                    events: [
                        {
                            id: 'edge-1',
                            time: '2026-09-09T12:00:00Z',
                            controller: 'door',
                            fob: 42,
                            allowed: true,
                        },
                    ],
                });
            },
        });
        assert.equal((await h.internal('/edge')).status, fail ? 502 : 200);
        assert.deepEqual(order, fail ? ['commit'] : ['commit', 'ack']);
    }
});
test('edge accepts empty 204 responses from versioned push and acknowledgement', async () => {
    const h = harness({
        fetch: async (url) => (url.includes('?limit=') ? response({ events: [] }) : new Response(null, { status: 204 })),
    });
    assert.equal((await h.internal('/edge')).status, 200);
});
test('Stripe reconciliation uses live subscription state instead of stale webhook payload', async () => {
    const who = member({ stripe_subscription_id: 'sub_new', stripe_subscription_state: 'active' });
    const event = {
        id: 'evt_old',
        type: 'customer.subscription.deleted',
        data: { object: { id: 'sub_old', customer: 'cus_member', status: 'canceled' } },
    };
    const h = harness({
        db: jobDB({ kind: 'stripe_event', payload: '{"event_id":"evt_old"}' }, who, (statement) => {
            if (statement.sql.startsWith('SELECT payload, processed')) return { payload: JSON.stringify(event), processed: null };
            if (statement.sql === 'SELECT * FROM members WHERE stripe_customer_id = ?') return who;
        }),
        fetch: async () =>
            response({
                has_more: false,
                data: [
                    {
                        id: 'sub_old',
                        customer: 'cus_member',
                        status: 'canceled',
                        created: 1,
                        metadata: { conway_member_id: '1' },
                    },
                    {
                        id: 'sub_new',
                        customer: 'cus_member',
                        status: 'active',
                        created: 2,
                        metadata: { conway_member_id: '1' },
                    },
                ],
            }),
    });
    await h.internal('/job', { id: 1 });
    const write = h.db.calls.find((call) => call.sql.startsWith('UPDATE members SET stripe_subscription_id'));
    assert.equal(write.values[0], 'sub_new');
    assert.equal(write.values[1], 'active');
    assert.ok(h.db.calls.every((call) => !/WHERE email/.test(call.sql)));
});
test('removed donation endpoint is not routed and has no billing effects', async () => {
    const h = harness();
    for (const method of ['GET', 'POST']) {
        const request = new Request('https://members.example.com/api/billing/donation', {
            method,
            ...(method === 'POST' ? { body: JSON.stringify({ price_id: 'price_removed' }) } : {}),
        });
        assert.equal(await h.api.integrationRoute(request, h.env), null);
    }
    assert.equal(h.db.calls.length, 0);
    assert.equal(h.storage.size, 0);
});
test('payment-mode checkout events complete without customer lookup, provider calls, or billing writes', async () => {
    for (const type of ['checkout.session.completed', 'checkout.session.async_payment_succeeded', 'checkout.session.async_payment_failed']) {
        for (const customer of ['cus_member', 'cus_unmapped', null]) {
            const event = { id: 'evt_payment', type, data: { object: { id: 'cs_payment', mode: 'payment', customer } } };
            const h = harness({
                db: jobDB({ kind: 'stripe_event', payload: '{"event_id":"evt_payment"}' }, member(), (statement) => {
                    if (statement.sql.startsWith('SELECT payload, processed')) return { payload: JSON.stringify(event), processed: null };
                }),
            });
            assert.equal((await h.api.integrationRoute(await signedStripe(event), h.env)).status, 200);
            await h.internal('/job', { id: 1 });
            assert.ok(h.db.calls.some((call) => call.sql.startsWith('UPDATE stripe_events SET processed')));
            assert.ok(h.db.calls.some((call) => call.sql.startsWith("UPDATE jobs SET status = 'completed'")));
            assert.ok(h.db.calls.every((call) => !/\b(?:members|member_events|donations)\b/.test(call.sql)));
        }
    }
});
test('subscription checkout events reconcile live membership and verify the checkout customer', async () => {
    for (const type of ['checkout.session.completed', 'checkout.session.async_payment_succeeded', 'checkout.session.async_payment_failed']) {
        for (const customer of ['cus_member', 'cus_wrong']) {
            const event = { id: 'evt_checkout', type, data: { object: { id: 'cs_checkout', mode: 'subscription', customer: 'cus_member' } } };
            const requests = [];
            const h = harness({
                db: jobDB({ kind: 'stripe_event', payload: '{"event_id":"evt_checkout"}' }, member(), (statement) => {
                    if (statement.sql.startsWith('SELECT payload, processed')) return { payload: JSON.stringify(event), processed: null };
                    if (statement.sql === 'SELECT * FROM members WHERE stripe_customer_id = ?') return member();
                }),
                fetch: async (url) => {
                    requests.push(url);
                    if (url.endsWith('/checkout/sessions/cs_checkout')) return response({ id: 'cs_checkout', customer, mode: 'subscription' });
                    assert.ok(url.includes('/subscriptions?'));
                    return response({ data: [{ id: 'sub_live', customer, status: 'active', metadata: { conway_member_id: '1' } }], has_more: false });
                },
            });
            await h.internal('/job', { id: 1 });
            const write = h.db.calls.find((call) => call.sql.startsWith('UPDATE members SET stripe_subscription_id'));
            if (customer === 'cus_member') {
                assert.equal(write.values[0], 'sub_live');
                assert.equal(write.values[1], 'active');
                assert.ok(h.db.calls.some((call) => call.sql.startsWith('UPDATE stripe_events SET processed')));
                assert.ok(h.db.calls.some((call) => call.sql.startsWith("UPDATE jobs SET status = 'completed'")));
            } else {
                assert.equal(write, undefined);
                assert.equal(requests.length, 1);
                assert.equal(h.db.calls.find((call) => call.sql.startsWith('UPDATE jobs SET status = ?')).values[0], 'dead');
            }
        }
    }
});
test('checkout uses the stored billing preference and rejects member overrides at both boundaries', async () => {
    for (const bill_annually of [0, 1]) {
        const who = member({ bill_annually });
        const forms = [];
        const h = harness({
            db: database((statement) => (statement.sql === 'SELECT * FROM members WHERE id = ?' ? who : undefined)),
            fetch: async (url, init) => {
                if (url.includes('/subscriptions?')) return response({ data: [], has_more: false });
                forms.push(new URLSearchParams(init.body));
                return response({ id: 'cs_stored', url: 'https://checkout.stripe.com/stored' });
            },
        });
        const checkout = (input) =>
            h.api.integrationRoute(
                new Request('https://members.example.com/api/billing/checkout', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify(input),
                }),
                h.env,
            );
        for (const input of [{ annual: !bill_annually }, { bill_annually: 1 - bill_annually }]) {
            await assert.rejects(checkout(input), { status: 400 });
            assert.equal((await h.internal('/checkout', { member_id: 1, ...input })).status, 400);
        }
        assert.equal(h.db.calls.length, 0);
        assert.equal(forms.length, 0);
        assert.equal((await checkout({})).status, 200);
        assert.equal(forms.length, 1);
        assert.equal(forms[0].get('line_items[0][price]'), bill_annually ? 'price_year' : 'price_month');
        assert.equal(forms[0].has('discounts[0][coupon]'), false);
        assert.equal(who.bill_annually, bill_annually);
        assert.ok(h.db.calls.every((call) => !call.sql.startsWith('UPDATE members SET bill_annually')));
    }
});
test('manual checkout operates while automation disabled; only approved/admin discounts apply', async () => {
    for (const status of ['requested', 'approved', null]) {
        const forms = [];
        const h = harness({
            enabled: 'false',
            db: database((statement) =>
                statement.sql === 'SELECT * FROM members WHERE id = ?' ? member({ bill_annually: 1, discount_type: 'student', discount_status: status }) : undefined,
            ),
            fetch: async (url, init) => {
                if (url.includes('/subscriptions?')) return response({ data: [], has_more: false });
                forms.push(new URLSearchParams(init.body));
                assert.ok(init.headers['Idempotency-Key']);
                return response({
                    id: 'cs_checkout',
                    url: 'https://checkout.stripe.com/test',
                    status: 'open',
                    expires_at: time + 3600,
                });
            },
        });
        const result = await h.internal('/checkout', { member_id: 1 });
        if (status === 'requested') {
            assert.equal(result.status, 409);
            assert.match((await result.json()).error, /pending approval/);
            assert.equal(forms.length, 0);
            continue;
        }
        assert.equal(result.status, 200);
        assert.equal(forms[0].get('mode'), 'subscription');
        assert.equal(forms[0].has('submit_type'), false);
        assert.ok([...forms[0].keys()].every((key) => !key.startsWith('payment_intent_data')));
        assert.equal(forms[0].get('line_items[0][price]'), 'price_year');
        assert.equal(forms[0].get('discounts[0][coupon]'), 'coupon_student');
        assert.equal(forms[0].get('subscription_data[metadata][conway_member_id]'), '1');
    }
});
test('pending discounts block new and lapsed subscription checkout without financial writes', async () => {
    for (const discount_type of ['student', 'family']) {
        for (const stripe_customer_id of [null, 'cus_member']) {
            for (const stripe_subscription_state of [null, 'canceled', 'incomplete_expired']) {
                const who = member({ stripe_customer_id, stripe_subscription_state, discount_type, discount_status: 'requested' });
                const calls = [];
                const h = harness({
                    db: database((statement) => (statement.sql === 'SELECT * FROM members WHERE id = ?' ? who : undefined)),
                    fetch: async (url, init) => {
                        calls.push({ url, method: init.method });
                        assert.ok(url.includes('/subscriptions?'));
                        return response({ data: [], has_more: false });
                    },
                });
                const result = await h.internal('/checkout', { member_id: 1 });
                assert.equal(result.status, 409);
                assert.deepEqual(await result.json(), { error: 'Your discount request is pending approval. Wait for approval before starting payment.' });
                assert.equal(calls.length, stripe_customer_id ? 1 : 0);
                assert.ok(calls.every((call) => call.method === 'GET'));
                assert.equal(h.storage.size, 0);
                assert.ok(h.db.calls.every((call) => call.method === 'first'));
            }
        }
    }
});
test('pending discount blocks cached open checkout reuse with old, new, or absent request keys', async () => {
    const who = member();
    const calls = [];
    const h = harness({
        db: database((statement) => (statement.sql === 'SELECT * FROM members WHERE id = ?' ? who : undefined)),
        fetch: async (url, init) => {
            calls.push({ url, method: init.method });
            if (url.includes('/subscriptions?')) return response({ data: [], has_more: false });
            return response({ id: 'cs_cached', url: 'https://checkout.stripe.com/cached', status: 'open', expires_at: time + 3600 });
        },
    });
    const input = { member_id: 1, request_key: 'request-old' };
    assert.equal((await h.internal('/checkout', input)).status, 200);
    assert.equal(new URLSearchParams(h.storage.get('checkout:1:membership').form).has('discounts[0][coupon]'), false);
    // Simulate a pre-existing full-price session for a pending member, without an epoch revocation masking the hold.
    who.discount_type = 'student';
    who.discount_status = 'requested';
    const stored = structuredClone([...h.storage]);
    calls.length = 0;
    for (const request_key of ['request-old', 'request-new', undefined]) {
        const result = await h.internal('/checkout', { member_id: 1, request_key });
        assert.equal(result.status, 409);
        assert.match((await result.json()).error, /pending approval/);
    }
    assert.equal(calls.length, 3);
    assert.ok(calls.every((call) => call.method === 'GET' && call.url.includes('/subscriptions?')));
    assert.deepEqual([...h.storage], stored);
});
test('different concurrent checkout keys share one customer and one open session', async () => {
    const who = member({ stripe_customer_id: null });
    let customers = 0;
    let sessions = 0;
    const h = harness({
        db: database((statement) => {
            if (statement.sql === 'SELECT * FROM members WHERE id = ?') return { ...who };
            if (statement.sql.startsWith('UPDATE members SET stripe_customer_id')) who.stripe_customer_id = statement.values[0];
        }),
        fetch: async (url, init) => {
            if (url.endsWith('/customers')) {
                customers++;
                return response({ id: 'cus_created' });
            }
            if (url.includes('/subscriptions?')) return response({ data: [], has_more: false });
            if (init.method === 'POST') sessions++;
            return response({
                id: 'cs_checkout',
                url: 'https://checkout.stripe.com/test',
                status: 'open',
                expires_at: time + 3600,
            });
        },
    });
    const input = { member_id: 1 };
    const results = await Promise.all(['request-a', 'request-b'].map((request_key) => h.internal('/checkout', { ...input, request_key })));
    assert.ok(results.every((result) => result.status === 200));
    assert.equal(customers, 1);
    assert.equal(sessions, 1);
    assert.equal((await h.internal('/checkout', { ...input, request_key: 'request-a' })).status, 200);
});
test('ambiguous customer creation retries with the same persisted Stripe idempotency key', async () => {
    const who = member({ stripe_customer_id: null });
    const keys = [];
    const h = harness({
        db: database((statement) => {
            if (statement.sql === 'SELECT * FROM members WHERE id = ?') return { ...who };
            if (statement.sql.startsWith('UPDATE members SET stripe_customer_id')) who.stripe_customer_id = statement.values[0];
        }),
        fetch: async (url, init) => {
            if (url.endsWith('/customers')) {
                keys.push(init.headers['Idempotency-Key']);
                if (keys.length === 1) throw new Error('connection lost after accepted write');
                return response({ id: 'cus_created' });
            }
            if (url.includes('/subscriptions?')) return response({ data: [], has_more: false });
            return response({ id: 'cs_checkout', url: 'https://checkout.stripe.com/membership' });
        },
    });
    const input = { member_id: 1 };
    assert.equal((await h.internal('/checkout', input)).status, 502);
    assert.equal((await h.internal('/checkout', input)).status, 200);
    assert.equal(keys.length, 2);
    assert.equal(keys[0], keys[1]);
});
test('existing subscriptions use billing portal instead of creating a second subscription', async () => {
    const calls = [];
    const who = member({ stripe_subscription_id: 'sub_active', stripe_subscription_state: 'active' });
    const h = harness({
        db: database((statement) => (statement.sql === 'SELECT * FROM members WHERE id = ?' ? who : undefined)),
        fetch: async (url, init) => {
            calls.push({ url, init });
            if (url.includes('/subscriptions?'))
                return response({
                    data: [{ id: 'sub_active', status: 'active', customer: 'cus_member', created: 1 }],
                    has_more: false,
                });
            assert.ok(url.endsWith('/billing_portal/sessions'));
            return response({ url: 'https://billing.stripe.com/portal' });
        },
    });
    assert.equal((await h.internal('/checkout', { member_id: 1 })).status, 200);
    assert.equal(calls.length, 2);
});

test('pending discounts preserve reconciled subscriber portal access and cached portal reuse', async () => {
    for (const status of ['active', 'trialing', 'past_due', 'unpaid', 'incomplete', 'paused']) {
        // No local subscription yet: the live lookup must discover it before applying the hold.
        const who = member({ discount_type: 'family', discount_status: 'requested' });
        const calls = [];
        const h = harness({
            db: database((statement) => {
                if (statement.sql === 'SELECT * FROM members WHERE id = ?') return { ...who };
                if (statement.sql.startsWith('UPDATE members SET stripe_subscription_id')) {
                    who.stripe_subscription_id = statement.values[0];
                    who.stripe_subscription_state = statement.values[1];
                }
            }),
            fetch: async (url, init) => {
                calls.push({ url, method: init.method });
                if (url.includes('/subscriptions?'))
                    return response({
                        data: [{ id: 'sub_existing', status, customer: 'cus_member', created: 1, metadata: { conway_member_id: '1' } }],
                        has_more: false,
                    });
                assert.ok(url.endsWith('/billing_portal/sessions'));
                assert.deepEqual(Object.fromEntries(new URLSearchParams(init.body)), { customer: 'cus_member', return_url: h.env.SITE_URL });
                return response({ url: 'https://billing.stripe.com/portal' });
            },
        });
        for (let attempt = 0; attempt < 2; attempt++) {
            const result = await h.internal('/checkout', { member_id: 1, request_key: 'request-portal' });
            assert.equal(result.status, 200);
            assert.deepEqual(await result.json(), { url: 'https://billing.stripe.com/portal' });
        }
        assert.equal(who.stripe_subscription_state, status);
        assert.equal(who.discount_status, 'requested');
        assert.equal(calls.filter((call) => call.method === 'POST').length, 1);
        assert.equal(calls.filter((call) => call.url.includes('/subscriptions?')).length, 2);
        assert.ok(h.db.calls.every((call) => !call.sql.includes('root_family_member IS NULL')));
    }
});

test('family approval SQL requires linked active primary and family checkout fails closed', async () => {
    const h = harness({
        db: database((statement) => {
            if (statement.sql.startsWith('SELECT id FROM members WHERE discord_user_id')) return { id: 9 };
        }),
    });
    const approvalResponse = await h.api.integrationRoute(await signedDiscord(h.env, approval), h.env);
    assert.equal((await approvalResponse.json()).data.flags, 64);
    const sql = h.db.calls.find((call) => call.sql.startsWith('UPDATE members')).sql;
    assert.match(sql, /root.id = members.root_family_member/);
    assert.match(sql, /root.root_family_member IS NULL AND root.payment_status IS NOT NULL/);
    for (const root of [null, 1, 2]) {
        const blocked = harness({
            db: database((statement) =>
                statement.sql === 'SELECT * FROM members WHERE id = ?'
                    ? member({
                          discount_type: 'family',
                          discount_status: 'approved',
                          root_family_member: root,
                      })
                    : undefined,
            ),
            fetch: async (url) => {
                assert.ok(url.includes('/subscriptions?'));
                return response({ data: [], has_more: false });
            },
        });
        assert.equal(
            (
                await blocked.internal('/checkout', {
                    member_id: 1,
                })
            ).status,
            409,
        );
    }
});

test('definitive Stripe validation rejection clears slot while ambiguous failures retain it', async () => {
    for (const [status, data, clears] of [
        [400, { error: { type: 'invalid_request_error', code: 'resource_missing' } }, true],
        [500, { error: { type: 'api_error' } }, false],
        [400, { error: { type: 'idempotency_error' } }, false],
        [400, {}, false],
    ]) {
        let attempt = 0;
        const keys = [];
        const h = harness({
            db: database((statement) => (statement.sql === 'SELECT * FROM members WHERE id = ?' ? member() : undefined)),
            fetch: async (url, init) => {
                if (url.includes('/subscriptions?')) return response({ data: [], has_more: false });
                keys.push(init.headers['Idempotency-Key']);
                if (!attempt++) return response(data, status);
                return response({ id: 'cs_retry', url: 'https://checkout.stripe.com/retry' });
            },
        });
        const input = { member_id: 1 };
        assert.equal((await h.internal('/checkout', input)).status, 502);
        assert.equal(h.storage.has('checkout:1:membership'), !clears);
        assert.equal((await h.internal('/checkout', input)).status, 200);
        assert.equal(keys[0] === keys[1], !clears);
    }
});

function mutationHarness({ who = member(), leader = true, cas = true, fetch, enabled = 'true', afterRead, root = null } = {}) {
    const batches = [];
    let current = who;
    const h = harness({
        enabled,
        fetch:
            fetch ||
            (async (url) => {
                if (url.includes('/subscriptions?') || url.includes('/checkout/sessions?')) return response({ data: [], has_more: false });
                throw new Error(`Unexpected fetch ${url}`);
            }),
        db: database((statement, method) => {
            if (statement.sql === 'SELECT * FROM members WHERE id = ?') {
                afterRead?.();
                return current ? { ...current } : null;
            }
            if (statement.sql === 'SELECT id FROM members WHERE id = ? AND leadership = 1') return leader ? { id: 9 } : null;
            if (statement.sql.startsWith('SELECT id FROM members WHERE id = ? AND root_family_member IS NULL')) return root;
            if (method === 'batch') {
                batches.push(statement.statements);
                const change = statement.statements[0];
                const success = cas && current !== null;
                if (success) {
                    if (change.sql.startsWith('DELETE')) current = null;
                    else {
                        const keys = change.sql
                            .split(' SET ')[1]
                            .split(' WHERE ')[0]
                            .split(', ')
                            .map((part) => part.split(' = ')[0]);
                        current = {
                            ...current,
                            ...Object.fromEntries(keys.map((key, i) => [key, change.values[i]])),
                            version: current.version + 1,
                        };
                    }
                }
                return [{ results: success ? [{ id: 1 }] : [], meta: { changes: success ? 1 : 0 } }, { results: [] }, { results: current ? [current] : [] }];
            }
        }),
    });
    return { ...h, batches, current: () => current };
}

test('member mutations require leadership and matching version before any external effects', async () => {
    for (const [leader, version, actor, expected] of [
        [false, 1, 1, 403],
        [true, 2, 9, 409],
    ]) {
        const h = mutationHarness({
            leader,
            fetch: async () => {
                assert.fail('must not fetch');
            },
        });
        assert.equal((await h.api.mutateMember(h.env, 1, { discount_type: null }, actor, version)).status, expected);
        assert.equal(h.batches.length, 0);
    }
});

test('self discount removal preserves family link and uses atomic CAS plus conditional audit', async () => {
    const h = mutationHarness({
        who: member({ discount_type: 'family', root_family_member: 2 }),
        root: { id: 2 },
        enabled: 'false',
    });
    const result = await h.api.mutateDiscount(h.env, 1, null, 1);
    assert.equal(result.status, 200);
    assert.deepEqual(await result.json(), { updated: true, version: 2 });
    const statements = h.batches[0];
    assert.match(statements[0].sql, /WHERE id = \? AND version = \?/);
    assert.doesNotMatch(statements[0].sql, /SET.*root_family_member/);
    assert.match(statements[1].sql, /WHERE changes\(\) = 1/);
    assert.equal(h.current().root_family_member, 2);
    assert.equal(h.storage.has('mutation-pending:1'), false);
});

test('self discount requests validate configuration and generate server request IDs', async () => {
    const invalid = mutationHarness();
    assert.equal((await invalid.api.mutateDiscount(invalid.env, 1, 'unconfigured', 1)).status, 400);
    assert.equal(invalid.batches.length, 0);
    const h = mutationHarness();
    assert.equal((await h.api.mutateDiscount(h.env, 1, 'student', 1)).status, 200);
    assert.equal(h.current().discount_status, 'requested');
    assert.match(h.current().discount_request_id, /^[A-Za-z0-9_-]+$/);
});

test('approval releases the pending payment hold and checkout applies the approved coupon', async () => {
    const forms = [];
    const h = mutationHarness({
        who: member({ discount_type: 'student', discount_status: 'requested' }),
        fetch: async (url, init) => {
            if (url.includes('/subscriptions?') || url.includes('/checkout/sessions?')) return response({ data: [], has_more: false });
            assert.ok(url.endsWith('/checkout/sessions'));
            forms.push(new URLSearchParams(init.body));
            return response({ id: 'cs_approved', url: 'https://checkout.stripe.com/approved' });
        },
    });
    const input = { member_id: 1, request_key: 'request-approval' };
    assert.equal((await h.internal('/checkout', input)).status, 409);
    assert.equal(forms.length, 0);
    assert.equal((await h.api.mutateMember(h.env, 1, { discount_status: 'approved' }, 9, 1)).status, 200);
    const result = await h.internal('/checkout', input);
    assert.equal(result.status, 200);
    assert.deepEqual(await result.json(), { url: 'https://checkout.stripe.com/approved' });
    assert.equal(forms.length, 1);
    assert.equal(forms[0].get('discounts[0][coupon]'), 'coupon_student');
});

test('approved discount without configured coupon never falls back to full-price checkout', async () => {
    const h = harness({
        db: database((statement) => (statement.sql === 'SELECT * FROM members WHERE id = ?' ? member({ discount_type: 'unconfigured', discount_status: 'approved' }) : undefined)),
        fetch: async (url, init) => {
            assert.equal(init.method, 'GET');
            assert.ok(url.includes('/subscriptions?'));
            return response({ data: [], has_more: false });
        },
    });
    const result = await h.internal('/checkout', { member_id: 1 });
    assert.equal(result.status, 400);
    assert.match((await result.json()).error, /discount coupon is not configured/);
    assert.equal(h.storage.size, 0);
});

test('post-network CAS conflict fails without claiming success and leaves checkout blocked', async () => {
    const h = mutationHarness({ cas: false, who: member({ discount_type: 'student' }) });
    assert.equal((await h.api.mutateMember(h.env, 1, { discount_type: null }, 9, 1)).status, 409);
    assert.match(h.batches[0][0].sql, /EXISTS.*actor.leadership = 1/);
    assert.match(h.batches[0][1].sql, /changes\(\) = 1/);
    assert.equal((await h.internal('/checkout', { member_id: 1 })).status, 409);
});

test('revocation expires stored and paginated live sessions before member CAS', async () => {
    const states = new Map([
        ['cs_known', 'open'],
        ['cs_page1', 'open'],
        ['cs_page2', 'open'],
    ]);
    const calls = [];
    let listed = 0;
    const h = mutationHarness({
        who: member({ discount_type: 'student' }),
        fetch: async (url, init) => {
            calls.push({ url, method: init.method });
            const parsed = new URL(url);
            if (parsed.pathname === '/v1/subscriptions') return response({ data: [], has_more: false });
            if (parsed.pathname === '/v1/checkout/sessions') {
                listed++;
                if (listed > 2) return response({ data: [], has_more: false });
                if (listed === 2) assert.equal(parsed.searchParams.get('starting_after'), 'cs_page1');
                const id = listed === 1 ? 'cs_page1' : 'cs_page2';
                return response({
                    data: [{ id, customer: 'cus_member', status: states.get(id) }],
                    has_more: listed === 1,
                });
            }
            const id = parsed.pathname.split('/')[4];
            if (init.method === 'POST') states.set(id, 'expired');
            return response({ id, customer: 'cus_member', status: states.get(id) });
        },
    });
    h.storage.set('checkout:1:membership', {
        key: 'key',
        created: time,
        path: '/checkout/sessions',
        form: {},
        result: { id: 'cs_known' },
    });
    assert.equal((await h.api.mutateMember(h.env, 1, { discount_type: null }, 9, 1)).status, 200);
    assert.ok([...states.values()].every((value) => value === 'expired'));
    assert.equal(calls.filter((call) => call.method === 'POST').length, 3);
    assert.equal(h.storage.has('checkout:1:membership'), false);
    assert.equal(h.batches.length, 1);
});

test('unconfirmed expiration fails closed and preserves pending mutation across retry', async () => {
    const h = mutationHarness({
        who: member({ discount_type: 'student' }),
        fetch: async (url, init) => {
            if (init.method === 'POST') return response({}, 503);
            if (url.includes('?'))
                return response({
                    data: [{ id: 'cs_open', customer: 'cus_member', status: 'open' }],
                    has_more: false,
                });
            return response({ id: 'cs_open', customer: 'cus_member', status: 'open' });
        },
    });
    assert.equal((await h.api.mutateDiscount(h.env, 1, null, 1)).status, 502);
    assert.equal(h.batches.length, 0);
    assert.equal(h.storage.get('mutation-pending:1'), true);
});

test('deletion blocks PayPal, active/uncertain states, and any Stripe subscription history', async () => {
    for (const who of [member({ paypal_subscription_id: 'paypal' }), member({ stripe_subscription_state: 'active' }), member({ stripe_subscription_state: 'unknown' })]) {
        const h = mutationHarness({
            who,
            fetch: async () => {
                assert.fail('must block before network');
            },
        });
        assert.equal((await h.api.mutateMember(h.env, 1, null, 9, 1)).status, 409);
        assert.equal(h.batches.length, 0);
    }
    const h = mutationHarness({
        fetch: async (url) =>
            response({
                data: url.includes('/subscriptions?') ? [{ id: 'sub_old', customer: 'cus_member', status: 'canceled' }] : [],
                has_more: false,
            }),
    });
    assert.equal((await h.api.mutateMember(h.env, 1, null, 9, 1)).status, 409);
    assert.equal(h.batches.length, 0);
});

test('safe physical deletion audits atomically and cached checkout cannot resurrect deleted member', async () => {
    const h = mutationHarness();
    assert.equal((await h.api.mutateMember(h.env, 1, null, 9, 1)).status, 200);
    assert.match(h.batches[0][0].sql, /DELETE FROM members WHERE id = \? AND version = \?/);
    assert.equal(h.current(), null);
    const key = 'request-a';
    const hashed = Buffer.from(await webcrypto.subtle.digest('SHA-256', new TextEncoder().encode(key))).toString('hex');
    h.storage.set(`request:1:membership:${hashed}`, {
        url: 'https://checkout.stripe.com/old',
    });
    assert.equal(
        (
            await h.internal('/checkout', {
                member_id: 1,
                request_key: key,
            })
        ).status,
        404,
    );
});

test('checkout waits for revocation and cannot recreate a discounted session concurrently', async () => {
    let release;
    const gate = new Promise((resolve) => {
        release = resolve;
    });
    let entered = false;
    const h = mutationHarness({
        fetch: async (url) => {
            if (url.includes('/checkout/sessions?') && !entered) {
                entered = true;
                await gate;
            }
            return response({ data: [], has_more: false });
        },
    });
    const mutation = h.api.mutateMember(h.env, 1, null, 9, 1);
    while (!entered) await new Promise((resolve) => setTimeout(resolve, 1));
    const checkout = h.internal('/checkout', {
        member_id: 1,
    });
    release();
    assert.equal((await mutation).status, 200);
    assert.equal((await checkout).status, 404);
});

test('revoked cached request key cannot return or recreate its old discounted checkout', async () => {
    const h = mutationHarness({ who: member({ discount_type: 'student' }) });
    const input = {
        member_id: 1,
        request_key: 'request-old',
    };
    const digest = async (value) => Buffer.from(await webcrypto.subtle.digest('SHA-256', new TextEncoder().encode(value))).toString('hex');
    const slot = `request:1:membership:${await digest(input.request_key)}`;
    h.storage.set(slot, {
        url: 'https://checkout.stripe.com/old',
        epoch: 0,
    });
    assert.equal((await h.api.mutateDiscount(h.env, 1, null, 1)).status, 200);
    const result = await h.internal('/checkout', input);
    assert.equal(result.status, 409);
    assert.match((await result.json()).error, /revoked/);
});

test('completed session racing expiry is reconciled before discount change, not retroactively canceled', async () => {
    let openList = 0;
    const order = [];
    const h = mutationHarness({
        who: member({ discount_type: 'student' }),
        fetch: async (url, init) => {
            if (url.includes('/subscriptions?')) {
                order.push('subscriptions');
                return response({
                    data: [
                        {
                            id: 'sub_completed',
                            customer: 'cus_member',
                            created: 1,
                            status: 'active',
                            metadata: { conway_member_id: '1' },
                        },
                    ],
                    has_more: false,
                });
            }
            if (url.includes('/checkout/sessions?'))
                return response({
                    data: openList++ ? [] : [{ id: 'cs_raced', customer: 'cus_member', status: 'open' }],
                    has_more: false,
                });
            if (init.method === 'POST') {
                assert.ok(url.endsWith('/expire'));
                order.push('expire');
                return response({ error: { type: 'invalid_request_error' } }, 400);
            }
            return response({
                id: 'cs_raced',
                customer: 'cus_member',
                status: 'complete',
                subscription: 'sub_completed',
            });
        },
    });
    assert.equal((await h.api.mutateDiscount(h.env, 1, null, 1)).status, 200);
    assert.deepEqual(order, ['expire', 'subscriptions']);
    assert.ok(h.db.calls.some((call) => call.sql.startsWith('UPDATE members SET stripe_subscription_id') && call.values[0] === 'sub_completed'));
    assert.equal(h.batches.length, 1);
});

test('linked active family primary permits family coupon, inactive primary never does', async () => {
    let coupon;
    const h = harness({
        db: database((statement) => {
            if (statement.sql === 'SELECT * FROM members WHERE id = ?')
                return member({
                    discount_type: 'family',
                    discount_status: 'approved',
                    root_family_member: 2,
                });
            if (statement.sql.startsWith('SELECT id FROM members WHERE id = ? AND root_family_member IS NULL')) return { id: 2 };
        }),
        fetch: async (url, init) => {
            if (url.includes('/subscriptions?')) return response({ data: [], has_more: false });
            coupon = new URLSearchParams(init.body).get('discounts[0][coupon]');
            return response({ id: 'cs_family', url: 'https://checkout.stripe.com/family' });
        },
    });
    assert.equal((await h.internal('/checkout', { member_id: 1 })).status, 200);
    assert.equal(coupon, 'coupon_family');
});

test('manual no-billing update needs no network with automation disabled', async () => {
    const h = mutationHarness({
        who: member({ stripe_customer_id: null }),
        enabled: 'false',
        fetch: async () => assert.fail('No billing mapping, no fetch'),
    });
    assert.equal((await h.api.mutateDiscount(h.env, 1, null, 1)).status, 200);
});

test('definitively rejected delete clears marker after safe revocation and permits billing again', async () => {
    const h = mutationHarness({
        fetch: async (url) => {
            if (url.includes('/checkout/sessions?')) return response({ data: [], has_more: false });
            if (url.includes('/subscriptions?'))
                return response({
                    data: [{ id: 'sub_old', customer: 'cus_member', status: 'canceled', created: 1 }],
                    has_more: false,
                });
            assert.ok(url.endsWith('/checkout/sessions'));
            return response({ id: 'cs_new', url: 'https://checkout.stripe.com/new' });
        },
    });
    // Also recovers a marker left by a previous interrupted attempt once revocation is confirmed.
    h.storage.set('mutation-pending:1', true);
    assert.equal((await h.api.mutateMember(h.env, 1, null, 9, 1)).status, 409);
    assert.equal(h.storage.has('mutation-pending:1'), false);
    assert.equal(h.batches.length, 0);
    assert.equal((await h.internal('/checkout', { member_id: 1 })).status, 200);
});

test('delete with unresolved subscription lookup retains pending marker', async () => {
    const h = mutationHarness({
        fetch: async (url) => (url.includes('/subscriptions?') ? response({}, 503) : response({ data: [], has_more: false })),
    });
    assert.equal((await h.api.mutateMember(h.env, 1, null, 9, 1)).status, 502);
    assert.equal(h.storage.get('mutation-pending:1'), true);
    assert.equal(h.batches.length, 0);
});

test('inactive family primary does not prevent existing subscriber opening billing portal', async () => {
    const who = member({
        discount_type: 'family',
        discount_status: 'approved',
        root_family_member: 2,
        stripe_subscription_id: 'sub_active',
        stripe_subscription_state: 'active',
    });
    const h = harness({
        db: database((statement) => (statement.sql === 'SELECT * FROM members WHERE id = ?' ? who : undefined)),
        fetch: async (url) => {
            if (url.includes('/subscriptions?'))
                return response({
                    data: [{ id: 'sub_active', customer: 'cus_member', status: 'active', created: 1 }],
                    has_more: false,
                });
            assert.ok(url.endsWith('/billing_portal/sessions'));
            return response({ url: 'https://billing.stripe.com/cancel' });
        },
    });
    const result = await h.internal('/checkout', {
        member_id: 1,
    });
    assert.equal(result.status, 200);
    assert.deepEqual(await result.json(), { url: 'https://billing.stripe.com/cancel' });
    assert.equal(
        h.db.calls.some((call) => call.sql.includes('root_family_member IS NULL')),
        false,
    );
});

test('unrelated and full-form security edits bypass unchanged inactive-family billing during Stripe outage', async () => {
    for (const fullForm of [false, true]) {
        const who = member({
            discount_type: 'family',
            discount_status: 'approved',
            root_family_member: 2,
            confirmed: 1,
            non_billable: 0,
            bill_annually: 0,
            leadership: 1,
        });
        let fetched = false;
        const h = mutationHarness({
            who,
            fetch: async () => {
                fetched = true;
                return response({}, 503);
            },
        });
        const fields = {
            discord_user_id: null,
            leadership: 0,
            ...(fullForm
                ? {
                      discount_type: who.discount_type,
                      discount_status: who.discount_status,
                      discount_request_id: who.discount_request_id,
                      root_family_member: who.root_family_member,
                      confirmed: who.confirmed,
                      non_billable: who.non_billable,
                      bill_annually: who.bill_annually,
                  }
                : {}),
        };
        h.storage.set('checkout:1:membership', { result: { id: 'cs_keep' } });
        h.storage.set('checkout-epoch:1', 7);
        assert.equal((await h.api.mutateMember(h.env, 1, fields, 9, 1)).status, 200);
        assert.equal(fetched, false);
        assert.equal(h.current().discord_user_id, null);
        assert.equal(h.current().leadership, 0);
        assert.equal(h.storage.get('checkout:1:membership').result.id, 'cs_keep');
        assert.equal(h.storage.get('checkout-epoch:1'), 7);
        assert.doesNotMatch(h.batches[0][0].sql, /root.payment_status/);
        assert.equal(
            h.db.calls.some((call) => call.sql.includes('root_family_member IS NULL')),
            false,
        );
    }
});

test('granting or changing approved family discount still requires an active primary', async () => {
    for (const [who, fields] of [
        [member({ root_family_member: 2 }), { discount_type: 'family', discount_status: 'approved' }],
        [member({ discount_type: 'family', discount_status: 'requested', root_family_member: 2 }), { discount_status: 'approved' }],
        [member({ discount_type: 'family', discount_status: 'approved', root_family_member: 2 }), { root_family_member: 3 }],
    ]) {
        const h = mutationHarness({ who });
        assert.equal((await h.api.mutateMember(h.env, 1, fields, 9, 1)).status, 409);
        assert.equal(h.batches.length, 0);
        assert.equal(h.storage.has('mutation-pending:1'), false);
    }
});
