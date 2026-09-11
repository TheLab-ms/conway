import { createHash } from 'node:crypto';
import jsQR from 'jsqr';
import { test, expect, sql } from './fixtures.mjs';

const baseURL = 'http://127.0.0.1:8799';
const trusted = { 'CF-Connecting-IP': '192.0.2.10' };
const hash = (token) => createHash('sha256').update(token).digest('hex');
async function scan(page, fob = 202) {
    await expect(page.getByLabel('Fob scanner', { exact: true })).toBeFocused();
    const previous = (await page.locator('.claim-link').count()) ? await page.locator('.claim-link').getAttribute('href') : null;
    await page.keyboard.type(String(fob));
    await page.keyboard.press('Enter');
    await expect(page.locator('qr-code canvas')).toBeVisible();
    if (previous) await expect(page.locator('.claim-link')).not.toHaveAttribute('href', previous);
    return page.locator('.claim-link').getAttribute('href');
}
async function confirm(page) {
    await page.getByRole('checkbox').check();
    await page.getByRole('button', { name: 'Link fob to my membership' }).click();
}

test('anonymous scanner QR, clipboard and separate phone confirmation replace a fob exactly once', async ({ page, browser, login }) => {
    await login();
    const kioskContext = await browser.newContext({ baseURL });
    try {
        await kioskContext.setExtraHTTPHeaders(trusted);
        const kiosk = await kioskContext.newPage();
        await kiosk.goto('/kiosk');
        const url = await scan(kiosk);
        expect(sql('SELECT member FROM sessions WHERE member IS NULL')).toHaveLength(1);
        const image = await kiosk.locator('canvas').evaluate((canvas) => {
            // One pixel per QR module keeps browser/trace payloads small.
            const small = document.createElement('canvas');
            small.width = canvas.width / 6;
            small.height = canvas.height / 6;
            const context = small.getContext('2d');
            context.imageSmoothingEnabled = false;
            context.drawImage(canvas, 0, 0, small.width, small.height);
            return { width: small.width, height: small.height, data: Array.from(context.getImageData(0, 0, small.width, small.height).data) };
        });
        expect(jsQR(Uint8ClampedArray.from(image.data), image.width, image.height)?.data).toBe(url);
        await kioskContext.grantPermissions(['clipboard-read', 'clipboard-write']);
        await kiosk.getByRole('button', { name: 'Copy enrollment link' }).click();
        await expect(kiosk.getByText('Enrollment link copied.', { exact: true })).toBeVisible();
        expect(await kiosk.evaluate(() => navigator.clipboard.readText())).toBe(url);
        await kiosk.evaluate(() => {
            navigator.clipboard.writeText = async () => {
                throw new DOMException('Denied', 'NotAllowedError');
            };
        });
        await kiosk.getByRole('button', { name: 'Copy enrollment link' }).click();
        await expect(kiosk.getByText('Clipboard unavailable.', { exact: false })).toBeVisible();
        await expect(kiosk.locator('.claim-link')).toHaveAttribute('href', url);
        await page.goto(url);
        await expect(page.getByText('Your account already has fob 101.', { exact: false })).toBeVisible();
        await page.getByRole('button', { name: 'Link fob to my membership' }).click();
        expect(sql('SELECT fob_id FROM members WHERE id=1')).toEqual([{ fob_id: 101 }]);
        expect(sql('SELECT claimed_by FROM enrollment_claims')).toEqual([{ claimed_by: null }]);
        await confirm(page);
        await expect(page).toHaveURL(`${baseURL}/billing`);
        expect(sql('SELECT fob_id FROM members WHERE id=1')).toEqual([{ fob_id: 202 }]);
        expect(sql('SELECT claimed_by FROM enrollment_claims')).toEqual([{ claimed_by: 1 }]);
        await expect(kiosk.getByText('Fob linked. Ready for the next scan.', { exact: true })).toBeVisible();
        await expect(kiosk.locator('qr-code, .claim-link')).toHaveCount(0);
        await expect(kiosk.getByLabel('Fob scanner')).toBeFocused();
        await page.goBack();
        await expect(page).not.toHaveURL(/token=/);
        await login(2);
        await page.goto(url);
        await confirm(page);
        await expect(page.getByRole('alert')).toContainText('expired or already used');
        expect(sql('SELECT fob_id FROM members WHERE id=2')).toEqual([{ fob_id: null }]);
        expect(sql('SELECT claimed_by FROM enrollment_claims')).toEqual([{ claimed_by: 1 }]);
        await scan(kiosk, 204);
        expect(sql('SELECT fob_id FROM enrollment_claims ORDER BY created,fob_id')).toEqual([{ fob_id: 202 }, { fob_id: 204 }]);
    } finally {
        await kioskContext.close();
    }
});

