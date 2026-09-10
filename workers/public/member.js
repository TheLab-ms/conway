import { api, state, el, field, check, link, panel, empty, badge, date, displayName, form, heading, safeReturn, checkoutURL } from './lib.js';

class MemberCard extends HTMLElement {
    set member(member) {
        this.replaceChildren(
            el(
                'article',
                { class: 'panel member-card' },
                el('h2', {}, displayName(member)),
                member.pronouns && el('p', { class: 'muted' }, member.pronouns),
                member.leadership ? badge('Leadership', 'good') : badge('Member'),
                el('p', { class: 'bio' }, member.bio || 'This member has not added a bio yet.'),
                member.discord_username && el('footer', {}, `Discord / ${member.discord_username}`),
            ),
        );
    }
}
customElements.define('member-card', MemberCard);

export function welcome() {
    return el(
        'div',
        {},
        el(
            'section',
            { class: 'hero' },
            el('p', { class: 'eyebrow' }, 'Build something. Belong here.'),
            el('h1', {}, 'Your space to make.'),
            el('p', {}, 'One place for your membership, building access, and the people who make this community work.'),
            el(
                'div',
                { class: 'actions' },
                link('Continue with Discord', '/login/discord?return_to=%2Fdashboard', 'button'),
                link('Enroll an access fob', '/kiosk', 'button secondary'),
            ),
            el('p', { class: 'hint section-gap' }, 'New here? Sign in with Discord to create your membership. No separate password required.'),
        ),
        el(
            'div',
            { class: 'grid thirds' },
            panel(
                null,
                el('p', { class: 'number' }, '01 / JOIN'),
                el('h2', {}, 'Meet your community'),
                el('p', { class: 'muted' }, 'Use your Discord account to join and introduce yourself in the member directory.'),
            ),
            panel(
                null,
                el('p', { class: 'number' }, '02 / GET READY'),
                el('h2', {}, 'Make it official'),
                el('p', { class: 'muted' }, 'Review your waiver, arrange membership billing, and check your access status.'),
            ),
            panel(
                null,
                el('p', { class: 'number' }, '03 / MAKE'),
                el('h2', {}, 'Open the door'),
                el('p', { class: 'muted' }, 'Link your access fob at the space. Your dashboard keeps the next steps clear.'),
            ),
        ),
    );
}

export function signup(app) {
    const info = state.session.signup;
    if (!info)
        return el(
            'div',
            { class: 'narrow' },
            heading('Join the community', 'Your Discord account is your sign-in identity.'),
            panel(
                'Start with Discord',
                el('p', {}, 'We will bring you back here to finish creating your membership.'),
                link('Continue with Discord', `/login/discord?return_to=${encodeURIComponent('/signup' + location.search)}`, 'button'),
            ),
        );
    return el(
        'div',
        { class: 'narrow' },
        heading('A little about you', 'Finish your membership profile. Your waiver and billing come next.'),
        panel(
            'Create membership',
            el('p', { class: 'muted' }, `Discord connected: ${info.discord_user_id}. ${info.email || ''}`),
            form(
                [
                    field('Full name', 'name', '', { required: true, autocomplete: 'name', maxlength: 200 }),
                    field(
                        'How did you hear about us?',
                        'heard_about',
                        '',
                        state.config.referral_sources?.length
                            ? {
                                  choices: [['', 'Select a source'], ...state.config.referral_sources],
                                  required: true,
                              }
                            : { maxlength: 500 },
                    ),
                ],
                'Create membership',
                async (data) => {
                    const result = await api('/api/signup', {
                        method: 'POST',
                        body: Object.fromEntries(data),
                    });
                    await app.refreshSession();
                    app.navigate(safeReturn(result.return_to));
                },
            ),
        ),
    );
}

