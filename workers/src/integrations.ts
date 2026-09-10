import type { Env, Member, Settings } from './types';
import { HttpError, body, hash, json, now, randomToken, requireMember, settings } from './http';

type ObjectData = Record<string, any>;
type Job = { id: number; kind: string; payload: string; attempts: number; lease_until: number };
type StripeEvent = { id: string; type: string; data: { object: ObjectData } };
const encoder = new TextEncoder();
const LEASE_SECONDS = 180;
const MAX_ATTEMPTS = 10;

class DeliveryError extends Error {
  constructor(message: string, readonly permanent = false, readonly retryAfter = 0, readonly definitiveRejection = false) {
    super(message);
  }
}

async function rawBody(request: Request | Response, limit: number): Promise<string> {
  if (Number(request.headers.get('content-length')) > limit) throw new HttpError(413, 'Request too large');
  const reader = request.body?.getReader();
  if (!reader) return '';
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > limit) {
        await reader.cancel();
        throw new HttpError(413, 'Request too large');
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
  return new TextDecoder('utf-8', { fatal: true, ignoreBOM: false }).decode(bytes);
}

function parseObject(text: string): ObjectData {
  try {
    const value = JSON.parse(text);
    if (value && typeof value === 'object' && !Array.isArray(value)) return value;
  } catch { /* Return the same bounded error for malformed JSON. */ }
  throw new HttpError(400, 'Invalid JSON object');
}

function hex(value: string, bytes: number): Uint8Array<ArrayBuffer> | null {
  if (!new RegExp(`^[a-fA-F0-9]{${bytes * 2}}$`).test(value)) return null;
  return Uint8Array.from(value.match(/../g)!, pair => Number.parseInt(pair, 16));
}

async function verifyStripe(request: Request, env: Env, text: string): Promise<void> {
  if (!env.STRIPE_WEBHOOK_SECRET) throw new HttpError(503, 'Stripe webhook is not configured');
  const parts = (request.headers.get('stripe-signature') || '').split(',').map(part => part.trim());
  const timestamps = parts.filter(part => part.startsWith('t=')).map(part => part.slice(2));
  const timestamp = timestamps[0] || '';
  if (timestamps.length !== 1 || !/^\d+$/.test(timestamp) || Math.abs(now() - Number(timestamp)) > 300) {
    throw new HttpError(401, 'Invalid Stripe signature');
  }
  const key = await crypto.subtle.importKey('raw', encoder.encode(env.STRIPE_WEBHOOK_SECRET),
    { name: 'HMAC', hash: 'SHA-256' }, false, ['verify']);
  for (const part of parts.filter(part => part.startsWith('v1='))) {
    const signature = hex(part.slice(3), 32);
    if (signature && await crypto.subtle.verify('HMAC', key, signature, encoder.encode(`${timestamp}.${text}`))) return;
  }
  throw new HttpError(401, 'Invalid Stripe signature');
}

async function verifyDiscord(request: Request, env: Env, text: string): Promise<void> {
  if (!env.DISCORD_PUBLIC_KEY) throw new HttpError(503, 'Discord interactions are not configured');
  const timestamp = request.headers.get('x-signature-timestamp') || '';
  const signature = hex(request.headers.get('x-signature-ed25519') || '', 64);
  const publicKey = hex(env.DISCORD_PUBLIC_KEY, 32);
  if (!signature || !publicKey || !/^\d+$/.test(timestamp) || Math.abs(now() - Number(timestamp)) > 300) {
    throw new HttpError(401, 'Invalid Discord signature');
  }
  const key = await crypto.subtle.importKey('raw', publicKey, 'Ed25519', false, ['verify']);
  if (!await crypto.subtle.verify('Ed25519', key, signature, encoder.encode(timestamp + text))) {
    throw new HttpError(401, 'Invalid Discord signature');
  }
}

async function remote(url: string, init: RequestInit, service: string, missingOK = false): Promise<ObjectData | null> {
  const response = await fetch(url, { ...init, redirect: 'error', signal: AbortSignal.timeout(15_000) });
  if (missingOK && response.status === 404) { await response.body?.cancel(); return null; }
  const text = await rawBody(response, 2 * 1024 * 1024);
  let data: ObjectData = {};
  try { data = text ? parseObject(text) : {}; } catch {
    if (response.ok) throw new DeliveryError(`${service} returned malformed JSON`);
  }
  if (!response.ok) {
    const retry = Number(response.headers.get('retry-after') || data.retry_after || 0);
    // Do not persist response bodies: payment APIs may include private billing details.
    throw new DeliveryError(`${service} HTTP ${response.status}`,
      response.status >= 400 && response.status < 500 && ![408, 409, 425, 429].includes(response.status),
      Number.isFinite(retry) ? Math.min(86400, Math.max(0, Math.ceil(retry))) : 0,
      service === 'Stripe' && [400, 404].includes(response.status) && data.error?.type === 'invalid_request_error' &&
        !String(data.error?.code || '').startsWith('idempotency') && response.headers.get('stripe-should-retry') !== 'true');
  }
  return data;
}

async function stripe(env: Env, path: string, form?: Record<string, string>, key?: string): Promise<ObjectData> {
  if (!env.STRIPE_SECRET_KEY) throw new DeliveryError('Stripe is not configured');
  return (await remote(`https://api.stripe.com/v1${path}`, {
    method: form ? 'POST' : 'GET',
    headers: { Authorization: `Bearer ${env.STRIPE_SECRET_KEY}`, 'Stripe-Version': '2024-06-20',
      ...(form ? { 'Content-Type': 'application/x-www-form-urlencoded' } : {}),
      ...(key ? { 'Idempotency-Key': key } : {}) },
    body: form ? new URLSearchParams(form).toString() : undefined,
  }, 'Stripe'))!;
}

async function discord(env: Env, path: string, method = 'GET', value?: ObjectData, missingOK = false): Promise<ObjectData | null> {
  if (!env.DISCORD_BOT_TOKEN) throw new DeliveryError('Discord bot is not configured');
  return remote(`https://discord.com/api/v10${path}`, {
    method, headers: { Authorization: `Bot ${env.DISCORD_BOT_TOKEN}`, 'Content-Type': 'application/json' },
    body: value ? JSON.stringify(value) : undefined,
  }, 'Discord', missingOK);
}

async function coordinated(env: Env, name: string, path: string, value: ObjectData): Promise<Response> {
  return env.COORDINATOR.get(env.COORDINATOR.idFromName(name)).fetch(`https://coordinator.internal${path}`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(value),
  });
}

// Trusted backend entry points: the router must authenticate and enforce CSRF before calling.
export async function mutateMember(env: Env, memberID: number, fields: Record<string, string | number | null> | null,
  actorID: number, expectedVersion: number): Promise<Response> {
  return coordinated(env, 'stripe', '/member-mutation', { member_id: memberID, fields, actor_id: actorID, expected_version: expectedVersion });
}

export async function mutateDiscount(env: Env, memberID: number, discountType: string | null, expectedVersion: number): Promise<Response> {
  return coordinated(env, 'stripe', '/discount-mutation', { member_id: memberID, discount_type: discountType, expected_version: expectedVersion });
}

