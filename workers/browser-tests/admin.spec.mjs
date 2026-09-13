import { test, expect, sql } from './fixtures.mjs';

const field = (page, name) => page.locator(`[name="${name}"]`);
const button = (page, name) => page.getByRole('button', { name, exact: true });
const rows = (page) => page.locator('tbody tr');
async function submit(page, label, method, status = 200) {
    const response = page.waitForResponse((r) => r.url().includes('/api/admin/') && r.request().method() === method);
    await button(page, label).click();
    expect((await response).status()).toBe(status);
}

test('member CRUD persists nullable fields and both checkbox values', async ({ page, login }) => {
    await login();
    await page.goto('/admin');
    await page.getByRole('link', { name: 'Add member', exact: true }).click();
    const values = {
        name: 'Casey Maker',
        email: 'casey@example.test',
        name_override: 'Casey C',
        discord_user_id: '423456789012345678',
        fob_id: '404',
        root_family_member: '1',
        admin_notes: 'Tour complete',
    };
    for (const [name, value] of Object.entries(values)) await field(page, name).fill(value);
    for (const name of ['confirmed', 'leadership', 'non_billable', 'bill_annually']) await field(page, name).check();
    await submit(page, 'Create member', 'POST', 201);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(values.discord_user_id);
    const { id } = sql('SELECT id FROM members WHERE email=?', values.email)[0];
    await page.getByText('Discount settings', { exact: true }).click();
    await field(page, 'discount_type').selectOption('student');
    await field(page, 'discount_status').selectOption('approved');
    await submit(page, 'Save member', 'PATCH');
    await expect(page.locator('.global-notice')).toHaveText('Member saved.');
    await page.reload();
    await page.getByText('Discount settings', { exact: true }).click();
    for (const [name, value] of Object.entries(values)) await expect(field(page, name)).toHaveValue(value);
    const flags = ['confirmed', 'leadership', 'non_billable', 'bill_annually'];
    for (const name of flags) await expect(field(page, name)).toBeChecked();
    expect(sql('SELECT * FROM members WHERE id=?', id)[0]).toMatchObject({
        discount_type: 'student',
        discount_status: 'approved',
        ...Object.fromEntries(flags.map((name) => [name, 1])),
    });
    const nullable = ['name_override', 'discord_user_id', 'fob_id', 'root_family_member', 'discount_type', 'discount_status'];
    for (const name of nullable) {
        if (name.startsWith('discount_')) await field(page, name).selectOption('');
        else await field(page, name).fill('');
    }
    for (const name of flags) await field(page, name).uncheck();
    await submit(page, 'Save member', 'PATCH');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(`Member #${id}`);
    expect(sql('SELECT * FROM members WHERE id=?', id)[0]).toMatchObject({
        ...Object.fromEntries(nullable.map((name) => [name, null])),
        ...Object.fromEntries(flags.map((name) => [name, 0])),
    });
    await page.reload();
    await page.getByText('Discount settings', { exact: true }).click();
    for (const name of nullable) await expect(field(page, name)).toHaveValue('');
    for (const name of flags) await expect(field(page, name)).not.toBeChecked();
    await field(page, 'confirmation').fill(String(id));
    await submit(page, 'Delete member permanently', 'DELETE');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Members');
    expect(sql('SELECT id FROM members WHERE id=?', id)).toEqual([]);
    await expect(page.locator(`tbody a[href="/admin/members/${id}"]`)).toHaveCount(0);
});

for (const [username, discordId, label] of [
    ['sam', '223456789012345678', 'sam'],
    [null, '223456789012345678', '223456789012345678'],
    [null, null, 'Member #2'],
])
    test(`admin member identity uses ${label} instead of billing names`, async ({ page, login }) => {
        sql('UPDATE members SET discord_username=?,discord_user_id=?,name_override=? WHERE id=2', username, discordId, 'Override Name');
        await login();
        await page.goto('/admin/members');
        const memberLink = page.locator('tbody a[href="/admin/members/2"]');
        await expect(memberLink).toHaveText(label);
        const row = rows(page).filter({ has: page.locator('a[href="/admin/members/2"]') });
        for (const text of ['Sam Member', 'sam@example.test', 'Override Name']) await expect(row).not.toContainText(text);
        await memberLink.click();
        await expect(page.getByRole('heading', { level: 1 })).toHaveText(label);
        await expect(field(page, 'discord_user_id')).toHaveValue(discordId || '');
    });

