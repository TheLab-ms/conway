import { createExecutionContext, waitOnExecutionContext } from 'cloudflare:test';
import { env } from 'cloudflare:workers';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { hash, now } from '../src/http';
import type { Member, Session } from '../src/types';
import worker from '../src/index';
import { api, login, member, responseCookie } from './fixtures';

afterEach(() => vi.restoreAllMocks());

async function signupSession(fields: Record<string, string> = {}) {
    const headers = await login();
    const pending = {
        email: 'new@example.test',
        discord_user_id: '123456789012345678',
        discord_username: 'new-member',
        return_to: '/dashboard',
        ...fields,
    };
    await env.DB.prepare('UPDATE sessions SET signup=? WHERE token_hash=?')
        .bind(JSON.stringify(pending), await hash(headers.Cookie.split('=')[1]))
        .run();
    return { headers, pending };
}

describe('session and signup through default fetch', () => {
    it('creates and reuses an anonymous CSRF session without leaking hashes', async () => {
        const response = await api('/api/session');
        expect(response.status).toBe(200);
        const data = await response.json<{ member: null; signup: null; csrf_token: string }>();
        expect(data).toEqual({
            member: null,
            signup: null,
            csrf_token: expect.stringMatching(/^[a-f0-9]{64}$/),
        });
        expect(response.headers.get('set-cookie')).toContain('HttpOnly; SameSite=Lax');
        expect(response.headers.get('set-cookie')).toContain('Secure');
        const again = await api('/api/session', 'GET', { Cookie: responseCookie(response) });
        expect(await again.json()).toEqual(data);
        expect(again.headers.get('set-cookie')).toBeNull();
        expect(await env.DB.prepare('SELECT count(*) n FROM sessions').first('n')).toBe(1);
    });

    it('enforces the anonymous D1 rate limit per trusted client IP and resets expired buckets', async () => {
        const ip = '192.0.2.20';
        const key = `auth:anonymous:${await hash(ip)}`;
        await env.DB.prepare('INSERT INTO rate_limits(key,count,expires) VALUES(?,60,?)')
            .bind(key, now() + 600)
            .run();
        expect((await api('/api/session', 'GET', { 'CF-Connecting-IP': ip })).status).toBe(429);
        expect(await env.DB.prepare('SELECT count(*) n FROM sessions').first('n')).toBe(0);
        expect(
            (
                await api('/api/session', 'GET', {
                    'CF-Connecting-IP': ip,
                    'X-Forwarded-For': '192.0.2.99',
                })
            ).status,
        ).toBe(429);
        expect((await api('/api/session', 'GET', { 'CF-Connecting-IP': '192.0.2.21' })).status).toBe(200);
        await env.DB.prepare('UPDATE rate_limits SET expires=? WHERE key=?')
            .bind(now() - 1, key)
            .run();
        expect((await api('/api/session', 'GET', { 'CF-Connecting-IP': ip })).status).toBe(200);
        expect(await env.DB.prepare('SELECT count FROM rate_limits WHERE key=?').bind(key).first('count')).toBe(1);
    });

    it('creates a confirmed Discord member and rotates token/CSRF despite member-created triggers', async () => {
        const { headers, pending } = await signupSession({ return_to: '/fobs/bind?token=example' });
        const before = await (await api('/api/session', 'GET', headers)).json<{ signup: unknown }>();
        expect(before.signup).toEqual({
            email: pending.email,
            discord_user_id: pending.discord_user_id,
        });
        const response = await api('/api/signup', 'POST', headers, {
            name: ' New Member ',
            heard_about: ' Friend ',
        });
        expect(response.status).toBe(200);
        expect(await response.json()).toEqual({ return_to: pending.return_to });
        const cookie = responseCookie(response);
        expect(cookie).not.toBe(headers.Cookie);
        const record = await env.DB.prepare('SELECT * FROM members').first<Member>();
        expect(record).toMatchObject({
            email: pending.email,
            confirmed: 1,
            name: 'New Member',
            heard_about: 'Friend',
            discord_user_id: pending.discord_user_id,
        });
        const session = await env.DB.prepare('SELECT * FROM sessions').first<Session>();
        expect(session).toMatchObject({
            member: record!.id,
            signup: null,
            token_hash: await hash(cookie.split('=')[1]),
        });
        expect(session?.csrf_token).not.toBe(headers['X-CSRF-Token']);
        expect(session!.expires).toBeGreaterThan(now() + 29 * 86400);
        expect(await env.DB.prepare("SELECT count(*) n FROM jobs WHERE kind='signup'").first('n')).toBe(1);
        expect((await api('/api/member', 'GET', { Cookie: cookie })).status).toBe(200);
        expect((await api('/api/member', 'GET', headers)).status).toBe(401);
        expect((await api('/api/signup', 'POST', headers, { name: 'Replay', heard_about: '' })).status).toBe(403);
    });

    it.each(['email', 'discord_user_id'])('does not rotate or authenticate signup on existing %s collision', async (field) => {
        const { headers, pending } = await signupSession();
        const victim = await member({ [field]: pending[field as keyof typeof pending] });
        const response = await api('/api/signup', 'POST', headers, {
            name: 'Attacker',
            heard_about: '',
        });
        expect(response.status).toBe(409);
        expect(response.headers.get('set-cookie')).toBeNull();
        expect(await env.DB.prepare('SELECT count(*) n FROM members').first('n')).toBe(1);
        expect(await env.DB.prepare('SELECT member FROM sessions').first('member')).toBeNull();
        expect((await api('/api/member', 'GET', headers)).status).toBe(401);
        expect(await env.DB.prepare('SELECT name FROM members WHERE id=?').bind(victim.id).first('name')).not.toBe('Attacker');
    });

    it('allows one of two pending sessions for the same identity to create/authenticate a member', async () => {
        const first = await signupSession();
        const second = await signupSession();
        const responses = await Promise.all([first, second].map(({ headers }) => api('/api/signup', 'POST', headers, { name: 'New Member', heard_about: '' })));
        expect(responses.map((response) => response.status).sort()).toEqual([200, 409]);
        expect(await env.DB.prepare('SELECT count(*) n FROM members').first('n')).toBe(1);
        expect(await env.DB.prepare('SELECT count(*) n FROM sessions WHERE member IS NOT NULL').first('n')).toBe(1);
        expect(await env.DB.prepare("SELECT count(*) n FROM jobs WHERE kind='signup'").first('n')).toBe(1);
    });

    it('cannot create two members or sessions when the same signup request races itself', async () => {
        const { headers } = await signupSession();
        const responses = await Promise.all([1, 2].map(() => api('/api/signup', 'POST', headers, { name: 'New', heard_about: '' })));
        expect(responses.filter((response) => response.status === 200)).toHaveLength(1);
        expect(responses.filter((response) => [403, 409].includes(response.status))).toHaveLength(1);
        expect(await env.DB.prepare('SELECT count(*) n FROM members').first('n')).toBe(1);
        expect(await env.DB.prepare('SELECT count(*) n FROM sessions').first('n')).toBe(1);
    });

    it('rechecks pending session expiration inside the signup transaction after body streaming', async () => {
        const { headers } = await signupSession();
        const stream = new TransformStream<Uint8Array, Uint8Array>();
        const writer = stream.writable.getWriter();
        // Direct default-handler invocation avoids the service binding buffering the
        // first chunk before auth has read the session; D1 and the handler stay real.
        const ctx = createExecutionContext();
        const response = worker.fetch(
            new Request(`${env.SITE_URL}/api/signup`, {
                method: 'POST',
                headers: { ...headers, 'Content-Type': 'application/json' },
                body: stream.readable,
            }),
            env,
            ctx,
        );
        await writer.write(new TextEncoder().encode('{"name":"New",'));
        await env.DB.prepare('UPDATE sessions SET expires=?')
            .bind(now() - 1)
            .run();
        await writer.write(new TextEncoder().encode('"heard_about":""}'));
        await writer.close();
        expect((await response).status).toBe(409);
        await waitOnExecutionContext(ctx);
        expect(await env.DB.prepare('SELECT count(*) n FROM members').first('n')).toBe(0);
        expect(await env.DB.prepare('SELECT member FROM sessions').first('member')).toBeNull();
    });

    it.each([{ name: '', heard_about: '' }, { name: 'Name' }, { name: 'Name\nInjected', heard_about: '' }, { name: 'x'.repeat(201), heard_about: '' }])(
        'rejects invalid signup input %j without mutation',
        async (data) => {
            const { headers } = await signupSession();
            expect((await api('/api/signup', 'POST', headers, data)).status).toBe(400);
            expect(await env.DB.prepare('SELECT count(*) n FROM members').first('n')).toBe(0);
            expect(await env.DB.prepare('SELECT signup FROM sessions').first('signup')).not.toBeNull();
        },
    );

    it('logout requires CSRF, deletes the session, and expires both cookies', async () => {
        const headers = await login((await member()).id);
        expect((await api('/api/logout', 'POST', { Cookie: headers.Cookie })).status).toBe(403);
        const response = await api('/api/logout', 'POST', headers);
        expect(response.status).toBe(200);
        expect(response.headers.getSetCookie()).toHaveLength(2);
        expect(response.headers.getSetCookie().every((cookie) => cookie.includes('Max-Age=0'))).toBe(true);
        expect((await api('/api/member', 'GET', headers)).status).toBe(401);
    });

    it('invalidates every existing session when leadership relinks a member to another Discord identity', async () => {
        const administrator = await member({ leadership: 1, discord_user_id: '987654321098765432' });
        const admin = await login(administrator.id);
        const target = await member({ discord_user_id: '123456789012345678' });
        const first = await login(target.id);
        const second = await login(target.id);
        const record = await (await api(`/api/admin/members/${target.id}`, 'GET', admin)).json<Member>();
        const response = await api(`/api/admin/members/${target.id}`, 'PATCH', admin, {
            discord_user_id: '111111111111111111',
            version: record.version,
        });
        expect(response.status).toBe(200);
        expect(await env.DB.prepare('SELECT count(*) n FROM sessions WHERE member=?').bind(target.id).first('n')).toBe(0);
        for (const headers of [first, second]) {
            expect((await api('/api/member', 'GET', headers)).status).toBe(401);
            expect((await api('/api/profile', 'PATCH', headers, { name: 'Old identity' })).status).toBe(403);
        }
        expect((await api('/api/admin/members', 'GET', admin)).status).toBe(200);
    });

    it('invalidates sessions on Discord unlink but preserves them for an unchanged identity', async () => {
        const administrator = await member({ leadership: 1, discord_user_id: '987654321098765432' });
        const admin = await login(administrator.id);
        const target = await member({ discord_user_id: '123456789012345678' });
        const headers = await login(target.id);
        for (const discord_user_id of [target.discord_user_id, null]) {
            const record = await (await api(`/api/admin/members/${target.id}`, 'GET', admin)).json<Member>();
            expect(
                (
                    await api(`/api/admin/members/${target.id}`, 'PATCH', admin, {
                        discord_user_id,
                        version: record.version,
                    })
                ).status,
            ).toBe(200);
            expect((await api('/api/member', 'GET', headers)).status).toBe(discord_user_id === null ? 401 : 200);
        }
    });

    it('does not invalidate sessions if a duplicate-identity relink fails', async () => {
        const administrator = await member({ leadership: 1, discord_user_id: '987654321098765432' });
        const admin = await login(administrator.id);
        const target = await member({ discord_user_id: '123456789012345678' });
        const headers = await login(target.id);
        const record = await (await api(`/api/admin/members/${target.id}`, 'GET', admin)).json<Member>();
        expect(
            (
                await api(`/api/admin/members/${target.id}`, 'PATCH', admin, {
                    discord_user_id: administrator.discord_user_id,
                    version: record.version,
                })
            ).status,
        ).toBe(409);
        expect((await api('/api/member', 'GET', headers)).status).toBe(200);
        expect(await env.DB.prepare('SELECT discord_user_id FROM members WHERE id=?').bind(target.id).first('discord_user_id')).toBe(target.discord_user_id);
    });
});

