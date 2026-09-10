// Real Chrome with Worker-aligned fixtures and its actual response policies.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile, mkdtemp, rm } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { tmpdir } from 'node:os';
import { join, resolve, extname } from 'node:path';
import { spawn } from 'node:child_process';

const root = fileURLToPath(new URL('../public/', import.meta.url));
const workerSource = await readFile(new URL('../src/index.ts', import.meta.url), 'utf8');
const csp = workerSource.match(/headers\.set\('Content-Security-Policy', "([^"]+)"\)/)?.[1];
assert.ok(csp?.includes("style-src 'self';") && !csp.includes('unsafe-inline'), 'Read strict CSP from Worker entrypoint');
const token = 'a'.repeat(64);
const bindPath = `/fobs/bind?token=${token}`;
const member = { id: 7, version: 1, name: 'Alex Maker', email: 'alex@example.test', leadership: 1, confirmed: 1,
  discord_user_id: '123456789012345678', discord_username: 'alex', waiver: null, payment_status: null,
  access_status: 'MissingWaiver', fob_id: null, bio: 'Wood, metal, and code.', pronouns: '',
  name_override: null, heard_about: '', admin_notes: '', root_family_member: null,
  non_billable: 0, bill_annually: 0, directory_hidden: 0, discord_checkin_notify: 0,
  discount_type: 'family', discount_status: 'approved', discount_request_id: null };
let anonymous = false, signup = null, claimed = false, fail = null;
let stripeHistory = false;
let csrfToken = 'test-csrf';
const requests = [], errors = [];
const networkFailures = [];
const config = { site_name: 'Conway', version: 4, referral_sources: ['Friend'], waiver_version: 3,
  monthly_price_id: 'price_month', yearly_price_id: 'price_year', discord_guild_id: '', discord_role_id: '',
  discord_leadership_channel_id: '', discord_badge_channel_id: '', signup_notify_enabled: false, badge_notify_enabled: false, access_denied_enabled: false,
  discounts: [{ id: 'family', label: 'Family', coupon_id: 'coupon_family' }], donations: [{ price_id: 'price_donate', label: '$25 donation' }],
  notification_templates: { signup: 'Hello {name}', discount: '{name} requested a discount', badge: '{name} checked in', denied: 'Access denied' } };
