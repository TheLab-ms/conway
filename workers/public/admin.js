import { api, el, field, check, link, button, panel, empty, badge, date, form, heading, table, state } from './lib.js';

export function adminNav(path) {
    return el(
        'nav',
        { class: 'admin-nav', 'aria-label': 'Leadership' },
        [
            ['Members', '/admin/members'],
            ['Configuration', '/admin/config'],
            ['Waiver', '/admin/waiver'],
            ['Audit log', '/admin/events'],
            ['Fob swipes', '/admin/swipes'],
            ['Jobs', '/admin/jobs'],
        ].map(([text, href]) =>
            el(
                'a',
                {
                    href,
                    'aria-current': path === href || (href === '/admin/members' && path.startsWith(href + '/')) ? 'page' : null,
                },
                text,
            ),
        ),
    );
}

function pagination(app, url, offset, count, total) {
    const trail = (new URL(url, location.origin).searchParams.get('pages') || '')
        .split(',')
        .filter(Boolean)
        .map(Number)
        .filter((value) => Number.isSafeInteger(value) && value >= 0 && value < offset);
    const move = (next, previous = false) => {
        const target = new URL(url, location.origin);
        target.searchParams.set('offset', String(Math.max(0, next)));
        const pages = previous ? trail.slice(0, -1) : [...trail, offset];
        if (pages.length) target.searchParams.set('pages', pages.join(','));
        else target.searchParams.delete('pages');
        app.navigate(target.pathname + target.search);
    };
    const previous = button('Previous', () => move(trail.at(-1) || 0, true));
    previous.disabled = offset === 0;
    const next = button('Next', () => move(offset + count));
    next.disabled = count === 0 || (total !== undefined && offset + count >= total);
    return el(
        'div',
        { class: 'pagination', 'aria-label': 'Pagination' },
        previous,
        el('span', { class: 'hint', role: 'status' }, count ? `${offset + 1}-${offset + count}${total !== undefined ? ` of ${total}` : ''}` : 'No results on this page'),
        next,
    );
}

export async function members(app, signal) {
    const params = new URLSearchParams(location.search);
    const offset = Math.max(0, Number(params.get('offset')) || 0);
    const search = params.get('search') || '';
    const status = params.get('status') || '';
    const query = new URLSearchParams({ search, status, offset });
    const data = await api(`/api/admin/members?${query}`, { signal });
    const filters = el(
        'form',
        { class: 'toolbar' },
        field('Search members', 'search', search, {
            type: 'search',
            placeholder: 'Discord handle or ID, name, email',
        }),
        field('Access status', 'status', status, {
            choices: [
                ['', 'All statuses'],
                'Ready',
                ['UnconfirmedEmail', 'Unconfirmed'],
                ['MissingWaiver', 'Missing waiver'],
                ['PaymentInactive', 'Payment inactive'],
                ['MissingKeyFob', 'Missing fob'],
                ['FamilyInactive', 'Family inactive'],
            ],
        }),
        el('button', { type: 'submit' }, 'Apply filters'),
    );
    filters.addEventListener('submit', (event) => {
        event.preventDefault();
        app.navigate('/admin/members?' + new URLSearchParams(new FormData(filters)));
    });
    return el(
        'div',
        {},
        heading('Members', 'Find members and review requests.', [link('Export CSV', '/api/admin/export', 'button secondary'), link('Add member', '/admin/members/new', 'button')]),
        filters,
        data.members.length
            ? table(
                  ['Member', 'Access', 'Billing', 'Discount', 'Discord ID'],
                  data.members.map((member) => [
                      el(
                          'div',
                          {},
                          link(member.discord_username || member.discord_user_id || `Member #${member.id}`, `/admin/members/${member.id}`),
                          el('p', { class: 'hint' }, `#${member.id}`),
                      ),
                      badge(member.access_status, member.access_status === 'Ready' ? 'good' : 'warn'),
                      member.payment_status || 'Inactive',
                      member.discount_type ? el('div', {}, member.discount_type, el('p', { class: 'hint' }, member.discount_status || 'Not approved')) : 'None',
                      member.discord_user_id || 'Not linked',
                  ]),
              )
            : empty('No members match these filters. Clear the search or add a new member.'),
        pagination(app, location.pathname + location.search, offset, data.members.length, data.total),
    );
}

