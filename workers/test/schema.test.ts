import { applyD1Migrations } from 'cloudflare:test';
import { env } from 'cloudflare:workers';
import { describe, expect, it } from 'vitest';
import type { Member } from '../src/types';
import { member } from './fixtures';

const getMember = (id: number) => env.DB.prepare('SELECT * FROM members WHERE id = ?').bind(id).first<Member>();

describe('0001_core migration in D1', () => {
    it('applies once, initializes settings, and starts each test with no members', async () => {
        expect(env.TEST_MIGRATIONS[0].name).toBe('0001_core.sql');
        await applyD1Migrations(env.DB, env.TEST_MIGRATIONS);
        expect(await env.DB.prepare('SELECT COUNT(*) AS n FROM members').first('n')).toBe(0);
        expect(await env.DB.prepare('SELECT automation_enabled FROM runtime_control WHERE id=1').first('automation_enabled')).toBe(1);
        const data = await env.DB.prepare('SELECT data FROM settings WHERE id=1').first<string>('data');
        expect(JSON.parse(data!).site_name).toBe('Conway');
        expect((await env.DB.prepare('PRAGMA foreign_key_check').all()).results).toEqual([]);
    });

    it('enforces case-insensitive emails and unique Discord identities without losing numeric precision', async () => {
        const discord = '123456789012345678';
        await member({ email: 'Person@example.test', discord_user_id: discord });
        await expect(member({ email: 'person@example.test' })).rejects.toThrow(/UNIQUE/);
        await expect(member({ discord_user_id: discord })).rejects.toThrow(/UNIQUE/);
        expect(await env.DB.prepare('SELECT discord_user_id FROM members').first('discord_user_id')).toBe(discord);
        await member();
        await member();
    });

    it.each(['abcde', '1234', '1'.repeat(26), '12345x'])('rejects invalid Discord ID %s', async (discord_user_id) => {
        await expect(member({ discord_user_id })).rejects.toThrow(/CHECK/);
    });

    it.each([0, -1, 4294967296])('rejects out-of-range fob %s', async (fob_id) => {
        await expect(member({ fob_id })).rejects.toThrow(/CHECK/);
    });

    it('preserves explicit imported IDs/timestamps and derives identifier instead of accepting stale status', async () => {
        const row = await member({
            id: 123,
            created: 1234567890,
            email: 'legacy@example.test',
            name: 'Legacy Member',
        });
        expect(row).toMatchObject({
            id: 123,
            created: 1234567890,
            identifier: 'Legacy Member',
            payment_status: null,
            access_status: 'UnconfirmedEmail',
        });
        await env.DB.prepare("UPDATE members SET name='' WHERE id=123").run();
        expect((await getMember(123))?.identifier).toBe('legacy@example.test');
        await expect(env.DB.prepare("UPDATE members SET access_status='Ready' WHERE id=123").run()).rejects.toThrow(/generated column/);
        expect((await member()).id).toBeGreaterThan(123);
    });

    it('resolves existing waivers on import and attaches newly signed waivers', async () => {
        await env.DB.prepare("INSERT INTO waivers (version,name,email) VALUES (1,'Before','before@example.test'),(2,'Before','before@example.test')").run();
        const before = await member({ email: 'before@example.test' });
        expect(await env.DB.prepare('SELECT version FROM waivers WHERE id=?').bind(before.waiver).first('version')).toBe(2);
        const after = await member({ email: 'after@example.test' });
        await env.DB.prepare("INSERT INTO waivers (version,name,email) VALUES (2,'After','after@example.test')").run();
        expect((await getMember(after.id))?.waiver).not.toBeNull();
    });
});

describe('legacy payment and access status parity', () => {
    it.each([
        [0, 0, null, null, null],
        [0, 1, 'active', 'paypal', null],
        [1, 0, null, 'paypal', 'ActivePaypal'],
        [1, 1, 'active', 'paypal', 'ActivePaypal'],
        [1, 0, 'active', null, 'ActiveStripe'],
        [1, 0, 'trialing', null, 'ActiveStripe'],
        [1, 1, 'active', null, 'ActiveStripe'],
        [1, 1, 'past_due', null, 'ActiveNonBillable'],
        [1, 0, 'past_due', null, null],
        [1, 0, 'canceled', null, null],
        [1, 0, 'unpaid', null, null],
        [1, 0, 'incomplete', null, null],
    ])('confirmed=%s non_billable=%s stripe=%s paypal=%s -> %s', async (confirmed, non_billable, stripe_subscription_state, paypal_subscription_id, expected) => {
        expect(
            (
                await member({
                    confirmed,
                    non_billable,
                    stripe_subscription_state,
                    paypal_subscription_id,
                })
            ).payment_status,
        ).toBe(expected);
    });

    it('moves through access prerequisites and only exposes Ready fobs', async () => {
        const row = await member({ email: 'access@example.test' });
        expect(row.access_status).toBe('UnconfirmedEmail');
        await env.DB.prepare('UPDATE members SET confirmed=1 WHERE id=?').bind(row.id).run();
        expect((await getMember(row.id))?.access_status).toBe('MissingWaiver');
        await env.DB.prepare("INSERT INTO waivers (version,name,email) VALUES (1,'Access','access@example.test')").run();
        expect((await getMember(row.id))?.access_status).toBe('PaymentInactive');
        await env.DB.prepare("UPDATE members SET stripe_subscription_state='active' WHERE id=?").bind(row.id).run();
        expect((await getMember(row.id))?.access_status).toBe('MissingKeyFob');
        await env.DB.prepare('UPDATE members SET fob_id=4294967295 WHERE id=?').bind(row.id).run();
        expect((await getMember(row.id))?.access_status).toBe('Ready');
        expect((await env.DB.prepare('SELECT * FROM active_keyfobs').all()).results).toEqual([{ fob_id: 4294967295 }]);
        await env.DB.prepare("UPDATE members SET stripe_subscription_state='canceled' WHERE id=?").bind(row.id).run();
        expect((await env.DB.prepare('SELECT * FROM active_keyfobs').all()).results).toEqual([]);
    });

    it('non-billable access bypasses email/waiver/payment but still requires a fob', async () => {
        const row = await member({ non_billable: 1 });
        expect(row).toMatchObject({ payment_status: null, access_status: 'MissingKeyFob' });
        await env.DB.prepare('UPDATE members SET fob_id=7 WHERE id=?').bind(row.id).run();
        expect((await getMember(row.id))?.access_status).toBe('Ready');
    });
});