test('actual kiosk IP enforcement rejects scans and polling from an untrusted address', async ({ page, context }) => {
    await context.setExtraHTTPHeaders({ 'CF-Connecting-IP': '192.0.2.11' });
    await page.goto('/kiosk');
    await expect(page.getByLabel('Fob scanner')).toBeFocused();
    await page.keyboard.type('202');
    await page.keyboard.press('Enter');
    await expect(page.getByRole('alert')).toContainText('only available at the makerspace kiosk');
    expect(sql('SELECT * FROM enrollment_claims')).toEqual([]);
    await context.setExtraHTTPHeaders(trusted);
    await scan(page);
    await context.setExtraHTTPHeaders({ 'CF-Connecting-IP': '192.0.2.11' });
    await expect(page.getByRole('alert')).toContainText('only available at the makerspace kiosk');
    await expect(page.locator('qr-code, .claim-link')).toHaveCount(0);
    expect(sql('SELECT * FROM enrollment_claims')).toHaveLength(1);
});

for (const scenario of ['expired', 'unknown', 'invalid', 'owned']) {
    test(`${scenario} claim cannot change membership or consume an owned fob claim`, async ({ page, login }) => {
        await login(2);
        const token = scenario === 'invalid' ? 'not-a-token' : 'a'.repeat(64);
        const now = Math.floor(Date.now() / 1000);
        if (['expired', 'owned'].includes(scenario))
            sql(
                'INSERT INTO enrollment_claims(token_hash,fob_id,created,expires) VALUES(?,?,?,?)',
                hash(token),
                scenario === 'owned' ? 103 : 202,
                now,
                now + (scenario === 'expired' ? -1 : 300),
            );
        await page.goto(`/fobs/bind?token=${token}`);
        if (scenario === 'invalid') {
            await expect(page.getByRole('heading', { name: 'Enrollment link needed' })).toBeVisible();
            await expect(page.getByRole('checkbox')).toHaveCount(0);
        } else {
            await confirm(page);
            await expect(page.getByRole('alert')).toContainText(scenario === 'owned' ? 'Conflicting member identity, fob' : 'expired or already used');
        }
        expect(sql('SELECT id,fob_id FROM members ORDER BY id')).toEqual([
            { id: 1, fob_id: 101 },
            { id: 2, fob_id: null },
            { id: 3, fob_id: 103 },
        ]);
        expect(sql('SELECT * FROM enrollment_claims WHERE claimed_by IS NOT NULL')).toEqual([]);
        if (scenario === 'owned') {
            sql('UPDATE members SET fob_id=NULL WHERE id=3');
            await confirm(page);
            await expect(page).toHaveURL(`${baseURL}/billing`);
            expect(sql('SELECT fob_id FROM members WHERE id=2')).toEqual([{ fob_id: 103 }]);
            expect(sql('SELECT claimed_by FROM enrollment_claims')).toEqual([{ claimed_by: 2 }]);
        }
    });
}

test('scan rate limit leaves existing claims intact', async ({ page, context }) => {
    await context.setExtraHTTPHeaders(trusted);
    const now = Math.floor(Date.now() / 1000);
    for (let i = 0; i < 30; i++) sql('INSERT INTO enrollment_claims(token_hash,fob_id,created,expires) VALUES(?,?,?,?)', hash(String(i)), 202 + i, now, now + 300);
    await page.goto('/kiosk');
    await expect(page.getByLabel('Fob scanner')).toBeFocused();
    await page.keyboard.type('999');
    const response = page.waitForResponse((r) => r.url().endsWith('/api/kiosk/claims') && r.request().method() === 'POST');
    await page.keyboard.press('Enter');
    expect((await response).status()).toBe(429);
    await expect(page.getByRole('alert')).toContainText('Too many scans; wait a minute');
    await expect(page.locator('qr-code')).toHaveCount(0);
    expect(sql('SELECT * FROM enrollment_claims')).toHaveLength(30);
});