test('billing information stays below Discord identity and saves with existing validation', async ({ page, login }) => {
    await login();
    await page.goto('/admin/members/2');
    const billing = page.getByRole('region', { name: 'Billing information', exact: true });
    await expect(billing.getByRole('heading', { name: 'Billing information', exact: true })).toBeVisible();
    await expect(billing.getByLabel('Full name', { exact: true })).toHaveValue('Sam Member');
    await expect(billing.getByLabel('Billing email', { exact: true })).toHaveValue('sam@example.test');
    await expect(billing.locator('[name="name"]')).toHaveAttribute('required', '');
    await expect(billing.locator('[name="name"]')).toHaveAttribute('maxlength', '200');
    await expect(billing.locator('[name="email"]')).toHaveAttribute('required', '');
    await expect(billing.locator('[name="email"]')).toHaveAttribute('type', 'email');
    await expect(billing.locator('[name="email"]')).toHaveAttribute('maxlength', '254');
    await expect(field(page, 'discord_user_id')).toHaveAttribute('pattern', '[0-9]{5,25}');
    expect(
        await billing.evaluate((section) => Boolean(document.querySelector('[name="discord_user_id"]').compareDocumentPosition(section) & Node.DOCUMENT_POSITION_FOLLOWING)),
    ).toBe(true);
    await billing.getByLabel('Full name', { exact: true }).fill('Sam Billing');
    await billing.getByLabel('Billing email', { exact: true }).fill('billing@example.test');
    await billing.getByLabel('Non-billable membership', { exact: true }).check();
    await billing.getByLabel('Annual billing', { exact: true }).check();
    await submit(page, 'Save member', 'PATCH');
    await expect(page.locator('.global-notice')).toHaveText('Member saved.');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('sam');
    expect(sql('SELECT name,email,discord_user_id,non_billable,bill_annually FROM members WHERE id=2')[0]).toEqual({
        name: 'Sam Billing',
        email: 'billing@example.test',
        discord_user_id: '223456789012345678',
        non_billable: 1,
        bill_annually: 1,
    });
});

test('duplicate email, Discord identity, and fob are rejected without overwriting records', async ({ page, login }) => {
    await login();
    await page.goto('/admin/members/new');
    await field(page, 'name').fill('Duplicate');
    await field(page, 'email').fill('ALEX@example.test');
    await submit(page, 'Create member', 'POST', 409);
    await expect(page.getByRole('alert')).toContainText('Conflicting member identity');
    expect(sql('SELECT count(*) AS n FROM members')[0].n).toBe(3);
    const before = sql('SELECT * FROM members WHERE id=2')[0];
    for (const [name, value] of [
        ['email', 'alex@example.test'],
        ['discord_user_id', '123456789012345678'],
        ['fob_id', '101'],
    ]) {
        await page.goto('/admin/members/2');
        await field(page, name).fill(value);
        await submit(page, 'Save member', 'PATCH', 409);
        await expect(page.getByRole('alert')).toContainText('Conflicting member identity');
        await expect(field(page, name)).toHaveValue(value);
        expect(sql('SELECT * FROM members WHERE id=2')[0]).toEqual(before);
    }
});

for (const method of ['PATCH', 'DELETE'])
    test(`stale ${method} preserves a concurrent member edit`, async ({ page, login }) => {
        await login();
        await page.goto('/admin/members/2');
        await field(page, 'admin_notes').fill('Unsaved local draft');
        sql("UPDATE members SET name='Sam Updated',admin_notes='Other leader edit' WHERE id=2");
        const current = sql('SELECT * FROM members WHERE id=2')[0];
        if (method === 'DELETE') await field(page, 'confirmation').fill('2');
        await submit(page, method === 'PATCH' ? 'Save member' : 'Delete member permanently', method, 409);
        await expect(page.getByRole('alert')).toContainText(/changed; reload and retry/);
        await expect(field(page, 'admin_notes')).toHaveValue('Unsaved local draft');
        expect(sql('SELECT * FROM members WHERE id=2')[0]).toEqual(current);
        expect(sql("SELECT id FROM member_events WHERE event IN ('AdminMemberUpdated','MemberDeleted')")).toEqual([]);
        await page.reload();
        await expect(field(page, 'admin_notes')).toHaveValue('Other leader edit');
    });