describe('families and last linked leader protection', () => {
    it('propagates family payment loss/restoration and resets cached state on unlink', async () => {
        const root = await member({ confirmed: 1, non_billable: 1 });
        const child = await member({
            confirmed: 1,
            non_billable: 1,
            fob_id: 8,
            root_family_member: root.id,
        });
        expect(child).toMatchObject({ root_family_member_active: 1, access_status: 'Ready' });
        await env.DB.prepare('UPDATE members SET non_billable=0 WHERE id=?').bind(root.id).run();
        expect(await getMember(child.id)).toMatchObject({
            root_family_member_active: 0,
            access_status: 'FamilyInactive',
        });
        await env.DB.prepare("UPDATE members SET paypal_subscription_id='restored' WHERE id=?").bind(root.id).run();
        expect(await getMember(child.id)).toMatchObject({
            root_family_member_active: 1,
            access_status: 'Ready',
        });
        await env.DB.prepare('UPDATE members SET root_family_member=NULL WHERE id=?').bind(child.id).run();
        expect(await getMember(child.id)).toMatchObject({
            root_family_member_active: null,
            access_status: 'Ready',
        });
    });

    it('blocks deleting a family root, self-links, chains, and converting a root with dependents', async () => {
        const root = await member();
        const child = await member({ root_family_member: root.id });
        const other = await member();
        await expect(env.DB.prepare('DELETE FROM members WHERE id=?').bind(root.id).run()).rejects.toThrow(/FOREIGN KEY/);
        await expect(env.DB.prepare('UPDATE members SET root_family_member=id WHERE id=?').bind(other.id).run()).rejects.toThrow(/CHECK/);
        await expect(member({ root_family_member: child.id })).rejects.toThrow(/primary member/);
        await expect(env.DB.prepare('UPDATE members SET root_family_member=? WHERE id=?').bind(other.id, root.id).run()).rejects.toThrow(/dependents/);
        await env.DB.prepare('DELETE FROM members WHERE id=?').bind(child.id).run();
        await env.DB.prepare('DELETE FROM members WHERE id=?').bind(root.id).run();
        expect(await getMember(root.id)).toBeNull();
    });

    it.each(['DELETE FROM members WHERE id=?', 'UPDATE members SET leadership=0 WHERE id=?', 'UPDATE members SET discord_user_id=NULL WHERE id=?'])(
        'protects last usable leader: %s',
        async (sql) => {
            const leader = await member({ leadership: 1, discord_user_id: '123456789012345678' });
            await member({ leadership: 1 });
            await expect(env.DB.prepare(sql).bind(leader.id).run()).rejects.toThrow(/last linked leader/);
            await member({ leadership: 1, discord_user_id: '987654321012345678' });
            await env.DB.prepare(sql).bind(leader.id).run();
        },
    );

    it('allows controlled imports with automation disabled and no outbox/audit side effects', async () => {
        await env.DB.prepare('UPDATE runtime_control SET automation_enabled=0 WHERE id=1').run();
        const root = await member({
            leadership: 1,
            discord_user_id: '123456789012345678',
            confirmed: 1,
            non_billable: 1,
        });
        const child = await member({ root_family_member: root.id });
        expect(child.root_family_member_active).toBe(1);
        await env.DB.prepare('UPDATE members SET leadership=0 WHERE id=?').bind(root.id).run();
        expect((await env.DB.prepare('SELECT * FROM jobs').all()).results).toEqual([]);
        expect((await env.DB.prepare('SELECT * FROM member_events').all()).results).toEqual([]);
        await env.DB.prepare('UPDATE runtime_control SET automation_enabled=1 WHERE id=1').run();
        await member();
        expect(await env.DB.prepare('SELECT COUNT(*) AS n FROM jobs').first('n')).toBe(2);
    });
});