const booleanFields = ['confirmed', 'leadership', 'non_billable', 'bill_annually'];
const nullableFields = ['name_override', 'discord_user_id', 'fob_id', 'root_family_member', 'discount_type', 'discount_status'];
const numberFields = ['fob_id', 'root_family_member'];

export async function memberEditor(app, id, signal) {
    const isNew = id === 'new';
    const member = isNew ? {} : await api(`/api/admin/members/${encodeURIComponent(id)}`, { signal });
    const fields = [
        field('Discord user ID', 'discord_user_id', member.discord_user_id, {
            inputmode: 'numeric',
            pattern: '[0-9]{5,25}',
            hint: 'Paste the numeric Discord user ID, not a username. This is the sign-in identity. Verify ownership before linking.',
        }),
        field('Display name override', 'name_override', member.name_override, {
            maxlength: 200,
            hint: 'Optional leadership override. Leave blank to use the member name.',
        }),
        el(
            'div',
            { class: 'grid' },
            field('Fob ID', 'fob_id', member.fob_id, {
                type: 'number',
                min: 1,
                max: 4294967295,
                step: 1,
            }),
            field('Family root member ID', 'root_family_member', member.root_family_member, {
                type: 'number',
                min: 1,
                step: 1,
                hint: 'The primary member ID, not an email. Blank removes the family link.',
            }),
        ),
        field('Leadership notes', 'admin_notes', member.admin_notes, {
            type: 'textarea',
            maxlength: 10000,
            hint: 'Internal notes. Do not store passwords, tokens, or payment card details.',
        }),
        el('div', { class: 'grid' }, check('Membership confirmed', 'confirmed', member.confirmed), check('Leadership access', 'leadership', member.leadership)),
        el(
            'section',
            { class: 'form-fields', 'aria-label': 'Billing information' },
            el('h3', {}, 'Billing information'),
            el(
                'div',
                { class: 'grid' },
                field('Full name', 'name', member.name, {
                    required: true,
                    maxlength: 200,
                    autocomplete: 'name',
                }),
                field('Billing email', 'email', member.email, {
                    type: 'email',
                    required: true,
                    maxlength: 254,
                    autocomplete: 'email',
                }),
            ),
            el('div', { class: 'grid' }, check('Non-billable membership', 'non_billable', member.non_billable), check('Annual billing', 'bill_annually', member.bill_annually)),
        ),
    ];
    if (!isNew)
        fields.push(
            el(
                'details',
                {},
                el('summary', {}, 'Discount settings'),
                el(
                    'div',
                    { class: 'form-fields' },
                    field('Discount type', 'discount_type', member.discount_type, {
                        choices: [
                            ['', 'No discount'],
                            ...(state.config.discounts || []).map((item) => [item.id, item.label]),
                            ...(member.discount_type && !state.config.discounts?.some((item) => item.id === member.discount_type)
                                ? [[member.discount_type, member.discount_type]]
                                : []),
                        ],
                    }),
                    field('Discount status', 'discount_status', member.discount_status, {
                        choices: [
                            ['', 'Not set'],
                            ['requested', 'Requested'],
                            ['approved', 'Approved'],
                        ],
                        hint: 'Prefer the request approval action below for pending requests.',
                    }),
                ),
            ),
        );
    const editor = form(fields, isNew ? 'Create member' : 'Save member', async (data) => {
        const body = Object.fromEntries(data);
        for (const key of booleanFields) {
            body[key] = data.has(key);
        }
        for (const key of nullableFields) if (key in body && body[key] === '') body[key] = null;
        for (const key of numberFields) if (body[key] !== null) body[key] = Number(body[key]);
        if (!isNew) body.version = member.version;
        const result = await api(isNew ? '/api/admin/members' : `/api/admin/members/${id}`, {
            method: isNew ? 'POST' : 'PATCH',
            body,
        });
        await app.refreshSession();
        app.navigate(isNew && result.id ? `/admin/members/${result.id}` : isNew ? '/admin/members' : `/admin/members/${id}`, isNew ? 'Member created.' : 'Member saved.');
    });
    const pending = member.discount_status === 'requested';
    return el(
        'div',
        {},
        heading(
            isNew ? 'Add a member' : member.discord_username || member.discord_user_id || `Member #${member.id}`,
            isNew ? 'Create a membership record. Discord is the only sign-in identity.' : `Member #${member.id} / joined ${date(member.created)}`,
            link('Back to members', '/admin/members', 'button secondary'),
        ),
        el(
            'div',
            { class: 'grid' },
            panel('Membership details', editor),
            el(
                'div',
                { class: 'stack' },
                !isNew &&
                    panel(
                        'Current status',
                        el(
                            'dl',
                            { class: 'record' },
                            el('dt', {}, 'Access'),
                            el('dd', {}, badge(member.access_status, member.access_status === 'Ready' ? 'good' : 'warn')),
                            el('dt', {}, 'Billing'),
                            el('dd', {}, member.payment_status || 'Inactive'),
                            el('dt', {}, 'Waiver'),
                            el('dd', {}, member.waiver ? `Signed / record #${member.waiver}` : 'Not signed'),
                            el('dt', {}, 'Subscription'),
                            el('dd', {}, member.stripe_subscription_state || 'None'),
                            el('dt', {}, 'Discord username'),
                            el('dd', {}, member.discord_username || 'Not linked'),
                            el('dt', {}, 'Last fob use'),
                            el('dd', {}, date(member.fob_last_seen)),
                        ),
                        link('View member audit trail', `/admin/events?member=${id}`, 'button secondary'),
                    ),
                pending &&
                    panel(
                        'Pending discount approval',
                        el('p', {}, `Requested discount: ${member.discount_type}`),
                        el('p', { class: 'hint mono' }, `Request: ${member.discount_request_id}`),
                        form(
                            [
                                field('Family root member ID (required for family approval)', 'root_family_member', member.root_family_member, {
                                    type: 'number',
                                    min: 1,
                                    max: Number.MAX_SAFE_INTEGER,
                                    step: 1,
                                    required: member.discount_type === 'family',
                                }),
                                check('I have reviewed eligibility for this request.', 'reviewed', false, {
                                    required: true,
                                }),
                            ],
                            'Approve request',
                            async (data) => {
                                const body = { request_id: member.discount_request_id };
                                if (data.get('root_family_member')) body.root_family_member = Number(data.get('root_family_member'));
                                await api(`/api/admin/members/${id}/approve`, { method: 'POST', body });
                                app.navigate(`/admin/members/${id}`, 'Discount request approved.');
                            },
                        ),
                    ),
                isNew &&
                    panel(
                        'Before you link',
                        el('p', { class: 'muted' }, 'Verify the Discord ID belongs to this member. A wrong linkage can give another person access to this membership.'),
                        el('p', { class: 'muted' }, 'Waivers must be signed by the member. Do not use manual access settings as a substitute for consent.'),
                    ),
                !isNew &&
                    el(
                        'section',
                        { class: 'panel danger-zone' },
                        el('h2', {}, 'Delete membership'),
                        el(
                            'p',
                            { class: 'muted' },
                            'Members with any Stripe subscription history cannot be physically deleted. Retain the member record even after cancellation so its billing history stays linked. Other records require billing cancellation and reconciliation before deletion. The server also protects the last leadership account and family roots with dependents.',
                        ),
                        form(
                            [
                                field(`Type member ID ${id} to confirm deletion`, 'confirmation', '', {
                                    required: true,
                                    inputmode: 'numeric',
                                    pattern: String(id),
                                }),
                            ],
                            'Delete member permanently',
                            async () => {
                                await api(`/api/admin/members/${id}`, {
                                    method: 'DELETE',
                                    body: { version: member.version },
                                });
                                await app.refreshSession();
                                app.navigate('/admin/members', 'Member deleted.');
                            },
                        ),
                    ),
            ),
        ),
    );
}

