import { test, expect, sql } from './fixtures.mjs';

test('membership home is a slim single column with optional discount before payment and in-person fob help', async ({ page, login }) => {
    await login(2);
    await page.goto('/');
    await expect(page).toHaveURL(/\/billing$/);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Your membership');
    await expect(page.locator('.brand, footer, nav, header')).toHaveCount(0);
    await expect(page.getByText('Member workspace', { exact: true })).toHaveCount(0);
    const membership = page.locator('main .narrow');
    await expect(membership.locator('section')).toHaveCount(1);
    await expect(membership.locator('.grid, dl, table')).toHaveCount(0);
    await expect(page.getByRole('heading', { name: 'Finish setting up your membership' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Edit profile' })).toHaveAttribute('href', '/profile');
    await expect(page.getByRole('link', { name: 'Sign membership waiver' })).toHaveAttribute('href', '/waiver');
    await expect(page.getByText('Ask leadership for your key fob.', { exact: false })).toContainText('on-site kiosk and your phone');
    await expect(page.locator('a[href="/kiosk"], a[href="/fobs/bind"], a[href="/discounts"]')).toHaveCount(0);
    const discount = page.locator('details').filter({ hasText: 'Eligible for a discount?' });
    await expect(discount.locator('summary')).toBeVisible();
    await expect(discount).not.toHaveAttribute('open');
    await expect(page.getByLabel('Discount to request')).toBeHidden();
    await expect(page.getByRole('button', { name: 'Start membership payment' })).toBeEnabled();
    expect(
        await discount.evaluate((node) =>
            Boolean(node.compareDocumentPosition(document.querySelector('button[type="submit"]:not(details button)')) & Node.DOCUMENT_POSITION_FOLLOWING),
        ),
    ).toBe(true);
    const box = await membership.boundingBox();
    expect(box.width).toBeLessThanOrEqual(740);
    await expect(page.locator('a[href="/dashboard"], .step-list')).toHaveCount(0);
    await expect(page.getByRole('heading', { name: 'Your next steps' })).toHaveCount(0);

    for (const path of ['/dashboard', '/discounts']) {
        await page.goto(path);
        await expect(page).toHaveURL(new RegExp(path + '$'));
        await expect(page.getByRole('heading', { level: 1 })).toHaveText('Page not found');
        await expect(page.locator('a[href="/dashboard"], a[href="/discounts"]')).toHaveCount(0);
        await expect(page.getByRole('button', { name: 'Request discount' })).toHaveCount(0);
    }
    await page.getByRole('link', { name: 'Go home' }).click();
    await expect(page).toHaveURL(/\/billing$/);
});

test('profile persists name without directory fields', async ({ page, login }) => {
    await login();
    await page.getByRole('link', { name: 'Edit profile', exact: true }).click();
    await page.getByLabel('Name', { exact: true }).fill('Alex Updated');
    await expect(page.locator('[name="pronouns"],[name="bio"],[name="directory_hidden"]')).toHaveCount(0);
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect(page.getByRole('status')).toContainText('Your profile has been saved.');
    await page.reload();
    await expect(page.getByLabel('Name', { exact: true })).toHaveValue('Alex Updated');
    await page.goto('/admin/members/2');
    await expect(page.getByRole('button', { name: 'Save member', exact: true })).toBeVisible();
    await expect(page.locator('[name="pronouns"],[name="bio"],[name="directory_hidden"]')).toHaveCount(0);
});

test('waiver requires every consent, records a signature and returns to membership payment', async ({ page, login }) => {
    await login(2);
    await page.getByRole('link', { name: 'Sign membership waiver' }).click();
    await expect(page.getByRole('region', { name: 'Full waiver text' })).toContainText('<script>');
    await page.getByRole('button', { name: 'Sign waiver' }).click();
    expect(sql('SELECT * FROM waivers')).toEqual([]);
    await page.getByLabel('I accept the risks.').check();
    await page.getByRole('button', { name: 'Sign waiver' }).click();
    expect(sql('SELECT * FROM waivers')).toEqual([]);
    await page.getByLabel('I will follow safety rules.').check();
    await page.getByLabel('Full legal name').fill('Sam Legal Name');
    await page.getByRole('button', { name: 'Sign waiver' }).click();
    await expect(page).toHaveURL(/\/billing$/);
    await expect(page.getByText('Your signed waiver is on file.', { exact: true })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Sign membership waiver' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Start membership payment' })).toBeEnabled();
    expect(sql('SELECT access_status FROM members WHERE id=2')[0].access_status).toBe('PaymentInactive');
    expect(sql('SELECT version,name,email FROM waivers')).toEqual([{ version: 1, name: 'Sam Legal Name', email: 'sam@example.test' }]);
    await page.goto('/waiver');
    await expect(page.getByText('A signed waiver is already on file', { exact: false })).toBeVisible();
    await page.getByLabel('I accept the risks.').check();
    await page.getByLabel('I will follow safety rules.').check();
    await page.getByRole('button', { name: 'Sign waiver' }).click();
    await expect(page).toHaveURL(/\/billing$/);
    expect(sql('SELECT * FROM waivers')).toHaveLength(1);
});

test('unpublished and changed waivers cannot be signed', async ({ page, login }) => {
    await login(2);
    sql("UPDATE settings SET data=json_set(data,'$.waiver_version',0)");
    await page.goto('/waiver');
    await expect(page.getByText('A waiver has not been published yet.', { exact: false })).toBeVisible();
    await expect(page.locator('main form')).toHaveCount(0);
    sql("UPDATE settings SET data=json_set(data,'$.waiver_version',1)");
    await page.reload();
    await page.getByLabel('I accept the risks.').check();
    await page.getByLabel('I will follow safety rules.').check();
    sql("UPDATE settings SET data=json_set(data,'$.waiver_version',2)");
    await page.getByRole('button', { name: 'Sign waiver' }).click();
    await expect(page.getByRole('alert')).toContainText('Waiver changed');
    expect(sql('SELECT * FROM waivers')).toEqual([]);
});

test('discount request, leadership family approval and explicit removal', async ({ page, login }) => {
    await login(2);
    await page.locator('summary').filter({ hasText: 'Eligible for a discount?' }).click();
    await page.getByLabel('Discount to request').selectOption('family');
    await page.getByRole('button', { name: 'Request discount' }).click();
    await expect(page.getByText('Family request pending.', { exact: false })).toContainText('in person');
    await expect(page.getByRole('button', { name: 'Start membership payment' })).toHaveCount(0);
    await expect(page.getByLabel('Discount to request')).toHaveCount(0);
    await login();
    await page.goto('/admin/members/2');
    await page.getByLabel('I have reviewed eligibility').check();
    await page.getByRole('button', { name: 'Approve request' }).click();
    expect(sql('SELECT discount_status FROM members WHERE id=2')[0].discount_status).toBe('requested');
    await page.getByLabel('Family root member ID (required').fill('1');
    await page.getByRole('button', { name: 'Approve request' }).click();
    await expect(page.getByText('Discount request approved.')).toBeVisible();
    expect(sql('SELECT discount_status,root_family_member FROM members WHERE id=2')[0]).toEqual({ discount_status: 'approved', root_family_member: 1 });
    await login(2);
    await page.goto('/billing');
    await expect(page.getByText('Family approved.', { exact: false })).toContainText('included when you start payment');
    await page.locator('summary').filter({ hasText: 'No longer eligible for this discount?' }).click();
    await page.getByRole('button', { name: 'Remove discount' }).click();
    expect(sql('SELECT discount_type FROM members WHERE id=2')[0].discount_type).toBe('family');
    await page.getByLabel('I understand removing this discount').check();
    await page.getByRole('button', { name: 'Remove discount' }).click();
    await expect(page.getByText('Your discount has been removed.')).toBeVisible();
    // Family relationships are managed separately by leadership.
    expect(sql('SELECT discount_type,discount_status,root_family_member FROM members WHERE id=2')[0]).toEqual({
        discount_type: null,
        discount_status: null,
        root_family_member: 1,
    });
});

for (const width of [1280, 390]) {
    test(`pending discount survives reload and withdrawal restores standard payment at ${width}px`, async ({ page, login }) => {
        await page.setViewportSize({ width, height: 844 });
        await login(2);
        await page.getByRole('link', { name: 'Sign membership waiver' }).click();
        await page.getByLabel('I accept the risks.').check();
        await page.getByLabel('I will follow safety rules.').check();
        await page.getByRole('button', { name: 'Sign waiver' }).click();
        await expect(page.getByRole('heading', { level: 1 })).toHaveText('Your membership');
        await page.locator('summary').filter({ hasText: 'Eligible for a discount?' }).click();
        await page.getByLabel('Discount to request').selectOption('student');
        await page.getByRole('button', { name: 'Request discount' }).click();
        await expect(page.getByText('Student request pending.', { exact: false })).toBeVisible();
        await page.reload();
        await expect(page.getByRole('heading', { name: 'Visit us Tuesday night', exact: true })).toBeVisible();
        await expect(page.getByText('Wait for approval before paying.', { exact: false })).toBeVisible();
        await expect(page.getByRole('button', { name: 'Start membership payment' })).toHaveCount(0);
        await expect(page.locator('details, nav, a[href^="/admin"]')).toHaveCount(0);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
        const session = await (await page.request.get('/api/session')).json();
        const blocked = await page.request.post('/api/billing/checkout', {
            headers: { Origin: 'http://127.0.0.1:8799', 'X-CSRF-Token': session.csrf_token },
            data: {},
        });
        expect(blocked.status()).toBe(409);
        expect(await blocked.text()).toContain('pending approval');
        expect(sql('SELECT discount_status,stripe_customer_id,payment_status FROM members WHERE id=2')[0]).toEqual({
            discount_status: 'requested',
            stripe_customer_id: null,
            payment_status: null,
        });
        await page.getByRole('button', { name: 'Withdraw discount request' }).click();
        await expect(page.getByText('Discount request withdrawn. You can now pay the standard membership price.')).toBeVisible();
        await expect(page.getByRole('button', { name: 'Start membership payment' })).toBeEnabled();
        await expect(page.getByRole('button', { name: 'Withdraw discount request' })).toHaveCount(0);
        await expect(page.locator('details')).not.toHaveAttribute('open');
        expect(sql('SELECT discount_type,discount_status,discount_request_id FROM members WHERE id=2')[0]).toEqual({
            discount_type: null,
            discount_status: null,
            discount_request_id: null,
        });
        await page.reload();
        await expect(page.getByRole('button', { name: 'Start membership payment' })).toBeEnabled();
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    });

    test(`active member sees only membership essentials at ${width}px`, async ({ page, login }) => {
        sql("INSERT INTO waivers(version,name,email) VALUES(1,'Sam Member','sam@example.test')");
        sql("UPDATE members SET fob_id=202,stripe_customer_id='cus_active_2',stripe_subscription_id='sub_2',stripe_subscription_state='active' WHERE id=2");
        await page.setViewportSize({ width, height: 844 });
        await login(2);
        await expect(page.getByRole('heading', { name: 'Your membership is active', exact: true })).toBeVisible();
        await expect(page.getByText('Your key fob is ready to use at the space.')).toBeVisible();
        await expect(page.getByRole('button', { name: 'Manage billing', exact: true })).toBeEnabled();
        await expect(page.locator('main section')).toHaveCount(1);
        await expect(page.locator('details, dl, table, nav, a[href^="/admin"], a[href="/waiver"], a[href="/kiosk"]')).toHaveCount(0);
        await expect(page.getByRole('heading', { name: /Tuesday|Finish setting up|Building access/ })).toHaveCount(0);
        await expect(page.getByRole('button', { name: /Request discount|Start membership payment|Withdraw discount/ })).toHaveCount(0);
        await expect(page.getByRole('link', { name: 'Edit profile', exact: true })).toBeVisible();
        await expect(page.getByRole('button', { name: 'Sign out', exact: true })).toBeVisible();
        expect(sql('SELECT access_status FROM members WHERE id=2')[0].access_status).toBe('Ready');
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
        const content = await page.locator('main .narrow').boundingBox();
        const account = await page.locator('.account-actions').boundingBox();
        expect(content.width).toBeLessThanOrEqual(740);
        expect(account.y).toBeGreaterThanOrEqual(content.y + content.height);
    });
}

test('expired sessions and failed saves preserve drafts; retry reloads real data', async ({ page, login }) => {
    await login(2);
    await page.goto('/profile');
    await page.getByLabel('Name', { exact: true }).fill('Keep this draft');
    sql('UPDATE sessions SET expires=0');
    await page.getByRole('button', { name: 'Save profile' }).click();
    await expect(page.getByRole('alert')).toContainText('Session expired');
    await expect(page.getByLabel('Name', { exact: true })).toHaveValue('Keep this draft');
    expect(sql('SELECT name FROM members WHERE id=2')[0].name).toBe('Sam Member');
    await page.goto('/profile');
    await expect(page.locator('main').getByRole('link', { name: 'Continue with Discord' })).toBeVisible();
    await login();
    await page.goto('/admin/members/999');
    await expect(page.getByText('Member not found', { exact: true })).toBeVisible();
    sql("INSERT INTO members(id,email,name) VALUES(999,'retry@example.test','Retry Member')");
    await page.getByRole('button', { name: 'Try again' }).click();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Member #999');
});

test('real SPA history, security policies and unknown routes', async ({ page, login }) => {
    await login();
    const response = await page.goto('/profile');
    expect(response.headers()['content-security-policy']).toContain("style-src 'self'");
    expect(response.headers()['content-security-policy']).not.toContain('unsafe-inline');
    expect(response.headers()['referrer-policy']).toBe('no-referrer');
    expect(response.headers()['x-content-type-options']).toBe('nosniff');
    await page.getByRole('link', { name: 'Your membership', exact: true }).click();
    await expect(page).toHaveURL(/\/billing$/);
    await page.goBack();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Your profile');
    await page.goForward();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Your membership');
    await expect(page.locator('a[href="/directory"],member-card')).toHaveCount(0);
    await page.goto('/directory');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Page not found');
    await page.goto('/does-not-exist');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Page not found');
});

for (const [path, title] of [
    ['/profile', 'Your profile'],
    ['/waiver', 'Membership waiver'],
    ['/billing', 'Your membership'],
    ['/kiosk', 'Scan a key fob'],
    ['/fobs/bind', 'Link your fob'],
    ['/admin/members', 'Members'],
    ['/admin/members/new', 'Add a member'],
    ['/admin/members/2', 'sam'],
    ['/admin/config', 'Space configuration'],
    ['/admin/waiver', 'Waiver publishing'],
    ['/admin/events', 'Audit log'],
    ['/admin/swipes', 'Fob swipes'],
    ['/admin/jobs', 'Background jobs'],
]) {
    test(`mobile deep link ${path} loads with labeled controls and no overflow`, async ({ page, login }) => {
        await login();
        await page.setViewportSize({ width: 390, height: 844 });
        await page.goto(path);
        await expect(page.getByRole('heading', { level: 1 })).toHaveText(title);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), path).toBe(true);
        const labeled = await page.locator('input,select,textarea').evaluateAll((nodes) => nodes.every((node) => node.labels.length || node.getAttribute('aria-label')?.trim()));
        expect(labeled, path).toBe(true);
        await expect(page.locator('[style],style,script:not([src])')).toHaveCount(0);
    });
}
