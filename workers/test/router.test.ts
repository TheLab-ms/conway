import { SELF } from 'cloudflare:test';
import { env } from 'cloudflare:workers';
import { describe, expect, it } from 'vitest';
import { hash, now } from '../src/http';
import type { Member, Settings } from '../src/types';
import { adminMutation, api, login, member } from './fixtures';

async function leader() {
    const row = await member({ leadership: 1, discord_user_id: '123456789012345678' });
    return { row, headers: await login(row.id) };
}

describe('default fetch router boundaries', () => {
    it('serves public config and assets with security headers, not private settings', async () => {
        const response = await api('/api/config');
        expect(response.status).toBe(200);
        expect(await response.json()).toMatchObject({
            site_name: 'Conway',
            waiver: null,
            stripe_enabled: false,
        });
        for (const name of ['content-security-policy', 'x-content-type-options', 'referrer-policy', 'permissions-policy', 'strict-transport-security']) {
            expect(response.headers.get(name)).toBeTruthy();
        }
        const config = await (await api('/api/config')).json<Record<string, unknown>>();
        expect(config).not.toHaveProperty('notification_templates');
        expect(config).not.toHaveProperty('referral_sources');
        expect(config).not.toHaveProperty('donations');
        expect(config.discounts).toEqual([
            { id: 'student', label: 'Student' },
            { id: 'family', label: 'Family' },
        ]);
        const asset = await api('/');
        expect(asset.status).toBe(200);
        expect(asset.headers.get('content-type')).toContain('text/html');
        expect(await asset.text()).toContain('<html');
    });

    it.each(['/api/member', '/api/admin/members', '/api/admin/config', '/api/admin/export'])('requires login for %s', async (path) => {
        expect((await api(path)).status).toBe(401);
        expect((await api(path, 'GET', await login())).status).toBe(401);
    });

    it.each(['/api/admin/members', '/api/admin/config', '/api/admin/events', '/api/admin/swipes', '/api/admin/jobs', '/api/admin/export'])(
        'denies non-leaders access to %s',
        async (path) => {
            expect((await api(path, 'GET', await login((await member()).id))).status).toBe(403);
        },
    );

    it.each([
        ['/api/profile', 'PATCH'],
        ['/api/logout', 'POST'],
        ['/api/waiver', 'POST'],
        ['/api/discount', 'DELETE'],
        ['/api/fobs/bind', 'POST'],
        ['/api/admin/members', 'POST'],
        ['/api/admin/config', 'PUT'],
        ['/api/kiosk/claims', 'POST'],
        ['/api/billing/checkout', 'POST'],
    ])('enforces CSRF before dispatching %s %s', async (path, method) => {
        const { headers } = await leader();
        for (const change of [{ Origin: '' }, { Origin: 'https://evil.test' }, { 'X-CSRF-Token': '' }, { Cookie: '' }]) {
            const response = await api(path, method, { ...headers, ...change }, {});
            expect(response.status).toBe(403);
            expect(await response.json()).toHaveProperty('error');
        }
    });

    it('does not send API misses to the SPA or bypass auth through unsupported methods', async () => {
        expect((await api('/api/not-real')).status).toBe(404);
        expect((await api('/login/not-real')).status).toBe(404);
        expect((await api('/', 'POST')).status).toBe(405);
        const headers = await login((await member()).id);
        expect((await api('/api/profile', 'GET', headers)).status).toBe(405);
        expect((await api('/api/profile', 'OPTIONS')).status).toBe(401);
        expect((await api('/webhooks/stripe', 'POST', {}, {})).status).not.toBe(403);
    });

    it('applies JSON content-type and size limits through the router without changing member data', async () => {
        const row = await member({ name: 'Original' });
        const headers = await login(row.id);
        const input = new Request(`${env.SITE_URL}/api/profile`, {
            method: 'PATCH',
            headers,
            body: '{"name":"No"}',
        });
        expect((await SELF.fetch(input)).status).toBe(415);
        expect((await api('/api/profile', 'PATCH', headers, { name: 'x'.repeat(128 * 1024) })).status).toBe(413);
        expect(await env.DB.prepare('SELECT name FROM members WHERE id=?').bind(row.id).first('name')).toBe('Original');
    });
});

