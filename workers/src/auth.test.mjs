import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { webcrypto } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { createInterface } from 'node:readline';
import test from 'node:test';
import vm from 'node:vm';

// node --test src/auth.test.mjs (TypeScript dev dependency and Python 3 required).
// Real auth/http modules and schema; only D1 transport and Discord are replaced.
const require = createRequire(import.meta.url);
let ts;
if (process.env.TYPESCRIPT_SOURCE_STDIN === '1') {
  let source = '';
  for await (const chunk of process.stdin) source += chunk;
  const module = { exports: {} };
  vm.runInNewContext(source, { module, exports: module.exports }, { filename: 'typescript.js' });
  ts = module.exports;
} else ts = require(process.env.TYPESCRIPT_PATH || 'typescript');
const compiled = {};
for (const name of ['http', 'auth']) {
  const result = ts.transpileModule(await readFile(new URL(`./${name}.ts`, import.meta.url), 'utf8'), {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS }, reportDiagnostics: true,
  });
  assert.equal(result.diagnostics?.length || 0, 0);
  compiled[name] = result.outputText;
}
const schema = await readFile(new URL('../migrations/0001_core.sql', import.meta.url), 'utf8');

async function database(t) {
  const child = spawn('python3', ['-u', '-c', `
import json, sqlite3, sys
db = sqlite3.connect(':memory:', isolation_level=None)
db.row_factory = sqlite3.Row
def execute(statement):
    cursor = db.execute(statement['sql'], statement.get('values', []))
    rows = [dict(row) for row in cursor.fetchall()]
    changes = db.execute('SELECT changes()').fetchone()[0]
    return {'results': rows, 'success': True, 'meta': {'changes': changes}}
for line in sys.stdin:
    try:
        message = json.loads(line)
        if 'schema' in message:
            db.executescript(message['schema'])
            result = None
        elif 'batch' in message:
            db.execute('BEGIN')
            try:
                result = [execute(statement) for statement in message['batch']]
                db.execute('COMMIT')
            except Exception:
                db.execute('ROLLBACK')
                raise
        else:
            result = execute(message)
        print(json.dumps({'result': result}))
    except Exception as error:
        print(json.dumps({'error': str(error)}))
`], { stdio: ['pipe', 'pipe', 'inherit'] });
  t.after(() => child.kill());
  const queue = [];
  createInterface({ input: child.stdout }).on('line', line => {
    const { resolve, reject } = queue.shift();
    const result = JSON.parse(line);
    if (result.error) reject(new Error(result.error)); else resolve(result.result);
  });
  child.on('error', error => { for (const item of queue.splice(0)) item.reject(error); });
  const send = value => new Promise((resolve, reject) => { queue.push({ resolve, reject }); child.stdin.write(JSON.stringify(value) + '\n'); });
  await send({ schema });
  const db = { prepare(sql) {
    return { sql, values: [], bind(...values) { this.values = values; return this; },
      async first() { return (await send(this)).results[0] ?? null; },
      async run() { return send(this); }, async all() { return send(this); } };
  }, batch: statements => send({ batch: statements }) };
  return db;
}

const origin = 'https://members.example.com';
const user = { id: '123456789012345678', username: 'a.member', email: 'Discord@Example.com', verified: true };
const cookies = response => [...response.headers.get('set-cookie')?.matchAll(/(?:^|, )(conway_\w+=[^;]*)/g) || []]
  .map(match => match[1]).filter(cookie => !cookie.endsWith('=')).join('; ');
const cookieValue = (header, name) => header.split('; ').find(value => value.startsWith(name + '='))?.slice(name.length + 1);
const status = expected => error => error.status === expected;