test('last linked leader cannot be demoted, unlinked, or deleted', async ({ page, login }) => {
    sql('UPDATE members SET leadership=0 WHERE id=3');
    await login();
    for (const action of ['demote', 'unlink', 'delete']) {
        await page.goto('/admin/members/1');
        if (action === 'demote') await field(page, 'leadership').uncheck();
        if (action === 'unlink') await field(page, 'discord_user_id').fill('');
        if (action === 'delete') await field(page, 'confirmation').fill('1');
        await submit(page, action === 'delete' ? 'Delete member permanently' : 'Save member', action === 'delete' ? 'DELETE' : 'PATCH', 409);
        await expect(page.getByRole('alert')).toContainText('last leadership account');
        expect(sql('SELECT leadership,discord_user_id FROM members WHERE id=1')[0]).toEqual({ leadership: 1, discord_user_id: '123456789012345678' });
        expect(sql('SELECT count(*) AS n FROM sessions WHERE member=1')[0].n).toBe(1);
    }
});

test('family roots and Stripe subscription history cannot be deleted', async ({ page, login }) => {
    sql('UPDATE members SET root_family_member=3 WHERE id=2');
    await login();
    await page.goto('/admin/members/3');
    await field(page, 'confirmation').fill('3');
    await submit(page, 'Delete member permanently', 'DELETE', 409);
    await expect(page.getByRole('alert')).toContainText('Reassign family dependents');
    expect(sql('SELECT root_family_member FROM members WHERE id=2')[0].root_family_member).toBe(3);
    // Local cancellation is not proof that the provider has no subscription history.
    sql("UPDATE members SET stripe_customer_id='cus_active_2',stripe_subscription_id='sub_2',stripe_subscription_state='canceled' WHERE id=2");
    await page.goto('/admin/members/2');
    await field(page, 'confirmation').fill('2');
    await submit(page, 'Delete member permanently', 'DELETE', 409);
    await expect(page.getByRole('alert')).toContainText('Stripe subscription history');
    expect(sql('SELECT stripe_subscription_id FROM members WHERE id=2')[0].stripe_subscription_id).toBe('sub_2');
    expect(sql('SELECT id FROM members WHERE id=3')).toHaveLength(1);
});

test('identity relink revokes all target sessions but keeps the editing leader signed in', async ({ page, login }) => {
    for (const token of ['sam-session-one', 'sam-session-two']) sql('INSERT INTO sessions(token_hash,member,csrf_token,expires) VALUES(?,2,?,unixepoch()+3600)', token, token);
    await login();
    await page.goto('/admin/members/2');
    await field(page, 'discord_user_id').fill('523456789012345678');
    await submit(page, 'Save member', 'PATCH');
    await expect(page.locator('.global-notice')).toHaveText('Member saved.');
    expect(sql('SELECT * FROM sessions WHERE member=2')).toEqual([]);
    expect(sql('SELECT * FROM sessions WHERE member=1')).toHaveLength(1);
    await page.reload();
    await expect(field(page, 'discord_user_id')).toHaveValue('523456789012345678');
});