describe('member profile', () => {
    it('edits only the logged-in profile and hides admin notes from member/session responses', async () => {
        const row = await member({ name: 'Before', admin_notes: 'Leader-only' });
        const other = await member({ name: 'Other' });
        const headers = await login(row.id);
        const response = await api('/api/profile', 'PATCH', headers, {
            name: ' After ',
            discord_checkin_notify: false,
        });
        expect(response.status).toBe(200);
        expect(await response.json()).toMatchObject({
            id: row.id,
            name: 'After',
            discord_checkin_notify: 0,
        });
        expect(await (await api('/api/member', 'GET', headers)).json()).not.toHaveProperty('admin_notes');
        expect((await (await api('/api/session', 'GET', headers)).json<{ member: Member }>()).member).not.toHaveProperty('admin_notes');
        expect(await env.DB.prepare('SELECT name FROM members WHERE id=?').bind(other.id).first('name')).toBe('Other');
    });

    it.each([
        { leadership: 1 },
        { non_billable: 1 },
        { email: 'changed@example.test' },
        { discord_user_id: '999999999999999999' },
        { admin_notes: 'overwrite' },
        { fob_id: 1 },
        { id: 9 },
        {},
        { discord_checkin_notify: 'false' },
    ])('rejects unauthorized/invalid profile fields %j atomically', async (data) => {
        const row = await member({ name: 'Original' });
        expect((await api('/api/profile', 'PATCH', await login(row.id), data)).status).toBe(400);
        expect(await env.DB.prepare('SELECT name FROM members WHERE id=?').bind(row.id).first('name')).toBe('Original');
    });

    it('does not expose the removed directory API to anonymous sessions, members, or leaders', async () => {
        const { headers: admin } = await leader();
        for (const headers of [{}, await login(), await login((await member()).id), admin]) {
            for (const method of ['GET', 'HEAD', 'OPTIONS']) {
                expect((await api('/api/directory', method, headers)).status).toBe(404);
            }
        }
    });

    it.each([{ pronouns: 'they/them' }, { bio: 'Introduction' }, { directory_hidden: true }])('rejects removed directory fields %j for members and leaders', async (fields) => {
        const row = await member({ admin_notes: 'Original' });
        const { headers: admin } = await leader();
        expect((await api('/api/profile', 'PATCH', await login(row.id), fields)).status).toBe(400);
        expect((await adminMutation(row.id, 'PATCH', admin, { admin_notes: 'Changed', ...fields })).status).toBe(400);
        expect(await env.DB.prepare('SELECT admin_notes FROM members WHERE id=?').bind(row.id).first('admin_notes')).toBe('Original');
        const count = await env.DB.prepare('SELECT count(*) FROM members').first('count(*)');
        expect((await api('/api/admin/members', 'POST', admin, { email: 'new@example.test', ...fields })).status).toBe(400);
        expect(await env.DB.prepare('SELECT count(*) FROM members').first('count(*)')).toBe(count);
    });
});

