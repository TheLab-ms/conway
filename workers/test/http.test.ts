import { env } from 'cloudflare:workers';
import { describe, expect, it } from 'vitest';
import { body, cookieHeader, csrf, hash, json, memberView, now, randomToken, requireLeader, requireMember, session } from '../src/http';
import { login, member } from './fixtures';

describe('bounded JSON body in the Workers runtime', () => {
    const request = (value?: string, contentType = 'application/json') =>
        new Request(env.SITE_URL, {
            method: 'POST',
            headers: { 'Content-Type': contentType },
            body: value,
        });

    it('accepts JSON objects with charset and never caches JSON responses', async () => {
        expect(await body(request('{"name":"Member"}', 'application/json; charset=utf-8'))).toEqual({
            name: 'Member',
        });
        const response = json({ ok: true }, 201);
        expect(response.status).toBe(201);
        expect(response.headers.get('cache-control')).toBe('no-store');
        expect(await response.json()).toEqual({ ok: true });
    });

    it.each(['null', '[]', 'true', '42', '"value"', '{invalid', ''])('rejects non-object or malformed JSON: %s', async (value) => {
        await expect(body(request(value))).rejects.toMatchObject({ status: 400 });
    });

    it('requires a body and a JSON content type', async () => {
        await expect(body(request())).rejects.toMatchObject({ status: 400 });
        await expect(body(request('{}', 'text/plain'))).rejects.toMatchObject({ status: 415 });
        await expect(body(new Request(env.SITE_URL, { method: 'POST', body: '{}' }))).rejects.toMatchObject({ status: 415 });
    });

    it('accepts exactly 128 KiB and rejects one additional byte regardless of claimed content length', async () => {
        const exact = JSON.stringify({ x: 'x'.repeat(128 * 1024 - 8) });
        expect(new TextEncoder().encode(exact).byteLength).toBe(128 * 1024);
        expect((await body<{ x: string }>(request(exact))).x.length).toBe(128 * 1024 - 8);
        const oversized = request(exact + ' ');
        oversized.headers.set('Content-Length', '2');
        await expect(body(oversized)).rejects.toMatchObject({ status: 413 });
    });

    it('counts streamed bytes, cancels oversize input, and releases the reader', async () => {
        let cancelled = false;
        const stream = new ReadableStream<Uint8Array>({
            start(controller) {
                controller.enqueue(new Uint8Array(64 * 1024));
                controller.enqueue(new Uint8Array(64 * 1024 + 1));
            },
            cancel() {
                cancelled = true;
            },
        });
        const input = new Request(env.SITE_URL, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: stream,
        });
        await expect(body(input)).rejects.toMatchObject({ status: 413 });
        expect(cancelled).toBe(true);
        expect(input.body?.locked).toBe(false);
    });
});

describe('D1-backed session, authorization, and CSRF', () => {
    it('uses cryptographic tokens and stores only the token hash', async () => {
        const token = randomToken();
        expect(token).toMatch(/^[a-f0-9]{64}$/);
        expect(randomToken()).not.toBe(token);
        expect(await hash('abc')).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
        const headers = await login();
        const row = await session(new Request(env.SITE_URL, { headers }), env);
        expect(row?.token_hash).toBe(await hash(headers.Cookie.split('=')[1]));
        expect(row?.token_hash).not.toBe(headers.Cookie.split('=')[1]);
    });

    it('accepts same-origin CSRF for an authenticated session', async () => {
        for (const id of [null, (await member()).id]) {
            const headers = await login(id);
            await expect(csrf(new Request(env.SITE_URL, { method: 'POST', headers }), env)).resolves.toBeUndefined();
        }
    });

    it.each(['https://evil.example.test', 'https://members.example.test.evil.test', 'http://members.example.test', 'null', ''])(
        'rejects missing or foreign Origin %s even with valid CSRF token',
        async (origin) => {
            const headers = new Headers(await login());
            if (origin) headers.set('Origin', origin);
            else headers.delete('Origin');
            await expect(csrf(new Request(env.SITE_URL, { method: 'POST', headers }), env)).rejects.toMatchObject({ status: 403 });
        },
    );

    it('rejects absent/wrong CSRF tokens and expired or unknown sessions', async () => {
        const valid = await login();
        for (const changes of [{ 'X-CSRF-Token': '' }, { 'X-CSRF-Token': 'wrong' }, { Cookie: '' }, { Cookie: `conway_session=${randomToken()}` }]) {
            await expect(csrf(new Request(env.SITE_URL, { method: 'POST', headers: { ...valid, ...changes } }), env)).rejects.toMatchObject({ status: 403 });
        }
        const expired = new Request(env.SITE_URL, { headers: await login(null, now()) });
        expect(await session(expired, env)).toBeNull();
        await expect(csrf(expired, env)).rejects.toMatchObject({ status: 403 });
    });

    it('requires membership then leadership and invalidates sessions when member is deleted', async () => {
        const row = await member({ admin_notes: 'Private notes' });
        const request = new Request(env.SITE_URL, { headers: await login(row.id) });
        expect((await requireMember(request, env)).id).toBe(row.id);
        expect(memberView(row)).not.toHaveProperty('admin_notes');
        await expect(requireLeader(request, env)).rejects.toMatchObject({ status: 403 });
        await env.DB.prepare('UPDATE members SET leadership=1 WHERE id=?').bind(row.id).run();
        expect((await requireLeader(request, env)).id).toBe(row.id);
        await env.DB.prepare('DELETE FROM members WHERE id=?').bind(row.id).run();
        expect(await session(request, env)).toBeNull();
        await expect(requireMember(request, env)).rejects.toMatchObject({ status: 401 });
        await expect(requireMember(new Request(env.SITE_URL, { headers: await login() }), env)).rejects.toMatchObject({ status: 401 });
    });

    it('sets secure, HttpOnly, same-site cookies for HTTPS while permitting local HTTP', () => {
        expect(cookieHeader(env, 'conway_session', 'token', 3600)).toBe('conway_session=token; Path=/; HttpOnly; SameSite=Lax; Max-Age=3600; Secure');
        expect(cookieHeader({ ...env, SITE_URL: 'http://localhost:8787' }, 'conway_session', '', 0)).not.toContain('Secure');
    });
});