test('member search, all six access statuses, and 50-row pagination', async ({ page, login }) => {
    sql("INSERT INTO waivers(id,version,name,email) VALUES(1,1,'Signed','signed@example.test')");
    const statuses = ['Ready', 'UnconfirmedEmail', 'MissingWaiver', 'PaymentInactive', 'MissingKeyFob', 'FamilyInactive'];
    for (let i = 0; i < 56; i++) {
        const status = statuses[i % 6];
        sql(
            'INSERT INTO members(email,name,confirmed,waiver,non_billable,fob_id,root_family_member) VALUES(?,?,?,?,?,?,?)',
            `filter${i}@example.test`,
            `Filter ${String(i).padStart(2, '0')}`,
            status === 'UnconfirmedEmail' ? 0 : 1,
            ['UnconfirmedEmail', 'MissingWaiver'].includes(status) ? null : 1,
            ['Ready', 'MissingKeyFob', 'FamilyInactive'].includes(status) ? 1 : 0,
            status === 'MissingKeyFob' ? null : 1000 + i,
            status === 'FamilyInactive' ? 2 : null,
        );
    }
    await login();
    await page.goto('/admin/members');
    await expect(rows(page)).toHaveCount(50);
    await expect(page.locator('.pagination')).toContainText('1-50 of 59');
    await expect(button(page, 'Previous')).toBeDisabled();
    const first = await rows(page).first().innerText();
    await button(page, 'Next').click();
    await expect(rows(page)).toHaveCount(9);
    await expect(page.locator('.pagination')).toContainText('51-59 of 59');
    await expect(button(page, 'Next')).toBeDisabled();
    await button(page, 'Previous').click();
    await expect(rows(page).first()).toHaveText(first, { useInnerText: true });
    for (const status of statuses) {
        await field(page, 'search').fill('Filter');
        await field(page, 'status').selectOption(status);
        await button(page, 'Apply filters').click();
        const count = sql("SELECT count(*) AS n FROM members WHERE name LIKE 'Filter%' AND access_status=?", status)[0].n;
        expect(count).toBeGreaterThan(0);
        await expect(rows(page)).toHaveCount(count);
        await expect(page.locator('tbody tr td:nth-child(2)')).toHaveText(Array(count).fill(status));
    }
    for (const search of ['sAm MeMbEr', 'sam@example.test', '223456789012345678']) {
        await field(page, 'status').selectOption('');
        await field(page, 'search').fill(search);
        await button(page, 'Apply filters').click();
        await expect(rows(page)).toHaveCount(1);
        await expect(rows(page).getByRole('link', { name: 'sam', exact: true })).toBeVisible();
    }
    await field(page, 'search').fill('no-such-member');
    await button(page, 'Apply filters').click();
    await expect(page.getByText('No members match these filters.', { exact: false })).toBeVisible();
});

test('CSV download includes all members, escapes quotes and neutralizes formulas', async ({ page, login }) => {
    sql('UPDATE members SET name=?,admin_notes=? WHERE id=2', '=SUM(1,2) "Sam"', 'private-note-not-for-export');
    await login();
    await page.goto('/admin/members?search=Robin');
    await expect(rows(page)).toHaveCount(1);
    const downloading = page.waitForEvent('download');
    await page.getByRole('link', { name: 'Export CSV', exact: true }).click();
    const download = await downloading;
    expect(download.suggestedFilename()).toBe('conway-members.csv');
    const chunks = [];
    for await (const chunk of await download.createReadStream()) chunks.push(chunk);
    const csv = Buffer.concat(chunks).toString('utf8');
    expect(csv.split('\r\n')).toHaveLength(4);
    expect(csv.split('\r\n')[0]).toBe('id,name,email,discord_user_id,payment_status,access_status,fob_id,root_family_member,discount_type');
    expect(csv).toContain('"2","\'=SUM(1,2) ""Sam""","sam@example.test"');
    for (const name of ['Alex Maker', 'Robin Leader']) expect(csv).toContain(name);
    expect(csv).not.toContain('private-note-not-for-export');
    expect(await download.failure()).toBeNull();
});

test('configuration persists prices and discount options', async ({ page, login }) => {
    await login();
    await page.goto('/admin/config');
    const values = {
        site_name: 'Maker Workshop',
        monthly_price_id: 'price_new_month',
        yearly_price_id: 'price_new_year',
        discounts: 'student | Learner | coupon_new\nfamily | Household |',
    };
    for (const [name, value] of Object.entries(values)) await field(page, name).fill(value);
    await submit(page, 'Save configuration', 'PUT');
    await expect(page.locator('.global-notice')).toHaveText('Configuration saved.');
    await page.reload();
    for (const [name, value] of Object.entries(values)) await expect(field(page, name)).toHaveValue(name === 'discounts' ? value + ' ' : value);
    const stored = JSON.parse(sql('SELECT data FROM settings')[0].data);
    expect(stored).toMatchObject({
        discounts: [
            { id: 'student', label: 'Learner', coupon_id: 'coupon_new' },
            { id: 'family', label: 'Household', coupon_id: '' },
        ],
    });
    expect(stored.site_name).toBe('Maker Workshop');
    expect(stored.monthly_price_id).toBe('price_new_month');
    expect(stored.yearly_price_id).toBe('price_new_year');
});