test('reset, expiration and SPA navigation stop polling and remove stale QR codes', async ({ page, context }) => {
    await context.setExtraHTTPHeaders(trusted);
    await page.clock.install();
    await page.goto('/kiosk');
    let polls = 0;
    page.on('request', (request) => {
        if (request.url().includes('/api/kiosk/claims/')) polls++;
    });
    await scan(page);
    await expect.poll(() => polls).toBeGreaterThan(0);
    await page.getByRole('button', { name: 'Done', exact: true }).click();
    await expect(page.getByLabel('Fob scanner')).toBeFocused();
    await expect(page.getByLabel('Fob scanner')).toHaveValue('');
    await expect(page.locator('qr-code, .claim-link')).toHaveCount(0);
    const stopped = polls;
    await page.clock.runFor(6000);
    expect(polls).toBe(stopped);
    await scan(page, 203);
    await page.clock.fastForward(301_000);
    await expect(page.getByText('Code expired. Scan your fob again.', { exact: true })).toBeVisible();
    await expect(page.locator('qr-code, .claim-link')).toHaveCount(0);
    await expect(page.getByLabel('Fob scanner')).toBeFocused();
    await page.clock.setFixedTime(new Date());
    await scan(page, 204);
    await page.evaluate(() => document.querySelector('conway-app').navigate('/'));
    await expect(page).toHaveURL(`${baseURL}/`);
    const navigated = polls;
    await page.clock.runFor(6000);
    expect(polls).toBe(navigated);
    await expect(page.locator('qr-code, .claim-link')).toHaveCount(0);
});

test('idle anonymous session expiry retains actionable reload error and reload recovers', async ({ page, context }) => {
    await context.setExtraHTTPHeaders(trusted);
    await page.clock.install();
    await page.goto('/kiosk');
    await expect(page.getByLabel('Fob scanner')).toBeFocused();
    sql('UPDATE sessions SET expires=0 WHERE member IS NULL');
    await page.keyboard.type('202');
    await page.keyboard.press('Enter');
    await expect(page.getByRole('alert')).toContainText('Session expired; reload and try again');
    await expect(page.getByLabel('Fob scanner')).toBeFocused();
    await page.clock.runFor(6000);
    await expect(page.getByRole('alert')).toContainText('Session expired; reload and try again');
    expect(sql('SELECT * FROM enrollment_claims')).toEqual([]);
    await page.reload();
    await scan(page);
});

test('scanner submits after one second idle, clears each scan and ignores empty Enter', async ({ page, context }) => {
    await context.setExtraHTTPHeaders(trusted);
    await page.clock.install();
    await page.goto('/kiosk');
    const scanner = page.getByLabel('Fob scanner');
    await expect(scanner).toBeFocused();
    await expect(scanner).not.toBeInViewport();
    await expect(page.getByLabel('Fob ID', { exact: true })).toHaveCount(0);
    await page.clock.pauseAt(new Date(Date.now() + 1000));
    await page.keyboard.press('Enter');
    await page.keyboard.type('00020');
    await page.clock.runFor(700);
    await page.keyboard.type('2');
    await page.clock.runFor(999);
    expect(sql('SELECT * FROM enrollment_claims')).toEqual([]);
    await page.clock.runFor(1);
    await expect(page.locator('qr-code canvas')).toBeVisible();
    expect(sql('SELECT fob_id FROM enrollment_claims')).toEqual([{ fob_id: 202 }]);
    await expect(scanner).toHaveValue('');
    await expect(scanner).toBeFocused();
    await page.keyboard.press('Enter');
    const first = await page.locator('.claim-link').getAttribute('href');
    await scan(page, 203);
    await expect(page.locator('.claim-link')).not.toHaveAttribute('href', first);
    await page.clock.runFor(1200);
    expect(sql('SELECT fob_id FROM enrollment_claims ORDER BY fob_id')).toEqual([{ fob_id: 202 }, { fob_id: 203 }]);
});