async function familyEligible(env: Env, member: Member): Promise<boolean> {
  if (!member.root_family_member || member.root_family_member === member.id) return false;
  return Boolean(await env.DB.prepare(`SELECT id FROM members WHERE id = ? AND root_family_member IS NULL
    AND payment_status IS NOT NULL`).bind(member.root_family_member).first());
}

export async function integrationRoute(request: Request, env: Env): Promise<Response | null> {
  const path = new URL(request.url).pathname;
  if (!['/api/billing/checkout', '/api/billing/donation', '/webhooks/stripe', '/discord/interactions'].includes(path)) return null;
  if (request.method !== 'POST') throw new HttpError(405, 'Method not allowed');
  if (path === '/webhooks/stripe') {
    const text = await rawBody(request, 1024 * 1024);
    await verifyStripe(request, env, text);
    const event = parseObject(text);
    if (typeof event.id !== 'string' || !/^evt_[A-Za-z0-9_]+$/.test(event.id) || typeof event.type !== 'string' || !event.data?.object) {
      throw new HttpError(400, 'Invalid Stripe event');
    }
    // Event and outbox insertion commit together; duplicate deliveries never replace the original payload.
    await env.DB.batch([
      env.DB.prepare('INSERT INTO stripe_events (id, created, payload) VALUES (?, ?, ?) ON CONFLICT(id) DO NOTHING')
        .bind(event.id, now(), text),
      env.DB.prepare(`INSERT INTO jobs (kind, payload, available_at, created, dedupe_key)
        SELECT 'stripe_event', ?, ?, ?, ? FROM stripe_events WHERE id = ? AND processed IS NULL
        ON CONFLICT(dedupe_key) DO NOTHING`).bind(JSON.stringify({ event_id: event.id }), now(), now(), `stripe:${event.id}`, event.id),
    ]);
    return json({ received: true });
  }
  if (path === '/discord/interactions') {
    const text = await rawBody(request, 64 * 1024);
    await verifyDiscord(request, env, text);
    const interaction = parseObject(text);
    if (interaction.type === 1) return json({ type: 1 });
    const reply = (content: string) => json({ type: 4, data: { content, flags: 64, allowed_mentions: { parse: [] } } });
    const cfg = await settings(env);
    if (interaction.type !== 3 || interaction.guild_id !== cfg.discord_guild_id || !cfg.discord_guild_id ||
        interaction.channel_id !== cfg.discord_leadership_channel_id || !cfg.discord_leadership_channel_id) {
      return reply('This approval is not available here.');
    }
    const userID = interaction.member?.user?.id;
    if (typeof userID !== 'string' || !/^\d+$/.test(userID)) return reply('Leadership authorization required.');
    const leader = await env.DB.prepare('SELECT id FROM members WHERE discord_user_id = ? AND leadership = 1').bind(userID).first<{ id: number }>();
    if (!leader) return reply('Leadership authorization required.');
    const match = /^approve:(\d+):([A-Za-z0-9_-]{1,64})$/.exec(interaction.data?.custom_id || '');
    if (!match || !Number.isSafeInteger(Number(match[1]))) return reply('Invalid approval request.');
    // Recheck leadership and the exact request in the same write, not merely at lookup time.
    const approved = await env.DB.prepare(`UPDATE members SET discount_status = 'approved'
      WHERE id = ? AND discount_status = 'requested' AND discount_request_id = ? AND discount_type IS NOT NULL
      AND EXISTS (SELECT 1 FROM members WHERE id = ? AND discord_user_id = ? AND leadership = 1)
      AND (discount_type != 'family' OR EXISTS (SELECT 1 FROM members root WHERE root.id = members.root_family_member
        AND root.id != members.id AND root.root_family_member IS NULL AND root.payment_status IS NOT NULL))
      RETURNING id, discount_type`).bind(Number(match[1]), match[2], leader.id, userID).first<{ id: number; discount_type: string }>();
    if (!approved) return reply('This request is no longer pending, your authorization changed, or the family primary is missing/inactive.');
    await env.DB.prepare('INSERT INTO member_events (created, member, actor, event, details) VALUES (?, ?, ?, ?, ?)')
      .bind(now(), approved.id, leader.id, 'DiscountApprovedViaDiscord', JSON.stringify({ request_id: match[2], discord_user_id: userID })).run();
    return json({ type: 7, data: { content: `Discount approved for member ${approved.id}. Request: ${match[2]}.${approved.discount_type === 'family' ? ' Linked active primary family account verified.' : ''}`,
      embeds: [], components: [], allowed_mentions: { parse: [] } } });
  }
  const member = await requireMember(request, env);
  const input = await body<ObjectData>(request);
  const donation = path.endsWith('/donation');
  if (donation ? typeof input.price_id !== 'string' : typeof input.annual !== 'boolean') throw new HttpError(400, 'Invalid checkout selection');
  const suppliedKey = request.headers.get('Idempotency-Key');
  if (suppliedKey !== null && !/^[A-Za-z0-9_-]{8,128}$/.test(suppliedKey)) throw new HttpError(400, 'Invalid idempotency key');
  return coordinated(env, 'stripe', '/checkout', { member_id: member.id, donation, annual: input.annual ?? false,
    price_id: input.price_id ?? '', request_key: suppliedKey });
}

export async function runJobs(env: Env): Promise<void> {
  if (env.AUTOMATION_ENABLED !== 'true') return;
  const timestamp = now();
  // A short publication lease avoids floods while retaining recovery when Queue.send fails or an ACK is lost.
  const due = await env.DB.prepare(`UPDATE jobs SET status = 'pending', lease_until = ? WHERE id IN (
    SELECT id FROM jobs WHERE status IN ('pending', 'processing') AND available_at <= ? AND lease_until <= ?
    ORDER BY available_at, id LIMIT 50) RETURNING id`).bind(timestamp + 60, timestamp, timestamp).all<{ id: number }>();
  for (const job of due.results) {
    try { await env.JOBS.send({ id: job.id }); } catch (error) {
      await env.DB.prepare('UPDATE jobs SET lease_until = 0 WHERE id = ? AND lease_until = ?')
        .bind(job.id, timestamp + 60).run();
      throw error;
    }
  }
}

export async function processJob(env: Env, id: number): Promise<void> {
  if (env.AUTOMATION_ENABLED !== 'true') return;
  if (!Number.isSafeInteger(id) || id < 1) throw new Error('Invalid job ID');
  const job = await env.DB.prepare('SELECT kind, payload FROM jobs WHERE id = ?').bind(id).first<{ kind: string; payload: string }>();
  if (!job) return;
  let payload: ObjectData = {};
  try { payload = parseObject(job.payload); } catch { /* The leased handler will dead-letter malformed payloads. */ }
  const name = job.kind === 'stripe_event' ? 'stripe' : `discord:${Number(payload.member_id) || 0}`;
  const response = await coordinated(env, name, '/job', { id });
  if (!response.ok) throw new Error(`Job coordinator HTTP ${response.status}`);
  await response.body?.cancel();
}

function objectID(value: any): string {
  return typeof value === 'string' ? value : typeof value?.id === 'string' ? value.id : '';
}