async function harness(t, { discordUser = user, remote, timeout } = {}) {
  const db = await database(t);
  const env = { DB: db, SITE_URL: origin, DISCORD_CLIENT_ID: 'client', DISCORD_CLIENT_SECRET: 'secret' };
  const calls = [];
  const context = { crypto: webcrypto, URL, URLSearchParams, Request, Response, TextEncoder, TextDecoder, Uint8Array, AbortController,
    setTimeout: (callback, delay) => setTimeout(callback, timeout ?? delay), clearTimeout,
    fetch: async (url, init) => {
      calls.push({ url, init });
      if (remote) return remote(url, init);
      if (url.endsWith('/oauth2/token')) return Response.json({ access_token: 'private_token', token_type: 'Bearer' });
      assert.equal(url, 'https://discord.com/api/v10/users/@me');
      return Response.json(discordUser);
    } };
  const http = { exports: {} };
  vm.runInNewContext(compiled.http, { ...context, exports: http.exports });
  const auth = { exports: {} };
  vm.runInNewContext(compiled.auth, { ...context, exports: auth.exports, require: name => { assert.equal(name, './http'); return http.exports; } });
  const route = (path, { cookie = '', method = 'GET', input, ip = '192.0.2.1' } = {}) => auth.exports.authRoute(new Request(origin + path, {
    method, headers: { Cookie: cookie, 'CF-Connecting-IP': ip, 'Content-Type': 'application/json' },
    body: input === undefined ? undefined : JSON.stringify(input),
  }), env);
  const start = async (target = '/fobs/bind?token=claim', cookie = '') => {
    const response = await route('/login/discord?return_to=' + encodeURIComponent(target), { cookie });
    const location = new URL(response.headers.get('Location'));
    return { response, location, cookie: cookies(response), state: location.searchParams.get('state') };
  };
  const callback = (start, cookie = '') => route('/login/discord/callback?code=one_use_code&state=' + start.state, { cookie: start.cookie + '; ' + cookie });
  const pending = async target => {
    const begun = await start(target);
    const response = await callback(begun);
    return { cookie: cookies(response), response, begun };
  };
  return { db, env, calls, route, start, callback, pending, http: http.exports };
}

test('session issues opaque hashed anonymous cookie and CSRF, then reuses it', async t => {
  const h = await harness(t);
  const response = await h.route('/api/session');
  const data = await response.json();
  assert.deepEqual({ ...data, csrf_token: null }, { member: null, csrf_token: null, signup: null });
  assert.match(data.csrf_token, /^[a-f0-9]{64}$/);
  assert.match(response.headers.get('Set-Cookie'), /HttpOnly; SameSite=Lax; Max-Age=3600; Secure/);
  assert.equal(response.headers.get('Cache-Control'), 'no-store');
  const cookie = cookies(response), token = cookieValue(cookie, 'conway_session');
  const row = await h.db.prepare('SELECT * FROM sessions').first();
  assert.notEqual(row.token_hash, token);
  assert.equal(row.token_hash, await h.http.hash(token));
  const again = await h.route('/api/session', { cookie });
  assert.equal(again.headers.get('Set-Cookie'), null);
  assert.deepEqual(await again.json(), data);
  await h.db.prepare('UPDATE sessions SET expires = 0').run();
  assert.ok((await h.route('/api/session', { cookie })).headers.has('Set-Cookie'));
});

test('session exposes sanitized member and only public pending signup fields', async t => {
  const h = await harness(t);
  const pending = await h.pending();
  assert.deepEqual((await (await h.route('/api/session', { cookie: pending.cookie })).json()).signup,
    { email: 'discord@example.com', discord_user_id: user.id });
  await h.db.prepare('INSERT INTO members (email, discord_user_id, admin_notes) VALUES (?, ?, ?)').bind('primary@example.com', user.id, 'private notes').run();
  const login = await h.callback(await h.start());
  const data = await (await h.route('/api/session', { cookie: cookies(login) })).json();
  assert.equal(data.member.email, 'primary@example.com');
  assert.equal('admin_notes' in data.member, false);
  assert.equal(data.signup, null);
});

