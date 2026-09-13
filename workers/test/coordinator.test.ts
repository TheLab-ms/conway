import { env } from 'cloudflare:workers';
import { runInDurableObject } from 'cloudflare:test';
import { describe, expect, it, vi } from 'vitest';
import { MembershipCoordinator, mutateDiscount, mutateMember } from '../src/integrations';
import type { Member } from '../src/types';
import { api, login, member } from './fixtures';

const read = (id: number) => env.DB.prepare('SELECT * FROM members WHERE id=?').bind(id).first<Member>();
const audits = (id: number) =>
    env.DB.prepare("SELECT actor,event,details FROM member_events WHERE member=? AND event IN ('AdminMemberUpdated','DiscountChanged') ORDER BY id")
        .bind(id)
        .all<{ actor: number; event: string; details: string }>();

describe('versioned mutations through the bound MembershipCoordinator', () => {
    it('only admins can change the stored billing preference and checkout rejects member selections', async () => {
        const admin = await member({ leadership: 1 });
        const target = await member();
        const headers = await login(target.id);
        const adminHeaders = await login(admin.id);
        for (const bill_annually of [1, 0]) {
            const current = (await read(target.id))!;
            expect((await api('/api/profile', 'PATCH', headers, { bill_annually })).status).toBe(400);
            expect((await api(`/api/admin/members/${target.id}`, 'PATCH', headers, { bill_annually, version: current.version })).status).toBe(403);
            expect((await mutateMember(env, target.id, { bill_annually }, target.id, current.version)).status).toBe(403);
            for (const input of [{ annual: Boolean(bill_annually) }, { bill_annually }]) {
                expect((await api('/api/billing/checkout', 'POST', headers, input)).status).toBe(400);
            }
            expect(await read(target.id)).toEqual(current);
            const response = await api(`/api/admin/members/${target.id}`, 'PATCH', adminHeaders, { bill_annually, version: current.version });
            expect(response.status).toBe(200);
            expect(await response.json()).toEqual(await read(target.id));
            expect((await read(target.id))?.bill_annually).toBe(bill_annually);
        }
        expect((await audits(target.id)).results).toHaveLength(2);
    });

    it.each(['PATCH', 'DELETE'])('requires a positive integer version JSON for admin %s', async (method) => {
        const admin = await member({ leadership: 1 });
        const target = await member();
        const headers = await login(admin.id);
        const path = `/api/admin/members/${target.id}`;
        for (const version of [undefined, null, 0, -1, 1.5, '1']) {
            expect((await api(path, method, headers, { name: 'Changed', version })).status).toBe(400);
        }
        expect((await api(path, method, headers)).status).toBe(415);
        expect((await read(target.id))?.version).toBe(target.version);
        expect((await audits(target.id)).results).toEqual([]);
    });

    it('returns the post-trigger D1 version and records one actor audit per successful CAS', async () => {
        const admin = await member({ leadership: 1 });
        const target = await member({ name: 'Before' });
        const headers = await login(admin.id);
        const response = await api(`/api/admin/members/${target.id}`, 'PATCH', headers, {
            name: 'After',
            version: target.version,
        });
        expect(response.status).toBe(200);
        const updated = await response.json<Member>();
        expect(updated.version).toBeGreaterThan(target.version);
        expect(updated).toEqual(await read(target.id));
        const rows = (await audits(target.id)).results;
        expect(rows).toHaveLength(1);
        expect(rows[0]).toMatchObject({ actor: admin.id, event: 'AdminMemberUpdated' });
        expect(JSON.parse(rows[0].details)).toMatchObject({
            member_id: target.id,
            actor_id: admin.id,
            expected_version: target.version,
            fields: ['name'],
        });
        expect(
            (
                await api(`/api/admin/members/${target.id}`, 'PATCH', headers, {
                    name: 'Lost update',
                    version: target.version,
                })
            ).status,
        ).toBe(409);
        expect((await read(target.id))?.name).toBe('After');
        expect((await audits(target.id)).results).toHaveLength(1);
    });

    it('serializes competing CAS writes and permits only the matching version to audit', async () => {
        const admin = await member({ leadership: 1 });
        const target = await member();
        const headers = await login(admin.id);
        const responses = await Promise.all(['First', 'Second'].map((name) => api(`/api/admin/members/${target.id}`, 'PATCH', headers, { name, version: target.version })));
        expect(responses.map((response) => response.status).sort()).toEqual([200, 409]);
        const winner = await responses.find((response) => response.status === 200)!.json<Member>();
        expect(winner).toEqual(await read(target.id));
        expect((await audits(target.id)).results).toHaveLength(1);
    });

    it('rejects stale deletes without audit and permits deletion at the current version', async () => {
        const admin = await member({ leadership: 1 });
        const target = await member();
        const headers = await login(admin.id);
        await env.DB.prepare("UPDATE members SET name_override='Concurrent profile update' WHERE id=?").bind(target.id).run();
        expect((await api(`/api/admin/members/${target.id}`, 'DELETE', headers, { version: target.version })).status).toBe(409);
        expect(await env.DB.prepare("SELECT count(*) n FROM member_events WHERE event='MemberDeleted'").first('n')).toBe(0);
        const current = (await read(target.id))!;
        expect(
            (
                await api(`/api/admin/members/${target.id}`, 'DELETE', headers, {
                    version: current.version,
                })
            ).status,
        ).toBe(200);
        expect(await read(target.id)).toBeNull();
        const event = await env.DB.prepare("SELECT actor,member,details FROM member_events WHERE event='MemberDeleted'").first<{ actor: number; member: null; details: string }>();
        expect(event).toMatchObject({ actor: admin.id, member: null });
        expect(JSON.parse(event!.details)).toMatchObject({
            member_id: target.id,
            expected_version: current.version,
        });
    });

    it('rejects admin snapshots made stale by billing writes and family payment triggers', async () => {
        const admin = await member({ leadership: 1 });
        const root = await member({ confirmed: 1, non_billable: 1 });
        const child = await member({
            confirmed: 1,
            non_billable: 1,
            fob_id: 9,
            root_family_member: root.id,
        });
        await env.DB.prepare('UPDATE members SET non_billable=0 WHERE id=?').bind(root.id).run();
        expect((await read(root.id))!.version).toBeGreaterThan(root.version);
        expect(await read(child.id)).toMatchObject({
            root_family_member_active: 0,
            access_status: 'FamilyInactive',
        });
        expect((await read(child.id))!.version).toBeGreaterThan(child.version);
        for (const stale of [root, child]) {
            const response = await mutateMember(env, stale.id, { name: 'Stale overwrite' }, admin.id, stale.version);
            expect(response.status).toBe(409);
            expect((await audits(stale.id)).results).toEqual([]);
        }
    });

    it('returns trigger-resolved family fields and version rather than UPDATE RETURNING snapshots', async () => {
        const admin = await member({ leadership: 1 });
        const root = await member({ confirmed: 1, non_billable: 1 });
        const child = await member({ confirmed: 1, non_billable: 1, fob_id: 7 });
        const response = await mutateMember(env, child.id, { root_family_member: root.id }, admin.id, child.version);
        expect(response.status).toBe(200);
        const linked = await response.json<Member>();
        expect(linked).toMatchObject({
            root_family_member: root.id,
            root_family_member_active: 1,
            access_status: 'Ready',
        });
        expect(linked).toEqual(await read(child.id));
        expect(linked.version).toBeGreaterThan(child.version);
        const unlinked = await mutateMember(env, child.id, { root_family_member: null }, admin.id, linked.version);
        expect(unlinked.status).toBe(200);
        expect(await unlinked.json()).toEqual(await read(child.id));
        expect((await read(child.id))?.root_family_member_active).toBeNull();
    });

    it('rechecks leader authorization inside the DO and rejects forged/generated fields', async () => {
        const admin = await member({ leadership: 1 });
        const target = await member();
        await env.DB.prepare('UPDATE members SET leadership=0 WHERE id=?').bind(admin.id).run();
        expect((await mutateMember(env, target.id, { name: 'Unauthorized' }, admin.id, target.version)).status).toBe(403);
        await env.DB.prepare('UPDATE members SET leadership=1 WHERE id=?').bind(admin.id).run();
        const forbidden: Record<string, string | number | null>[] = [{ version: 999 }, { access_status: 'Ready' }, { stripe_subscription_state: 'active' }];
        for (const fields of forbidden) {
            expect((await mutateMember(env, target.id, fields, admin.id, target.version)).status).toBe(400);
        }
        expect((await read(target.id))?.version).toBe(target.version);
        expect((await audits(target.id)).results).toEqual([]);
    });

    it('checks active primary roots for approved family discounts and preserves rejected state', async () => {
        const admin = await member({ leadership: 1 });
        const inactive = await member();
        const root = await member({ confirmed: 1, non_billable: 1 });
        const dependent = await member({ root_family_member: root.id });
        const target = await member();
        for (const rootID of [inactive.id, dependent.id, target.id]) {
            const response = await mutateMember(env, target.id, { discount_type: 'family', discount_status: 'approved', root_family_member: rootID }, admin.id, target.version);
            expect(response.status).toBe(409);
        }
        expect((await audits(target.id)).results).toEqual([]);
        expect((await read(target.id))?.version).toBe(target.version);
        const approved = await mutateMember(env, target.id, { discount_type: 'family', discount_status: 'approved', root_family_member: root.id }, admin.id, target.version);
        expect(approved.status).toBe(200);
        expect(await approved.json()).toEqual(await read(target.id));
        expect((await read(target.id))?.root_family_member_active).toBe(1);
    });

    it.each([false, true])('allows unrelated inactive-family admin saves without billing effects (resend billing fields: %s)', async (resendBilling) => {
        const admin = await member({ leadership: 1 });
        const root = await member();
        const target = await member({
            name: 'Family member',
            confirmed: 1,
            non_billable: 1,
            fob_id: 42,
            root_family_member: root.id,
            discount_type: 'family',
            discount_status: 'approved',
            stripe_customer_id: 'cus_family',
            stripe_subscription_id: 'sub_family',
            stripe_subscription_state: 'active',
        });
        expect(target.access_status).toBe('FamilyInactive');
        const headers = await login(admin.id);
        const jobsBefore = await env.DB.prepare('SELECT count(*) n FROM jobs').first('n');
        const fields = resendBilling
            ? {
                  discount_type: target.discount_type,
                  discount_status: target.discount_status,
                  root_family_member: target.root_family_member,
                  confirmed: target.confirmed,
                  non_billable: target.non_billable,
                  bill_annually: target.bill_annually,
              }
            : {};
        const external = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
            throw new Error('Unrelated save must not contact billing');
        });
        try {
            const response = await api(`/api/admin/members/${target.id}`, 'PATCH', headers, {
                ...fields,
                version: target.version,
                admin_notes: 'Follow up with primary member',
            });
            expect(response.status).toBe(200);
            const updated = await response.json<Member>();
            expect(updated).toEqual(await read(target.id));
            expect(updated).toMatchObject({
                admin_notes: 'Follow up with primary member',
                access_status: 'FamilyInactive',
                root_family_member: root.id,
                root_family_member_active: 0,
                discount_type: 'family',
                discount_status: 'approved',
                stripe_subscription_id: 'sub_family',
            });
            expect(updated.version).toBeGreaterThan(target.version);
            expect((await audits(target.id)).results).toHaveLength(1);
            expect((await audits(target.id)).results[0]).toMatchObject({
                actor: admin.id,
                event: 'AdminMemberUpdated',
            });
            expect(await env.DB.prepare('SELECT count(*) n FROM jobs').first('n')).toBe(jobsBefore);
            expect(external).not.toHaveBeenCalled();
        } finally {
            external.mockRestore();
        }
    });

    it('mutates self-discounts with real DO CAS, post-trigger version, actor audit, and one outbox job', async () => {
        const target = await member();
        const first = await mutateDiscount(env, target.id, 'student', target.version);
        expect(first.status).toBe(200);
        const result = await first.json<{ updated: boolean; version: number }>();
        const current = (await read(target.id))!;
        expect(result).toEqual({ updated: true, version: current.version });
        expect(current.version).toBeGreaterThan(target.version);
        expect(current).toMatchObject({ discount_type: 'student', discount_status: 'requested' });
        expect(current.discount_request_id).toMatch(/^[a-f0-9]{64}$/);
        expect((await mutateDiscount(env, target.id, null, target.version)).status).toBe(409);
        expect((await mutateDiscount(env, target.id, 'family', current.version)).status).toBe(409);
        expect(await env.DB.prepare("SELECT count(*) n FROM jobs WHERE kind='discount'").first('n')).toBe(1);
        const rows = (await audits(target.id)).results;
        expect(rows).toHaveLength(1);
        expect(rows[0]).toMatchObject({ actor: target.id, event: 'DiscountChanged' });
        const removed = await mutateDiscount(env, target.id, null, current.version);
        expect(removed.status).toBe(200);
        expect(await removed.json()).toEqual({
            updated: true,
            version: (await read(target.id))!.version,
        });
        expect(await read(target.id)).toMatchObject({
            discount_type: null,
            discount_status: null,
            discount_request_id: null,
        });
        expect((await audits(target.id)).results).toHaveLength(2);
    });

    it.each(['student', 'family'])('holds new member payment after a %s discount request without contacting Stripe', async (discount_type) => {
        const target = await member();
        const headers = await login(target.id);
        expect((await api('/api/discount', 'POST', headers, { discount_type })).status).toBe(200);
        const pending = (await read(target.id))!;
        expect(pending.discount_status).toBe('requested');
        const external = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
            throw new Error('Pending discount must not contact Stripe for a new member');
        });
        try {
            for (const requestHeaders of [headers, { ...headers, 'Idempotency-Key': 'request-pending' }]) {
                const response = await api('/api/billing/checkout', 'POST', requestHeaders, {});
                expect(response.status).toBe(409);
                expect(await response.json()).toEqual({ error: 'Your discount request is pending approval. Wait for approval before starting payment.' });
            }
            expect(await read(target.id)).toEqual(pending);
            expect(external).not.toHaveBeenCalled();
        } finally {
            external.mockRestore();
        }
    });

    it.each(['active', 'trialing'])('keeps the pending payment hold when checkout reconciles local %s to canceled in D1', async (stripe_subscription_state) => {
        const target = await member({
            confirmed: 1,
            stripe_customer_id: 'cus_pending',
            stripe_subscription_id: 'sub_pending',
            stripe_subscription_state,
            discount_type: 'student',
            discount_status: 'requested',
            discount_request_id: 'request-pending',
        });
        expect(target.payment_status).toBe('ActiveStripe');
        await env.DB.prepare("UPDATE settings SET data=json_set(data,'$.monthly_price_id','price_month') WHERE id=1").run();
        const external = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
            expect(String(input)).toContain('https://api.stripe.com/v1/subscriptions?');
            expect(init?.method).toBe('GET');
            return Response.json({
                data: [{ id: 'sub_pending', customer: 'cus_pending', status: 'canceled', created: 1 }],
                has_more: false,
            });
        });
        try {
            const stub = env.COORDINATOR.get(env.COORDINATOR.idFromName('stripe'));
            await runInDurableObject(stub, async (_instance, state) => {
                // Use real DO storage and D1 triggers, with only Stripe's secret and HTTP response supplied by the test.
                const coordinator = new MembershipCoordinator(state, { ...env, STRIPE_SECRET_KEY: 'sk_test' });
                for (let attempt = 0; attempt < 2; attempt++) {
                    const response = await coordinator.fetch(
                        new Request('https://coordinator.internal/checkout', {
                            method: 'POST',
                            headers: { 'Content-Type': 'application/json' },
                            body: JSON.stringify({ member_id: target.id, request_key: 'request-pending' }),
                        }),
                    );
                    expect(response.status).toBe(409);
                    expect(await response.json()).toEqual({ error: 'Your discount request is pending approval. Wait for approval before starting payment.' });
                }
                expect(await state.storage.get(`checkout:${target.id}:membership`)).toBeUndefined();
            });
            expect(await read(target.id)).toMatchObject({
                stripe_subscription_state: 'canceled',
                payment_status: null,
                discount_type: 'student',
                discount_status: 'requested',
                discount_request_id: 'request-pending',
            });
            expect(external).toHaveBeenCalledTimes(2);
        } finally {
            external.mockRestore();
        }
    });

    it('updates member revision monotonically for nested payment-lapse and waiver triggers', async () => {
        const target = await member({
            email: 'revision@example.test',
            confirmed: 1,
            stripe_subscription_state: 'active',
            discount_type: 'student',
            discount_status: 'approved',
        });
        await env.DB.prepare("UPDATE members SET stripe_subscription_state='canceled' WHERE id=?").bind(target.id).run();
        const lapsed = (await read(target.id))!;
        expect(lapsed.version).toBeGreaterThan(target.version);
        expect(lapsed).toMatchObject({
            payment_status: null,
            discount_type: null,
            discount_status: null,
        });
        await env.DB.prepare("INSERT INTO waivers(version,name,email) VALUES(1,'Signer','revision@example.test')").run();
        const signed = (await read(target.id))!;
        expect(signed.version).toBeGreaterThan(lapsed.version);
        expect(signed.waiver).not.toBeNull();
    });

    it('returns the current revision from profile edits rather than the pre-trigger snapshot', async () => {
        const target = await member();
        const response = await api('/api/profile', 'PATCH', await login(target.id), {
            name: 'Updated profile',
        });
        expect(response.status).toBe(200);
        const updated = await response.json<Member>();
        const persisted = (await read(target.id))!;
        expect(updated.version).toBe(persisted.version);
        expect(updated.version).toBeGreaterThan(target.version);
    });
});