async function stripeList(env: Env, path: string, query: URLSearchParams): Promise<ObjectData[]> {
  const items: ObjectData[] = [];
  const seen = new Set<string>();
  for (let page = 0; page < 100; page++) {
    const result = await stripe(env, `${path}?${query}`);
    if (!Array.isArray(result.data) || typeof result.has_more !== 'boolean') throw new DeliveryError('Invalid Stripe list response');
    for (const item of result.data) {
      if (!item || typeof item.id !== 'string' || seen.has(item.id)) throw new DeliveryError('Invalid Stripe pagination');
      seen.add(item.id);
      items.push(item);
    }
    if (!result.has_more) return items;
    if (!result.data.length) throw new DeliveryError('Stripe pagination made no progress');
    query.set('starting_after', result.data[result.data.length - 1].id);
  }
  throw new DeliveryError('Stripe listing exceeds safety limit');
}

async function reconcileStripe(env: Env, member: Member, subscriptions?: ObjectData[]): Promise<void> {
  if (!member.stripe_customer_id) return;
  const query = new URLSearchParams({ customer: member.stripe_customer_id, status: 'all', limit: '100', 'expand[]': 'data.latest_invoice.payment_intent' });
  const list = subscriptions || await stripeList(env, '/subscriptions', query);
  const cfg = await settings(env);
  const candidates = list.filter((sub: ObjectData) => objectID(sub.customer) === member.stripe_customer_id &&
    (sub.id === member.stripe_subscription_id || sub.metadata?.conway_member_id === String(member.id) ||
      sub.items?.data?.some((item: ObjectData) => [cfg.monthly_price_id, cfg.yearly_price_id].filter(Boolean).includes(objectID(item.price)))));
  candidates.sort((a: ObjectData, b: ObjectData) => Number(b.created) - Number(a.created) || String(b.id).localeCompare(String(a.id)));
  const sub = candidates.find((candidate: ObjectData) => ['active', 'trialing'].includes(candidate.status)) ||
    candidates.find((candidate: ObjectData) => candidate.id === member.stripe_subscription_id) || candidates[0];
  if (!sub) {
    if (member.stripe_subscription_id) throw new DeliveryError('Tracked Stripe subscription is missing from customer listing');
    return;
  }
  if (typeof sub.status !== 'string' || typeof sub.id !== 'string') throw new DeliveryError('Invalid Stripe subscription response');
  const healthy = ['active', 'trialing'].includes(sub.status);
  const cancellation = healthy ? '' : String(sub.cancellation_details?.reason || '');
  const paymentError = healthy ? '' : String(sub.latest_invoice?.payment_intent?.last_payment_error?.message || '');
  await env.DB.prepare(`UPDATE members SET stripe_subscription_id = ?, stripe_subscription_state = ?,
    stripe_cancellation_reason = ?, stripe_last_payment_error = ? WHERE id = ? AND stripe_customer_id = ?
    AND (stripe_subscription_id IS NOT ? OR stripe_subscription_state IS NOT ? OR stripe_cancellation_reason IS NOT ? OR stripe_last_payment_error IS NOT ?)`)
    .bind(sub.id, sub.status, cancellation, paymentError, member.id, member.stripe_customer_id, sub.id, sub.status, cancellation, paymentError).run();
}

async function stripeEvent(env: Env, eventID: string): Promise<void> {
  const stored = await env.DB.prepare('SELECT payload, processed FROM stripe_events WHERE id = ?').bind(eventID).first<{ payload: string; processed: number | null }>();
  if (!stored || stored.processed !== null) return;
  const event = parseObject(stored.payload) as StripeEvent;
  const relevant = event.type.startsWith('customer.subscription.') ||
    ['checkout.session.completed', 'checkout.session.async_payment_succeeded', 'checkout.session.async_payment_failed', 'invoice.paid', 'invoice.payment_failed'].includes(event.type);
  if (relevant) {
    const customerID = objectID(event.data.object.customer);
    if (!customerID) throw new DeliveryError('Stripe event has no customer ID', true);
    const member = await env.DB.prepare('SELECT * FROM members WHERE stripe_customer_id = ?').bind(customerID).first<Member>();
    if (member) {
      if (event.type.startsWith('checkout.session.')) {
        const sessionID = objectID(event.data.object);
        if (!/^cs_[A-Za-z0-9_]+$/.test(sessionID)) throw new DeliveryError('Invalid Stripe checkout session ID', true);
        const session = await stripe(env, `/checkout/sessions/${encodeURIComponent(sessionID)}`);
        if (objectID(session.customer) !== customerID) throw new DeliveryError('Stripe checkout customer mismatch', true);
        if (session.mode === 'payment') {
          if (!Number.isSafeInteger(session.amount_total) || session.amount_total < 0 || !/^[a-z]{3}$/.test(session.currency) || typeof session.payment_status !== 'string') {
            throw new DeliveryError('Invalid Stripe donation details', true);
          }
          await env.DB.prepare(`INSERT INTO donations (id, member, amount, currency, status, created) VALUES (?, ?, ?, ?, ?, ?)
            ON CONFLICT(id) DO UPDATE SET amount = excluded.amount, currency = excluded.currency, status = excluded.status
            WHERE donations.member = excluded.member AND donations.status != 'paid'`)
            .bind(sessionID, member.id, session.amount_total, session.currency, session.payment_status, Number(session.created) || now()).run();
        } else if (session.mode === 'subscription') await reconcileStripe(env, member);
      } else await reconcileStripe(env, member);
    } else {
      // Email is never an identity fallback. Unknown customers may belong to unrelated Stripe products.
      await env.DB.prepare('INSERT INTO member_events (created, event, details) VALUES (?, ?, ?)')
        .bind(now(), 'StripeCustomerUnmapped', JSON.stringify({ event_id: eventID, customer_id: customerID })).run();
    }
  }
  await env.DB.prepare('UPDATE stripe_events SET processed = ? WHERE id = ? AND processed IS NULL').bind(now(), eventID).run();
}

function denialReason(member: Member): string {
  switch (member.access_status) {
    case 'UnconfirmedEmail': return 'Your account is not confirmed. Contact leadership to verify your Discord account.';
    case 'MissingWaiver': return 'Sign the current waiver from your membership page.';
    case 'MissingKeyFob': return 'Register your key fob at the kiosk, or ask leadership for help.';
    case 'FamilyInactive': return 'Your family membership is inactive. Contact the primary account holder.';
    case 'PaymentInactive':
      if (member.stripe_subscription_state === 'past_due') return `Your last payment failed${member.stripe_last_payment_error ? `: ${member.stripe_last_payment_error}` : '.'} Update your payment method from your membership page.`;
      if (member.stripe_cancellation_reason === 'payment_disputed') return 'Your payment was disputed. Contact leadership to resolve the dispute.';
      if (member.stripe_subscription_state === 'canceled') return 'Your membership was canceled. Renew from your membership page, or contact leadership.';
      return 'Your membership payment is inactive. Complete checkout or update your payment method from your membership page.';
    default: return 'Access is unavailable. Contact leadership for help.';
  }
}