test('OAuth start stores hashes, fixes callback and scope, and restricts return paths', async t => {
  const h = await harness(t);
  const cases = [
    ['/fobs/bind?token=abc%20def&next=%2Fdashboard#claim', '/fobs/bind?token=abc%20def&next=%2Fdashboard#claim'],
    ['/signup?return_to=%2Ffobs%2Fbind%3Ftoken%3Dabc', '/signup?return_to=%2Ffobs%2Fbind%3Ftoken%3Dabc'],
    ['https://evil.example', '/dashboard'], ['//evil.example', '/dashboard'], ['/\\evil.example', '/dashboard'],
    ['/%2fevil.example', '/dashboard'], ['/a/..//evil.example', '/dashboard'], ['/x\r\nLocation: evil', '/dashboard'],
    ['/login/discord', '/dashboard'], ['/api/logout', '/dashboard'],
  ];
  for (const [input, expected] of cases) {
    const started = await h.start(input);
    assert.equal(started.location.origin, 'https://discord.com');
    assert.equal(started.location.searchParams.get('scope'), 'identify email');
    assert.equal(started.location.searchParams.get('redirect_uri'), origin + '/login/discord/callback');
    const row = await h.db.prepare('SELECT * FROM oauth_states WHERE state_hash = ?').bind(await h.http.hash(started.state)).first();
    assert.equal(row.return_to, expected);
    assert.equal(row.browser_hash, await h.http.hash(cookieValue(started.cookie, 'conway_oauth')));
    assert.notEqual(row.state_hash, started.state);
  }
});

test('OAuth state requires its browser, is one-use under concurrent callbacks, and expires', async t => {
  const h = await harness(t);
  const started = await h.start();
  const path = '/login/discord/callback?code=code&state=' + started.state;
  await assert.rejects(h.route(path), status(400));
  await assert.rejects(h.route(path, { cookie: 'conway_oauth=' + 'a'.repeat(64) }), status(400));
  assert.equal(h.calls.length, 0);
  const attempts = await Promise.allSettled([h.callback(started), h.callback(started)]);
  assert.equal(attempts.filter(attempt => attempt.status === 'fulfilled').length, 1);
  assert.equal(attempts.find(attempt => attempt.status === 'rejected').reason.status, 400);
  assert.equal(h.calls.length, 2);
  const expired = await h.start();
  await h.db.prepare('UPDATE oauth_states SET expires = 0').run();
  await assert.rejects(h.callback(expired), status(400));
  assert.equal(h.calls.length, 2);
});

test('provider denial and malformed callback consume valid state without remote calls', async t => {
  const h = await harness(t);
  for (const query of ['error=access_denied', 'code=', 'code=a&code=b']) {
    const started = await h.start();
    await assert.rejects(h.route('/login/discord/callback?state=' + started.state + '&' + query, { cookie: started.cookie }), status(400));
    await assert.rejects(h.callback(started), status(400));
  }
  assert.equal(h.calls.length, 0);
});

test('existing Discord-ID login rotates old session without overwriting primary email', async t => {
  const h = await harness(t);
  await h.db.prepare('INSERT INTO members (email, discord_user_id, name) VALUES (?, ?, ?)').bind('old-contact@example.com', user.id, 'Existing Name').run();
  // Even a contact email belonging to another member cannot change identity.
  await h.db.prepare('INSERT INTO members (email) VALUES (?)').bind(user.email).run();
  const old = cookies(await h.route('/api/session'));
  const first = await h.callback(await h.start('/fobs/bind?token=abc', old), old);
  assert.equal(first.headers.get('Location'), '/fobs/bind?token=abc');
  assert.match(first.headers.get('Set-Cookie'), /conway_oauth=;.*Max-Age=0/);
  assert.equal(await h.db.prepare('SELECT * FROM sessions WHERE token_hash = ?').bind(await h.http.hash(cookieValue(old, 'conway_session'))).first(), null);
  const member = await h.db.prepare('SELECT * FROM members WHERE discord_user_id = ?').bind(user.id).first();
  assert.equal(member.email, 'old-contact@example.com');
  assert.equal(member.discord_email, 'discord@example.com');
  assert.equal(member.name, 'Existing Name');
  const firstCookie = cookies(first);
  const second = await h.callback(await h.start('/dashboard', firstCookie), firstCookie);
  assert.notEqual(cookieValue(cookies(second), 'conway_session'), cookieValue(firstCookie, 'conway_session'));
  assert.equal(await h.db.prepare('SELECT * FROM sessions WHERE token_hash = ?').bind(await h.http.hash(cookieValue(firstCookie, 'conway_session'))).first(), null);
  assert.equal((await h.db.prepare('SELECT count(*) AS n FROM members').first()).n, 2);
});

