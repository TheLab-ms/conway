import type { Env, Member, Session, Settings } from './types';
export class HttpError extends Error {
    constructor(
        public status: number,
        message: string,
    ) {
        super(message);
    }
}
export const now = () => Math.floor(Date.now() / 1000);
export const randomToken = () => Array.from(crypto.getRandomValues(new Uint8Array(32)), (b) => b.toString(16).padStart(2, '0')).join('');
export async function hash(value: string): Promise<string> {
    return Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value))), (b) => b.toString(16).padStart(2, '0')).join('');
}
export function json(value: unknown, status = 200): Response {
    return Response.json(value, { status, headers: { 'Cache-Control': 'no-store' } });
}
export async function body<T = Record<string, unknown>>(request: Request): Promise<T> {
    if (!request.headers.get('content-type')?.toLowerCase().startsWith('application/json')) throw new HttpError(415, 'JSON content type required');
    const reader = request.body?.getReader();
    if (!reader) throw new HttpError(400, 'JSON body required');
    const chunks: Uint8Array[] = [];
    let length = 0;
    try {
        for (;;) {
            const { done, value } = await reader.read();
            if (done) break;
            length += value.byteLength;
            if (length > 128 * 1024) {
                await reader.cancel();
                throw new HttpError(413, 'Request too large');
            }
            chunks.push(value);
        }
    } finally {
        reader.releaseLock();
    }
    const bytes = new Uint8Array(length);
    let offset = 0;
    for (const chunk of chunks) {
        bytes.set(chunk, offset);
        offset += chunk.length;
    }
    try {
        const parsed = JSON.parse(new TextDecoder().decode(bytes));
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error();
        return parsed;
    } catch {
        throw new HttpError(400, 'Invalid JSON object');
    }
}
export function cookie(request: Request, name: string): string | null {
    const match = (request.headers.get('Cookie') || '')
        .split(';')
        .map((v) => v.trim())
        .find((v) => v.startsWith(name + '='));
    return match ? match.slice(name.length + 1) : null;
}
export function cookieHeader(env: Env, name: string, value: string, age: number): string {
    return `${name}=${value}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${age}${new URL(env.SITE_URL).protocol === 'https:' ? '; Secure' : ''}`;
}
export async function session(request: Request, env: Env): Promise<Session | null> {
    const token = cookie(request, 'conway_session');
    if (!token || !/^[a-f0-9]{64}$/.test(token)) return null;
    return env.DB.prepare('SELECT * FROM sessions WHERE token_hash = ? AND expires > ?')
        .bind(await hash(token), now())
        .first<Session>();
}
export async function requireMember(request: Request, env: Env): Promise<Member> {
    const s = await session(request, env);
    const member = s?.member ? await env.DB.prepare('SELECT * FROM members WHERE id = ?').bind(s.member).first<Member>() : null;
    if (!member) throw new HttpError(401, 'Sign in with Discord');
    return member;
}
export async function requireLeader(request: Request, env: Env): Promise<Member> {
    const member = await requireMember(request, env);
    if (!member.leadership) throw new HttpError(403, 'Leadership access required');
    return member;
}
export function memberView(member: Member): Omit<Member, 'admin_notes'> {
    const { admin_notes: _notes, ...visible } = member;
    return visible;
}
export async function settings(env: Env): Promise<Settings> {
    const row = await env.DB.prepare('SELECT version, data FROM settings WHERE id = 1').first<{
        version: number;
        data: string;
    }>();
    if (!row) throw new HttpError(503, 'Database has not been initialized');
    return { ...JSON.parse(row.data), version: row.version };
}
export async function csrf(request: Request, env: Env): Promise<void> {
    if (request.headers.get('Origin') !== new URL(env.SITE_URL).origin) throw new HttpError(403, 'Same-origin request required');
    const s = await session(request, env);
    if (!s || request.headers.get('X-CSRF-Token') !== s.csrf_token) throw new HttpError(403, 'Session expired; reload and try again');
}