test('malformed option rows and invalid prices preserve configuration drafts', async ({ page, login }) => {
    await login();
    await page.goto('/admin/config');
    const before = sql('SELECT * FROM settings');
    for (const [name, value, message] of [['discounts', 'student | Missing column', 'Discount options, line 1']]) {
        const original = await field(page, name).inputValue();
        await field(page, name).fill(value);
        await button(page, 'Save configuration').click();
        await expect(page.getByRole('alert')).toContainText(message);
        await expect(field(page, name)).toHaveValue(value);
        expect(sql('SELECT * FROM settings')).toEqual(before);
        await field(page, name).fill(original);
    }
    await field(page, 'monthly_price_id').fill('not-a-price');
    await submit(page, 'Save configuration', 'PUT', 400);
    await expect(page.getByRole('alert')).toContainText('Invalid monthly_price_id');
    expect(sql('SELECT * FROM settings')).toEqual(before);
});

test('configuration 409 keeps draft and shows conflict notice', async ({ page, login }) => {
    await login();
    await page.goto('/admin/config');
    await field(page, 'site_name').fill('My unsaved draft');
    const other = await page.context().newPage();
    await other.goto('/admin/config');
    await field(other, 'site_name').fill('Other leader saved');
    await submit(other, 'Save configuration', 'PUT');
    await expect(other.locator('.global-notice')).toHaveText('Configuration saved.');
    await submit(page, 'Save configuration', 'PUT', 409);
    await expect(page.getByRole('alert')).toContainText('Your edits are still here.');
    await expect(field(page, 'site_name')).toHaveValue('My unsaved draft');
    expect(JSON.parse(sql('SELECT data FROM settings')[0].data).site_name).toBe('Other leader saved');
    await page.reload();
    await expect(field(page, 'site_name')).toHaveValue('Other leader saved');
    await field(page, 'site_name').fill('Merged configuration');
    await submit(page, 'Save configuration', 'PUT');
    await expect(page.locator('.global-notice')).toHaveText('Configuration saved.');
    expect(sql('SELECT version FROM settings')[0].version).toBe(3);
    await other.close();
});

test('waiver publication validates consent and agreements and retains signed versions', async ({ page, login }) => {
    sql("INSERT INTO waivers(version,name,email) VALUES(1,'Sam Member','sam@example.test')");
    const old = sql('SELECT * FROM waiver_versions');
    const signature = sql('SELECT * FROM waivers');
    await login();
    await page.goto('/admin/waiver');
    await field(page, 'content').fill('New safe-use terms. <b>Plain text</b>');
    await button(page, 'Publish new waiver version').click();
    await expect(field(page, 'reviewed')).not.toBeChecked();
    expect(sql('SELECT * FROM waiver_versions')).toEqual(old);
    await field(page, 'reviewed').check();
    for (const agreements of ['   ', Array(31).fill('Agreement').join('\n'), 'x'.repeat(2001)]) {
        await field(page, 'agreements').fill(agreements);
        await button(page, 'Publish new waiver version').click();
        await expect(page.getByRole('alert')).toContainText('Provide 1 to 30 agreements');
        expect(sql('SELECT * FROM waiver_versions')).toEqual(old);
    }
    await field(page, 'agreements').fill('I accept.\nI follow the rules.');
    await field(page, 'content').fill('   ');
    await submit(page, 'Publish new waiver version', 'PUT', 400);
    await expect(page.getByRole('alert')).toContainText('Invalid waiver content');
    expect(sql('SELECT * FROM waiver_versions')).toEqual(old);
    await field(page, 'content').fill('New safe-use terms. <b>Plain text</b>');
    await submit(page, 'Publish new waiver version', 'PUT', 201);
    await expect(page.getByRole('heading', { name: 'Current version / 2', exact: true })).toBeVisible();
    await page.reload();
    await expect(field(page, 'content')).toHaveValue('New safe-use terms. <b>Plain text</b>');
    await expect(field(page, 'agreements')).toHaveValue('I accept.\nI follow the rules.');
    await expect(field(page, 'reviewed')).not.toBeChecked();
    expect(JSON.parse(sql('SELECT agreements FROM waiver_versions WHERE version=2')[0].agreements)).toEqual(['I accept.', 'I follow the rules.']);
    expect(sql('SELECT * FROM waiver_versions WHERE version=1')).toEqual(old);
    expect(sql('SELECT * FROM waivers')).toEqual(signature);
    expect(sql('SELECT waiver FROM members WHERE id=2')[0].waiver).toBe(signature[0].id);
    expect(JSON.parse(sql('SELECT data FROM settings')[0].data).waiver_version).toBe(2);
});

