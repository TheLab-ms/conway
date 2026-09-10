import { randomInt } from 'node:crypto';
import { test as base, expect, sql } from './fixtures.mjs';

const test = base.extend({
    billingMember: async ({ login }, use) => {
        // The real shared Stripe DO keeps per-member slots across D1 resets.
        const id = randomInt(1_000_000, 2_000_000_000);
        sql('INSERT INTO members(id,email,name,confirmed) VALUES(?,?,?,1)', id, `billing-${id}@example.test`, 'Billing Member');
        await login(id);
        await use(id);
    },
});

test.beforeEach(async ({ page }) => {
    // Only the hosted external destination is browser-stubbed, never /api/*.
    await page.route(/^https:\/\/(checkout|billing)\.stripe\.com\//, (route) =>
        route.fulfill({
            contentType: 'text/html',
            body: '<!doctype html><html lang="en"><head><title>Hosted Stripe test page</title></head><body><h1>Hosted Stripe test page</h1></body></html>',
        }),
    );
});

async function checkout(page, frequency = 'monthly') {
    await page.goto('/billing');
    await page.getByLabel('Billing frequency').selectOption(frequency);
    await page.getByRole('button', { name: 'Continue to secure billing' }).click();
    await expect(page).toHaveURL(/^https:\/\/(checkout|billing)\.stripe\.com\//);
    await expect(page.getByRole('heading', { name: 'Hosted Stripe test page' })).toBeVisible();
    return new URL(page.url());
}

for (const [frequency, price, annual] of [
    ['monthly', 'price_month', 0],
    ['annual', 'price_year', 1],
]) {
    test(`${frequency} membership uses real checkout and persists billing identity`, async ({ page, billingMember }) => {
        const url = await checkout(page, frequency);
        expect(url.hostname).toBe('checkout.stripe.com');
        expect(Object.fromEntries(url.searchParams)).toMatchObject({
            mode: 'subscription',
            customer: `cus_member_${billingMember}`,
            client_reference_id: String(billingMember),
            'metadata[conway_member_id]': String(billingMember),
            'subscription_data[metadata][conway_member_id]': String(billingMember),
            'line_items[0][price]': price,
            'line_items[0][quantity]': '1',
            success_url: 'http://127.0.0.1:8799',
            cancel_url: 'http://127.0.0.1:8799',
        });
        expect(url.searchParams.has('discounts[0][coupon]')).toBe(false);
        expect(sql('SELECT stripe_customer_id,bill_annually,payment_status FROM members WHERE id=?', billingMember)[0]).toEqual({
            stripe_customer_id: `cus_member_${billingMember}`,
            bill_annually: annual,
            payment_status: null,
        });
    });
}

test('same selection reuses checkout; annual change expires the earlier session', async ({ page, billingMember }) => {
    const monthly = await checkout(page);
    expect((await checkout(page)).href).toBe(monthly.href);
    const annual = await checkout(page, 'annual');
    expect(annual.pathname).not.toBe(monthly.pathname);
    expect(annual.searchParams.get('line_items[0][price]')).toBe('price_year');
    expect(sql('SELECT bill_annually FROM members WHERE id=?', billingMember)[0].bill_annually).toBe(1);
});

test('approved student coupon is submitted to Stripe', async ({ page, billingMember }) => {
    sql("UPDATE members SET discount_type='student',discount_status='approved' WHERE id=?", billingMember);
    const url = await checkout(page);
    expect(url.searchParams.get('discounts[0][coupon]')).toBe('coupon_student');
});

test('active subscriber opens the real billing portal flow', async ({ page, billingMember }) => {
    sql(
        "UPDATE members SET stripe_customer_id=?,stripe_subscription_id=?,stripe_subscription_state='active' WHERE id=?",
        `cus_active_${billingMember}`,
        `sub_${billingMember}`,
        billingMember,
    );
    const url = await checkout(page);
    expect(url.hostname).toBe('billing.stripe.com');
    expect(Object.fromEntries(url.searchParams)).toEqual({ customer: `cus_active_${billingMember}`, return_url: 'http://127.0.0.1:8799' });
    expect(sql('SELECT payment_status FROM members WHERE id=?', billingMember)[0].payment_status).toBe('ActiveStripe');
});

for (const tracked of [true, false]) {
    test(`${tracked ? 'tracked' : 'newly discovered'} subscriber can open the portal without configured prices or coupon`, async ({ page, billingMember }) => {
        sql(
            "UPDATE members SET stripe_customer_id=?,stripe_subscription_id=?,stripe_subscription_state=?,discount_type='student',discount_status='approved' WHERE id=?",
            `cus_active_${billingMember}`,
            tracked ? `sub_${billingMember}` : null,
            tracked ? 'active' : null,
            billingMember,
        );
        sql("UPDATE settings SET data=json_set(data,'$.monthly_price_id','','$.yearly_price_id','','$.discounts',json('[]')) WHERE id=1");
        const url = await checkout(page);
        expect(url.hostname).toBe('billing.stripe.com');
        expect(Object.fromEntries(url.searchParams)).toEqual({ customer: `cus_active_${billingMember}`, return_url: 'http://127.0.0.1:8799' });
        expect(sql('SELECT stripe_subscription_id,payment_status FROM members WHERE id=?', billingMember)[0]).toEqual({
            stripe_subscription_id: `sub_${billingMember}`,
            payment_status: 'ActiveStripe',
        });
    });
}

test('missing membership price fails closed with an actionable error', async ({ page, billingMember }) => {
    sql("UPDATE settings SET data=json_set(data,'$.monthly_price_id','') WHERE id=1");
    await page.goto('/billing');
    const response = page.waitForResponse((response) => response.url().endsWith('/api/billing/checkout') && response.request().method() === 'POST');
    await page.getByRole('button', { name: 'Continue to secure billing' }).click();
    expect((await response).status()).toBe(400);
    await expect(page.getByRole('alert')).toContainText('Selected Stripe price is not configured');
    await expect(page).toHaveURL(/\/billing$/);
    expect(sql('SELECT stripe_customer_id FROM members WHERE id=?', billingMember)[0].stripe_customer_id).toBeNull();
});

test('existing customer without a subscription still needs a configured price', async ({ page, billingMember }) => {
    sql('UPDATE members SET stripe_customer_id=? WHERE id=?', `cus_member_${billingMember}`, billingMember);
    sql("UPDATE settings SET data=json_set(data,'$.monthly_price_id','') WHERE id=1");
    await page.goto('/billing');
    const response = page.waitForResponse((response) => response.url().endsWith('/api/billing/checkout') && response.request().method() === 'POST');
    await page.getByRole('button', { name: 'Continue to secure billing' }).click();
    expect((await response).status()).toBe(400);
    await expect(page.getByRole('alert')).toContainText('Selected Stripe price is not configured');
    await expect(page).toHaveURL(/\/billing$/);
    expect(sql('SELECT stripe_subscription_id,payment_status FROM members WHERE id=?', billingMember)[0]).toEqual({ stripe_subscription_id: null, payment_status: null });
});

for (const [configuration, update] of [
    ['empty coupon', "json_set(data,'$.discounts[0].coupon_id','')"],
    ['missing coupon', "json_remove(data,'$.discounts[0].coupon_id')"],
    ['removed discount', "json_remove(data,'$.discounts[0]')"],
]) {
    for (const cached of [false, true]) {
        test(`approved discount with ${configuration} blocks ${cached ? 'cached' : 'new'} checkout instead of charging full price`, async ({ page, billingMember }) => {
            sql("UPDATE members SET discount_type='student',discount_status='approved' WHERE id=?", billingMember);
            if (cached) expect((await checkout(page)).searchParams.get('discounts[0][coupon]')).toBe('coupon_student');
            sql(`UPDATE settings SET data=${update} WHERE id=1`);
            await page.goto('/billing');
            const frequency = cached ? 'monthly' : 'annual';
            await page.getByLabel('Billing frequency').selectOption(frequency);
            const response = page.waitForResponse((response) => response.url().endsWith('/api/billing/checkout') && response.request().method() === 'POST');
            await page.getByRole('button', { name: 'Continue to secure billing' }).click();
            expect((await response).status()).toBe(400);
            await expect(page.getByRole('alert')).toContainText('Your discount coupon is not configured');
            await expect(page).toHaveURL(/\/billing$/);
            await expect(page.getByLabel('Billing frequency')).toHaveValue(frequency);
            expect(sql('SELECT discount_type,discount_status,bill_annually,payment_status FROM members WHERE id=?', billingMember)[0]).toEqual({
                discount_type: 'student',
                discount_status: 'approved',
                bill_annually: 0,
                payment_status: null,
            });
        });
    }
}

test('coupon removed at Stripe is rejected without falling back to full price', async ({ page, billingMember }) => {
    sql("UPDATE members SET discount_type='student',discount_status='approved' WHERE id=?", billingMember);
    sql("UPDATE settings SET data=json_set(data,'$.discounts[0].coupon_id','coupon_removed') WHERE id=1");
    await page.goto('/billing');
    const response = page.waitForResponse((response) => response.url().endsWith('/api/billing/checkout') && response.request().method() === 'POST');
    await page.getByRole('button', { name: 'Continue to secure billing' }).click();
    expect((await response).status()).toBe(502);
    await expect(page.getByRole('alert')).toContainText('Stripe HTTP 404');
    await expect(page).toHaveURL(/\/billing$/);
    expect(sql('SELECT payment_status FROM members WHERE id=?', billingMember)[0].payment_status).toBeNull();
    sql("UPDATE settings SET data=json_set(data,'$.discounts[0].coupon_id','coupon_student') WHERE id=1");
    expect((await checkout(page)).searchParams.get('discounts[0][coupon]')).toBe('coupon_student');
});

test('unrelated leadership edits preserve an unchanged obsolete discount', async ({ page, billingMember, login }) => {
    sql("UPDATE members SET discount_type='obsolete',discount_status='approved',discount_request_id='original-request' WHERE id=?", billingMember);
    await login();
    await page.goto(`/admin/members/${billingMember}`);
    await page.getByText('Profile & discount fields', { exact: true }).click();
    await expect(page.getByLabel('Discount type', { exact: true })).toHaveValue('obsolete');
    await page.getByLabel('Leadership notes').fill('Updated without changing billing');
    const response = page.waitForResponse((response) => response.url().endsWith(`/api/admin/members/${billingMember}`) && response.request().method() === 'PATCH');
    await page.getByRole('button', { name: 'Save member', exact: true }).click();
    expect((await response).status()).toBe(200);
    await expect(page.locator('.global-notice')).toHaveText('Member saved.');
    expect(sql('SELECT admin_notes,discount_type,discount_status,discount_request_id FROM members WHERE id=?', billingMember)[0]).toEqual({
        admin_notes: 'Updated without changing billing',
        discount_type: 'obsolete',
        discount_status: 'approved',
        discount_request_id: 'original-request',
    });
    await page.reload();
    await page.getByText('Profile & discount fields', { exact: true }).click();
    await expect(page.getByLabel('Discount type', { exact: true })).toHaveValue('obsolete');
    await expect(page.getByLabel('Discount status', { exact: true })).toHaveValue('approved');
});

test('provider errors preserve the selection and allow retry', async ({ page, billingMember }) => {
    sql("UPDATE settings SET data=json_set(data,'$.yearly_price_id','price_error') WHERE id=1");
    await page.goto('/billing');
    await page.getByLabel('Billing frequency').selectOption('annual');
    await page.getByRole('button', { name: 'Continue to secure billing' }).click();
    await expect(page.getByRole('alert')).toContainText('Stripe HTTP 503');
    await expect(page.getByLabel('Billing frequency')).toHaveValue('annual');
    await expect(page.getByRole('button', { name: 'Continue to secure billing' })).toBeEnabled();
    expect(sql('SELECT payment_status FROM members WHERE id=?', billingMember)[0].payment_status).toBeNull();
});

test('no discount options leaves no misleading forms', async ({ page, billingMember }) => {
    sql("UPDATE settings SET data=json_set(data,'$.discounts',json('[]')) WHERE id=1");
    await page.goto('/billing');
    await expect(page.getByText('There are no discounts available to request at the moment.')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Request discount' })).toHaveCount(0);
});