test('scanner recovers page focus without intercepting controls or surviving route abort', async ({ page, context }) => {
    await context.setExtraHTTPHeaders(trusted);
    await page.clock.install();
    await page.goto('/kiosk');
    await expect(page.getByLabel('Fob scanner')).toBeFocused();
    await page.clock.pauseAt(new Date(Date.now() + 1000));
    await page.evaluate(() => {
        document.activeElement.blur();
    });
    await page.keyboard.type('202');
    await expect(page.getByLabel('Fob scanner')).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page.locator('qr-code canvas')).toBeVisible();
    await page.getByRole('button', { name: 'Done', exact: true }).focus();
    await page.keyboard.type('999');
    await page.keyboard.press('Enter');
    await expect(page.locator('qr-code')).toHaveCount(0);
    await expect(page.getByLabel('Fob scanner')).toBeFocused();
    await page.evaluate(() => {
        const input = document.createElement('input');
        input.id = 'unrelated-input';
        document.querySelector('.kiosk').append(input);
        input.focus();
    });
    await page.keyboard.type('888');
    await page.keyboard.press('Enter');
    await page.clock.runFor(1200);
    await expect(page.locator('#unrelated-input')).toHaveValue('888');
    await expect(page.locator('#unrelated-input')).toBeFocused();
    expect(sql('SELECT fob_id FROM enrollment_claims')).toEqual([{ fob_id: 202 }]);
    await page.getByRole('heading', { name: 'Scan a key fob' }).click();
    await expect(page.getByLabel('Fob scanner')).toBeFocused();
    await page.keyboard.type('203');
    // Navigate without changing focus first, so abort must cancel the idle scan.
    await page.evaluate(() => document.querySelector('conway-app').navigate('/'));
    await expect(page).toHaveURL(`${baseURL}/`);
    await page.keyboard.type('777');
    await page.keyboard.press('Enter');
    await page.clock.runFor(6000);
    expect(sql('SELECT fob_id FROM enrollment_claims')).toEqual([{ fob_id: 202 }]);
    await expect(page.locator('qr-code, .kiosk-scanner')).toHaveCount(0);
    await page.evaluate(() => document.querySelector('conway-app').navigate('/kiosk'));
    await page.clock.runFor(1);
    await scan(page, 204);
    expect(sql('SELECT fob_id FROM enrollment_claims ORDER BY fob_id')).toEqual([{ fob_id: 202 }, { fob_id: 204 }]);
});

test('invalid scanner IDs never create claims and the next scan recovers', async ({ page, context }) => {
    await context.setExtraHTTPHeaders(trusted);
    await page.goto('/kiosk');
    for (const value of ['0', '4294967296', '2e2', '-202']) {
        await expect(page.getByLabel('Fob scanner')).toBeFocused();
        await page.keyboard.type(value);
        await page.keyboard.press('Enter');
        await expect(page.getByRole('alert')).toHaveText('Fob not recognized. Scan again.');
        await expect(page.getByLabel('Fob scanner')).toHaveValue('');
    }
    expect(sql('SELECT * FROM enrollment_claims')).toEqual([]);
    await scan(page);
});

test('a slow previous scan cannot replace the newest QR code', async ({ page, context }) => {
    await context.setExtraHTTPHeaders(trusted);
    let firstRoute;
    await page.route('**/api/kiosk/claims', async (route) => {
        if (route.request().postDataJSON().fob_id === 202) firstRoute = route;
        else await route.continue();
    });
    await page.goto('/kiosk');
    await expect(page.getByLabel('Fob scanner')).toBeFocused();
    await page.keyboard.type('202');
    await page.keyboard.press('Enter');
    await expect.poll(() => Boolean(firstRoute)).toBe(true);
    const newest = await scan(page, 203);
    const response = page.waitForResponse((r) => r.url().endsWith('/api/kiosk/claims') && r.request().postDataJSON().fob_id === 202);
    await firstRoute.fulfill({
        json: { token: 'a'.repeat(64), url: `/fobs/bind?token=${'a'.repeat(64)}`, expires: Math.floor(Date.now() / 1000) + 300 },
    });
    await (await response).finished();
    await expect(page.locator('.claim-link')).toHaveAttribute('href', newest);
    expect(sql('SELECT fob_id FROM enrollment_claims')).toEqual([{ fob_id: 203 }]);
});