async function discordJob(env: Env, job: Job, payload: ObjectData, state: DurableObjectState): Promise<void> {
  if (!Number.isSafeInteger(payload.member_id) || payload.member_id < 1) throw new DeliveryError('Invalid member job payload', true);
  const member = await env.DB.prepare('SELECT * FROM members WHERE id = ?').bind(payload.member_id).first<Member>();
  const cfg = await settings(env);
  if (job.kind === 'discord_sync') {
    const previous = await state.storage.get<{ user: string; guild: string; role: string }>('discord-role');
    if (previous && (previous.user !== member?.discord_user_id || previous.guild !== cfg.discord_guild_id || previous.role !== cfg.discord_role_id)) {
      await discord(env, `/guilds/${encodeURIComponent(previous.guild)}/members/${encodeURIComponent(previous.user)}/roles/${encodeURIComponent(previous.role)}`, 'DELETE', undefined, true);
      await state.storage.delete('discord-role');
    }
    if (!member?.discord_user_id) return;
    if (!cfg.discord_guild_id || !cfg.discord_role_id) throw new DeliveryError('Discord role sync is not configured');
    const path = `/guilds/${encodeURIComponent(cfg.discord_guild_id)}/members/${encodeURIComponent(member.discord_user_id)}`;
    // Record the target before a role write so retries can clean up even if the identity changes after a crash.
    await state.storage.put('discord-role', { user: member.discord_user_id, guild: cfg.discord_guild_id, role: cfg.discord_role_id });
    const guildMember = await discord(env, path, 'GET', undefined, true);
    if (guildMember) {
      if (!Array.isArray(guildMember.roles)) throw new DeliveryError('Invalid Discord guild member response');
      // Legacy roles follow billing, not fob/waiver readiness.
      const wanted = Boolean(member.payment_status);
      if (guildMember.roles.includes(cfg.discord_role_id) !== wanted) {
        await discord(env, `${path}/roles/${encodeURIComponent(cfg.discord_role_id)}`, wanted ? 'PUT' : 'DELETE');
      }
    }
    await env.DB.prepare(`UPDATE members SET discord_last_synced = ?, discord_username = ?
      WHERE id = ? AND discord_user_id = ? AND payment_status IS ?`)
      .bind(now(), guildMember?.nick || guildMember?.user?.global_name || guildMember?.user?.username || member.discord_username || '',
        member.id, member.discord_user_id, member.payment_status).run();
    return;
  }
  if (!member) return;
  if (!['signup', 'discount', 'badge', 'denied'].includes(job.kind)) throw new DeliveryError(`Unknown job kind: ${job.kind}`, true);
  if (job.kind === 'signup' && !cfg.signup_notify_enabled) return;
  if (job.kind === 'discount' && (typeof payload.request_id !== 'string' || member.discount_request_id !== payload.request_id || member.discount_status !== 'requested')) return;
  if (job.kind === 'badge' && (!cfg.badge_notify_enabled || !member.discord_checkin_notify)) return;
  if (job.kind === 'denied' && (!cfg.access_denied_enabled || !member.discord_user_id)) return;
  let interval = 0;
  if (job.kind === 'badge' || job.kind === 'denied') {
    if (typeof payload.swipe_id !== 'string') throw new DeliveryError('Missing swipe ID', true);
    const swipe = await env.DB.prepare('SELECT allowed FROM fob_swipes WHERE uid = ? AND member = ?')
      .bind(payload.swipe_id, member.id).first<{ allowed: number }>();
    if (!swipe || Boolean(swipe.allowed) !== (job.kind === 'badge')) return;
    interval = job.kind === 'badge' ? 4 * 3600 : 3600;
    const last = await env.DB.prepare('SELECT last_sent FROM notification_state WHERE member = ? AND kind = ?')
      .bind(member.id, job.kind).first<{ last_sent: number }>();
    if (last && last.last_sent > now() - interval) return;
  }
  const displayName = member.name_override || member.name || member.discord_username || `Member ${member.id}`;
  const defaults: Record<string, string> = {
    signup: '{name} joined {site_name}.', discount: '{name} requested the {discount} discount. Request: {request_id}',
    badge: '{name} just badged into the makerspace.', denied: 'Hi {name}, your fob was denied access. {reason} {site_url}',
  };
  const variables: Record<string, string> = { name: displayName, site_name: cfg.site_name, site_url: env.SITE_URL,
    discount: cfg.discounts.find(item => item.id === member.discount_type)?.label || member.discount_type || '',
    request_id: member.discount_request_id || '', reason: denialReason(member), member_id: String(member.id),
    discount_type: member.discount_type || '', access_status: member.access_status };
  const template = cfg.notification_templates[job.kind as keyof Settings['notification_templates']] || defaults[job.kind];
  const content = template.replace(/\{([a-z_]+)\}/g, (token, key: string) => variables[key] ?? token).slice(0, 2000);
  let channel = job.kind === 'badge' ? cfg.discord_badge_channel_id : cfg.discord_leadership_channel_id;
  if (job.kind === 'denied') {
    const dm = await discord(env, '/users/@me/channels', 'POST', { recipient_id: member.discord_user_id });
    if (!dm || typeof dm.id !== 'string') throw new DeliveryError('Invalid Discord DM channel');
    channel = dm.id;
  }
  if (!channel) throw new DeliveryError('Discord notification channel is not configured');
  const message: ObjectData = { content, allowed_mentions: { parse: [] }, nonce: String(job.id), enforce_nonce: true };
  if (job.kind === 'discount') {
    const customID = `approve:${member.id}:${member.discount_request_id}`;
    if (customID.length > 100 || !/^approve:\d+:[A-Za-z0-9_-]{1,64}$/.test(customID)) throw new DeliveryError('Invalid discount request ID', true);
    message.components = [{ type: 1, components: [{ type: 2, style: 3, label: 'Approve', custom_id: customID }] }];
  }
  const receipt = `message:${job.id}`;
  if (!await state.storage.get<number>(receipt)) {
    await discord(env, `/channels/${encodeURIComponent(channel)}/messages`, 'POST', message);
    // Survives a later D1 failure; Discord's nonce covers the narrow send/receipt crash window.
    await state.storage.put(receipt, now());
  }
  if (interval) await env.DB.prepare(`INSERT INTO notification_state (member, kind, last_sent) VALUES (?, ?, ?)
    ON CONFLICT(member, kind) DO UPDATE SET last_sent = excluded.last_sent`).bind(member.id, job.kind, now()).run();
}

async function executeJob(env: Env, id: number, state: DurableObjectState): Promise<void> {
  if (env.AUTOMATION_ENABLED !== 'true') return;
  if (!Number.isSafeInteger(id) || id < 1) throw new HttpError(400, 'Invalid job ID');
  const timestamp = now();
  const job = await env.DB.prepare(`UPDATE jobs SET status = 'processing', attempts = attempts + 1, lease_until = ?
    WHERE id = ? AND available_at <= ? AND (status = 'pending' OR (status = 'processing' AND lease_until <= ?)) RETURNING *`)
    .bind(timestamp + LEASE_SECONDS, id, timestamp, timestamp).first<Job>();
  if (!job) return;
  try {
    let payload: ObjectData;
    try { payload = parseObject(job.payload); } catch { throw new DeliveryError('Invalid job JSON', true); }
    if (job.kind === 'stripe_event') {
      if (typeof payload.event_id !== 'string') throw new DeliveryError('Missing Stripe event ID', true);
      await stripeEvent(env, payload.event_id);
    } else await discordJob(env, job, payload, state);
    await env.DB.prepare(`UPDATE jobs SET status = 'completed', completed = ?, lease_until = 0, last_error = ''
      WHERE id = ? AND status = 'processing' AND attempts = ?`).bind(now(), id, job.attempts).run();
  } catch (error) {
    const dead = job.attempts >= MAX_ATTEMPTS || (error instanceof DeliveryError && error.permanent);
    const delay = Math.max(error instanceof DeliveryError ? error.retryAfter : 0,
      Math.min(21600, 30 * 2 ** Math.min(job.attempts - 1, 10)) + Math.floor(Math.random() * 15));
    await env.DB.prepare(`UPDATE jobs SET status = ?, available_at = ?, lease_until = 0, last_error = ?, completed = ?
      WHERE id = ? AND status = 'processing' AND attempts = ?`)
      .bind(dead ? 'dead' : 'pending', now() + delay, (error instanceof Error ? error.message : 'Delivery failed').slice(0, 500),
        dead ? now() : null, id, job.attempts).run();
  }
}

