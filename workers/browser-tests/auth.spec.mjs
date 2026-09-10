import { test, expect, sql } from './fixtures.mjs';

async function discord(page, code) {
    // Playwright only routes the first request in an HTTP redirect chain.
    // Execute the real login handler, then stand in for Discord's consent redirect.
    await page.route(/\/login\/discord(?:\?|$)/, async (route) => {
        const response = await route.fetch({ maxRedirects: 0 });
        expect(response.status()).toBe(302);
        const authorization = new URL(response.headers().location);
        expect(authorization.origin).toBe('https://discord.com');
        expect(authorization.searchParams.get('scope')).toBe('identify email');
        const callback = new URL(authorization.searchParams.get('redirect_uri'));
        callback.searchParams.set('state', authorization.searchParams.get('state'));
        callback.searchParams.set(code === 'cancel' ? 'error' : 'code', code);
        await route.fulfill({ response, headers: { ...response.headers(), location: callback.href } });
    });
}

test('Discord login rotates the anonymous session, follows return path and signs out', async ({ page, context }) => {
    await discord(page, 'existing');
    await page.goto('/profile');
    await expect(page.locator('main').getByRole('link', { name: 'Continue with Discord' })).toBeVisible();
    const before = (await context.cookies()).find((cookie) => cookie.name === 'conway_session').value;
    await page.locator('main').getByRole('link', { name: 'Continue with Discord' }).click();
    await expect(page.getByRole('heading', { name: 'Your profile' })).toBeVisible();
    const after = (await context.cookies()).find((cookie) => cookie.name === 'conway_session');
    expect(after.value).not.toBe(before);
    expect(after.httpOnly).toBe(true);
    expect(after.sameSite).toBe('Lax');
    expect(sql('SELECT * FROM oauth_states')).toEqual([]);
    await page.getByRole('button', { name: 'Sign out' }).click();
    await expect(page.getByText('You have been signed out.')).toBeVisible();
    expect(sql('SELECT * FROM sessions WHERE member=1')).toEqual([]);
    await page.goto('/dashboard');
    await expect(page.locator('main').getByRole('link', { name: 'Continue with Discord' })).toBeVisible();
});

test('Discord login creates a new member directly and preserves return path', async ({ page }) => {
    await discord(page, 'new');
    const target = '/fobs/bind?token=' + 'a'.repeat(64);
    await page.goto(target);
    await page.locator('main').getByRole('link', { name: 'Continue with Discord' }).click();
    await expect(page).toHaveURL('http://127.0.0.1:8799' + target);
    expect(sql("SELECT name,confirmed,discord_user_id FROM members WHERE email='new@example.test'")[0]).toEqual({
        name: 'New Maker',
        confirmed: 1,
        discord_user_id: '423456789012345678',
    });
    await page.reload();
    await expect(page.getByRole('button', { name: /Link.*fob/i })).toBeVisible();
});

for (const [code, error] of [
    ['collision', /already associated/],
    ['unverified', /verify your email/i],
    ['failure', /could not be completed/],
    ['cancel', /not authorized/],
]) {
    test(`Discord ${code} fails safely without creating a membership`, async ({ page }) => {
        await discord(page, code);
        await page.goto('/');
        await page.locator('main').getByRole('link', { name: 'Continue with Discord' }).click();
        await expect(page.locator('body')).toContainText(error);
        expect(sql('SELECT * FROM members')).toHaveLength(3);
        expect(sql('SELECT * FROM sessions WHERE member IS NOT NULL')).toEqual([]);
    });
}

test('invalid OAuth callbacks and unsafe return destinations cannot authenticate or redirect offsite', async ({ page }) => {
    const response = await page.goto('/login/discord/callback?state=invalid&code=existing');
    expect(response.status()).toBe(400);
    expect(response.headers()['cache-control']).toBe('no-store');
    await expect(page.locator('body')).toContainText('Invalid or expired');
    await discord(page, 'existing');
    await page.goto('/login/discord?return_to=https://evil.example');
    await expect(page).toHaveURL(/\/dashboard$/);
    await expect(page.getByRole('heading', { level: 1 })).toContainText('Alex Maker');
});

test('ordinary members cannot reach leadership UI or APIs, and mutations require CSRF and Origin', async ({ page, login }) => {
    await login(2);
    await expect(page.getByRole('link', { name: 'Leadership', exact: true })).toHaveCount(0);
    for (const path of ['/admin/members', '/admin/config', '/admin/waiver', '/admin/events', '/admin/swipes', '/admin/jobs']) {
        await page.goto(path);
        await expect(page.getByRole('heading', { name: 'Leadership access required' })).toBeVisible();
        expect((await page.request.get('/api' + path)).status()).toBe(403);
    }
    const session = await (await page.request.get('/api/session')).json();
    for (const headers of [{ Origin: 'http://127.0.0.1:8799' }, { 'X-CSRF-Token': session.csrf_token, Origin: 'https://evil.example' }]) {
        expect((await page.request.patch('/api/profile', { headers, data: { name: 'Unauthorized' } })).status()).toBe(403);
    }
    expect(sql('SELECT name FROM members WHERE id=2')[0].name).toBe('Sam Member');
});