test('verified email conflict never authenticates, links, or overwrites existing membership', async t => {
  const h = await harness(t);
  await h.db.prepare('INSERT INTO members (email, name) VALUES (?, ?)').bind('DISCORD@example.com', 'Legacy').run();
  await assert.rejects(h.callback(await h.start()), error => error.status === 409 && /leadership/.test(error.message));
  const member = await h.db.prepare('SELECT * FROM members').first();
  assert.equal(member.name, 'Legacy');
  assert.equal(member.discord_user_id, null);
  assert.equal((await h.db.prepare('SELECT count(*) AS n FROM sessions').first()).n, 0);
});

test('strict Discord identity and verified email validation rejects unsafe provider data', async t => {
  for (const patch of [{ id: 123456789012345678 }, { id: '12345' }, { id: '012345678901234567' }, { id: '18446744073709551616' },
    { id: '12345678901234567x' }, { verified: 'true' }, { verified: false }, { email: null }, { email: 'a@b\r\n.example' },
    { username: '' }, { bot: true }]) {
    const h = await harness(t, { discordUser: { ...user, ...patch } });
    await assert.rejects(h.callback(await h.start()), error => [403, 502].includes(error.status));
    assert.equal((await h.db.prepare('SELECT count(*) AS n FROM sessions').first()).n, 0);
  }
});

test('remote fetch is bounded, redirect-disabled, timed, and does not expose credentials', async t => {
  const scenarios = [
    () => new Response('private_token secret', { status: 400 }),
    () => new Response('not JSON', { headers: { 'Content-Type': 'application/json' } }),
    () => Response.json({ access_token: 'bad\r\ntoken', token_type: 'Bearer' }),
    () => Response.json({ access_token: 'private_token', token_type: 'other' }),
    () => new Response('x'.repeat(65537), { headers: { 'Content-Type': 'application/json' } }),
    () => new Response('{}', { headers: { 'Content-Type': 'application/json', 'Content-Length': '65537' } }),
    (_url, init) => new Promise((_, reject) => init.signal.addEventListener('abort', () => reject(new Error('private_token')), { once: true })),
  ];
  for (const remote of scenarios) {
    const h = await harness(t, { remote, timeout: 20 });
    const started = await h.start();
    await assert.rejects(h.callback(started), error => error.status === 502 && !/private_token|secret|one_use_code/.test(error.message));
    assert.equal(h.calls.length, 1);
    assert.equal(h.calls[0].init.redirect, 'error');
    assert.ok(h.calls[0].init.signal);
    const submitted = new URLSearchParams(h.calls[0].init.body);
    assert.equal(submitted.get('redirect_uri'), origin + '/login/discord/callback');
    assert.equal(submitted.get('grant_type'), 'authorization_code');
    await assert.rejects(h.callback(started), status(400));
  }
});

