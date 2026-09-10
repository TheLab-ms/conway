import { createHash, randomBytes } from 'node:crypto';
import { globSync } from 'node:fs';
import { DatabaseSync } from 'node:sqlite';
import { fileURLToPath } from 'node:url';
import { test as base, expect } from '@playwright/test';

const state = fileURLToPath(new URL('./.state/', import.meta.url));
let databasePath;

function database() {
    if (!databasePath) {
        for (const path of globSync('**/*.sqlite', { cwd: state })) {
            const candidate = new URL(path, new URL('./.state/', import.meta.url));
            const db = new DatabaseSync(fileURLToPath(candidate));
            try {
                if (db.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name='members'").get()) {
                    databasePath = fileURLToPath(candidate);
                    break;
                }
            } finally {
                db.close();
            }
        }
    }
    if (!databasePath) throw new Error('No migrated browser-test D1 database found; start browser-tests/run.mjs first');
    const db = new DatabaseSync(databasePath);
    db.exec('PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;');
    return db;
}

// One statement with optional bound parameters, returning rows (including RETURNING).
export function sql(query, ...params) {
    const db = database();
    try {
        return db
            .prepare(query)
            .all(...params)
            .map((row) => ({ ...row }));
    } finally {
        db.close();
    }
}

function reset() {
    const db = database();
    try {
        db.exec('PRAGMA foreign_keys=OFF; BEGIN IMMEDIATE; UPDATE runtime_control SET automation_enabled=0 WHERE id=1;');
        const tables = db
            .prepare(
                "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '_cf_%' AND name NOT IN ('d1_migrations','runtime_control','settings')",
            )
            .all();
        for (const { name } of tables) db.exec(`DELETE FROM "${name.replaceAll('"', '""')}"`);
        db.exec('DELETE FROM sqlite_sequence;');
        db.prepare('INSERT INTO waiver_versions(version,content,agreements) VALUES(1,?,?)').run(
            'Use tools safely. <script>window.unsafe=true</script>',
            JSON.stringify(['I accept the risks.', 'I will follow safety rules.']),
        );
        const member = db.prepare('INSERT INTO members(id,email,name,discord_user_id,discord_username,confirmed,leadership,non_billable,fob_id) VALUES(?,?,?,?,?,1,?,?,?)');
        member.run(1, 'alex@example.test', 'Alex Maker', '123456789012345678', 'alex', 1, 1, 101);
        member.run(2, 'sam@example.test', 'Sam Member', '223456789012345678', 'sam', 0, 0, null);
        member.run(3, 'robin@example.test', 'Robin Leader', '323456789012345678', 'robin', 1, 1, 103);
        db.prepare('UPDATE settings SET version=1,data=? WHERE id=1').run(
            JSON.stringify({
                site_name: 'Conway',
                discounts: [
                    { id: 'student', label: 'Student', coupon_id: 'coupon_student' },
                    { id: 'family', label: 'Family', coupon_id: 'coupon_family' },
                ],
                monthly_price_id: 'price_month',
                yearly_price_id: 'price_year',
                waiver_version: 1,
            }),
        );
        db.exec('UPDATE runtime_control SET automation_enabled=1 WHERE id=1; COMMIT; PRAGMA foreign_keys=ON;');
        expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([]);
    } catch (error) {
        if (db.isTransaction) db.exec('ROLLBACK');
        throw error;
    } finally {
        db.close();
    }
}

export const test = base.extend({
    _database: [
        async ({}, use) => {
            reset();
            await use();
        },
        { auto: true },
    ],
    _browserErrors: [
        async ({ context, _database }, use, testInfo) => {
            const errors = [];
            const watch = (page) => {
                page.on('pageerror', (error) => errors.push(error.stack || error.message));
                page.on('console', (message) => {
                    if (/content.security.policy|\bCSP\b|violates.*directive|refused to (?:execute|load|apply|connect|frame)/i.test(message.text())) errors.push(message.text());
                });
            };
            context.pages().forEach(watch);
            context.on('page', watch);
            await use();
            if (errors.length) await testInfo.attach('browser-errors', { body: errors.join('\n'), contentType: 'text/plain' });
            expect(errors, 'No uncaught browser exceptions or CSP/security errors').toEqual([]);
        },
        { auto: true },
    ],
    login: async ({ page, context, _database }, use) => {
        await use(async (memberId = 1) => {
            const token = randomBytes(32).toString('hex');
            const csrf = randomBytes(32).toString('hex');
            const expires = Math.floor(Date.now() / 1000) + 3600;
            sql('INSERT INTO sessions(token_hash,member,csrf_token,expires) VALUES(?,?,?,?)', createHash('sha256').update(token).digest('hex'), memberId, csrf, expires);
            await context.addCookies([{ name: 'conway_session', value: token, url: 'http://127.0.0.1:8799', httpOnly: true, sameSite: 'Lax', expires }]);
            await page.goto('/dashboard');
            await expect(page.getByRole('heading', { level: 1 })).toContainText('Hello,');
        });
    },
});

export { expect };