async function edgeRequest(env: Env, path: string, value?: ObjectData): Promise<ObjectData> {
  if (!env.EDGE_URL || !env.EDGE_TOKEN) throw new DeliveryError('Edge is not configured');
  const url = new URL(env.EDGE_URL);
  if (url.protocol !== 'https:') throw new DeliveryError('EDGE_URL must use HTTPS', true);
  return (await remote(new URL(path, url).toString(), {
    method: value ? 'POST' : 'GET', headers: { Authorization: `Bearer ${env.EDGE_TOKEN}`, 'Content-Type': 'application/json' },
    body: value ? JSON.stringify(value) : undefined,
  }, 'Edge'))!;
}

async function ingestSwipes(env: Env): Promise<void> {
  const result = await edgeRequest(env, '/api/swipes?limit=100');
  if (!Array.isArray(result.events) || result.events.length > 100) throw new DeliveryError('Invalid edge swipe batch');
  const statements: D1PreparedStatement[] = [];
  const ids: string[] = [];
  for (const event of result.events) {
    const timestamp = typeof event.time === 'string' ? Date.parse(event.time) / 1000 : NaN;
    if (typeof event.id !== 'string' || !event.id || event.id.length > 200 || !Number.isFinite(timestamp) || timestamp < 0 ||
        !/T.*(?:Z|[+-]\d\d:\d\d)$/.test(event.time) || !Number.isSafeInteger(event.fob) || event.fob < 1 || event.fob > 0xffffffff ||
        typeof event.allowed !== 'boolean' || typeof event.controller !== 'string' || event.controller.length > 200) {
      throw new DeliveryError('Invalid edge swipe event');
    }
    ids.push(event.id);
    statements.push(env.DB.prepare(`INSERT INTO fob_swipes (uid, timestamp, fob_id, member, allowed, controller)
      VALUES (?, ?, ?, (SELECT id FROM members WHERE fob_id = ?), ?, ?) ON CONFLICT(uid) DO NOTHING`)
      .bind(event.id, Math.floor(timestamp), event.fob, event.fob, event.allowed ? 1 : 0, event.controller));
  }
  // Never acknowledge until the whole batch and its trigger-created outbox jobs are durable.
  if (statements.length) {
    await env.DB.batch(statements);
    await edgeRequest(env, '/api/swipes/ack', { ids });
  }
}

export async function scheduled(env: Env): Promise<void> {
  const timestamp = now();
  await env.DB.batch([
    env.DB.prepare('DELETE FROM sessions WHERE expires <= ?').bind(timestamp),
    env.DB.prepare('DELETE FROM oauth_states WHERE expires <= ?').bind(timestamp),
    env.DB.prepare('DELETE FROM enrollment_claims WHERE expires <= ?').bind(timestamp),
    env.DB.prepare('DELETE FROM rate_limits WHERE expires <= ?').bind(timestamp),
  ]);
  if (env.AUTOMATION_ENABLED !== 'true') return;
  // Keep event/job dedupe tombstones and unacknowledged swipes; deleting them can replay external effects.
  await env.DB.prepare(`INSERT INTO jobs (kind, payload, available_at, created, dedupe_key)
    SELECT 'discord_sync', json_object('member_id', m.id), ?, ?, 'discord-reconcile:' || m.id || ':' || ?
    FROM members m WHERE m.discord_user_id IS NOT NULL AND COALESCE(m.discord_last_synced, 0) < ?
    AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.kind = 'discord_sync' AND j.status IN ('pending', 'processing')
      AND json_extract(j.payload, '$.member_id') = m.id)
    ORDER BY COALESCE(m.discord_last_synced, 0), m.id LIMIT 50 ON CONFLICT(dedupe_key) DO NOTHING`)
    .bind(timestamp, timestamp, Math.floor(timestamp / 3600), timestamp - 86400).run();
  const operations: Promise<unknown>[] = [runJobs(env)];
  if (env.STRIPE_SECRET_KEY) operations.push(coordinated(env, 'stripe', '/reconcile', {}).then(async response => {
    if (!response.ok) throw new Error(`Stripe reconciliation HTTP ${response.status}`);
    await response.body?.cancel();
  }));
  if (env.EDGE_URL && env.EDGE_TOKEN) operations.push(coordinated(env, 'edge', '/edge', {}).then(async response => {
    if (!response.ok) throw new Error(`Edge synchronization HTTP ${response.status}`);
    await response.body?.cancel();
  }));
  const results = await Promise.allSettled(operations);
  const failures = results.filter((result): result is PromiseRejectedResult => result.status === 'rejected');
  if (failures.length) throw new AggregateError(failures.map(result => result.reason), 'Scheduled integration failures');
}

type Mutation = { key: string; created: number; path: string; form: Record<string, string>; result?: ObjectData };

export class MembershipCoordinator {
  private tail: Promise<unknown> = Promise.resolve();
  constructor(private readonly state: DurableObjectState, private readonly env: Env) {}

  fetch(request: Request): Promise<Response> {
    // DO input gates alone do not serialize fetch awaits. Chain the entire operation, including all I/O.
    const result = this.tail.then(() => this.handle(request));
    this.tail = result.catch(() => undefined);
    return result;
  }

  private async mutate(slot: string, path: string, form: Record<string, string>): Promise<ObjectData> {
    let operation = await this.state.storage.get<Mutation>(slot);
    if (!operation) {
      operation = { key: `conway:${await hash(this.env.SITE_URL)}:${randomToken()}`, created: now(), path, form };
      await this.state.storage.put(slot, operation);
    }
    if (operation.path !== path || JSON.stringify(operation.form) !== JSON.stringify(form)) {
      throw new HttpError(409, 'An earlier billing operation is pending; retry its original selection');
    }
    if (operation.result) return operation.result;
    // Stripe forgets idempotency keys after 24h. Never silently repeat an ambiguous financial write after that window.
    if (operation.created < now() - 23 * 3600) throw new HttpError(409, 'An earlier billing operation needs leadership reconciliation');
    let result: ObjectData;
    try { result = await stripe(this.env, path, form, operation.key); } catch (error) {
      // Only Stripe's explicit validation/resource rejection proves this write did not execute.
      if (error instanceof DeliveryError && error.definitiveRejection) await this.state.storage.delete(slot);
      throw error;
    }
    await this.state.storage.put(slot, { ...operation, result });
    return result;
  }