test('signup explicitly validates profile, atomically creates member and rotates pending session', async t => {
  const h = await harness(t);
  const pending = await h.pending('/signup?return_to=%2Ffobs%2Fbind%3Ftoken%3Dabc');
  assert.equal(pending.response.headers.get('Location'), '/signup?return_to=%2Ffobs%2Fbind%3Ftoken%3Dabc');
  assert.equal((await h.db.prepare('SELECT count(*) AS n FROM members').first()).n, 0);
  for (const input of [{}, { name: '', heard_about: '' }, { name: 'A' }, { name: 'a'.repeat(201), heard_about: '' },
    { name: 'A', heard_about: 'x'.repeat(501) }, { name: 'A\u0000', heard_about: '' }]) {
    await assert.rejects(h.route('/api/signup', { method: 'POST', cookie: pending.cookie, input }), status(400));
  }
  const before = await h.db.prepare('SELECT * FROM sessions').first();
  const response = await h.route('/api/signup', { method: 'POST', cookie: pending.cookie,
    input: { name: '  New Member  ', heard_about: '  Friend  ', email: 'attacker@example.com', discord_user_id: '999999999999999999', leadership: 1 } });
  assert.deepEqual(await response.json(), { return_to: '/fobs/bind?token=abc' });
  const member = await h.db.prepare('SELECT * FROM members').first();
  assert.equal(member.name, 'New Member');
  assert.equal(member.heard_about, 'Friend');
  assert.equal(member.discord_user_id, user.id);
  assert.equal(member.email, 'discord@example.com');
  assert.equal(member.confirmed, 1);
  assert.equal(member.leadership, 0);
  const after = await h.db.prepare('SELECT * FROM sessions').first();
  assert.equal(after.signup, null);
  assert.equal(after.member, member.id);
  assert.notEqual(after.token_hash, before.token_hash);
  assert.notEqual(after.csrf_token, before.csrf_token);
  assert.equal(after.token_hash, await h.http.hash(cookieValue(cookies(response), 'conway_session')));
  await assert.rejects(h.route('/api/signup', { method: 'POST', cookie: pending.cookie, input: { name: 'Duplicate', heard_about: '' } }), status(409));
  assert.equal((await h.db.prepare("SELECT count(*) AS n FROM jobs WHERE kind = 'signup'").first()).n, 1);
});

test('concurrent signup from the same pending snapshot creates and authenticates exactly once', async t => {
  const h = await harness(t);
  const pending = await h.pending();
  const prepare = h.db.prepare.bind(h.db);
  let reads = 0, release;
  const barrier = new Promise(resolve => { release = resolve; });
  h.db.prepare = sql => {
    const statement = prepare(sql);
    if (sql.startsWith('SELECT * FROM sessions WHERE token_hash')) {
      const first = statement.first.bind(statement);
      statement.first = async () => { const row = await first(); if (++reads === 2) release(); await barrier; return row; };
    }
    return statement;
  };
  const attempts = await Promise.allSettled(['First', 'Second'].map(name => h.route('/api/signup', {
    method: 'POST', cookie: pending.cookie, input: { name, heard_about: '' },
  })));
  assert.equal(reads, 2);
  assert.equal(attempts.filter(attempt => attempt.status === 'fulfilled').length, 1);
  assert.equal(attempts.find(attempt => attempt.status === 'rejected').reason.status, 409);
  assert.equal((await h.db.prepare('SELECT count(*) AS n FROM members').first()).n, 1);
  assert.equal((await h.db.prepare('SELECT count(*) AS n FROM sessions WHERE member IS NOT NULL').first()).n, 1);
  assert.equal((await h.db.prepare("SELECT count(*) AS n FROM jobs WHERE kind = 'signup'").first()).n, 1);
});

test('separate pending sessions for the same identity cannot reissue membership', async t => {
  const h = await harness(t);
  const [first, second] = [await h.pending(), await h.pending()];
  const attempts = await Promise.allSettled([first, second].map(pending => h.route('/api/signup', {
    method: 'POST', cookie: pending.cookie, input: { name: 'Member', heard_about: '' },
  })));
  assert.equal(attempts.filter(attempt => attempt.status === 'fulfilled').length, 1);
  assert.equal(attempts.find(attempt => attempt.status === 'rejected').reason.status, 409);
  assert.equal((await h.db.prepare('SELECT count(*) AS n FROM members').first()).n, 1);
  assert.equal((await h.db.prepare('SELECT count(*) AS n FROM sessions WHERE member IS NOT NULL').first()).n, 1);
});