describe('admin configuration and CRUD', () => {
    it('preserves case-sensitive legacy discount IDs when saving migrated settings', async () => {
        const { headers } = await leader();
        const discounts = [{ id: 'firstResponder', label: 'First Responder', coupon_id: 'first_responder' }];
        expect((await api('/api/admin/config', 'PUT', headers, { version: 1, discounts })).status).toBe(200);
        const current = await (await api('/api/admin/config', 'GET', headers)).json<Settings>();
        expect(current.discounts).toEqual(discounts);
    });

    it('uses optimistic settings versions and rejects stale/concurrent updates', async () => {
        const { headers } = await leader();
        const initial = await (await api('/api/admin/config', 'GET', headers)).json<Settings>();
        const responses = await Promise.all(['First', 'Second'].map((site_name) => api('/api/admin/config', 'PUT', headers, { version: initial.version, site_name })));
        expect(responses.map((r) => r.status).sort()).toEqual([200, 409]);
        const current = await (await api('/api/admin/config', 'GET', headers)).json<Settings>();
        expect(current.version).toBe(initial.version + 1);
        expect(['First', 'Second']).toContain(current.site_name);
        expect(await env.DB.prepare("SELECT count(*) n FROM member_events WHERE event='SettingsUpdated'").first('n')).toBe(1);
    });

    it.each([
        { unknown: true },
        { STRIPE_SECRET_KEY: 'secret' },
        {
            discounts: [
                { id: 'same', label: 'One' },
                { id: 'same', label: 'Two' },
            ],
        },
        { notification_templates: { arbitrary: 'No' } },
        { monthly_price_id: 'not-a-price' },
    ])('rejects invalid settings %j without incrementing version', async (fields) => {
        const { headers } = await leader();
        expect((await api('/api/admin/config', 'PUT', headers, { version: 1, ...fields })).status).toBe(400);
        expect(await env.DB.prepare('SELECT version FROM settings').first('version')).toBe(1);
    });

    it('creates, searches, updates, exports, and deletes members with actor audit records', async () => {
        const { row: actor, headers } = await leader();
        const created = await api('/api/admin/members', 'POST', headers, {
            email: 'NEW@EXAMPLE.TEST',
            name: '=Formula, "quoted"',
            admin_notes: 'Private',
            non_billable: true,
        });
        expect(created.status).toBe(201);
        const { id } = await created.json<{ id: number }>();
        const read = await (await api(`/api/admin/members/${id}`, 'GET', headers)).json<Member>();
        expect(read).toMatchObject({
            email: 'new@example.test',
            admin_notes: 'Private',
            non_billable: 1,
        });
        expect((await adminMutation(id, 'PATCH', headers, { fob_id: 7, confirmed: true })).status).toBe(200);
        const list = await (await api('/api/admin/members?search=NEW%40&status=Ready', 'GET', headers)).json<{ members: Member[]; total: number }>();
        expect(list.total).toBe(1);
        expect(list.members[0].id).toBe(id);
        expect((await api('/api/admin/members?offset=-1', 'GET', headers)).status).toBe(400);
        const csv = await (await api('/api/admin/export', 'GET', headers)).text();
        expect(csv).toContain('"\'=Formula, ""quoted"""');
        expect(csv).not.toContain('Private');
        expect((await adminMutation(id, 'DELETE', headers)).status).toBe(200);
        expect((await api(`/api/admin/members/${id}`, 'GET', headers)).status).toBe(404);
        const events = await env.DB.prepare("SELECT actor,event FROM member_events WHERE event IN ('AdminMemberCreated','AdminMemberUpdated','MemberDeleted') ORDER BY id").all();
        expect(events.results).toEqual(
            ['AdminMemberCreated', 'AdminMemberUpdated', 'MemberDeleted'].map((event) => ({
                actor: actor.id,
                event,
            })),
        );
    });

    it('maps duplicate Discord/email/fob identities to 409 and leaves existing members unchanged', async () => {
        const { headers } = await leader();
        const original = await member({
            email: 'duplicate@example.test',
            discord_user_id: '987654321098765432',
            fob_id: 42,
        });
        const target = await member();
        for (const fields of [{ email: 'DUPLICATE@example.test' }, { discord_user_id: original.discord_user_id }, { fob_id: 42 }]) {
            expect((await adminMutation(target.id, 'PATCH', headers, fields)).status).toBe(409);
        }
        expect((await api('/api/admin/members', 'POST', headers, { email: 'duplicate@example.test' })).status).toBe(409);
        expect(await env.DB.prepare('SELECT fob_id FROM members WHERE id=?').bind(target.id).first('fob_id')).toBeNull();
    });

    it('protects family roots and billed members and rolls back failed deletion audit', async () => {
        const { headers } = await leader();
        const root = await member();
        const child = await member({ root_family_member: root.id });
        const billed = await member({ stripe_subscription_state: 'active' });
        for (const id of [root.id, billed.id]) expect((await adminMutation(id, 'DELETE', headers)).status).toBe(409);
        expect(await env.DB.prepare("SELECT count(*) n FROM member_events WHERE event='MemberDeleted'").first('n')).toBe(0);
        expect((await adminMutation(child.id, 'DELETE', headers)).status).toBe(200);
        expect((await adminMutation(root.id, 'DELETE', headers)).status).toBe(200);
    });

    it('protects the last linked leader against delete, demotion, and Discord unlink', async () => {
        const { row, headers } = await leader();
        for (const [method, input] of [
            ['DELETE', undefined],
            ['PATCH', { leadership: false }],
            ['PATCH', { discord_user_id: null }],
        ] as const) {
            expect((await adminMutation(row.id, method, headers, input)).status).toBe(409);
        }
        await member({ leadership: 1, discord_user_id: '987654321098765432' });
        expect((await adminMutation(row.id, 'PATCH', headers, { leadership: false })).status).toBe(200);
        expect((await api('/api/admin/members', 'GET', headers)).status).toBe(403);
    });

    it('keeps one linked leader when two leaders concurrently demote themselves', async () => {
        const first = await leader();
        const second = await member({ leadership: 1, discord_user_id: '987654321098765432' });
        const secondHeaders = await login(second.id);
        const responses = await Promise.all([
            api(`/api/admin/members/${first.row.id}`, 'PATCH', first.headers, {
                leadership: false,
                version: first.row.version,
            }),
            api(`/api/admin/members/${second.id}`, 'PATCH', secondHeaders, {
                leadership: false,
                version: second.version,
            }),
        ]);
        expect(responses.map((response) => response.status).sort()).toEqual([200, 409]);
        expect(await env.DB.prepare('SELECT count(*) n FROM members WHERE leadership=1 AND discord_user_id IS NOT NULL').first('n')).toBe(1);
    });

    it('only retries dead jobs and atomically clears their retry state', async () => {
        const { row, headers } = await leader();
        const job = await env.DB.prepare(
            "INSERT INTO jobs(kind,payload,status,attempts,lease_until,last_error,completed) VALUES('discord_sync','{}','dead',10,123,'Failure',456) RETURNING id",
        ).first<{ id: number }>();
        const response = await api(`/api/admin/jobs/${job!.id}/retry`, 'POST', headers);
        expect(response.status).toBe(200);
        expect(await env.DB.prepare('SELECT status,attempts,lease_until,last_error,completed FROM jobs WHERE id=?').bind(job!.id).first()).toEqual({
            status: 'pending',
            attempts: 0,
            lease_until: 0,
            last_error: '',
            completed: null,
        });
        expect((await api(`/api/admin/jobs/${job!.id}/retry`, 'POST', headers)).status).toBe(409);
        expect((await api('/api/admin/jobs/999999/retry', 'POST', headers)).status).toBe(409);
        expect(await env.DB.prepare("SELECT actor FROM member_events WHERE event='JobRetryRequested'").first('actor')).toBe(row.id);
    });
});

