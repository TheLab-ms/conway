import type { Env, Member, Session } from './types';
import { body, cookie, cookieHeader, hash, HttpError, json, memberView, now, randomToken, session } from './http';

const SESSION_AGE = 30 * 24 * 60 * 60;
const PREAUTH_AGE = 60 * 60;
const OAUTH_AGE = 10 * 60;
const OPAQUE = /^[a-f0-9]{64}$/;
const SUPPORT = 'This email is already associated with a membership. Contact leadership to link your Discord account.';
interface Signup { email: string; discord_user_id: string; discord_username: string; return_to: string }

function localReturn(value: string | null, env: Env): string {
  if (!value || value.length > 2048 || !value.startsWith('/') || value.startsWith('//') ||
      /[\\\x00-\x20\x7f]|%(?:0[0-9a-f]|1[0-9a-f]|5c|7f)/i.test(value)) return '/dashboard';
  const url = new URL(value, env.SITE_URL);
  if (url.origin !== new URL(env.SITE_URL).origin || url.pathname.startsWith('//') || /%2f/i.test(url.pathname) ||
      url.pathname.startsWith('/login') || url.pathname.startsWith('/api/')) return '/dashboard';
  return url.pathname + url.search + url.hash;
}

function redirect(location: string): Response {
  return new Response(null, { status: 302, headers: { Location: location, 'Cache-Control': 'no-store', 'Referrer-Policy': 'no-referrer' } });
}

async function rateLimit(request: Request, env: Env, kind: 'oauth' | 'anonymous'): Promise<void> {
  // CF supplies this header; missing IPs share a fail-closed bucket, not a forwarded header.
  const key = `auth:${kind}:${await hash(request.headers.get('CF-Connecting-IP') || 'unknown')}`;
  const time = now();
  const result = await env.DB.prepare(`INSERT INTO rate_limits (key, count, expires) VALUES (?, 1, ?)
    ON CONFLICT(key) DO UPDATE SET count = CASE WHEN rate_limits.expires <= ? THEN 1 ELSE rate_limits.count + 1 END,
    expires = CASE WHEN rate_limits.expires <= ? THEN excluded.expires ELSE rate_limits.expires END
    WHERE rate_limits.expires <= ? OR rate_limits.count < ? RETURNING count`)
    .bind(key, time + 600, time, time, time, kind === 'oauth' ? 20 : 60).first();
  if (!result) throw new HttpError(429, 'Too many sign-in requests. Please try again in ten minutes.');
}

async function discordJSON(url: string, init: RequestInit): Promise<Record<string, unknown>> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 8000);
  try {
    const response = await fetch(url, { ...init, redirect: 'error', signal: controller.signal });
    if (!response.ok || !response.headers.get('Content-Type')?.toLowerCase().startsWith('application/json') ||
        Number(response.headers.get('Content-Length')) > 65536) {
      await response.body?.cancel();
      throw new Error('Invalid Discord response');
    }
    const reader = response.body?.getReader();
    if (!reader) throw new Error('Missing Discord response');
    const chunks: Uint8Array[] = []; let length = 0;
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        length += value.byteLength;
        if (length > 65536) { await reader.cancel(); throw new Error('Discord response too large'); }
        chunks.push(value);
      }
    } finally { reader.releaseLock(); }
    const bytes = new Uint8Array(length); let offset = 0;
    for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
    const data: unknown = JSON.parse(new TextDecoder().decode(bytes));
    if (!data || typeof data !== 'object' || Array.isArray(data)) throw new Error('Invalid Discord JSON');
    return data as Record<string, unknown>;
  } catch {
    // Never expose provider responses, authorization codes, or access tokens.
    throw new HttpError(502, 'Discord sign-in could not be completed. Please start again.');
  } finally { clearTimeout(timer); }
}