let waiver = { version: 3, content: 'Use tools safely.\n<script>window.unsafe = true</script>', agreements: ['I understand the risks.', 'I agree to follow safety rules.'] };
const jobs = ['dead', 'pending', 'processing', 'completed'].map((status, index) => ({ id: 11 + index, kind: 'discord_sync', status, attempts: 3, available_at: 1700000000, last_error: status === 'dead' ? 'Discord unavailable' : '' }));
const editable = ['name', 'email', 'pronouns', 'bio', 'heard_about', 'admin_notes', 'directory_hidden', 'discord_checkin_notify', 'leadership', 'non_billable', 'confirmed', 'bill_annually', 'name_override', 'discord_user_id', 'discount_type', 'discount_status', 'fob_id', 'root_family_member'];
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost'), path = url.pathname;
  res.setHeader('Content-Security-Policy', csp);
  res.setHeader('Referrer-Policy', 'no-referrer');
  res.setHeader('X-Content-Type-Options', 'nosniff');
  res.setHeader('Permissions-Policy', 'camera=(), microphone=(), geolocation=()');
  if (path.startsWith('/api/')) {
    let raw = ''; for await (const chunk of req) raw += chunk;
    const body = raw ? JSON.parse(raw) : undefined;
    requests.push({ path, method: req.method, body, headers: req.headers, query: url.search });
    res.setHeader('Content-Type', 'application/json');
    res.setHeader('Cache-Control', 'no-store');
    const reject = (status, error) => { res.statusCode = status; res.end(JSON.stringify({ error })); };
    if (fail?.path === path && (!fail.method || fail.method === req.method)) {
      res.statusCode = fail.status; res.end(JSON.stringify({ error: fail.message })); return;
    }
    if (req.method !== 'GET' && (req.headers['x-csrf-token'] !== csrfToken || req.headers.origin !== `http://${req.headers.host}`)) {
      res.statusCode = 403; res.end(JSON.stringify({ error: 'Missing CSRF or Origin' })); return;
    }
    let data = {};
    if (path === '/api/session') data = { member: anonymous ? null : member, csrf_token: csrfToken, signup };
    else if (path === '/api/config') data = {site_name: config.site_name, referral_sources: config.referral_sources,
      discounts: config.discounts.map(({id, label}) => ({id, label})), donations: config.donations, waiver, stripe_enabled: true};
    else if (path === '/api/admin/config') {
      if (req.method === 'PUT') {
        if (Object.keys(body).some(key => !Object.keys(config).filter(key => key !== 'waiver_version').includes(key))) return reject(400, 'Unknown setting');
        if (body.version !== config.version) return reject(409, 'Settings changed; reload before saving');
        if (Object.keys(body.notification_templates).some(key => !['signup', 'discount', 'badge', 'denied'].includes(key))) return reject(400, 'Unknown template');
        Object.assign(config, body, {version: config.version + 1});
      }
      data = config;
    }
    else if (path === '/api/signup') { anonymous = false; signup = null; member.name = body.name; csrfToken = 'rotated-csrf'; data = {return_to: bindPath}; }
    else if (path === '/api/logout') { anonymous = true; signup = null; }
    else if (path === '/api/member' || /^\/api\/admin\/members\/\d+$/.test(path)) {
      if (['PATCH', 'DELETE'].includes(req.method)) {
        if (!Number.isSafeInteger(body?.version) || body.version < 1) return reject(400, 'Invalid member version');
        if (body.version !== member.version) return reject(409, 'Member changed; reload and retry');
      }
      if (req.method === 'PATCH') {
        const {version, ...fields} = body;
        if (Object.keys(fields).some(key => !editable.includes(key))) return reject(400, 'Field cannot be edited');
        Object.assign(member, fields, {version: version + 1});
      }
      if (req.method === 'DELETE' && stripeHistory) return reject(409, 'Stripe subscription history must retain its member mapping; physical deletion is blocked');
      data = req.method === 'DELETE' ? {deleted: true} : member;
    }
    else if (/^\/api\/admin\/members\/\d+\/approve$/.test(path)) {
      if (member.discount_type === 'family' && !body.root_family_member && !member.root_family_member) return reject(400, 'Family approval requires a primary member');
      if (body.request_id !== member.discount_request_id || member.discount_status !== 'requested') return reject(409, 'This discount request is no longer pending');
      member.discount_status = 'approved'; member.root_family_member = body.root_family_member; member.version++; data = {approved: true};
    }
    else if (path === '/api/profile') Object.assign(member, body, {version: member.version + 1});
    else if (path === '/api/directory') data = { members: [member, { id: 8, name: '<img src=x onerror=window.unsafe=true>', bio: '<script>alert(1)</script>' }] };
    else if (path === '/api/admin/members') {
      if (req.method === 'POST' && Object.keys(body).some(key => !editable.includes(key))) return reject(400, 'Field cannot be edited');
      const matches = !url.searchParams.get('status') || url.searchParams.get('status') === member.access_status;
      data = req.method === 'POST' ? { id: 9 } : { members: matches ? [member] : [], total: matches ? 1 : 0 };
      if (req.method === 'POST') res.statusCode = 201;
    }
    else if (path === '/api/discount') {
      if (req.method === 'POST') {
        if (member.discount_status === 'requested') return reject(409, 'A discount request is already pending');
        member.discount_type = body.discount_type; member.discount_status = 'requested'; member.discount_request_id = token; data = {requested: true};
      } else { member.discount_type = null; member.discount_status = null; member.discount_request_id = null; data = {removed: true}; }
      member.version++;
    }
    else if (path === '/api/waiver') {
      if (!waiver || body.version !== waiver.version) return reject(409, 'Waiver changed; reload and read the current version');
      if (body.agreements.length !== waiver.agreements.length || body.agreements.some(value => value !== true)) return reject(400, 'All waiver agreements are required');
      member.waiver = 1; member.version++; data = {signed: true, version: waiver.version};
    }
    else if (path === '/api/admin/waiver') {
      if (Object.keys(body).sort().join(',') !== 'agreements,content') return reject(400, 'Invalid waiver fields');
      waiver = {...body, version: config.waiver_version + 1}; config.waiver_version = waiver.version; config.version++;
      data = {version: waiver.version}; res.statusCode = 201;
    }
    else if (path === '/api/admin/events') data = { events: [{ created: 1700000000, member: 7, actor: null, event: 'Updated', details: '<img src=x>' }] };
    else if (path === '/api/admin/swipes') data = { swipes: [{ timestamp: 1700000000, member: 7, fob_id: 42, allowed: false, controller: 'Front door' }] };
    else if (path === '/api/admin/jobs') data = { jobs };
    else if (/^\/api\/admin\/jobs\/\d+\/retry$/.test(path)) {
      const job = jobs.find(job => path === `/api/admin/jobs/${job.id}/retry`);
      if (job?.status !== 'dead') return reject(409, 'Only dead jobs can be retried');
      job.status = 'pending'; data = {retried: true};
    }
    else if (path === '/api/kiosk/claims') { data = { token, url: `http://${req.headers.host}${bindPath}`, expires: Math.floor(Date.now() / 1000) + 300 }; res.statusCode = 201; }
    else if (path === `/api/kiosk/claims/${token}`) data = { claimed, expires: Math.floor(Date.now() / 1000) + 300 };
    else if (path === '/api/fobs/bind') {
      if (!/^[a-f0-9]{64}$/.test(body.token)) return reject(400, 'Invalid enrollment claim');
      member.fob_id = 42; member.version++; data = {bound: true};
    }
    else if (path === '/api/billing/checkout' || path === '/api/billing/donation') data = { url: 'javascript:alert(1)' };
    else return reject(404, `Unmocked API: ${req.method} ${path}`);
    res.end(JSON.stringify(data)); return;
  }
  const file = resolve(root, '.' + path);
  if (file !== resolve(root) && !file.startsWith(root)) { res.writeHead(403).end(); return; }
  try {
    const asset = extname(path) ? file : join(root, 'index.html');
    res.setHeader('Content-Type', { '.js': 'text/javascript', '.mjs': 'text/javascript', '.css': 'text/css', '.html': 'text/html' }[extname(asset)] || 'text/plain');
    res.end(await readFile(asset));
  } catch { res.writeHead(404).end(); }
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const origin = `http://127.0.0.1:${server.address().port}`;
const profile = await mkdtemp(join(process.env.TMPDIR || tmpdir(), 'conway-browser-'));
const chrome = spawn(process.env.CHROME || 'google-chrome', ['--headless=new', '--no-sandbox', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-pipe', `--user-data-dir=${profile}`, 'about:blank'], { stdio: ['ignore', 'ignore', 'ignore', 'pipe', 'pipe'] });
let sequence = 0, buffer = '', session;
const callbacks = new Map();
chrome.stdio[4].on('data', chunk => {
  buffer += chunk.toString(); let end;
  while ((end = buffer.indexOf('\0')) >= 0) {
    const message = JSON.parse(buffer.slice(0, end)); buffer = buffer.slice(end + 1);
    if (message.id) { const pending = callbacks.get(message.id); callbacks.delete(message.id); message.error ? pending?.reject(new Error(message.error.message)) : pending?.resolve(message.result); }
    else if (message.method === 'Runtime.exceptionThrown') errors.push(message.params.exceptionDetails.exception?.description || message.params.exceptionDetails.text);
    else if (message.method === 'Log.entryAdded' && message.params.entry.source === 'security') errors.push(message.params.entry.text);
    else if (message.method === 'Network.loadingFailed') networkFailures.push(message.params.errorText);
  }
});
function cdp(method, params = {}, sessionId) {
  const id = ++sequence;
  return new Promise((resolve, reject) => { callbacks.set(id, { resolve, reject }); chrome.stdio[3].write(JSON.stringify({ id, method, params, sessionId }) + '\0'); });
}
async function evaluate(expression) {
  const result = await cdp('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true }, session);
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || 'Browser evaluation failed');
  return result.result.value;
}
async function wait(expression, timeout = 6000) {
  const start = Date.now();
  while (Date.now() - start < timeout) { if (await evaluate(`(${expression}) && !document.querySelector('[aria-busy=true]')`)) return; await new Promise(resolve => setTimeout(resolve, 50)); }
  throw new Error(`Timed out: ${expression}\nNetwork failures: ${networkFailures.join(', ')}\n${await evaluate('document.body.innerText')}`);
}
const ready = "!!document.querySelector('conway-app h1') && !document.querySelector('[aria-busy=true]')";
async function open(path) { await cdp('Page.navigate', { url: origin + path }, session); await wait(ready); }
async function route(path) { await evaluate(`document.querySelector('conway-app').navigate(${JSON.stringify(path)})`); await wait(ready); }
async function fill(name, value) { await evaluate(`(() => { const input = document.querySelector('[name=${name}]'); input.value = ${JSON.stringify(value)}; input.dispatchEvent(new Event('input', {bubbles:true})); })()`); }
async function submit(index = 0) { await evaluate(`document.querySelectorAll('main form')[${index}].requestSubmit()`); }
const contains = text => `document.body.innerText.includes(${JSON.stringify(text)})`;
const last = path => requests.filter(request => request.path === path && request.method !== 'GET').at(-1);
async function test(name, run) { await run(); console.log(`PASS ${name}`); }
try {
  const target = await cdp('Target.createTarget', { url: 'about:blank' });
  session = (await cdp('Target.attachToTarget', { targetId: target.targetId, flatten: true })).sessionId;
  await cdp('Runtime.enable', {}, session); await cdp('Page.enable', {}, session); await cdp('Log.enable', {}, session);
  await cdp('Network.enable', {}, session);
  await test('dashboard and navigation', async () => {
    await open('/dashboard'); assert.equal(await evaluate("document.querySelector('h1').textContent"), 'Hello, Alex Maker.');
    assert.equal(await evaluate("fetch('/dashboard').then(response => response.headers.get('Content-Security-Policy'))"), csp);
    assert.equal(await evaluate("document.querySelector('meta[name=referrer]').content"), 'no-referrer');
    assert.equal(await evaluate("getComputedStyle(document.documentElement).getPropertyValue('--green').trim()"), '#57e58a');
    assert.equal(await evaluate("document.querySelectorAll('[style],style,script:not([src])').length"), 0);
  });
  await test('safe return paths and legitimate payment destinations', async () => {
    assert.deepEqual(await evaluate(`(async () => {
      const {safeReturn, checkoutURL} = await import('/lib.js');
      return [safeReturn('//evil.test'), safeReturn('/\\\\evil.test'), safeReturn('/api/logout'),
        safeReturn('/%2f/evil.test'), safeReturn('/%5cevil.test'), safeReturn('/%0aevil.test'),
        safeReturn('/fobs/bind?token=abc'), checkoutURL('https://checkout.stripe.com/c/pay/test'),
        checkoutURL('https://billing.stripe.com/p/session/test')];
    })()`), ['/dashboard', '/dashboard', '/dashboard', '/dashboard', '/dashboard', '/dashboard', '/fobs/bind?token=abc', 'https://checkout.stripe.com/c/pay/test', 'https://billing.stripe.com/p/session/test']);
  });
  await test('text-only directory and safe search', async () => {
    await route('/directory'); assert.equal(await evaluate("document.querySelectorAll('member-card').length"), 2);
    assert.equal(await evaluate("document.querySelectorAll('img').length"), 0); assert.equal(await evaluate('Boolean(window.unsafe)'), false);
    await fill('search', 'Wood'); assert.equal(await evaluate("document.querySelectorAll('member-card').length"), 1);
    await fill('search', 'not-a-member'); assert.equal(await evaluate(contains('No members match')), true);
  });
  await test('profile CSRF and boolean payloads', async () => {
    await route('/profile'); await fill('bio', 'Updated bio'); await submit(); await wait(contains('Your profile has been saved.'));
    assert.equal(last('/api/profile').body.bio, 'Updated bio'); assert.equal(last('/api/profile').body.directory_hidden, false);
    assert.equal(last('/api/profile').headers['x-csrf-token'], 'test-csrf');
  });
  await test('waiver consent and exact version', async () => {
    await route('/waiver'); await submit(); assert.equal(last('/api/waiver'), undefined);
    await evaluate("document.querySelectorAll('input[type=checkbox]').forEach(input => input.checked = true)");
    await submit(); await wait("location.pathname === '/dashboard'");
    assert.deepEqual(last('/api/waiver').body, { version: 3, name: 'Alex Maker', agreements: [true, true] });
    assert.equal(await evaluate('Boolean(window.unsafe)'), false);
  });
  await test('null waiver and first publication editor', async () => {
    const saved = waiver; waiver = null;
    await route('/waiver'); assert.equal(await evaluate(contains('A waiver has not been published yet')), true);
    assert.equal(await evaluate("document.querySelectorAll('main form').length"), 0);
    await route('/admin/waiver'); assert.equal(await evaluate(contains('Not published')), true);
    assert.equal(await evaluate("document.querySelector('[name=content]').value"), '');
    waiver = saved;
  });
  await test('stale waiver requires review of new version', async () => {
    await route('/waiver');
    await evaluate("document.querySelectorAll('input[type=checkbox]').forEach(input => input.checked = true)");
    const saved = waiver; waiver = {...waiver, version: 99};
    await submit(); await wait(contains('Waiver changed; reload and read the current version'));
    assert.equal(await evaluate('location.pathname'), '/waiver');
    assert.equal(await evaluate(contains('Your edits are still here')), false);
    waiver = saved;
  });
  await test('billing rejects unsafe redirect', async () => {
    await route('/billing'); await fill('frequency', 'annual'); await submit(); await wait(contains('invalid link'));
    assert.deepEqual(last('/api/billing/checkout').body, { annual: true });
  });
  await test('discount request/removal and donation', async () => {
    await route('/billing'); await submit(1); await wait(contains('Discount requested.'));
    assert.deepEqual(last('/api/discount').body, { discount_type: 'family' });
    assert.equal(await evaluate("document.querySelectorAll('[name=discount_type]').length"), 0, 'No duplicate pending request form');
    await evaluate("document.querySelector('[name=confirm]').checked = true"); await submit(1); await wait(contains('Your discount has been removed.'));
    assert.equal(last('/api/discount').method, 'DELETE');
    await route('/donations'); await submit(); await wait(contains('invalid link'));
    assert.deepEqual(last('/api/billing/donation').body, { price_id: 'price_donate' });
  });
  await test('member CRUD and approval', async () => {
    await route('/admin/members/new'); await fill('name', 'New Member'); await fill('email', 'new@example.test');
    await fill('discord_user_id', '999999999999999999'); await submit(); await wait(contains('Member created.'));
    assert.equal(last('/api/admin/members').body.discord_user_id, '999999999999999999');
    const editVersion = member.version;
    await route('/admin/members/7'); await fill('admin_notes', 'Reviewed'); await submit(); await wait(contains('Member saved.'));
    assert.equal(last('/api/admin/members/7').body.version, editVersion);
    assert.equal(member.version, editVersion + 1);
    assert.equal(last('/api/admin/members/7').body.root_family_member, null);
    Object.assign(member, {discount_type: 'family', discount_status: 'requested', discount_request_id: token});
    await route('/admin/members/7');
    await evaluate("document.querySelector('[name=reviewed]').checked = true"); await submit(1);
    assert.equal(last('/api/admin/members/7/approve'), undefined, 'Family primary is required');
    await evaluate("document.querySelectorAll('main form')[1].querySelector('[name=root_family_member]').value = '8'");
    await submit(1); await wait(contains('Discount request approved.'));
    assert.deepEqual(last('/api/admin/members/7/approve').body, {request_id: token, root_family_member: 8});
    const deleteVersion = member.version;
    stripeHistory = true;
    member.stripe_subscription_state = 'canceled';
    assert.equal(await evaluate(contains('Retain the member record even after cancellation')), true);
    await fill('confirmation', '7'); await submit(1); await wait(contains('physical deletion is blocked'));
    assert.deepEqual(last('/api/admin/members/7').body, {version: deleteVersion});
    assert.equal(await evaluate('location.pathname'), '/admin/members/7');
    assert.equal(await evaluate(contains('Reload the latest version')), false, 'Billing conflict is not a version conflict');
    stripeHistory = false; member.stripe_subscription_state = null;
    await submit(1); await wait(contains('Member deleted.')); assert.equal(last('/api/admin/members/7').method, 'DELETE');
    assert.deepEqual(last('/api/admin/members/7').body, {version: deleteVersion});
  });
  await test('stale PATCH and DELETE preserve record until latest revision is loaded', async () => {
    member.non_billable = 1; member.version++;
    await route('/admin/members/7');
    const staleVersion = member.version;
    await fill('admin_notes', 'Unsaved review');
    // Another editor removes a privilege after this form loaded.
    member.non_billable = 0; member.version++;
    await submit(); await wait(contains('Member changed; reload and retry'));
    assert.equal(last('/api/admin/members/7').body.version, staleVersion);
    assert.equal(member.non_billable, 0, 'Stale privilege values were not written');
    assert.equal(member.admin_notes, 'Reviewed');
    assert.equal(await evaluate("document.querySelector('[name=admin_notes]').value"), 'Unsaved review');
    await fill('confirmation', '7'); await submit(1); await wait(contains('Member changed; reload and retry'));
    assert.equal(last('/api/admin/members/7').method, 'DELETE');
    assert.deepEqual(last('/api/admin/members/7').body, {version: staleVersion});
    assert.equal(await evaluate('location.pathname'), '/admin/members/7');
    await route('/admin/members/7');
    assert.equal(await evaluate("document.querySelector('[name=non_billable]').checked"), false);
    const latestVersion = member.version;
    await fill('admin_notes', 'Reviewed latest revision'); await submit(); await wait(contains('Member saved.'));
    assert.equal(last('/api/admin/members/7').body.version, latestVersion);
    assert.equal(member.version, latestVersion + 1);
    await fill('confirmation', '7'); await submit(1); await wait(contains('Member deleted.'));
    assert.deepEqual(last('/api/admin/members/7').body, {version: latestVersion + 1});
  });
  await test('config conflict preserves edits and version', async () => {
    await route('/admin/config'); await fill('site_name', 'My unsaved space');
    fail = { path: '/api/admin/config', method: 'PUT', status: 409, message: 'Configuration changed.' };
    await submit(); await wait(contains('Your edits are still here.'));
    assert.equal(await evaluate("document.querySelector('[name=site_name]').value"), 'My unsaved space');
    assert.equal(last('/api/admin/config').body.version, 4); assert.deepEqual(last('/api/admin/config').body.discounts, config.discounts);
    assert.deepEqual(Object.keys(last('/api/admin/config').body).sort(), Object.keys(config).filter(key => key !== 'waiver_version').sort());
    assert.equal(last('/api/admin/config').body.notification_templates.signup, 'Hello {name}');
    fail = null; await submit(); await wait(contains('Configuration saved.'));
    assert.equal(await evaluate(contains('Editing configuration version 5')), true);
  });
  await test('waiver publishing', async () => {
    await route('/admin/waiver'); await evaluate("document.querySelector('[name=reviewed]').checked = true"); await submit();
    await wait(contains('New waiver version published.')); assert.equal(last('/api/admin/waiver').method, 'PUT');
    assert.deepEqual(Object.keys(last('/api/admin/waiver').body).sort(), ['agreements', 'content']);
    assert.equal(await evaluate(contains('Current version / 4')), true);
  });
  await test('audit, swipes, jobs, export', async () => {
    await route('/admin/events?member=7'); assert.equal(await evaluate(contains('Updated')), true); assert.equal(await evaluate("document.querySelectorAll('img').length"), 0);
    await route('/admin/swipes'); assert.equal(await evaluate(contains('Denied')), true);
    await route('/admin/jobs'); assert.equal(await evaluate("document.querySelectorAll('main form').length"), 1, 'Only dead jobs offer retry');
    for (const status of ['dead', 'pending', 'processing', 'completed']) assert.equal(await evaluate(contains(status)), true);
    await submit(); await wait(contains('queued for retry'));
    assert.equal(await evaluate("document.querySelectorAll('main form').length"), 0);
    await route('/admin/members'); assert.equal(await evaluate("document.querySelector('a[href=\"/api/admin/export\"]').textContent"), 'Export CSV');
  });
  await test('backend access status filters and empty results', async () => {
    for (const status of ['Ready', 'UnconfirmedEmail', 'MissingWaiver', 'PaymentInactive', 'MissingKeyFob', 'FamilyInactive']) {
      member.access_status = status;
      await route('/admin/members'); await fill('status', status); await submit();
      await wait(`location.search.includes(${JSON.stringify(`status=${status}`)}) && !!document.querySelector('tbody')`);
      assert.equal(new URLSearchParams(requests.filter(request => request.path === '/api/admin/members').at(-1).query).get('status'), status);
    }
    await route('/admin/members?status=Ready'); assert.equal(await evaluate(contains('No members match')), true);
    member.access_status = 'MissingWaiver';
  });
  await test('failure and retry', async () => {
    fail = { path: '/api/directory', status: 503, message: 'Temporarily unavailable' }; await route('/directory');
    assert.equal(await evaluate(contains('Temporarily unavailable')), true); fail = null;
    await evaluate("[...document.querySelectorAll('button')].find(button => button.textContent === 'Try again').click()"); await wait("document.querySelectorAll('member-card').length === 2");
  });
  await test('expired sessions, forbidden mutation, and rate limiting stay actionable', async () => {
    fail = {path: '/api/member', status: 401, message: 'Sign in with Discord'};
    await route('/profile');
    assert.equal(await evaluate("!!document.querySelector('main a[href^=\"/login/discord?\"]')"), true);
    fail = null; await route('/profile'); await fill('bio', 'Preserve this unsaved bio');
    fail = {path: '/api/profile', status: 403, message: 'Session expired; reload and try again'};
    await submit(); await wait(contains('Session expired; reload and try again'));
    assert.equal(await evaluate("document.querySelector('[name=bio]').value"), 'Preserve this unsaved bio');
    fail = {path: '/api/profile', status: 400, message: 'Invalid name'};
    await submit(); await wait(contains('Invalid name'));
    assert.equal(await evaluate("document.querySelector('[name=bio]').value"), 'Preserve this unsaved bio');
    fail = {path: '/api/kiosk/claims', status: 429, message: 'Too many scans; wait a minute'};
    await route('/kiosk'); await fill('fob_id', '42'); await submit(); await wait(contains('Too many scans; wait a minute'));
    assert.equal(await evaluate("document.querySelector('main button[type=submit]').disabled"), false);
    fail = null;
  });
  await test('local QR, polling, cleanup', async () => {
    fail = {path: '/api/kiosk/claims', status: 403, message: 'Use a trusted kiosk.'};
    await route('/kiosk'); await fill('fob_id', '42'); await submit(); await wait(contains('Use a trusted kiosk.'));
    assert.equal(await evaluate("document.querySelectorAll('canvas').length"), 0); fail = null;
    await route('/kiosk'); await fill('fob_id', '42'); await submit(); await wait("!!document.querySelector('qr-code canvas')");
    assert.deepEqual(last('/api/kiosk/claims').body, { fob_id: 42 });
    assert.equal(await evaluate("document.querySelectorAll('[style],style,img').length"), 0, 'Canvas QR needs no inline style or image');
    assert.equal(await evaluate("document.querySelector('.claim-link').getAttribute('href')"), origin + bindPath);
    assert.equal(await evaluate("document.querySelector('canvas').getContext('2d').getImageData(0,0,1,1).data[0]"), 255, 'QR renders under CSP');
    claimed = true; await wait(contains('has been claimed')); assert.equal(await evaluate("document.querySelectorAll('canvas,.claim-link').length"), 0);
  });
  await test('fob confirmation and URL cleanup', async () => {
    await route('/fobs/bind?token=invalid'); assert.equal(await evaluate(contains('Enrollment link needed')), true);
    await route(bindPath); await submit(); assert.equal(last('/api/fobs/bind'), undefined);
    await evaluate("document.querySelector('[name=confirm]').checked = true"); await submit(); await wait(contains('Your access fob is now linked.'));
    assert.deepEqual(last('/api/fobs/bind').body, { token }); assert.equal(await evaluate('location.search'), '');
  });
  await test('mobile layout and labels', async () => {
    await cdp('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true }, session);
    for (const path of ['/dashboard', '/directory', '/billing', '/profile', '/admin/members', '/admin/members/7', '/admin/config', '/admin/waiver', '/admin/events', '/admin/jobs', '/kiosk']) {
      await route(path); assert.equal(await evaluate('document.documentElement.scrollWidth <= innerWidth'), true, `Overflow on ${path}`);
      assert.equal(await evaluate("[...document.querySelectorAll('input,select,textarea')].every(input => input.labels.length > 0)"), true, `Unlabelled control on ${path}`);
    }
  });
  await test('leadership gating', async () => {
    member.leadership = 0; const before = requests.filter(request => request.path === '/api/admin/members').length;
    await open('/admin/members'); assert.equal(await evaluate(contains('Leadership access required')), true);
    assert.equal(requests.filter(request => request.path === '/api/admin/members').length, before);
  });
  await test('anonymous login return path', async () => {
    anonymous = true; await open(bindPath);
    assert.equal(await evaluate(`[...document.querySelectorAll('main a')].some(a => new URL(a.href).searchParams.get('return_to') === ${JSON.stringify(bindPath)})`), true);
    await open('/'); assert.equal(await evaluate(contains('Your space to make.')), true);
  });
  await test('anonymous kiosk mutations use preauth CSRF without login', async () => {
    await route('/kiosk'); await fill('fob_id', '42'); await submit(); await wait("!!document.querySelector('qr-code canvas')");
    assert.equal(last('/api/kiosk/claims').headers['x-csrf-token'], csrfToken);
    assert.equal(await evaluate("document.querySelectorAll('main a[href^=\"/login/discord\"]').length"), 0);
    await route('/');
  });
  await test('Discord signup returns to enrollment', async () => {
    signup = { email: 'alex@example.test', discord_user_id: '123456789012345678' };
    await open('/signup?return_to=%2Fprofile'); await fill('name', 'Alex Maker'); await fill('heard_about', 'Friend');
    await submit(); await wait("location.pathname === '/fobs/bind'"); assert.deepEqual(last('/api/signup').body, { name: 'Alex Maker', heard_about: 'Friend' });
    assert.equal(await evaluate('location.pathname + location.search'), bindPath, 'Server-owned return destination wins');
  });
  await test('history navigation and signout', async () => {
    await route('/dashboard'); await route('/profile'); await evaluate('history.back()'); await wait("location.pathname === '/dashboard' && !!document.querySelector('h1')");
    await evaluate("[...document.querySelectorAll('button')].find(button => button.textContent === 'Sign out').click()");
    await wait(contains('You have been signed out.')); assert.equal(last('/api/logout').headers['x-csrf-token'], 'rotated-csrf');
  });
  assert.deepEqual(errors, [], 'No uncaught browser exceptions or CSP/security errors');
  console.log(`All browser checks passed. ${requests.length} mocked API requests.`);
} finally {
  chrome.kill('SIGTERM'); server.closeAllConnections(); await new Promise(resolve => server.close(resolve));
  await new Promise(resolve => chrome.exitCode !== null ? resolve() : chrome.once('exit', resolve));
  await rm(profile, { recursive: true, force: true });
}