describe('waiver versions and discounts', () => {
    it('serializes concurrent waiver publication and advances settings to the latest immutable version', async () => {
        const { headers } = await leader();
        const responses = await Promise.all(['First', 'Second'].map((content) => api('/api/admin/waiver', 'PUT', headers, { content, agreements: ['Agree'] })));
        expect(responses.map((response) => response.status)).toEqual([201, 201]);
        const versions = await Promise.all(responses.map((response) => response.json<{ version: number }>()));
        expect(versions.map((row) => row.version).sort()).toEqual([1, 2]);
        expect(await env.DB.prepare('SELECT version FROM settings').first('version')).toBe(3);
        const cfg = await (await api('/api/config')).json<{ waiver: { version: number; content: string } }>();
        expect(cfg.waiver.version).toBe(2);
        expect(['First', 'Second']).toContain(cfg.waiver.content);
    });

    it('publishes immutable versions, requires all agreements, deduplicates signing, and rejects stale versions', async () => {
        const { headers: admin } = await leader();
        const row = await member({ confirmed: 1 });
        const headers = await login(row.id);
        expect(
            (
                await api('/api/admin/waiver', 'PUT', admin, {
                    content: 'Read this',
                    agreements: ['One', 'Two'],
                })
            ).status,
        ).toBe(201);
        expect(
            (
                await api('/api/waiver', 'POST', headers, {
                    version: 1,
                    name: 'Signer',
                    agreements: [true, false],
                })
            ).status,
        ).toBe(400);
        for (let i = 0; i < 2; i++)
            expect(
                (
                    await api('/api/waiver', 'POST', headers, {
                        version: 1,
                        name: 'Signer',
                        agreements: [true, true],
                    })
                ).status,
            ).toBe(200);
        expect(await env.DB.prepare('SELECT count(*) n FROM waivers').first('n')).toBe(1);
        expect(await env.DB.prepare('SELECT waiver FROM members WHERE id=?').bind(row.id).first('waiver')).not.toBeNull();
        expect((await api('/api/admin/waiver', 'PUT', admin, { content: 'Updated', agreements: ['New'] })).status).toBe(201);
        expect(
            (
                await api('/api/waiver', 'POST', headers, {
                    version: 1,
                    name: 'Signer',
                    agreements: [true, true],
                })
            ).status,
        ).toBe(409);
        expect((await (await api('/api/config')).json<{ waiver: { version: number } }>()).waiver.version).toBe(2);
    });

    it('rechecks waiver version if publication changes during streaming request body', async () => {
        const { headers: admin } = await leader();
        const headers = await login((await member()).id);
        await api('/api/admin/waiver', 'PUT', admin, { content: 'Before', agreements: ['Agree'] });
        const stream = new TransformStream<Uint8Array, Uint8Array>();
        const writer = stream.writable.getWriter();
        const pending = SELF.fetch(`${env.SITE_URL}/api/waiver`, {
            method: 'POST',
            headers: { ...headers, 'Content-Type': 'application/json' },
            body: stream.readable,
        });
        await writer.write(new TextEncoder().encode('{"version":1,'));
        await api('/api/admin/waiver', 'PUT', admin, { content: 'After', agreements: ['Agree again'] });
        await writer.write(new TextEncoder().encode('"name":"Signer","agreements":[true]}'));
        await writer.close();
        expect((await pending).status).toBe(409);
        expect(await env.DB.prepare('SELECT count(*) n FROM waivers').first('n')).toBe(0);
    });

    it('deduplicates pending discount requests and rejects stale approval buttons', async () => {
        const { headers: admin } = await leader();
        const row = await member();
        const headers = await login(row.id);
        expect((await api('/api/discount', 'POST', headers, { discount_type: 'student' })).status).toBe(200);
        const old = await env.DB.prepare('SELECT discount_request_id FROM members WHERE id=?').bind(row.id).first<string>('discount_request_id');
        expect((await api('/api/discount', 'POST', headers, { discount_type: 'student' })).status).toBe(409);
        await api('/api/discount', 'DELETE', headers);
        await api('/api/discount', 'POST', headers, { discount_type: 'student' });
        expect((await api(`/api/admin/members/${row.id}/approve`, 'POST', admin, { request_id: old })).status).toBe(409);
        const current = await env.DB.prepare('SELECT discount_request_id FROM members WHERE id=?').bind(row.id).first('discount_request_id');
        expect((await api(`/api/admin/members/${row.id}/approve`, 'POST', admin, { request_id: current })).status).toBe(200);
        expect((await api(`/api/admin/members/${row.id}/approve`, 'POST', admin, { request_id: current })).status).toBe(409);
    });

    it('requires a primary family root for approval and propagates its payment state', async () => {
        const { headers: admin } = await leader();
        const root = await member({ confirmed: 1, non_billable: 1 });
        const dependent = await member({ root_family_member: root.id });
        const row = await member();
        await api('/api/discount', 'POST', await login(row.id), { discount_type: 'family' });
        const request_id = await env.DB.prepare('SELECT discount_request_id FROM members WHERE id=?').bind(row.id).first('discount_request_id');
        const path = `/api/admin/members/${row.id}/approve`;
        expect((await api(path, 'POST', admin, { request_id })).status).toBe(400);
        expect((await api(path, 'POST', admin, { request_id, root_family_member: dependent.id })).status).toBe(409);
        expect((await api(path, 'POST', admin, { request_id, root_family_member: root.id })).status).toBe(200);
        expect(await env.DB.prepare('SELECT discount_status,root_family_member,root_family_member_active FROM members WHERE id=?').bind(row.id).first()).toEqual({
            discount_status: 'approved',
            root_family_member: root.id,
            root_family_member_active: 1,
        });
    });
});