export async function authRoute(request: Request, env: Env): Promise<Response | null> {
  const url = new URL(request.url);
  if (url.pathname === '/api/session' && request.method === 'GET') {
    let current = await session(request, env);
    let token: string | null = null;
    if (!current) {
      await rateLimit(request, env, 'anonymous');
      token = randomToken();
      current = { token_hash: await hash(token), csrf_token: randomToken(), expires: now() + PREAUTH_AGE, member: null, signup: null };
      await env.DB.prepare('INSERT INTO sessions (token_hash, member, csrf_token, expires, signup) VALUES (?, NULL, ?, ?, NULL)')
        .bind(current.token_hash, current.csrf_token, current.expires).run();
    }
    const member = current.member === null ? null : await env.DB.prepare('SELECT * FROM members WHERE id = ?').bind(current.member).first<Member>();
    const pending: Signup | null = current.member === null && current.signup ? JSON.parse(current.signup) : null;
    const response = json({ member: member ? memberView(member) : null, csrf_token: current.csrf_token,
      signup: pending ? { email: pending.email, discord_user_id: pending.discord_user_id } : null });
    if (token) response.headers.set('Set-Cookie', cookieHeader(env, 'conway_session', token, PREAUTH_AGE));
    return response;
  }

  if (url.pathname === '/login/discord' && request.method === 'GET') {
    if (!env.DISCORD_CLIENT_ID || !env.DISCORD_CLIENT_SECRET) throw new HttpError(503, 'Discord sign-in is not configured');
    await rateLimit(request, env, 'oauth');
    const state = randomToken(), browser = randomToken();
    await env.DB.prepare('INSERT INTO oauth_states (state_hash, browser_hash, return_to, expires) VALUES (?, ?, ?, ?)')
      .bind(await hash(state), await hash(browser), localReturn(url.searchParams.get('return_to'), env), now() + OAUTH_AGE).run();
    const target = new URL('https://discord.com/oauth2/authorize');
    target.search = new URLSearchParams({ client_id: env.DISCORD_CLIENT_ID, response_type: 'code', scope: 'identify email',
      redirect_uri: new URL('/login/discord/callback', env.SITE_URL).href, state }).toString();
    const response = redirect(target.href);
    response.headers.set('Set-Cookie', cookieHeader(env, 'conway_oauth', browser, OAUTH_AGE));
    return response;
  }

  if (url.pathname === '/login/discord/callback' && request.method === 'GET') {
    const state = url.searchParams.get('state'), browser = cookie(request, 'conway_oauth');
    if (!state || !browser || !OPAQUE.test(state) || !OPAQUE.test(browser) || url.searchParams.getAll('state').length !== 1)
      throw new HttpError(400, 'Invalid or expired Discord sign-in. Please start again.');
    // DELETE RETURNING makes redemption one-use even across concurrent callbacks.
    const pending = await env.DB.prepare('DELETE FROM oauth_states WHERE state_hash = ? AND browser_hash = ? AND expires > ? RETURNING return_to')
      .bind(await hash(state), await hash(browser), now()).first<{ return_to: string }>();
    if (!pending) throw new HttpError(400, 'Invalid or expired Discord sign-in. Please start again.');
    const code = url.searchParams.get('code');
    if (url.searchParams.has('error') || !code || code.length > 2048 || /[^\x21-\x7e]/.test(code) || url.searchParams.getAll('code').length !== 1)
      throw new HttpError(400, 'Discord sign-in was not authorized. Please start again.');
    if (!env.DISCORD_CLIENT_ID || !env.DISCORD_CLIENT_SECRET) throw new HttpError(503, 'Discord sign-in is not configured');
    const token = await discordJSON('https://discord.com/api/v10/oauth2/token', { method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', Accept: 'application/json' },
      body: new URLSearchParams({ client_id: env.DISCORD_CLIENT_ID, client_secret: env.DISCORD_CLIENT_SECRET,
        grant_type: 'authorization_code', code, redirect_uri: new URL('/login/discord/callback', env.SITE_URL).href }).toString() });
    if (typeof token.access_token !== 'string' || !/^[A-Za-z0-9._~+-]{1,2048}$/.test(token.access_token) ||
        typeof token.token_type !== 'string' || token.token_type.toLowerCase() !== 'bearer')
      throw new HttpError(502, 'Discord returned an invalid sign-in token. Please start again.');
    const user = await discordJSON('https://discord.com/api/v10/users/@me', {
      headers: { Authorization: `Bearer ${token.access_token}`, Accept: 'application/json' } });
    if (typeof user.id !== 'string' || !/^[1-9][0-9]{16,19}$/.test(user.id) || BigInt(user.id) > 18446744073709551615n ||
        typeof user.username !== 'string' || !user.username.trim() || user.username.length > 80 || /[\x00-\x1f\x7f]/.test(user.username) || user.bot === true)
      throw new HttpError(502, 'Discord returned an invalid user identity. Please start again.');
    if (user.verified !== true || typeof user.email !== 'string' || user.email.length > 254 ||
        !/^[^\s@\x00-\x1f\x7f-\uffff]{1,64}@[^\s@\x00-\x1f\x7f-\uffff]+\.[^\s@\x00-\x1f\x7f-\uffff]+$/.test(user.email))
      throw new HttpError(403, 'Verify your email in Discord before signing in.');
    const email = user.email.toLowerCase();
    // Email is contact information only. It must never identify or link a member.
    const member = await env.DB.prepare('SELECT * FROM members WHERE discord_user_id = ?').bind(user.id).first<Member>();
    if (!member && await env.DB.prepare('SELECT id FROM members WHERE email = ?').bind(email).first()) throw new HttpError(409, SUPPORT);
    let returnTo = localReturn(pending.return_to, env);
    const returnURL = new URL(returnTo, env.SITE_URL);
    if (returnURL.pathname === '/signup') returnTo = localReturn(returnURL.searchParams.get('return_to'), env);
    const signup: Signup | null = member ? null : { email, discord_user_id: user.id, discord_username: user.username, return_to: returnTo };
    const old = await session(request, env), fresh = randomToken(), freshHash = await hash(fresh);
    const age = member ? SESSION_AGE : PREAUTH_AGE;
    const statements = [];
    if (member) statements.push(env.DB.prepare('UPDATE members SET discord_username = ?, discord_email = ?, discord_last_synced = ? WHERE id = ? AND discord_user_id = ?')
      .bind(user.username, email, now(), member.id, user.id));
    // Guard linkage again inside the transaction, in case leadership changed it during OAuth.
    statements.push(member ? env.DB.prepare(`INSERT INTO sessions (token_hash, member, csrf_token, expires, signup)
      SELECT ?, id, ?, ?, NULL FROM members WHERE id = ? AND discord_user_id = ?`)
      .bind(freshHash, randomToken(), now() + age, member.id, user.id) :
      env.DB.prepare('INSERT INTO sessions (token_hash, member, csrf_token, expires, signup) VALUES (?, NULL, ?, ?, ?)')
        .bind(freshHash, randomToken(), now() + age, JSON.stringify(signup)));
    if (old) statements.push(env.DB.prepare('DELETE FROM sessions WHERE token_hash = ? AND EXISTS (SELECT 1 FROM sessions WHERE token_hash = ?)')
      .bind(old.token_hash, freshHash));
    const results = await env.DB.batch(statements);
    if (results[member ? 1 : 0].meta.changes !== 1) throw new HttpError(409, 'Membership linkage changed. Please sign in again.');
    const response = redirect(member ? returnTo : '/signup?return_to=' + encodeURIComponent(returnTo));
    response.headers.append('Set-Cookie', cookieHeader(env, 'conway_session', fresh, age));
    response.headers.append('Set-Cookie', cookieHeader(env, 'conway_oauth', '', 0));
    return response;
  }

  if (url.pathname === '/api/signup' && request.method === 'POST') {
    const current: Session | null = await session(request, env);
    if (!current || current.member !== null || !current.signup) throw new HttpError(409, 'No pending signup. Please sign in with Discord again.');
    const input = await body(request);
    if (typeof input.name !== 'string' || !input.name.trim() || input.name.trim().length > 200 || /[\x00-\x1f\x7f]/.test(input.name) ||
        typeof input.heard_about !== 'string' || input.heard_about.trim().length > 500 || /[\x00-\x1f\x7f]/.test(input.heard_about))
      throw new HttpError(400, 'Provide your name (up to 200 characters) and how you heard about us (up to 500 characters).');
    const pending: Signup = JSON.parse(current.signup);
    const fresh = randomToken();
    // No preflight identity lookup can authorize this insert. Both statements run in
    // one D1 transaction, and changes() gates rotation on THIS request's new member.
    const result = await env.DB.batch([
      env.DB.prepare(`INSERT INTO members (email, confirmed, name, heard_about, discord_user_id, discord_username, discord_email, discord_last_synced)
        SELECT ?, 1, ?, ?, ?, ?, ?, ? FROM sessions
        WHERE token_hash = ? AND member IS NULL AND signup = ? AND expires > ?
        AND NOT EXISTS (SELECT 1 FROM members WHERE email = ? OR discord_user_id = ?) RETURNING id`)
        .bind(pending.email, input.name.trim(), input.heard_about.trim(), pending.discord_user_id, pending.discord_username, pending.email, now(),
          current.token_hash, current.signup, now(), pending.email, pending.discord_user_id),
      env.DB.prepare(`UPDATE sessions SET token_hash = ?, member = (SELECT id FROM members WHERE discord_user_id = ?),
        signup = NULL, csrf_token = ?, expires = ?
        WHERE token_hash = ? AND member IS NULL AND signup = ? AND changes() = 1`)
        .bind(await hash(fresh), pending.discord_user_id, randomToken(), now() + SESSION_AGE, current.token_hash, current.signup),
    ]);
    if (result[0].results.length !== 1 || result[1].meta.changes !== 1) {
      if (await env.DB.prepare('SELECT id FROM members WHERE email = ?').bind(pending.email).first()) throw new HttpError(409, SUPPORT);
      throw new HttpError(409, 'Signup was already completed or expired. Please sign in with Discord again.');
    }
    const response = json({ return_to: localReturn(pending.return_to, env) });
    response.headers.set('Set-Cookie', cookieHeader(env, 'conway_session', fresh, SESSION_AGE));
    return response;
  }

  if (url.pathname === '/api/logout' && request.method === 'POST') {
    const token = cookie(request, 'conway_session');
    if (token && OPAQUE.test(token)) await env.DB.prepare('DELETE FROM sessions WHERE token_hash = ?').bind(await hash(token)).run();
    const browser = cookie(request, 'conway_oauth');
    if (browser && OPAQUE.test(browser)) await env.DB.prepare('DELETE FROM oauth_states WHERE browser_hash = ?').bind(await hash(browser)).run();
    const response = json({ ok: true });
    response.headers.append('Set-Cookie', cookieHeader(env, 'conway_session', '', 0));
    response.headers.append('Set-Cookie', cookieHeader(env, 'conway_oauth', '', 0));
    return response;
  }
  return null;
}
