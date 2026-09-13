import { api, state, el, field, check, link, panel, empty, form, heading, checkoutURL } from './lib.js';

export function welcome() {
    return el(
        'div',
        { class: 'narrow' },
        heading('Become a member', 'Start online. Meet us at the space on Tuesday night.'),
        panel(
            null,
            el(
                'ol',
                { class: 'onboarding-list' },
                el('li', {}, 'Sign up with Discord and sign the membership waiver.'),
                el('li', {}, 'Start your membership payment, or request a discount if you are eligible. Wait for approval before paying if you request a discount.'),
                el('li', {}, 'Come to the space on Tuesday night to get your key fob. Leadership will review any discount request with you in person.'),
            ),
            link('Continue with Discord', '/login/discord?return_to=%2Fbilling', 'button'),
            el('p', { class: 'hint section-gap' }, 'Already a member? Use the same button to sign in.'),
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
    const pending = member.discount_status === 'requested';
    const subscriber = member.stripe_subscription_state && !['canceled', 'incomplete_expired'].includes(member.stripe_subscription_state);
    const needsPayment = !member.payment_status && !member.non_billable && !member.paypal_subscription_id && Boolean(member.confirmed);
    const needsWaiver = !member.non_billable && !member.waiver;
    const needsVisit = !member.fob_id || pending;
    const ready = member.access_status === 'Ready';
    const discountLabel = discounts.find((item) => item.id === member.discount_type)?.label || 'Membership discount';
    const checkout =
        (subscriber || (pending && member.stripe_customer_id) || (needsPayment && !pending)) &&
        form(
            [
                el(
                    'p',
                    { class: 'hint' },
                    subscriber
                        ? 'Update your payment method, view invoices, or cancel through Stripe.'
                        : pending
                          ? 'Already paid? Check for an existing Stripe subscription. This will not start a new payment while your discount is pending.'
                          : 'Review your membership price and recurring payment terms on Stripe before confirming.',
                ),
            ],
            subscriber ? 'Manage billing' : pending ? 'Check existing billing' : 'Start membership payment',
            async () => {
                const result = await api('/api/billing/checkout', {
                    method: 'POST',
                    body: {},
                });
                location.assign(checkoutURL(result.url));
                return 'Opening Stripe...';
            },
        );
    if (checkout && !config.stripe_enabled) checkout.querySelector('button[type=submit]').disabled = true;
    return el(
        'div',
        { class: 'narrow' },
        heading('Your membership'),
        panel(
            ready ? 'Your membership is active' : needsVisit && !needsWaiver && (!needsPayment || pending) ? 'Visit us Tuesday night' : 'Finish setting up your membership',
            el(
                'p',
                { class: 'muted' },
                ready ? 'Your key fob is ready to use at the space.' : 'Building access will be ready once your membership requirements are complete and your key fob is linked.',
            ),
            !member.non_billable && !member.confirmed && el('p', { class: 'notice warn' }, 'Contact leadership to confirm your account before building access can be enabled.'),
            needsWaiver && el('div', { class: 'section-gap' }, link('Sign membership waiver', '/waiver', 'button')),
            member.root_family_member &&
                !member.root_family_member_active &&
                el('p', { class: 'notice warn' }, 'Your family membership needs an active primary member. Ask leadership to review your family link.'),
            needsVisit &&
                el(
                    'div',
                    { class: 'section-gap' },
                    el('h3', {}, 'Meet us at the space on Tuesday night'),
                    !member.fob_id && el('p', {}, 'Ask leadership for your key fob. They will help you link it to your account using the on-site kiosk and your phone.'),
                    pending && el('p', {}, `${discountLabel} request pending. Leadership will review your eligibility with you in person.`),
                    pending &&
                        el(
                            'p',
                            {},
                            subscriber
                                ? 'Your existing subscription is unchanged. Discuss any billing adjustment with leadership; you can still manage billing below.'
                                : 'Wait for approval before paying. After the review, return here to start payment with your approved discount.',
                        ),
                    pending &&
                        form([], 'Withdraw discount request', async () => {
                            await api('/api/discount', { method: 'DELETE' });
                            app.navigate(
                                '/billing',
                                subscriber
                                    ? 'Discount request withdrawn. Your existing subscription is unchanged.'
                                    : 'Discount request withdrawn. You can now pay the standard membership price.',
                            );
                        }),
                ),
            member.discount_type &&
                !pending &&
                el('p', { class: 'section-gap' }, `${discountLabel} approved.${needsPayment && !subscriber ? ' Your discount will be included when you start payment.' : ''}`),
            !member.discount_type &&
                needsPayment &&
                !subscriber &&
                discounts.length > 0 &&
                el(
                    'details',
                    { class: 'section-gap' },
                    el('summary', {}, 'Eligible for a discount?'),
                    el('p', {}, 'Request it before paying. Leadership will review your eligibility on Tuesday night, including linking a primary member for a family discount.'),
                    form(
                        [
                            field('Discount to request', 'discount_type', '', {
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
                            app.navigate('/billing', 'Discount requested. Meet with leadership on Tuesday night for review.');
                        },
                    ),
                ),
            member.discount_type &&
                !pending &&
                needsPayment &&
                !subscriber &&
                el(
                    'details',
                    { class: 'section-gap' },
                    el('summary', {}, 'No longer eligible for this discount?'),
                    form(
                        [check('I understand removing this discount may change my membership price or family access.', 'confirm', false, { required: true })],
                        'Remove discount',
                        async () => {
                            await api('/api/discount', { method: 'DELETE' });
                            app.navigate('/billing', 'Your discount has been removed.');
                        },
                    ),
                ),
            member.stripe_last_payment_error &&
                el('p', { class: 'notice error', role: 'alert' }, 'Your last payment failed. Update your payment method in Stripe, or contact leadership for help.'),
            member.paypal_subscription_id &&
                !subscriber &&
                el('p', { class: 'hint section-gap' }, 'Your membership is billed through PayPal. Contact leadership for billing changes.'),
            checkout &&
                el(
                    'div',
                    { class: 'section-gap' },
                    !config.stripe_enabled && el('p', { class: 'notice warn' }, 'Online payments are not available right now. Please contact leadership.'),
                    checkout,
                ),
        ),
    );
}