  private async expireCheckouts(member: Member): Promise<void> {
    const sessions = new Map<string, ObjectData>();
    const slots = [`checkout:${member.id}:membership`, `checkout:${member.id}:donation`];
    for (const slot of slots) {
      const operation = await this.state.storage.get<Mutation>(slot);
      if (!operation || operation.path !== '/checkout/sessions') continue;
      // Resolve an ambiguous create using its original idempotency key, then revoke the resulting session.
      const result = operation.result || await this.mutate(slot, operation.path, operation.form);
      if (!/^cs_[A-Za-z0-9_]+$/.test(result.id)) throw new DeliveryError('Invalid stored checkout session');
      sessions.set(result.id, await stripe(this.env, `/checkout/sessions/${encodeURIComponent(result.id)}`));
    }
    if (!member.stripe_customer_id) {
      if (sessions.size || await this.state.storage.get<Mutation>(`customer:${member.id}`)) {
        throw new HttpError(409, 'Unresolved Stripe customer mapping; reconcile billing before changing this member');
      }
      return;
    }
    const query = () => new URLSearchParams({ customer: member.stripe_customer_id!, status: 'open', limit: '100' });
    for (const session of await stripeList(this.env, '/checkout/sessions', query())) sessions.set(session.id, session);
    for (const [id, session] of sessions) {
      if (objectID(session.customer) !== member.stripe_customer_id) throw new DeliveryError('Checkout customer mismatch');
      if (session.status === 'open') {
        try { await this.mutate(`expire:${id}`, `/checkout/sessions/${encodeURIComponent(id)}/expire`, {}); } catch (error) {
          // Completion can win the race with expiration. Never treat a timeout as proof of revocation.
          const current = await stripe(this.env, `/checkout/sessions/${encodeURIComponent(id)}`);
          if (objectID(current.customer) !== member.stripe_customer_id || !['complete', 'expired'].includes(current.status)) throw error;
        }
        const current = await stripe(this.env, `/checkout/sessions/${encodeURIComponent(id)}`);
        if (objectID(current.customer) !== member.stripe_customer_id || !['complete', 'expired'].includes(current.status)) {
          throw new HttpError(409, 'Checkout revocation is not confirmed');
        }
      } else if (!['complete', 'expired'].includes(session.status)) throw new HttpError(409, 'Checkout state is uncertain');
    }
    if ((await stripeList(this.env, '/checkout/sessions', query())).length) throw new HttpError(409, 'Open checkout sessions remain');
    for (const slot of slots) await this.state.storage.delete(slot);
  }

  private async memberMutation(input: ObjectData, selfDiscount: boolean): Promise<Response> {
    const { member_id: id, expected_version: version } = input;
    if (!Number.isSafeInteger(id) || id < 1 || !Number.isSafeInteger(version) || version < 1) throw new HttpError(400, 'Member ID and version are required');
    const member = await this.env.DB.prepare('SELECT * FROM members WHERE id = ?').bind(id).first<Member & { version: number }>();
    if (!member) throw new HttpError(404, 'Member not found');
    if (member.version !== version) throw new HttpError(409, 'Member changed; reload and retry');
    const actor = selfDiscount ? id : input.actor_id;
    if (!selfDiscount && (!Number.isSafeInteger(actor) || !await this.env.DB.prepare('SELECT id FROM members WHERE id = ? AND leadership = 1').bind(actor).first())) {
      throw new HttpError(403, 'Leadership authorization required');
    }
    let fields: Record<string, string | number | null> | null = input.fields;
    let requiresFamilyEligibility = false;
    const cfg = await settings(this.env);
    if (selfDiscount) {
      if (input.discount_type !== null && (typeof input.discount_type !== 'string' || !cfg.discounts.some(item => item.id === input.discount_type))) {
        throw new HttpError(400, 'Unknown discount type');
      }
      if (input.discount_type !== null && member.discount_status === 'requested') throw new HttpError(409, 'A discount request is already pending');
      fields = { discount_type: input.discount_type, discount_status: input.discount_type === null ? null : 'requested',
        discount_request_id: input.discount_type === null ? null : randomToken() };
    }
    if (fields !== null) {
      if (!fields || typeof fields !== 'object' || Array.isArray(fields) || !Object.keys(fields).length) throw new HttpError(400, 'No fields supplied');
      const strings: Record<string, number> = { name: 200, email: 254, pronouns: 100, bio: 4000, heard_about: 300, admin_notes: 10000 };
      const nullable = ['name_override', 'discord_user_id', 'discount_type', 'discount_status', 'discount_request_id'];
      const flags = ['directory_hidden', 'discord_checkin_notify', 'leadership', 'non_billable', 'confirmed', 'bill_annually'];
      for (const [key, value] of Object.entries(fields)) {
        if (Object.hasOwn(strings, key)) {
          if (typeof value !== 'string' || value.length > strings[key]) throw new HttpError(400, `Invalid ${key}`);
        } else if (nullable.includes(key)) {
          if (value !== null && (typeof value !== 'string' || !value || value.length > 200)) throw new HttpError(400, `Invalid ${key}`);
        } else if (flags.includes(key)) {
          if (value !== 0 && value !== 1) throw new HttpError(400, `Invalid ${key}`);
        } else if (['fob_id', 'root_family_member'].includes(key)) {
          if (value !== null && (!Number.isSafeInteger(value) || Number(value) < 1 || Number(value) > (key === 'fob_id' ? 0xffffffff : Number.MAX_SAFE_INTEGER))) throw new HttpError(400, `Invalid ${key}`);
        } else throw new HttpError(400, `Field cannot be edited: ${key}`);
      }
      if (fields.email !== undefined && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(String(fields.email))) throw new HttpError(400, 'Invalid contact email');
      if (fields.discord_user_id && !/^\d{5,25}$/.test(String(fields.discord_user_id))) throw new HttpError(400, 'Invalid Discord ID');
      if (fields.discount_status && !['approved', 'requested'].includes(String(fields.discount_status))) throw new HttpError(400, 'Invalid discount status');
      if (fields.discount_type && !cfg.discounts.some(item => item.id === fields!.discount_type)) throw new HttpError(400, 'Unknown discount type');
      if (fields.discount_type === null) { fields.discount_status = null; fields.discount_request_id = null; }
      const next = { ...member, ...fields } as Member;
      if (!next.discount_type && next.discount_status) throw new HttpError(400, 'A discount type is required');
      requiresFamilyEligibility = next.discount_type === 'family' && next.discount_status !== 'requested' &&
        ['discount_type', 'discount_status', 'root_family_member'].some(key => Object.hasOwn(fields!, key) && fields![key] !== member[key as keyof Member]);
      if (requiresFamilyEligibility && !await familyEligible(this.env, next)) {
        throw new HttpError(409, 'Family discount requires a linked active primary member');
      }
      if (next.root_family_member !== member.root_family_member && next.root_family_member !== null && next.root_family_member !== undefined) {
        if (next.root_family_member === id || !await this.env.DB.prepare('SELECT id FROM members WHERE id = ? AND root_family_member IS NULL').bind(next.root_family_member).first()) {
          throw new HttpError(400, 'Family root must be a primary member');
        }
      }
      if (next.discount_status === 'requested' && (member.discount_status !== 'requested' || member.discount_type !== next.discount_type)) {
        fields.discount_request_id = randomToken();
      }
    } else if (member.paypal_subscription_id || member.stripe_subscription_state && !['canceled', 'incomplete_expired'].includes(member.stripe_subscription_state)) {
      throw new HttpError(409, 'Cancel and reconcile billing before deleting a member');
    }
    if (fields === null && await this.env.DB.prepare('SELECT id FROM members WHERE root_family_member = ? LIMIT 1').bind(id).first()) {
      throw new HttpError(409, 'Reassign family dependents before deleting their primary member');
    }
    const keys = fields === null ? [] : Object.keys(fields);
    const changedKeys = keys.filter(key => fields![key] !== member[key as keyof Member]);
    const affectsCheckout = fields === null || changedKeys.some(key => ['discount_type', 'discount_status', 'discount_request_id', 'root_family_member', 'confirmed', 'non_billable', 'bill_annually'].includes(key));
    const marker = `mutation-pending:${id}`;
    if (affectsCheckout || await this.state.storage.get<boolean>(marker)) {
      // Persist before revocation: an interrupted mutation must not make its old checkout available again.
      await this.state.storage.put(marker, true);
      await this.state.storage.put(`checkout-epoch:${id}`, (await this.state.storage.get<number>(`checkout-epoch:${id}`) || 0) + 1);
      await this.expireCheckouts(member);
      if (member.stripe_customer_id) {
        const subscriptions = await stripeList(this.env, '/subscriptions', new URLSearchParams({ customer: member.stripe_customer_id, status: 'all', limit: '100', 'expand[]': 'data.latest_invoice.payment_intent' }));
        if (fields === null && subscriptions.length) {
          // Revocation is confirmed and deletion is definitively rejected; no unfinished mutation remains.
          await this.state.storage.delete(marker);
          throw new HttpError(409, 'Stripe subscription history must retain its member mapping; physical deletion is blocked');
        }
        await reconcileStripe(this.env, member, subscriptions);
      } else if (fields === null && member.stripe_subscription_id) throw new HttpError(409, 'Stripe subscription mapping is uncertain');
      // A primary losing eligibility must not leave its dependents' discounted checkout URLs usable.
      if (changedKeys.some(key => ['confirmed', 'non_billable', 'root_family_member'].includes(key))) {
        const dependents = await this.env.DB.prepare('SELECT * FROM members WHERE root_family_member = ?').bind(id).all<Member>();
        for (const dependent of dependents.results) {
          await this.state.storage.put(`checkout-epoch:${dependent.id}`, (await this.state.storage.get<number>(`checkout-epoch:${dependent.id}`) || 0) + 1);
          await this.expireCheckouts(dependent);
        }
      }
    }
    const authorization = selfDiscount ? '' : ' AND EXISTS (SELECT 1 FROM members actor WHERE actor.id = ? AND actor.leadership = 1)';
    const args = selfDiscount ? [id, version] : [id, version, actor];
    const familyGuard = requiresFamilyEligibility ?
      ` AND EXISTS (SELECT 1 FROM members root WHERE root.id = ? AND root.id != members.id AND root.root_family_member IS NULL AND root.payment_status IS NOT NULL)` : '';
    if (familyGuard) args.push(Number(fields!.root_family_member ?? member.root_family_member));
    const mutation = fields === null ? this.env.DB.prepare(`DELETE FROM members WHERE id = ? AND version = ?${authorization} RETURNING id`).bind(...args) :
      this.env.DB.prepare(`UPDATE members SET ${keys.map(key => `${key} = ?`).join(', ')} WHERE id = ? AND version = ?${authorization}${familyGuard} RETURNING id`)
        .bind(...keys.map(key => fields![key]), ...args);
    const result = await this.env.DB.batch<Record<string, unknown>>([
      mutation,
      // changes() is the preceding top-level CAS write, excluding its triggers. Failed CAS produces no audit.
      this.env.DB.prepare(`INSERT INTO member_events (created, member, actor, event, details)
        SELECT ?, ?, (SELECT id FROM members WHERE id = ?), ?, ? WHERE changes() = 1`)
        .bind(now(), fields === null ? null : id, actor, fields === null ? 'MemberDeleted' : selfDiscount ? 'DiscountChanged' : 'AdminMemberUpdated',
          JSON.stringify({ member_id: id, actor_id: actor, expected_version: version, fields: keys, stripe_customer_id: member.stripe_customer_id })),
      this.env.DB.prepare('SELECT * FROM members WHERE id = ?').bind(id),
    ]);
    if (!result[0].results.length) throw new HttpError(409, 'Member or authorization changed; reload and retry');
    await this.state.storage.delete(marker);
    return json(fields === null ? { deleted: true } : selfDiscount ? { updated: true, version: result[2].results[0]?.version } : result[2].results[0]);
  }

