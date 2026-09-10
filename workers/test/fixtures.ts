import { env } from 'cloudflare:workers';
import { SELF } from 'cloudflare:test';
import { hash, now, randomToken } from '../src/http';
import type { Member } from '../src/types';

export async function member(fields: Record<string, string | number | null> = {}): Promise<Member> {
    const values = { email: `${randomToken()}@example.test`, ...fields };
    const row = await env.DB.prepare(
        `INSERT INTO members (${Object.keys(values).join(',')})
    VALUES (${Object.keys(values)
        .map(() => '?')
        .join(',')}) RETURNING *`,
    )
        .bind(...Object.values(values))
        .first<Member>();
    if (!row) throw new Error('Member fixture insert returned no row');
    // AFTER triggers are not reflected in INSERT RETURNING.
    return (await env.DB.prepare('SELECT * FROM members WHERE id = ?').bind(row.id).first<Member>())!;
}

export async function login(memberId: number | null = null, expires = now() + 3600) {
    const token = randomToken();
    const csrfToken = randomToken();
    await env.DB.prepare('INSERT INTO sessions (token_hash, member, csrf_token, expires) VALUES (?, ?, ?, ?)')
        .bind(await hash(token), memberId, csrfToken, expires)
        .run();
    return { Cookie: `conway_session=${token}`, Origin: env.SITE_URL, 'X-CSRF-Token': csrfToken };
}

export function api(path: string, method = 'GET', headers: HeadersInit = {}, data?: unknown): Promise<Response> {
    const combined = new Headers(headers);
    if (data !== undefined) combined.set('Content-Type', 'application/json');
    return SELF.fetch(new URL(path, env.SITE_URL).href, {
        method,
        headers: combined,
        body: data === undefined ? undefined : JSON.stringify(data),
        redirect: 'manual',
    });
}

export function responseCookie(response: Response, name = 'conway_session'): string {
    const value = response.headers.getSetCookie().find((cookie) => cookie.startsWith(`${name}=`));
    if (!value) throw new Error(`Missing ${name} cookie`);
    return value.split(';')[0];
}

export async function adminMutation(id: number, method: 'PATCH' | 'DELETE', headers: HeadersInit, fields: Record<string, unknown> = {}) {
    const response = await api(`/api/admin/members/${id}`, 'GET', headers);
    if (!response.ok) throw new Error(`Member fixture read failed: ${response.status}`);
    const current = await response.json<Member>();
    return api(`/api/admin/members/${id}`, method, headers, { ...fields, version: current.version });
}