for (const kind of ['events', 'swipes'])
    test(`${kind} supports ordered 50-row pagination${kind === 'events' ? ' and member filtering' : ''}`, async ({ page, login }) => {
        for (let i = 0; i < 53; i++) {
            if (kind === 'events') sql('INSERT INTO member_events(member,actor,event,details) VALUES(2,1,?,?)', `Audit ${i}`, JSON.stringify({ sequence: i }));
            else sql('INSERT INTO fob_swipes VALUES(?, ?, ?, ?, ?, ?)', `swipe-${i}`, 1700000000 + i, 2000 + i, i % 2 ? null : 2, i % 2, `Door ${i}`);
        }
        if (kind === 'events') sql("INSERT INTO member_events(member,event) VALUES(3,'Other member event')");
        await login();
        await page.goto(`/admin/${kind}`);
        if (kind === 'events') {
            await field(page, 'member').fill('2');
            await button(page, 'Apply filter').click();
        }
        await expect(rows(page)).toHaveCount(50);
        await expect(rows(page).first()).toContainText(kind === 'events' ? 'Audit 52' : 'Door 52');
        await expect(rows(page).last()).toContainText(kind === 'events' ? 'Audit 3' : 'Door 3');
        if (kind === 'events') await expect(page.getByText('Other member event', { exact: true })).toHaveCount(0);
        else {
            await expect(rows(page).first()).toContainText('Denied');
            await expect(rows(page).nth(1)).toContainText('Allowed');
            await expect(rows(page).nth(1)).toContainText('Unknown');
        }
        await button(page, 'Next').click();
        await expect(rows(page)).toHaveCount(3);
        await expect(page.locator('.pagination')).toContainText('51-53');
        await expect(rows(page).last()).toContainText(kind === 'events' ? 'Audit 0' : 'Door 0');
        if (kind === 'events') await expect(field(page, 'member')).toHaveValue('2');
        await button(page, 'Previous').click();
        await expect(rows(page)).toHaveCount(50);
        await expect(button(page, 'Previous')).toBeDisabled();
    });

test('jobs display every state and retry only dead work with persisted reset fields', async ({ page, login }) => {
    for (const status of ['dead', 'processing', 'pending', 'completed'])
        sql(
            'INSERT INTO jobs(kind,payload,status,attempts,lease_until,last_error,completed) VALUES(?,?,?,?,?,?,?)',
            `test_${status}`,
            '{}',
            status,
            5,
            123,
            status === 'dead' ? 'Delivery failed' : '',
            status === 'completed' ? 100 : null,
        );
    await login();
    await page.goto('/admin/jobs');
    await expect(rows(page)).toHaveCount(4);
    await expect(page.locator('tbody tr td:nth-child(2)')).toHaveText(['dead', 'processing', 'pending', 'completed']);
    await expect(button(page, 'Retry job')).toHaveCount(1);
    await expect(rows(page).first()).toContainText('Delivery failed');
    await submit(page, 'Retry job', 'POST');
    await expect(page.locator('.global-notice')).toContainText('queued for retry');
    await page.reload();
    await expect(rows(page).filter({ hasText: 'test_dead' })).toContainText('pending');
    await expect(button(page, 'Retry job')).toHaveCount(0);
    expect(sql("SELECT status,attempts,lease_until,last_error,completed FROM jobs WHERE kind='test_dead'")[0]).toEqual({
        status: 'pending',
        attempts: 0,
        lease_until: 0,
        last_error: '',
        completed: null,
    });
    expect(sql("SELECT available_at FROM jobs WHERE kind='test_dead'")[0].available_at).toBeGreaterThan(Math.floor(Date.now() / 1000) - 60);
    expect(sql("SELECT actor FROM member_events WHERE event='JobRetryRequested'")).toEqual([{ actor: 1 }]);
});