  private async checkout(input: ObjectData): Promise<Response> {
    if (!Number.isSafeInteger(input.member_id) || input.member_id < 1 || typeof input.donation !== 'boolean' || typeof input.annual !== 'boolean') {
      throw new HttpError(400, 'Invalid checkout request');
    }
    let member = await this.env.DB.prepare('SELECT * FROM members WHERE id = ?').bind(input.member_id).first<Member>();
    if (!member) throw new HttpError(404, 'Member not found');
    if (await this.state.storage.get<boolean>(`mutation-pending:${member.id}`)) throw new HttpError(409, 'Member billing change is pending; retry the member change first');
    const requestSlot = input.request_key ? `request:${input.member_id}:${input.donation ? 'donation' : 'membership'}:${await hash(input.request_key)}` : '';
    const epoch = await this.state.storage.get<number>(`checkout-epoch:${member.id}`) || 0;
    const requestFingerprint = await hash(JSON.stringify({ donation: input.donation, annual: input.annual, price_id: input.price_id }));
    if (requestSlot) {
      const previous = await this.state.storage.get<{ fingerprint: string; url: string; epoch?: number }>(requestSlot);
      if (previous) {
        if (previous.fingerprint !== requestFingerprint) throw new HttpError(409, 'Idempotency key was already used for another selection');
        if ((previous.epoch || 0) !== epoch) throw new HttpError(409, 'Checkout was revoked; start a new checkout request');
        // Never replay a cached URL before checking current membership, pricing, and live session status.
      }
    }
    if (!this.env.STRIPE_SECRET_KEY) throw new HttpError(503, 'Stripe is not configured');
    const cfg = await settings(this.env);
    const price = input.donation ? cfg.donations.find(item => item.price_id === input.price_id)?.price_id :
      input.annual ? cfg.yearly_price_id : cfg.monthly_price_id;
    if (!price) throw new HttpError(400, 'Selected Stripe price is not configured');
    if (!member.stripe_customer_id) {
      const slot = `customer:${member.id}`;
      const prior = await this.state.storage.get<Mutation>(slot);
      const customer = await this.mutate(slot, '/customers', prior?.form || {
        email: member.email, name: member.name, 'metadata[conway_member_id]': String(member.id),
      });
      if (!/^cus_[A-Za-z0-9_]+$/.test(customer.id)) throw new DeliveryError('Invalid Stripe customer response');
      await this.env.DB.prepare(`UPDATE members SET stripe_customer_id = ? WHERE id = ? AND (stripe_customer_id IS NULL OR stripe_customer_id = '')`)
        .bind(customer.id, member.id).run();
      member = (await this.env.DB.prepare('SELECT * FROM members WHERE id = ?').bind(member.id).first<Member>())!;
      if (member.stripe_customer_id !== customer.id) throw new HttpError(409, 'Billing identity changed; retry');
    }
    if (!input.donation) {
      // Also discover a just-completed checkout whose webhook has not arrived yet.
      await reconcileStripe(this.env, member);
      member = (await this.env.DB.prepare('SELECT * FROM members WHERE id = ?').bind(member.id).first<Member>())!;
      await this.env.DB.prepare('UPDATE members SET bill_annually = ? WHERE id = ? AND bill_annually != ?')
        .bind(input.annual ? 1 : 0, member.id, input.annual ? 1 : 0).run();
    }
    const portal = !input.donation && member.stripe_subscription_state && !['canceled', 'incomplete_expired'].includes(member.stripe_subscription_state);
    if (!portal && !input.donation && member.discount_type === 'family' && member.discount_status !== 'requested' && !await familyEligible(this.env, member)) {
      throw new HttpError(409, 'Family discount requires a linked active primary member');
    }
    const coupon = !input.donation && member.discount_type && member.discount_status !== 'requested' ?
      cfg.discounts.find(item => item.id === member.discount_type)?.coupon_id : '';
    const form: Record<string, string> = portal ? { customer: member.stripe_customer_id!, return_url: this.env.SITE_URL } : {
      mode: input.donation ? 'payment' : 'subscription', customer: member.stripe_customer_id!,
      success_url: this.env.SITE_URL, cancel_url: this.env.SITE_URL, client_reference_id: String(member.id),
      'metadata[conway_member_id]': String(member.id), 'line_items[0][price]': price, 'line_items[0][quantity]': '1',
      ...(input.donation ? { submit_type: 'donate', 'payment_intent_data[setup_future_usage]': 'on_session',
        'payment_intent_data[metadata][conway_member_id]': String(member.id) } : { 'subscription_data[metadata][conway_member_id]': String(member.id) }),
      ...(coupon ? { 'discounts[0][coupon]': coupon } : {}),
    };
    const fingerprint = await hash(JSON.stringify(form));
    const slot = `checkout:${member.id}:${input.donation ? 'donation' : 'membership'}`;
    const prior = await this.state.storage.get<Mutation>(slot);
    if (prior?.result) {
      if (portal) {
        if (prior.created < now() - 300 || prior.path !== '/billing_portal/sessions') await this.state.storage.delete(slot);
      } else if (prior.path === '/checkout/sessions') {
        const session = await stripe(this.env, `/checkout/sessions/${encodeURIComponent(prior.result.id)}`);
        if (session.status === 'open' && Number(session.expires_at) > now()) {
          if (await hash(JSON.stringify(prior.form)) === fingerprint) {
            if (typeof session.url !== 'string' || !session.url.startsWith('https://')) throw new DeliveryError('Invalid Stripe checkout URL');
            if (requestSlot) await this.state.storage.put(requestSlot, { fingerprint: requestFingerprint, url: session.url, epoch });
            return json({ url: session.url });
          }
          // Explicitly expire the old selection before permitting a second subscription checkout.
          await this.mutate(`${slot}:expire:${session.id}`, `/checkout/sessions/${encodeURIComponent(session.id)}/expire`, {});
          await this.state.storage.delete(slot);
        } else if (session.status === 'complete' || session.status === 'expired') {
          if (requestSlot && await this.state.storage.get(requestSlot)) throw new HttpError(409, 'This checkout request is already complete or expired; start a new request');
          if (!input.donation && session.status === 'complete' && objectID(session.subscription) !== member.stripe_subscription_id) {
            throw new HttpError(409, 'Your previous membership checkout is still being reconciled');
          }
          await this.state.storage.delete(slot);
        } else throw new HttpError(409, 'Previous checkout is still being resolved');
      } else await this.state.storage.delete(slot);
    }
    const session = await this.mutate(slot, portal ? '/billing_portal/sessions' : '/checkout/sessions', form);
    if (typeof session.url !== 'string' || !session.url.startsWith('https://')) throw new DeliveryError('Invalid Stripe checkout URL');
    if (requestSlot) await this.state.storage.put(requestSlot, { fingerprint: requestFingerprint, url: session.url, epoch });
    return json({ url: session.url });
  }

