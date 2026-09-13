import { randomInt } from 'node:crypto';
import { test, expect, sql, discord } from './fixtures.mjs';

const baseURL = 'http://127.0.0.1:8799';

for (const student of [true, false]) {
    test(`online Discord signup to Ready: ${student ? 'student discount and Tuesday approval' : 'standard price without discount'} (settlement simulated in D1)`, async ({
        page,
        browser,
    }) => {
        // The real billing DO and provider mock retain state between D1 resets.
        sql("UPDATE sqlite_sequence SET seq=? WHERE name='members'", randomInt(1_000_000, 2_000_000_000));
        await discord(page, 'new');
        // Only hosted Stripe is browser-stubbed; all /api/* requests run unchanged.
        await page.route(/^https:\/\/checkout\.stripe\.com\//, (route) =>
            route.fulfill({
                contentType: 'text/html',
                body: '<!doctype html><html lang="en"><head><title>Hosted Stripe test page</title></head><body><h1>Hosted Stripe test page</h1></body></html>',
            }),
        );

        await test.step('Sign up online through the real Discord callback and sign the waiver', async () => {
            await page.goto('/');
            await expect(page.getByRole('heading', { name: 'Become a member' })).toBeVisible();
            await expect(page.getByText('Start online. Meet us at the space on Tuesday night.')).toBeVisible();
            await expect(page.locator('nav, a[href^="/admin"], a[href="/kiosk"]')).toHaveCount(0);
            await page.getByRole('link', { name: 'Continue with Discord' }).click();
            await expect(page).toHaveURL(`${baseURL}/billing`);
            await expect(page.getByRole('heading', { level: 1 })).toHaveText('Your membership');
            expect(sql("SELECT confirmed,discord_username,waiver,fob_id FROM members WHERE email='new@example.test'")[0]).toEqual({
                confirmed: 1,
                discord_username: 'newmaker',
                waiver: null,
                fob_id: null,
            });
            await page.getByRole('link', { name: 'Sign membership waiver' }).click();
            await page.getByLabel('I accept the risks.').check();
            await page.getByLabel('I will follow safety rules.').check();
            await page.getByLabel('Full legal name').fill('New Maker');
            await page.getByRole('button', { name: 'Sign waiver' }).click();
            await expect(page.getByText('Your signed waiver is on file.', { exact: true })).toBeVisible();
            expect(sql("SELECT version,name,email FROM waivers WHERE email='new@example.test'")).toEqual([{ version: 1, name: 'New Maker', email: 'new@example.test' }]);
        });
        const { id } = sql("SELECT id FROM members WHERE email='new@example.test'")[0];

        if (student) {
            await test.step('Request Student before payment and wait for an in-person Tuesday review', async () => {
                await page.locator('summary').filter({ hasText: 'Eligible for a discount?' }).click();
                await page.getByLabel('Discount to request').selectOption('student');
                await page.getByRole('button', { name: 'Request discount' }).click();
                await expect(page.getByRole('heading', { name: 'Visit us Tuesday night', exact: true })).toBeVisible();
                await expect(page.getByText('Student request pending.', { exact: false })).toContainText('review your eligibility with you in person');
                await expect(page.getByText('Wait for approval before paying.', { exact: false })).toBeVisible();
                await expect(page.getByRole('button', { name: 'Start membership payment' })).toHaveCount(0);
                await expect(page.locator('nav, a[href^="/admin"]')).toHaveCount(0);
                const session = await (await page.request.get('/api/session')).json();
                const response = await page.request.post('/api/billing/checkout', {
                    headers: { Origin: baseURL, 'X-CSRF-Token': session.csrf_token },
                    data: {},
                });
                expect(response.status()).toBe(409);
                expect(await response.text()).toContain('pending approval');
                expect(sql('SELECT discount_type,discount_status,stripe_customer_id FROM members WHERE id=?', id)[0]).toEqual({
                    discount_type: 'student',
                    discount_status: 'requested',
                    stripe_customer_id: null,
                });
            });

            await test.step('Leadership records the Tuesday eligibility review through direct admin entry', async () => {
                // The human in-person eligibility review is represented by leadership's explicit confirmation.
                const leaderContext = await browser.newContext({ baseURL });
                try {
                    const leader = await leaderContext.newPage();
                    await discord(leader, 'existing');
                    await leader.goto('/admin');
                    await leader.getByRole('link', { name: 'Continue with Discord' }).click();
                    await expect(leader.getByRole('heading', { level: 1 })).toHaveText('Members');
                    await leader.locator(`tbody a[href="/admin/members/${id}"]`).click();
                    await expect(leader.getByRole('heading', { name: 'Pending discount approval', exact: true })).toBeVisible();
                    await leader.getByRole('button', { name: 'Approve request' }).click();
                    expect(sql('SELECT discount_status FROM members WHERE id=?', id)[0].discount_status).toBe('requested');
                    await leader.getByLabel('I have reviewed eligibility').check();
                    const approval = leader.waitForResponse((response) => response.url().endsWith(`/api/admin/members/${id}/approve`) && response.request().method() === 'POST');
                    await leader.getByRole('button', { name: 'Approve request' }).click();
                    expect((await approval).status()).toBe(200);
                    await expect(leader.getByText('Discount request approved.', { exact: true })).toBeVisible();
                    expect(sql('SELECT discount_type,discount_status FROM members WHERE id=?', id)[0]).toEqual({ discount_type: 'student', discount_status: 'approved' });
                } finally {
                    await leaderContext.close();
                }
                await page.reload();
                await expect(page.getByText('Student approved.', { exact: false })).toContainText('included when you start payment');
            });
        } else {
            await expect(page.locator('details')).not.toHaveAttribute('open');
            await expect(page.getByLabel('Discount to request')).toBeHidden();
        }

        await test.step('Create real coordinated checkout with the approved coupon or standard price', async () => {
            const request = page.waitForRequest((request) => request.url().endsWith('/api/billing/checkout') && request.method() === 'POST');
            await page.getByRole('button', { name: 'Start membership payment' }).click();
            expect((await request).postDataJSON()).toEqual({});
            await expect(page.getByRole('heading', { name: 'Hosted Stripe test page' })).toBeVisible();
            const hosted = new URL(page.url());
            expect(hosted.hostname).toBe('checkout.stripe.com');
            expect(Object.fromEntries(hosted.searchParams)).toMatchObject({
                mode: 'subscription',
                customer: `cus_member_${id}`,
                client_reference_id: String(id),
                'line_items[0][price]': 'price_month',
                'line_items[0][quantity]': '1',
            });
            expect(hosted.searchParams.get('discounts[0][coupon]')).toBe(student ? 'coupon_student' : null);
            expect(sql('SELECT stripe_customer_id,payment_status FROM members WHERE id=?', id)[0]).toEqual({ stripe_customer_id: `cus_member_${id}`, payment_status: null });
            // A browser return is not proof of payment.
            await page.goto(hosted.searchParams.get('success_url'));
            await expect(page.getByRole('heading', { level: 1 })).toHaveText('Your membership');
            await expect(page.getByRole('heading', { name: 'Your membership is active', exact: true })).toHaveCount(0);
            expect(sql('SELECT access_status FROM members WHERE id=?', id)[0].access_status).toBe('PaymentInactive');
        });

        await test.step('SIMULATED boundary: Stripe settlement writes subscription state to D1', async () => {
            // No card charge or webhook is exercised here; continue from reconciled payment state.
            sql("UPDATE members SET stripe_subscription_id=?,stripe_subscription_state='active' WHERE id=?", `sub_journey_${id}`, id);
            await page.reload();
            await expect(page.getByRole('heading', { name: 'Visit us Tuesday night', exact: true })).toBeVisible();
            await expect(page.getByText('Ask leadership for your key fob.', { exact: false })).toBeVisible();
            expect(sql('SELECT payment_status,access_status FROM members WHERE id=?', id)[0]).toEqual({ payment_status: 'ActiveStripe', access_status: 'MissingKeyFob' });
        });

        await test.step('Scan at the real kiosk API, bind on the member phone and become Ready', async () => {
            const kioskContext = await browser.newContext({ baseURL, extraHTTPHeaders: { 'CF-Connecting-IP': '192.0.2.10' } });
            try {
                const kiosk = await kioskContext.newPage();
                await kiosk.goto('/kiosk');
                await expect(kiosk.getByLabel('Fob scanner')).toBeFocused();
                await kiosk.keyboard.type('404');
                await kiosk.keyboard.press('Enter');
                await expect(kiosk.locator('qr-code canvas')).toBeVisible();
                const claimURL = await kiosk.getByRole('link', { name: 'Open enrollment link' }).getAttribute('href');
                await page.setViewportSize({ width: 390, height: 844 });
                await page.goto(claimURL);
                await page.getByRole('checkbox', { name: 'This is my fob, and I want to link it to my membership.' }).check();
                await page.getByRole('button', { name: 'Link fob to my membership' }).click();
                await expect(page).toHaveURL(`${baseURL}/billing`);
                await expect(page.getByRole('heading', { name: 'Your membership is active', exact: true })).toBeVisible();
                await expect(kiosk.getByText('Fob linked. Ready for the next scan.', { exact: true })).toBeVisible();
                expect(sql('SELECT fob_id,access_status,discount_type FROM members WHERE id=?', id)[0]).toEqual({
                    fob_id: 404,
                    access_status: 'Ready',
                    discount_type: student ? 'student' : null,
                });
                expect(sql('SELECT fob_id,claimed_by FROM enrollment_claims')).toEqual([{ fob_id: 404, claimed_by: id }]);
                await page.reload();
                await expect(page.getByText('Your key fob is ready to use at the space.')).toBeVisible();
                await expect(page.getByRole('button', { name: 'Manage billing', exact: true })).toBeEnabled();
                await expect(page.locator('nav, details, a[href^="/admin"], a[href="/waiver"], a[href="/kiosk"]')).toHaveCount(0);
                expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
            } finally {
                await kioskContext.close();
            }
        });
    });
}