function lines(value) {
    return String(value || '')
        .split('\n')
        .map((line) => line.trim())
        .filter(Boolean);
}
function parseRows(value, columns, label) {
    return lines(value).map((line, index) => {
        const parts = line.split('|').map((part) => part.trim());
        if (parts.length !== columns.length || parts.some((part, i) => !part && columns[i] !== 'coupon_id'))
            throw new Error(`${label}, line ${index + 1}: enter ${columns.join(' | ')}.`);
        return Object.fromEntries(columns.map((column, i) => [column, parts[i]]));
    });
}

export async function configEditor(app, signal) {
    const config = await api('/api/admin/config', { signal });
    const editor = form(
        [
            el('p', { class: 'notice' }, `Editing configuration version ${config.version}. Saving uses optimistic concurrency; another leader's changes will not be overwritten.`),
            field('Site name', 'site_name', config.site_name, { required: true, maxlength: 200 }),
            el(
                'div',
                { class: 'grid' },
                field('Monthly Stripe price ID', 'monthly_price_id', config.monthly_price_id),
                field('Annual Stripe price ID', 'yearly_price_id', config.yearly_price_id),
            ),
            field('Discount options', 'discounts', (config.discounts || []).map((item) => [item.id, item.label, item.coupon_id].join(' | ')).join('\n'), {
                type: 'textarea',
                hint: 'One per line: id | member-facing label | Stripe coupon ID. Use no pipe characters within values.',
            }),
        ],
        'Save configuration',
        async (data) => {
            const body = { version: config.version };
            body.site_name = data.get('site_name').trim();
            body.monthly_price_id = data.get('monthly_price_id').trim();
            body.yearly_price_id = data.get('yearly_price_id').trim();
            body.discounts = parseRows(data.get('discounts'), ['id', 'label', 'coupon_id'], 'Discount options');
            await api('/api/admin/config', { method: 'PUT', body });
            state.config = await api('/api/config');
            app.navigate('/admin/config', 'Configuration saved.');
        },
    );
    return el(
        'div',
        { class: 'narrow' },
        heading('Space configuration', 'Site name, payment prices, and discount options.'),
        panel('Settings', editor),
        el('p', { class: 'hint section-gap' }, 'Waiver content is versioned separately. ', link('Edit the waiver', '/admin/waiver')),
    );
}