export async function dashboard(app, signal) {
    const member = await api('/api/member', { signal });
    state.session.member = member;
    const step = (title, detail, complete, href, action) =>
        el('li', {}, el('div', {}, el('strong', {}, title), el('small', {}, detail)), complete ? badge('Complete', 'good') : link(action, href));
    const status = member.access_status || 'Not ready';
    return el(
        'div',
        {},
        heading(
            `Hello, ${displayName(member)}.`,
            'Your membership at a glance. Keep the essentials ready, then get back to making.',
            link('Edit profile', '/profile', 'button secondary'),
        ),
        el(
            'div',
            { class: 'grid thirds' },
            panel(
                'Building access',
                el('p', { class: 'status-value' }, badge(status, status === 'Ready' ? 'good' : 'warn')),
                el('p', { class: 'hint' }, 'Access is determined by your membership, waiver, and fob.'),
            ),
            panel(
                'Membership',
                el('p', { class: 'status-value' }, member.payment_status || 'Not active'),
                el('p', { class: 'hint' }, member.bill_annually ? 'Annual billing preference' : 'Monthly billing preference'),
            ),
            panel('Access fob', el('p', { class: 'status-value mono' }, member.fob_id || 'Not linked'), el('p', { class: 'hint' }, `Last seen: ${date(member.fob_last_seen)}`)),
        ),
        el(
            'div',
            { class: 'grid dashboard-grid section-gap' },
            panel(
                'Your next steps',
                el(
                    'ol',
                    { class: 'step-list' },
                    step('Connect Discord', member.discord_username || 'Your sign-in identity is connected.', Boolean(member.discord_user_id), '/profile', 'View profile'),
                    step(
                        'Sign the waiver',
                        member.waiver ? 'Your signed waiver is on file.' : 'Read and accept the membership waiver.',
                        Boolean(member.waiver),
                        '/waiver',
                        'Review waiver',
                    ),
                    step(
                        'Set up membership',
                        member.payment_status || 'Choose billing or request an eligible discount.',
                        Boolean(member.payment_status),
                        '/billing',
                        'View billing',
                    ),
                    step(
                        'Link your fob',
                        member.fob_id ? 'Your fob is linked to your account.' : 'Use the enrollment kiosk at the space.',
                        Boolean(member.fob_id),
                        '/kiosk',
                        'How to enroll',
                    ),
                ),
            ),
            el(
                'div',
                { class: 'stack' },
                panel(
                    'Made by members',
                    el('p', { class: 'muted' }, 'Find familiar names, discover shared interests, and introduce yourself. No profile photos, just people and what they make.'),
                    link('Explore the directory', '/directory', 'button secondary'),
                ),
                panel(
                    'Support the space',
                    el('p', { class: 'muted' }, 'A little extra helps keep a shared place for making available to everyone.'),
                    link('Make a donation', '/donations', 'button secondary'),
                ),
            ),
        ),
    );
}