describe('Discord OAuth with only external HTTP mocked', () => {
    it('renders safe non-cached HTML for GET login errors without reflecting callback input', async () => {
        const payload = '<script>alert("login")</script><img src=x onerror=alert(1)>&\'';
        const query = new URLSearchParams({
            state: payload,
            code: 'private-authorization-code',
            error_description: payload,
            return_to: 'javascript:alert(1)',
        });
        const response = await api(`/login/discord/callback?${query}`, 'GET', { Accept: 'text/html' });
        expect(response.status).toBe(400);
        expect(response.headers.get('content-type')).toBe('text/html; charset=utf-8');
        expect(response.headers.get('cache-control')).toBe('no-store');
        expect(response.headers.get('x-content-type-options')).toBe('nosniff');
        expect(response.headers.get('referrer-policy')).toBe('no-referrer');
        expect(response.headers.get('content-security-policy')).toContain("frame-ancestors 'none'");
        expect(response.headers.get('content-security-policy')).not.toContain("'unsafe-inline'");
        expect(response.headers.get('set-cookie')).toBeNull();
        const html = await response.text();
        expect(html).toContain('<!doctype html>');
        expect(html).toContain('<h1>Sign-in unavailable</h1>');
        expect(html).toContain('Invalid or expired Discord sign-in. Please start again.');
        expect(html).toContain('<a href="/login">Return to sign in</a>');
        expect(html).not.toMatch(/<script|<img|onerror|javascript:|private-authorization-code/);
        expect(html).not.toContain(payload);
        expect(await env.DB.prepare('SELECT count(*) n FROM sessions').first('n')).toBe(0);
    });

    it('keeps API and non-GET login errors JSON even when the browser accepts HTML', async () => {
        for (const [path, method, status] of [
            ['/api/member', 'GET', 401],
            ['/login/discord/callback', 'POST', 404],
        ] as const) {
            const response = await api(path, method, { Accept: 'text/html' });
            expect(response.status).toBe(status);
            expect(response.headers.get('content-type')).toContain('application/json');
            expect(response.headers.get('cache-control')).toBe('no-store');
            expect(await response.json()).toHaveProperty('error');
        }
    });

    async function begin(returnTo = '/dashboard') {
        const response = await api(`/login/discord?return_to=${encodeURIComponent(returnTo)}`);
        expect(response.status).toBe(302);
        const target = new URL(response.headers.get('location')!);
        expect(target.origin).toBe('https://discord.com');
        expect(target.searchParams.get('scope')).toBe('identify email');
        return {
            state: target.searchParams.get('state')!,
            cookie: responseCookie(response, 'conway_oauth'),
        };
    }

    function discord(fields: Record<string, unknown> = {}) {
        return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
            const url = String(input);
            if (url === 'https://discord.com/api/v10/oauth2/token') {
                expect(init?.method).toBe('POST');
                return Response.json({ access_token: 'mock-token', token_type: 'Bearer' });
            }
            if (url === 'https://discord.com/api/v10/users/@me') {
                expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer mock-token');
                return Response.json({
                    id: '123456789012345678',
                    username: 'discord-name',
                    email: 'new@example.test',
                    verified: true,
                    ...fields,
                });
            }
            throw new Error(`Unexpected external request: ${url}`);
        });
    }

    it.each(['https://evil.test', '//evil.test', '/\\evil.test', '/%2f%2fevil.test', '/login/discord', '/api/member'])(
        'sanitizes unsafe return URL %s in persisted OAuth state',
        async (returnTo) => {
            const { state } = await begin(returnTo);
            expect(
                await env.DB.prepare('SELECT return_to FROM oauth_states WHERE state_hash=?')
                    .bind(await hash(state))
                    .first('return_to'),
            ).toBe('/dashboard');
        },
    );

    it('requires matching browser cookie and makes successful callback state one-use', async () => {
        const auth = await begin('/directory');
        const external = discord();
        const path = `/login/discord/callback?state=${auth.state}&code=code`;
        expect((await api(path, 'GET', { Cookie: `conway_oauth=${'a'.repeat(64)}` })).status).toBe(400);
        expect(external).not.toHaveBeenCalled();
        const response = await api(path, 'GET', { Cookie: auth.cookie });
        expect(response.status).toBe(302);
        expect(response.headers.get('location')).toBe('/signup?return_to=%2Fdirectory');
        expect(external).toHaveBeenCalledTimes(2);
        expect((await api(path, 'GET', { Cookie: auth.cookie })).status).toBe(400);
        expect(external).toHaveBeenCalledTimes(2);
        const current = await (await api('/api/session', 'GET', { Cookie: responseCookie(response) })).json<{ member: null; signup: unknown }>();
        expect(current).toMatchObject({
            member: null,
            signup: { email: 'new@example.test', discord_user_id: '123456789012345678' },
        });
    });

    it('rejects expired or duplicate state parameters without contacting Discord', async () => {
        const auth = await begin();
        const external = discord();
        const path = `/login/discord/callback?state=${auth.state}&code=code`;
        expect((await api(`${path}&state=${auth.state}`, 'GET', { Cookie: auth.cookie })).status).toBe(400);
        await env.DB.prepare('UPDATE oauth_states SET expires=?')
            .bind(now() - 1)
            .run();
        expect((await api(path, 'GET', { Cookie: auth.cookie })).status).toBe(400);
        expect(external).not.toHaveBeenCalled();
    });

    it('authenticates existing members solely by Discord ID, without changing membership contact email', async () => {
        const row = await member({ email: 'old@example.test', discord_user_id: '123456789012345678' });
        const old = await login();
        const auth = await begin('/directory');
        discord({ email: 'different@example.test' });
        const response = await api(`/login/discord/callback?state=${auth.state}&code=code`, 'GET', {
            Cookie: `${auth.cookie}; ${old.Cookie}`,
        });
        expect(response.status).toBe(302);
        expect(response.headers.get('location')).toBe('/directory');
        expect(await env.DB.prepare('SELECT email,discord_email,discord_username FROM members WHERE id=?').bind(row.id).first()).toEqual({
            email: 'old@example.test',
            discord_email: 'different@example.test',
            discord_username: 'discord-name',
        });
        expect(await env.DB.prepare('SELECT count(*) n FROM sessions').first('n')).toBe(1);
        expect((await (await api('/api/member', 'GET', { Cookie: responseCookie(response) })).json<Member>()).id).toBe(row.id);
    });

    it('refuses to auto-link a matching email with a different Discord identity', async () => {
        await member({ email: 'new@example.test' });
        const auth = await begin();
        discord();
        const response = await api(`/login/discord/callback?state=${auth.state}&code=code`, 'GET', {
            Cookie: auth.cookie,
        });
        expect(response.status).toBe(409);
        expect(response.headers.get('set-cookie')).toBeNull();
        expect(await env.DB.prepare('SELECT discord_user_id FROM members').first('discord_user_id')).toBeNull();
        expect(await env.DB.prepare('SELECT count(*) n FROM sessions').first('n')).toBe(0);
    });

    it('rejects unverified Discord email and consumes failed callback state', async () => {
        const auth = await begin();
        discord({ verified: false });
        expect(
            (
                await api(`/login/discord/callback?state=${auth.state}&code=code`, 'GET', {
                    Cookie: auth.cookie,
                })
            ).status,
        ).toBe(403);
        expect(await env.DB.prepare('SELECT count(*) n FROM sessions').first('n')).toBe(0);
        expect(await env.DB.prepare('SELECT count(*) n FROM oauth_states').first('n')).toBe(0);
    });

    it('does not expose provider errors or issue sessions when the external token exchange fails', async () => {
        const auth = await begin();
        vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('private-provider-error-and-token', { status: 500 }));
        const response = await api(`/login/discord/callback?state=${auth.state}&code=private-code`, 'GET', { Cookie: auth.cookie });
        expect(response.status).toBe(502);
        expect(response.headers.get('content-type')).toBe('text/html; charset=utf-8');
        expect(response.headers.get('cache-control')).toBe('no-store');
        const html = await response.text();
        expect(html).toContain('<h1>Sign-in unavailable</h1>');
        expect(html).not.toMatch(/private-provider|private-code|test-secret/);
        expect(response.headers.get('set-cookie')).toBeNull();
        expect(await env.DB.prepare('SELECT count(*) n FROM sessions').first('n')).toBe(0);
        expect(await env.DB.prepare('SELECT count(*) n FROM oauth_states').first('n')).toBe(0);
    });
});