describe('transactional trigger outbox', () => {
    it('enqueues creation and Discord changes, but not unrelated profile edits', async () => {
        const row = await member();
        expect((await env.DB.prepare('SELECT kind,payload FROM jobs ORDER BY id').all()).results).toEqual([
            { kind: 'signup', payload: JSON.stringify({ member_id: row.id }) },
            { kind: 'discord_sync', payload: JSON.stringify({ member_id: row.id }) },
        ]);
        await env.DB.prepare("UPDATE members SET name_override='Profile only' WHERE id=?").bind(row.id).run();
        expect(await env.DB.prepare('SELECT COUNT(*) AS n FROM jobs').first('n')).toBe(2);
        await env.DB.prepare("UPDATE members SET confirmed=1,stripe_subscription_state='active' WHERE id=?").bind(row.id).run();
        expect(await env.DB.prepare('SELECT COUNT(*) AS n FROM jobs').first('n')).toBe(3);
        const event = await env.DB.prepare("SELECT details FROM member_events WHERE event='MemberUpdated' ORDER BY id DESC LIMIT 1").first<string>('details');
        expect(JSON.parse(event!)).toMatchObject({
            payment_before: null,
            payment_after: 'ActiveStripe',
        });
        await env.DB.prepare('DELETE FROM members WHERE id=?').bind(row.id).run();
        expect(await env.DB.prepare('SELECT COUNT(*) AS n FROM jobs').first('n')).toBe(4);
    });

    it.each(['active', 'trialing'])('enqueues a discount request once and preserves it when %s payment lapses', async (stripe_subscription_state) => {
        const row = await member({ confirmed: 1, stripe_subscription_state });
        const update = env.DB.prepare("UPDATE members SET discount_type='student',discount_status='requested',discount_request_id='request-1' WHERE id=?").bind(row.id);
        await update.run();
        await update.run();
        expect((await env.DB.prepare("SELECT payload,dedupe_key FROM jobs WHERE kind='discount'").all()).results).toEqual([
            {
                payload: JSON.stringify({ member_id: row.id, request_id: 'request-1' }),
                dedupe_key: 'discount:request-1',
            },
        ]);
        await env.DB.prepare("UPDATE members SET stripe_subscription_state='canceled' WHERE id=?").bind(row.id).run();
        expect(await getMember(row.id)).toMatchObject({
            payment_status: null,
            discount_type: 'student',
            discount_status: 'requested',
            discount_request_id: 'request-1',
        });
        expect(await env.DB.prepare("SELECT count(*) n FROM jobs WHERE kind='discount'").first('n')).toBe(1);
    });

    it.each(['approved', null])('still clears granted discount metadata on payment lapse (status: %s)', async (discount_status) => {
        const row = await member({
            confirmed: 1,
            stripe_subscription_state: 'active',
            discount_type: 'student',
            discount_status,
            discount_request_id: 'granted-request',
        });
        await env.DB.prepare("UPDATE members SET stripe_subscription_state='canceled' WHERE id=?").bind(row.id).run();
        expect(await getMember(row.id)).toMatchObject({
            payment_status: null,
            discount_type: null,
            discount_status: null,
            discount_request_id: null,
        });
    });

    it('deduplicates and audits all swipes, never moves last-seen backwards, and only notifies denials', async () => {
        const row = await member({ fob_id: 42 });
        await env.DB.prepare('DELETE FROM jobs').run();
        const swipe = (uid: string, timestamp: number, allowed: number) =>
            env.DB.prepare('INSERT INTO fob_swipes (uid,timestamp,fob_id,member,allowed) VALUES (?,?,42,?,?) ON CONFLICT(uid) DO NOTHING')
                .bind(uid, timestamp, row.id, allowed)
                .run();
        await swipe('new', 200, 1);
        await swipe('old', 100, 0);
        await swipe('new', 300, 1);
        expect((await getMember(row.id))?.fob_last_seen).toBe(200);
        expect((await env.DB.prepare('SELECT uid,timestamp,member,allowed FROM fob_swipes ORDER BY timestamp').all()).results).toEqual([
            { uid: 'old', timestamp: 100, member: row.id, allowed: 0 },
            { uid: 'new', timestamp: 200, member: row.id, allowed: 1 },
        ]);
        expect((await env.DB.prepare('SELECT kind,dedupe_key FROM jobs ORDER BY id').all()).results).toEqual([{ kind: 'denied', dedupe_key: 'swipe:old' }]);
    });

    it('rolls back member, audit, and outbox writes when a D1 batch fails', async () => {
        await expect(
            env.DB.batch([
                env.DB.prepare("INSERT INTO members (email) VALUES ('rollback@example.test')"),
                env.DB.prepare("INSERT INTO members (email) VALUES ('ROLLBACK@example.test')"),
            ]),
        ).rejects.toThrow(/UNIQUE/);
        for (const table of ['members', 'member_events', 'jobs']) {
            expect(await env.DB.prepare(`SELECT COUNT(*) AS n FROM ${table}`).first('n')).toBe(0);
        }
    });
});