export async function waiverEditor(app, signal) {
    const config = await api('/api/config', { signal });
    const waiver = config.waiver || {};
    return el(
        'div',
        { class: 'narrow' },
        heading('Waiver publishing', 'Publishing creates a new version. Existing signatures remain tied to the version members signed.'),
        panel(
            `Current version / ${waiver.version || 'Not published'}`,
            form(
                [
                    field('Waiver content', 'content', waiver.content, {
                        type: 'textarea',
                        required: true,
                        maxlength: 60000,
                        rows: 18,
                        hint: 'Full legal text, displayed as plain text. Line breaks are preserved.',
                    }),
                    field('Required agreements', 'agreements', (waiver.agreements || []).join('\n'), {
                        type: 'textarea',
                        required: true,
                        hint: 'One complete agreement per line. Members must check every agreement.',
                    }),
                    check('I have reviewed the content and intend to publish a new waiver version.', 'reviewed', false, { required: true }),
                ],
                'Publish new waiver version',
                async (data) => {
                    const agreements = lines(data.get('agreements'));
                    if (!agreements.length || agreements.length > 30 || agreements.some((value) => value.length > 2000))
                        throw new Error('Provide 1 to 30 agreements, each at most 2,000 characters.');
                    await api('/api/admin/waiver', {
                        method: 'PUT',
                        body: { content: data.get('content'), agreements },
                    });
                    state.config = await api('/api/config');
                    app.navigate('/admin/waiver', 'New waiver version published.');
                },
            ),
        ),
    );
}