export async function profile(app, signal) {
    const member = await api('/api/member', { signal });
    return el(
        'div',
        { class: 'narrow' },
        heading('Your profile', 'Introduce yourself in your own words. Member profiles are text only.'),
        panel(
            'Community profile',
            form(
                [
                    field('Name', 'name', member.name, {
                        required: true,
                        autocomplete: 'name',
                        maxlength: 200,
                    }),
                    field('Pronouns', 'pronouns', member.pronouns, {
                        maxlength: 100,
                        placeholder: 'Optional',
                    }),
                    field('About you', 'bio', member.bio, {
                        type: 'textarea',
                        maxlength: 4000,
                        hint: 'Interests, projects, and things you enjoy making. Plain text only.',
                    }),
                    check('Hide my profile from the member directory', 'directory_hidden', member.directory_hidden),
                    check('Allow Discord check-in notifications when I use my fob', 'discord_checkin_notify', member.discord_checkin_notify),
                ],
                'Save profile',
                async (data) => {
                    await api('/api/profile', {
                        method: 'PATCH',
                        body: {
                            name: data.get('name'),
                            pronouns: data.get('pronouns'),
                            bio: data.get('bio'),
                            directory_hidden: data.has('directory_hidden'),
                            discord_checkin_notify: data.has('discord_checkin_notify'),
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
                el('p', { class: 'hint' }, 'Need to change your Discord linkage? Contact leadership so your existing membership stays connected.'),
            ),
        ),
    );
}

export async function directory(app, signal) {
    const { members } = await api('/api/directory', { signal });
    const cards = el('div', { class: 'directory-grid' });
    const count = el('p', { class: 'hint', role: 'status' });
    const search = field('Find a member', 'search', '', {
        type: 'search',
        placeholder: 'Search names, interests, or Discord usernames',
        autocomplete: 'off',
    });
    const render = (query) => {
        const visible = members.filter((member) =>
            [displayName(member), member.bio, member.pronouns, member.discord_username].join(' ').toLowerCase().includes(query.toLowerCase()),
        );
        count.textContent = `${visible.length} ${visible.length === 1 ? 'member' : 'members'}`;
        cards.replaceChildren(
            ...visible.map((member) => {
                const card = el('member-card');
                card.member = member;
                return card;
            }),
        );
        if (!visible.length)
            cards.append(
                empty(
                    members.length
                        ? 'No members match your search. Try a different name or interest.'
                        : 'The directory is quiet for now. Visible member profiles will appear here.',
                ),
            );
    };
    search.querySelector('input').addEventListener('input', (event) => render(event.target.value));
    render('');
    return el(
        'div',
        {},
        heading('People, not profiles.', 'A community of curious minds and capable hands.', link('Edit your introduction', '/profile', 'button secondary')),
        el('div', { class: 'toolbar' }, search),
        count,
        cards,
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
        heading('Read. Understand. Make.', 'Please read the full waiver before signing. Your signature is recorded with this version.'),
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
                    app.navigate('/dashboard', 'Your signed waiver is on file.');
                },
            ),
        ),
    );
}

export async function billing(app, signal) {
    const member = await api('/api/member', { signal });
    const config = state.config;
    const discounts = config.discounts || [];
    const checkout = form(
        [
            field('Billing frequency', 'frequency', member.bill_annually ? 'annual' : 'monthly', {
                choices: [
                    ['monthly', 'Monthly membership'],
                    ['annual', 'Annual membership'],
                ],
            }),
            el('p', { class: 'hint' }, 'Review the exact price and terms securely on Stripe before confirming. Existing subscription handling is managed by the payment service.'),
        ],
        'Continue to secure billing',
        async (data) => {
            const result = await api('/api/billing/checkout', {
                method: 'POST',
                body: { annual: data.get('frequency') === 'annual' },
            });
            location.assign(checkoutURL(result.url));
            return 'Opening secure billing...';
        },
    );
    if (!config.stripe_enabled) checkout.querySelector('button[type=submit]').disabled = true;
    return el(
        'div',
        {},
        heading('Membership & billing', 'Keep your membership moving. Payment details stay with the payment provider.', link('Make a donation', '/donations', 'button secondary')),
        el(
            'div',
            { class: 'grid' },
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
                el('p', { class: 'muted' }, 'Choose an eligible discount for leadership to review. Family account linking is completed by leadership.'),
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

export function donations() {
    const choices = state.config.donations || [];
    const donate = choices.length
        ? form(
              [
                  field('Donation amount', 'price_id', choices[0].price_id, {
                      choices: choices.map((item) => [item.price_id, item.label]),
                      required: true,
                  }),
                  el('p', { class: 'hint' }, 'You will review the amount and payment details on Stripe before making a donation. A donation does not replace membership dues.'),
              ],
              'Continue to donation',
              async (data) => {
                  const result = await api('/api/billing/donation', {
                      method: 'POST',
                      body: { price_id: data.get('price_id') },
                  });
                  location.assign(checkoutURL(result.url));
                  return 'Opening secure donation checkout...';
              },
          )
        : empty('No donation options are configured yet. Thank you for wanting to support the space.');
    if (!state.config.stripe_enabled) donate.querySelector('button')?.setAttribute('disabled', '');
    return el(
        'div',
        { class: 'narrow' },
        heading('Keep the space making.', 'Support the shared tools, shared knowledge, and possibilities that bring us together.'),
        panel(
            'Make a donation',
            !state.config.stripe_enabled && el('p', { class: 'notice warn' }, 'Online donations are currently unavailable. Please contact leadership.'),
            donate,
        ),
    );
}