  private async handle(request: Request): Promise<Response> {
    try {
      if (request.method !== 'POST') return json({ error: 'Method not allowed' }, 405);
      const input = await body<ObjectData>(request);
      const path = new URL(request.url).pathname;
      if (path === '/checkout') return await this.checkout(input);
      if (path === '/member-mutation' || path === '/discount-mutation') return await this.memberMutation(input, path === '/discount-mutation');
      if (this.env.AUTOMATION_ENABLED !== 'true') return json({ disabled: true });
      if (path === '/job') {
        await executeJob(this.env, input.id, this.state);
      } else if (path === '/edge') {
        // Reserve before network I/O. Failed pushes consume versions; retries always reread the authorized set.
        const version = Math.max((await this.state.storage.get<number>('edge-version') || 0) + 1, Date.now());
        if (!Number.isSafeInteger(version)) throw new Error('Edge version exhausted');
        await this.state.storage.put('edge-version', version);
        const fobs = await this.env.DB.prepare("SELECT DISTINCT fob_id FROM members WHERE access_status = 'Ready' AND fob_id > 0 ORDER BY fob_id").all<{ fob_id: number }>();
        try {
          await edgeRequest(this.env, '/api/goal/versioned', { version, fobs: fobs.results.map(row => row.fob_id) });
          await this.state.storage.put('edge-last-success', now());
        } catch (error) {
          await this.env.DB.prepare('INSERT INTO member_events (created, event, details) VALUES (?, ?, ?)')
            .bind(now(), 'EdgeSyncFailed', JSON.stringify({ version, error: error instanceof Error ? error.message : 'Push failed' })).run();
          throw error;
        }
        await ingestSwipes(this.env);
      } else if (path === '/reconcile') {
        const cursor = await this.state.storage.get<number>('stripe-cursor') || 0;
        const members = await this.env.DB.prepare(`SELECT * FROM members WHERE stripe_customer_id IS NOT NULL AND stripe_customer_id != '' AND id > ? ORDER BY id LIMIT 5`)
          .bind(cursor).all<Member>();
        for (const member of members.results) {
          try { await reconcileStripe(this.env, member); } catch (error) {
            await this.env.DB.prepare('INSERT INTO member_events (created, member, event, details) VALUES (?, ?, ?, ?)')
              .bind(now(), member.id, 'StripeReconcileFailed', error instanceof Error ? error.message : 'Reconciliation failed').run();
          }
          await this.state.storage.put('stripe-cursor', member.id);
        }
        if (members.results.length < 5) await this.state.storage.put('stripe-cursor', 0);
      } else return json({ error: 'Not found' }, 404);
      return json({ ok: true });
    } catch (error) {
      if (error instanceof HttpError) return json({ error: error.message }, error.status);
      if (error instanceof Error && /constraint failed|Family root|Cannot remove last linked leader/i.test(error.message)) {
        return json({ error: 'Conflicting member identity, fob, family relationship, or last leadership account. Review the record before retrying.' }, 409);
      }
      return json({ error: error instanceof DeliveryError ? error.message : 'Integration operation failed' }, 502);
    }
  }
}