test('signup contact conflict after OAuth cannot link or mutate another membership', async t => {
  const h = await harness(t);
  const pending = await h.pending();
  await h.db.prepare('INSERT INTO members (email, name) VALUES (?, ?)').bind('DISCORD@example.com', 'Legacy').run();
  await assert.rejects(h.route('/api/signup', { method: 'POST', cookie: pending.cookie, input: { name: 'New', heard_about: '' } }),
    error => error.status === 409 && /leadership/.test(error.message));
  const member = await h.db.prepare('SELECT * FROM members').first();
  assert.equal(member.name, 'Legacy');
  assert.equal(member.discord_user_id, null);
  assert.equal((await h.db.prepare('SELECT * FROM sessions').first()).member, null);
});

test('signup batch failure rolls back member, outbox, and pending session consumption', async t => {
  const h = await harness(t);
  const pending = await h.pending();
  await h.db.prepare("CREATE TRIGGER reject_rotation BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'test failure'); END").run();
  await assert.rejects(h.route('/api/signup', { method: 'POST', cookie: pending.cookie, input: { name: 'Member', heard_about: '' } }), /test failure/);
  assert.equal((await h.db.prepare('SELECT count(*) AS n FROM members').first()).n, 0);
  assert.equal((await h.db.prepare('SELECT count(*) AS n FROM jobs').first()).n, 0);
  assert.ok((await h.db.prepare('SELECT * FROM sessions').first()).signup);
});

test('D1 rate limits apply per CF IP, independently for OAuth and anonymous issuance', async t => {
  const h = await harness(t);
  for (let i = 0; i < 20; i++) await h.start();
  await assert.rejects(h.start(), status(429));
  await h.route('/login/discord', { ip: '192.0.2.2' });
  let last;
  for (let i = 0; i < 60; i++) last = await h.route('/api/session');
  await assert.rejects(h.route('/api/session'), status(429));
  assert.equal((await h.route('/api/session', { cookie: cookies(last) })).status, 200);
  await h.route('/api/session', { ip: '192.0.2.2' });
  await h.db.prepare('UPDATE rate_limits SET expires = 0').run();
  await h.start();
  await h.route('/api/session');
  assert.equal((await h.db.prepare("SELECT count FROM rate_limits WHERE key LIKE 'auth:oauth:%' AND expires > 0").first()).count, 1);
});

test('logout deletes session and pending OAuth state and expires both cookies', async t => {
  const h = await harness(t);
  const pending = await h.pending();
  const next = await h.start();
  const response = await h.route('/api/logout', { method: 'POST', cookie: pending.cookie + '; ' + next.cookie });
  assert.deepEqual(await response.json(), { ok: true });
  assert.match(response.headers.get('Set-Cookie'), /conway_session=;.*Max-Age=0/);
  assert.match(response.headers.get('Set-Cookie'), /conway_oauth=;.*Max-Age=0/);
  assert.equal((await h.db.prepare('SELECT count(*) AS n FROM sessions').first()).n, 0);
  assert.equal((await h.db.prepare('SELECT count(*) AS n FROM oauth_states').first()).n, 0);
  await assert.rejects(h.callback(next), status(400));
});

test('unowned routes and wrong methods fall through, with no alternative auth', async t => {
  const h = await harness(t);
  for (const path of ['/login/google', '/login/email', '/login/dev', '/api/member', '/api/signup', '/api/logout']) assert.equal(await h.route(path), null);
  assert.equal(await h.route('/login/discord', { method: 'POST' }), null);
  h.env.DISCORD_CLIENT_SECRET = '';
  await assert.rejects(h.start(), status(503));
  assert.equal(h.calls.length, 0);
});
