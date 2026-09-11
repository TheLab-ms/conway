import { test, expect, sql } from './fixtures.mjs';

test('billing is the member home with access status and essential links, without a dashboard', async ({ page, login }) => {
    await login(2);
    await page.goto('/');
    await expect(page).toHaveURL(/\/billing$/);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Membership & billing');
    await expect(page.locator('.brand, footer')).toHaveCount(0);
    await expect(page.getByText('Member workspace', { exact: true })).toHaveCount(0);
    await expect(page.getByRole('link', { name: 'Billing', exact: true })).toHaveAttribute('aria-current', 'page');
    const access = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Building access', exact: true }) });
    await expect(access.getByText('MissingWaiver', { exact: true })).toBeVisible();
    await expect(access.locator('dd').first()).toHaveText('Not linked');
    await expect(access.locator('dd').nth(1)).toHaveText('Never');
    await expect(page.getByRole('link', { name: 'Edit profile' })).toHaveAttribute('href', '/profile');
    await expect(access.getByRole('link', { name: 'Review waiver' })).toHaveAttribute('href', '/waiver');
    await expect(access.getByRole('link', { name: 'Enroll an access fob' })).toHaveAttribute('href', '/kiosk');
    await expect(page.locator('a[href="/dashboard"], .step-list')).toHaveCount(0);
    await expect(page.getByRole('heading', { name: 'Your next steps' })).toHaveCount(0);

    sql('UPDATE members SET non_billable=1,fob_id=202,fob_last_seen=1700000000 WHERE id=2');
    await page.reload();
    await expect(access.getByText('Ready', { exact: true })).toBeVisible();
    await expect(access.locator('dd').first()).toHaveText('202');
    const lastSeen = await page.evaluate(() => new Date(1700000000000).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }));
    await expect(access.locator('dd').nth(1)).toHaveText(lastSeen);

    await page.goto('/dashboard');
    await expect(page).toHaveURL(/\/dashboard$/);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Page not found');
    await expect(page.getByRole('heading', { name: 'Building access' })).toHaveCount(0);
    await expect(page.locator('a[href="/dashboard"]')).toHaveCount(0);
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

test('waiver requires every consent, records a signature and updates billing access status', async ({ page, login }) => {
    await login(2);
    await expect(page.getByText('MissingWaiver', { exact: true })).toBeVisible();
    await page.getByRole('link', { name: 'Review waiver' }).click();
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
    await expect(page.getByText('PaymentInactive', { exact: true })).toBeVisible();
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
    await page.goto('/discounts');
    await page.getByLabel('Discount to request').selectOption('family');
    await page.getByRole('button', { name: 'Request discount' }).click();
    await expect(page.getByText('Your family request is waiting for review.', { exact: false })).toBeVisible();
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
    await expect(page.getByText('Linked to member #1')).toBeVisible();
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
    await page.getByRole('link', { name: 'Billing', exact: true }).click();
    await expect(page).toHaveURL(/\/billing$/);
    await page.goBack();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Your profile');
    await page.goForward();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Membership & billing');
    await expect(page.locator('a[href="/directory"],member-card')).toHaveCount(0);
    await page.goto('/directory');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Page not found');
    await page.goto('/does-not-exist');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Page not found');
});

for (const [path, title] of [
    ['/profile', 'Your profile'],
    ['/waiver', 'Membership waiver'],
    ['/billing', 'Membership & billing'],
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
