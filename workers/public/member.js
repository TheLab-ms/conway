import { api, state, el, field, check, link, panel, empty, badge, date, form, heading, checkoutURL } from './lib.js';

export function welcome() {
    return el(
        'div',
        { class: 'narrow' },
        heading('Membership', 'Sign in with Discord to manage your membership and building access.'),
        panel(
            null,
            el(
                'div',
                { class: 'actions' },
                link('Continue with Discord', '/login/discord?return_to=%2Fbilling', 'button'),
                link('Enroll an access fob', '/kiosk', 'button secondary'),
            ),
            el('p', { class: 'hint section-gap' }, 'New members can create an account by signing in.'),
        ),
    );
}

export async function profile(app, signal) {
    const member = await api('/api/member', { signal });
    return el(
        'div',
        { class: 'narrow' },
        heading('Your profile', 'Manage your membership details.'),
        panel(
            'Membership details',
            form(
                [
                    field('Name', 'name', member.name, {
                        required: true,
                        autocomplete: 'name',
                        maxlength: 200,
                    }),
                ],
                'Save profile',
                async (data) => {
                    await api('/api/profile', {
                        method: 'PATCH',
                        body: {
                            name: data.get('name'),
                        },
                    });
                    await app.refreshSession();
                    return 'Your profile has been saved.';
                },
            ),
        ),
        el(
            'div',
            { class: 'section-gap' },
            panel(
                'Discord identity',
                el(
                    'dl',
                    { class: 'record' },
                    el('dt', {}, 'Username'),
                    el('dd', {}, member.discord_username || 'Not available'),
                    el('dt', {}, 'Discord ID'),
                    el('dd', { class: 'mono' }, member.discord_user_id || 'Not linked'),
                ),
                el('p', { class: 'hint' }, 'Contact leadership to change your linked Discord account.'),
            ),
        ),
    );
}

export async function waiver(app, signal) {
    const [config, member] = await Promise.all([api('/api/config', { signal }), api('/api/member', { signal })]);
    state.config = config;
    const waiver = config.waiver;
    if (!waiver?.content || !waiver.version)
        return el('div', { class: 'narrow' }, heading('Membership waiver'), empty('A waiver has not been published yet. Please contact leadership before proceeding.'));
    return el(
        'div',
        { class: 'narrow' },
        heading('Membership waiver', 'Read the full waiver before signing.'),
        member.waiver && el('p', { class: 'notice' }, 'A signed waiver is already on file for your membership. You may review the current version below.'),
        panel(
            `Waiver / version ${waiver.version}`,
            el('div', { class: 'document', tabindex: '0', role: 'region', 'aria-label': 'Full waiver text' }, waiver.content),
            form(
                [
                    ...(waiver.agreements || []).map((text, index) => check(text, `agreement_${index}`, false, { required: true })),
                    field('Full legal name / electronic signature', 'name', member.name, {
                        required: true,
                        autocomplete: 'name',
                        maxlength: 200,
                    }),
                    el('p', { class: 'hint' }, 'By submitting, you sign this waiver electronically and affirm each agreement above.'),
                ],
                'Sign waiver',
                async (data) => {
                    await api('/api/waiver', {
                        method: 'POST',
                        body: {
                            version: waiver.version,
                            name: data.get('name'),
                            agreements: (waiver.agreements || []).map((_, index) => data.has(`agreement_${index}`)),
                        },
                    });
                    await app.refreshSession();
                    app.navigate('/billing', 'Your signed waiver is on file.');
                },
            ),
        ),
    );
}

export async function billing(app, signal) {
    const member = await api('/api/member', { signal });
    const config = state.config;
    const discounts = config.discounts || [];
    const checkout = form([el('p', { class: 'hint' }, 'Review the price and terms on Stripe before confirming.')], 'Continue to Stripe', async () => {
        const result = await api('/api/billing/checkout', {
            method: 'POST',
            body: {},
        });
        location.assign(checkoutURL(result.url));
        return 'Opening Stripe...';
    });
    if (!config.stripe_enabled) checkout.querySelector('button[type=submit]').disabled = true;
    return el(
        'div',
        {},
        heading('Membership & billing', 'Manage your membership and building access.', link('Edit profile', '/profile', 'button secondary')),
        panel(
            'Building access',
            el('p', { class: 'status-value' }, badge(member.access_status || 'Not ready', member.access_status === 'Ready' ? 'good' : 'warn')),
            el('p', { class: 'hint' }, 'Access depends on your membership, waiver, and fob.'),
            el(
                'dl',
                { class: 'record' },
                el('dt', {}, 'Access fob'),
                el('dd', { class: 'mono' }, member.fob_id ?? 'Not linked'),
                el('dt', {}, 'Last seen'),
                el('dd', {}, date(member.fob_last_seen)),
            ),
            el('div', { class: 'actions' }, link('Review waiver', '/waiver', 'button secondary'), link('Enroll an access fob', '/kiosk', 'button secondary')),
        ),
        el(
            'div',
            { class: 'grid section-gap' },
            panel(
                'Your membership',
                el('p', { class: 'status-value' }, member.payment_status || 'Not active'),
                el(
                    'dl',
                    { class: 'record' },
                    el('dt', {}, 'Subscription'),
                    el('dd', {}, member.stripe_subscription_state || (member.paypal_subscription_id ? 'Legacy PayPal membership' : 'Not set up')),
                    el('dt', {}, 'Discount'),
                    el('dd', {}, member.discount_type ? `${member.discount_type} / ${member.discount_status || 'Not approved'}` : 'None'),
                    el('dt', {}, 'Family membership'),
                    el('dd', {}, member.root_family_member ? `Linked to member #${member.root_family_member}` : 'Not linked'),
                ),
                member.stripe_last_payment_error && el('p', { class: 'notice error', role: 'alert' }, member.stripe_last_payment_error),
                member.stripe_cancellation_reason && el('p', { class: 'hint' }, `Cancellation: ${member.stripe_cancellation_reason}`),
                !config.stripe_enabled && el('p', { class: 'notice warn' }, 'Online payments are not available right now. Please contact leadership.'),
                checkout,
            ),
            panel(
                'Discounts & family membership',
                el('p', { class: 'muted' }, 'Leadership approves discounts and links family memberships.'),
                member.discount_status === 'requested'
                    ? el('p', { class: 'notice' }, `Your ${member.discount_type} request is waiting for review. Remove the pending request before choosing a different discount.`)
                    : discounts.length
                      ? form(
                            [
                                field('Discount to request', 'discount_type', member.discount_type || '', {
                                    required: true,
                                    choices: [['', 'Choose a discount'], ...discounts.map((item) => [item.id, item.label])],
                                }),
                            ],
                            'Request discount',
                            async (data) => {
                                await api('/api/discount', {
                                    method: 'POST',
                                    body: { discount_type: data.get('discount_type') },
                                });
                                app.navigate('/billing', 'Discount requested. Leadership will review your request.');
                            },
                        )
                      : empty('There are no discounts available to request at the moment.'),
                member.discount_type &&
                    el(
                        'div',
                        { class: 'section-gap' },
                        form(
                            [check('I understand removing this discount may change my membership price or family access.', 'confirm', false, { required: true })],
                            'Remove discount',
                            async () => {
                                await api('/api/discount', { method: 'DELETE' });
                                app.navigate('/billing', 'Your discount has been removed.');
                            },
                        ),
                    ),
            ),
        ),
    );
}
