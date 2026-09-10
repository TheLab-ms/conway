import application from '../src/index';
export { MembershipCoordinator } from '../src/index';

const realFetch = globalThis.fetch.bind(globalThis);
const origin = 'http://127.0.0.1:8799';
type ProviderObject = Record<string, any>;
const sessions = new Map<string, ProviderObject>();
const mutations = new Map<string, { fingerprint: string; result: ProviderObject }>();
const json = (value: unknown, status = 200) => Response.json(value, { status });
const invalid = (message: string, status = 400) => json({ error: { type: 'invalid_request_error', message } }, status);

// Only external provider traffic is replaced. Application routes, D1, assets,
// sessions, CSRF, and Durable Object coordination run unchanged.
globalThis.fetch = async (input, init) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url);
    if (!['discord.com', 'api.stripe.com'].includes(url.hostname)) return realFetch(input, init);
    // Workerd fetch accepts redirect:error, but its Request constructor does not.
    const request = new Request(input, { ...init, redirect: 'manual' });
    const form = new URLSearchParams(request.method === 'GET' ? url.search : await request.text());
    if (url.hostname === 'discord.com') {
        if (url.pathname === '/api/v10/oauth2/token' && request.method === 'POST') {
            if (form.get('client_id') !== 'browser-test-client' || form.get('client_secret') !== 'browser-test-secret' || form.get('grant_type') !== 'authorization_code' || form.get('redirect_uri') !== `${origin}/login/discord/callback`) return invalid('Invalid OAuth token request');
            const code = form.get('code') || 'existing';
            if (code === 'failure') return json({ error: 'invalid_grant' }, 502);
            return json({ access_token: code, token_type: 'Bearer' });
        }
        if (url.pathname === '/api/v10/users/@me' && request.method === 'GET') {
            const code = request.headers.get('Authorization')?.replace(/^Bearer /, '') || 'existing';
            if (code === 'user-failure') return json({ error: 'unavailable' }, 502);
            const existing = code === 'existing';
            return json({
                id: code === 'invalid-id' ? 'invalid' : existing ? '123456789012345678' : '423456789012345678',
                username: existing ? 'alex' : 'newmaker',
                email: existing || code === 'collision' ? 'alex@example.test' : 'new@example.test',
                verified: code !== 'unverified',
                bot: code === 'bot',
            });
        }
        return invalid(`Unimplemented Discord request: ${request.method} ${url.pathname}`, 404);
    }

    const path = url.pathname.replace(/^\/v1/, '');
    const reject = (message: string, status = 400) => {
        // The application deliberately hides provider response bodies. Keep stub failures diagnosable without logging secrets or billing data.
        console.warn(`Stripe stub rejected ${request.method} ${path}: ${message}`);
        return invalid(message, status);
    };
    if (request.headers.get('Authorization') !== 'Bearer sk_test_browser_only') return reject('Invalid Stripe authorization', 401);
    const version = request.headers.get('Stripe-Version');
    if (version && !/^\d{4}-\d{2}-\d{2}(?:\.[a-z]+)?$/.test(version)) return reject('Invalid Stripe API version');
    const customer = form.get('customer') || '';
    if (customer.startsWith('cus_error_') || form.get('line_items[0][price]') === 'price_error') return invalid('Simulated provider failure', 503);
    if (request.method === 'GET') {
        if (path === '/subscriptions' || path === '/checkout/sessions') {
            const limit = Number(form.get('limit') ?? '10');
            const status = form.get('status');
            const statuses = path === '/subscriptions' ? ['all', 'active', 'canceled', 'incomplete', 'incomplete_expired', 'past_due', 'paused', 'trialing', 'unpaid'] : ['open', 'complete', 'expired'];
            if (!Number.isInteger(limit) || limit < 1 || limit > 100 || (status !== null && !statuses.includes(status))) return reject('Invalid Stripe list limit or status');
            if (form.has('starting_after') && form.has('ending_before')) return reject('Stripe list cursors are mutually exclusive');
            const memberId = customer.match(/^cus_active_(\d+)$/)?.[1];
            let data = path === '/subscriptions'
                ? (memberId ? [{ id: `sub_${memberId}`, customer, status: 'active', created: 1, metadata: { conway_member_id: memberId }, items: { data: [{ price: { id: 'price_month' } }] } }] : [])
                : [...sessions.values()].reverse();
            data = data.filter((item) => (!customer || item.customer === customer) && (status && status !== 'all' ? item.status === status : path !== '/subscriptions' || status === 'all' || item.status !== 'canceled'));
            const cursor = form.get('starting_after') || form.get('ending_before');
            if (cursor) {
                const index = data.findIndex((item) => item.id === cursor);
                if (index < 0) return reject('Unknown Stripe list cursor');
                data = form.has('starting_after') ? data.slice(index + 1) : data.slice(0, index);
            }
            return json({ object: 'list', url: url.pathname, data: form.has('ending_before') ? data.slice(-limit) : data.slice(0, limit), has_more: data.length > limit });
        }
        const session = sessions.get(path.replace(/^\/checkout\/sessions\//, ''));
        return session ? json(session) : reject('Unimplemented Stripe GET', 404);
    }
    if (request.method !== 'POST') return reject('Unsupported Stripe method', 405);
    if (request.headers.get('Content-Type')?.split(';')[0].trim() !== 'application/x-www-form-urlencoded') return reject('Stripe POST requires form encoding');
    const key = request.headers.get('Idempotency-Key');
    if (key && key.length > 255) return reject('Stripe idempotency key is too long');
    const fingerprint = `${path}:${form.toString()}`;
    const prior = key ? mutations.get(key) : undefined;
    if (prior) return prior.fingerprint === fingerprint ? json(prior.result) : json({ error: { type: 'idempotency_error', code: 'idempotency_key_in_use' } }, 400);
    let result: ProviderObject;
    if (path === '/customers') {
        const memberId = form.get('metadata[conway_member_id]');
        if (!memberId || !form.get('email') || !form.get('name')) return reject('Missing customer metadata');
        result = { id: `cus_member_${memberId}`, email: form.get('email'), name: form.get('name'), metadata: { conway_member_id: memberId } };
    } else if (path === '/checkout/sessions' || path === '/billing_portal/sessions') {
        if (!/^cus_\w+$/.test(customer)) return reject('Invalid customer');
        const portal = path === '/billing_portal/sessions';
        if (portal) {
            if (form.get('return_url') !== origin) return reject('Invalid portal return URL');
        } else {
            const memberId = form.get('client_reference_id');
            const donation = form.get('mode') === 'payment';
            if (!['subscription', 'payment'].includes(form.get('mode') || '') || !memberId || form.get('metadata[conway_member_id]') !== memberId || form.get('line_items[0][quantity]') !== '1' || !['price_month', 'price_year', 'price_donate'].includes(form.get('line_items[0][price]') || '') || form.get('success_url') !== origin || form.get('cancel_url') !== origin) return reject('Invalid checkout parameters');
            if (donation ? form.get('submit_type') !== 'donate' || form.get('payment_intent_data[setup_future_usage]') !== 'on_session' || form.get('payment_intent_data[metadata][conway_member_id]') !== memberId || form.has('discounts[0][coupon]') : form.get('subscription_data[metadata][conway_member_id]') !== memberId) return reject('Invalid checkout metadata');
            if (form.has('discounts[0][coupon]') && !['coupon_student', 'coupon_family'].includes(form.get('discounts[0][coupon]')!)) return reject('Unknown coupon', 404);
            // Test invariant: Conway must expire the old subscription checkout before replacing it.
            if (!donation && [...sessions.values()].some((session) => session.customer === customer && session.mode === 'subscription' && session.status === 'open')) return reject('Previous subscription checkout is still open');
        }
        const id = `${portal ? 'bps' : 'cs'}_${crypto.randomUUID().replaceAll('-', '')}`;
        const hosted = new URL(portal ? `https://billing.stripe.com/p/session/${id}` : `https://checkout.stripe.com/c/pay/${id}`);
        hosted.search = form.toString();
        result = { id, url: hosted.href, customer, mode: form.get('mode'), status: 'open', expires_at: Math.floor(Date.now() / 1000) + 1800 };
        if (!portal) sessions.set(id, result);
    } else if (/^\/checkout\/sessions\/cs_\w+\/expire$/.test(path)) {
        const session = sessions.get(path.split('/')[3]);
        if (!session) return reject('Unknown checkout session', 404);
        if (session.status !== 'open') return reject('Only open checkout sessions can be expired');
        session.status = 'expired';
        result = { ...session };
    } else return reject('Unimplemented Stripe POST', 404);
    if (key) mutations.set(key, { fingerprint, result: { ...result } });
    return json(result);
};

export default application;