export async function activity(app, kind, signal) {
    const params = new URLSearchParams(location.search);
    const offset = Math.max(0, Number(params.get('offset')) || 0);
    const query = new URLSearchParams({ offset });
    if (kind === 'events' && params.get('member')) query.set('member', params.get('member'));
    const data = await api(`/api/admin/${kind}?${query}`, { signal });
    const isEvents = kind === 'events';
    const entries = data[kind];
    const filters = isEvents
        ? el(
              'form',
              { class: 'toolbar' },
              field('Filter by member ID', 'member', params.get('member'), {
                  type: 'number',
                  min: 1,
                  step: 1,
                  placeholder: 'All members',
              }),
              el('button', { type: 'submit' }, 'Apply filter'),
          )
        : null;
    filters?.addEventListener('submit', (event) => {
        event.preventDefault();
        app.navigate('/admin/events?' + new URLSearchParams(new FormData(filters)));
    });
    const memberLink = (id) => (id ? link(`#${id}`, `/admin/members/${id}`) : 'Unknown');
    return el(
        'div',
        {},
        heading(
            isEvents ? 'Audit log' : 'Fob swipes',
            isEvents ? 'Membership changes and administrative actions, in order.' : 'Fob activity recorded by the access controllers.',
            button('Refresh', () => app.navigate(location.pathname + location.search)),
        ),
        filters,
        entries.length
            ? table(
                  isEvents ? ['When', 'Member', 'Actor', 'Event', 'Details'] : ['When', 'Fob', 'Member', 'Result', 'Controller'],
                  entries.map((entry) =>
                      isEvents
                          ? [
                                date(entry.created),
                                memberLink(entry.member),
                                entry.actor ? memberLink(entry.actor) : 'System',
                                entry.event,
                                el('pre', {}, typeof entry.details === 'object' ? JSON.stringify(entry.details, null, 2) : entry.details),
                            ]
                          : [
                                date(entry.timestamp),
                                String(entry.fob_id),
                                memberLink(entry.member),
                                badge(entry.allowed ? 'Allowed' : 'Denied', entry.allowed ? 'good' : 'warn'),
                                entry.controller || 'Unknown',
                            ],
                  ),
              )
            : empty(isEvents ? 'No audit events on this page.' : 'No fob swipes on this page. Activity will appear after the next controller sync.'),
        pagination(app, location.pathname + location.search, offset, entries.length),
    );
}

export async function jobs(app, signal) {
    const { jobs } = await api('/api/admin/jobs', { signal });
    return el(
        'div',
        {},
        heading(
            'Background jobs',
            'Delivery and reconciliation work. A retry requeues work; it does not guarantee immediate delivery.',
            button('Refresh', () => app.navigate('/admin/jobs')),
        ),
        jobs.length
            ? table(
                  ['Job', 'State', 'Attempts', 'Available', 'Last error', 'Action'],
                  jobs.map((job) => [
                      el('div', {}, el('strong', {}, job.kind), el('p', { class: 'hint mono' }, `#${job.id}`)),
                      badge(job.status, job.status === 'completed' ? 'good' : job.status === 'dead' ? 'warn' : ''),
                      job.attempts,
                      date(job.available_at),
                      job.last_error || 'None',
                      job.status === 'dead'
                          ? form([], 'Retry job', async () => {
                                await api(`/api/admin/jobs/${job.id}/retry`, { method: 'POST', body: {} });
                                app.navigate('/admin/jobs', `Job #${job.id} queued for retry.`);
                            })
                          : el('span', { class: 'hint' }, 'No action'),
                  ]),
              )
            : empty('No background jobs. New membership activity will appear here when work is queued.'),
    );
}