describe('kiosk enrollment and D1 changes() atomicity', () => {
    async function claim(fob = 42) {
        const headers = { ...(await login()), 'CF-Connecting-IP': env.KIOSK_IPS };
        const response = await api('/api/kiosk/claims', 'POST', headers, { fob_id: fob });
        expect(response.status).toBe(201);
        return { ...(await response.json<{ token: string; expires: number; url: string }>()), headers };
    }

    it('requires exact trusted IP plus anonymous-session CSRF; forwarded headers cannot substitute', async () => {
        const headers = await login();
        const changes: Record<string, string>[] = [{}, { 'CF-Connecting-IP': '192.0.2.100' }, { 'X-Forwarded-For': env.KIOSK_IPS }];
        for (const change of changes) {
            expect((await api('/api/kiosk/claims', 'POST', { ...headers, ...change }, { fob_id: 42 })).status).toBe(403);
        }
        const created = await claim();
        expect(created.expires).toBeGreaterThan(now());
        expect(created.url).toBe(`${env.SITE_URL}/fobs/bind?token=${created.token}`);
        expect(await env.DB.prepare('SELECT token_hash FROM enrollment_claims').first('token_hash')).toBe(await hash(created.token));
        expect((await api(`/api/kiosk/claims/${created.token}`)).status).toBe(403);
    });

    it('consumes a claim once, prevents same/different-member replay, and updates poll state', async () => {
        const created = await claim();
        const first = await member();
        const second = await member({ fob_id: 99 });
        const headers = await login(first.id);
        expect((await api('/api/fobs/bind', 'POST', headers, { token: created.token })).status).toBe(200);
        expect((await api('/api/fobs/bind', 'POST', headers, { token: created.token })).status).toBe(409);
        expect((await api('/api/fobs/bind', 'POST', await login(second.id), { token: created.token })).status).toBe(409);
        expect(await env.DB.prepare('SELECT fob_id FROM members WHERE id=?').bind(second.id).first('fob_id')).toBe(99);
        expect(await (await api(`/api/kiosk/claims/${created.token}`, 'GET', created.headers)).json()).toMatchObject({ claimed: true });
    });

    it('rolls claim consumption back on unique-fob conflict so it can be retried after release', async () => {
        const owner = await member({ fob_id: 42 });
        const target = await member({ fob_id: 99 });
        const headers = await login(target.id);
        const created = await claim();
        expect((await api('/api/fobs/bind', 'POST', headers, { token: created.token })).status).toBe(409);
        expect(await env.DB.prepare('SELECT claimed_by FROM enrollment_claims').first('claimed_by')).toBeNull();
        expect(await env.DB.prepare('SELECT fob_id FROM members WHERE id=?').bind(target.id).first('fob_id')).toBe(99);
        await env.DB.prepare('UPDATE members SET fob_id=NULL WHERE id=?').bind(owner.id).run();
        expect((await api('/api/fobs/bind', 'POST', headers, { token: created.token })).status).toBe(200);
    });

    it('allows only one winner under concurrent redemption', async () => {
        const created = await claim();
        const headers = await Promise.all([(await member()).id, (await member()).id].map((id) => login(id)));
        const responses = await Promise.all(headers.map((auth) => api('/api/fobs/bind', 'POST', auth, { token: created.token })));
        expect(responses.map((response) => response.status).sort()).toEqual([200, 409]);
        expect(await env.DB.prepare('SELECT count(*) n FROM members WHERE fob_id=42').first('n')).toBe(1);
    });

    it('rejects malformed, expired, and nonexistent tokens without changing an existing fob', async () => {
        const created = await claim();
        await env.DB.prepare('UPDATE enrollment_claims SET expires=?')
            .bind(now() - 1)
            .run();
        const row = await member({ fob_id: 99 });
        const headers = await login(row.id);
        expect((await api('/api/fobs/bind', 'POST', headers, { token: 'bad' })).status).toBe(400);
        for (const token of [created.token, 'a'.repeat(64)]) expect((await api('/api/fobs/bind', 'POST', headers, { token })).status).toBe(409);
        expect(await env.DB.prepare('SELECT fob_id FROM members WHERE id=?').bind(row.id).first('fob_id')).toBe(99);
    });

    it('does not resurrect a consumed claim when its original member is deleted', async () => {
        const created = await claim();
        const original = await member();
        expect((await api('/api/fobs/bind', 'POST', await login(original.id), { token: created.token })).status).toBe(200);
        const { headers: admin } = await leader();
        expect((await adminMutation(original.id, 'DELETE', admin)).status).toBe(200);
        const replacement = await member();
        expect((await api('/api/fobs/bind', 'POST', await login(replacement.id), { token: created.token })).status).toBe(409);
        expect(await env.DB.prepare('SELECT fob_id FROM members WHERE id=?').bind(replacement.id).first('fob_id')).toBeNull();
    });
});
