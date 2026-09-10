export const state = { session: null, config: null };

export class ApiError extends Error {
    constructor(message, status) {
        super(message);
        this.status = status;
    }
}

export async function api(path, { method = 'GET', body, signal } = {}) {
    if (!path.startsWith('/api/')) throw new Error('Invalid API path.');
    const headers = { Accept: 'application/json' };
    if (method !== 'GET') {
        if (!state.session?.csrf_token) {
            state.session = await api('/api/session', { signal });
        }
        if (!state.session?.csrf_token) throw new ApiError('Your session has expired. Please sign in again, then retry.', 401);
        headers['X-CSRF-Token'] = state.session.csrf_token;
        headers['Content-Type'] = 'application/json';
    }
    let response;
    try {
        response = await fetch(path, {
            method,
            headers,
            credentials: 'same-origin',
            cache: 'no-store',
            signal,
            body: body === undefined ? undefined : JSON.stringify(body),
        });
    } catch (error) {
        if (error.name === 'AbortError') throw error;
        throw new ApiError('Unable to reach Conway. Check your connection and try again.', 0);
    }
    const data = response.status === 204 ? {} : await response.json().catch(() => null);
    if (!response.ok) {
        const fallback =
            response.status === 403
                ? 'You do not have permission for this action. Try signing in again.'
                : response.status === 401
                  ? 'Your session has expired. Please sign in again.'
                  : 'The request could not be completed. Please try again.';
        const message =
            response.status === 409 && path === '/api/admin/config'
                ? `${data?.error || 'Settings changed.'} Reload the latest version before saving again. Your edits are still here.`
                : data?.error || fallback;
        throw new ApiError(message, response.status);
    }
    if (data === null) throw new ApiError('The server returned an unexpected response. Please try again.', response.status);
    return data;
}

// Only text nodes and explicit attributes are used, including for API content.
export function el(tag, attrs = {}, ...children) {
    const node = document.createElement(tag);
    for (const [key, value] of Object.entries(attrs)) {
        if (value === undefined || value === null || value === false) continue;
        if (key.startsWith('on')) node.addEventListener(key.slice(2).toLowerCase(), value);
        else if (key === 'class') node.className = value;
        else if (['checked', 'disabled', 'required', 'readOnly', 'multiple'].includes(key)) node[key] = value;
        else node.setAttribute(key, value === true ? '' : String(value));
    }
    for (const child of children.flat(Infinity)) {
        if (child !== null && child !== undefined && child !== false) node.append(child instanceof Node ? child : document.createTextNode(String(child)));
    }
    return node;
}

let fieldID = 0;
export function field(label, name, value = '', options = {}) {
    const { type = 'text', hint, choices, ...attrs } = options;
    const id = `field-${++fieldID}`;
    const control =
        type === 'textarea'
            ? el('textarea', { id, name, ...attrs })
            : choices
              ? el(
                    'select',
                    { id, name, ...attrs },
                    choices.map((choice) => {
                        const [val, text] = Array.isArray(choice) ? choice : [choice, choice];
                        return el('option', { value: val }, text);
                    }),
                )
              : el('input', { id, name, type, ...attrs });
    control.value = value ?? '';
    if (hint) control.setAttribute('aria-describedby', `${id}-hint`);
    return el('div', { class: 'field' }, el('label', { for: id }, label), control, hint && el('small', { id: `${id}-hint` }, hint));
}
export function check(label, name, checked = false, attrs = {}) {
    return el('label', { class: 'check' }, el('input', { type: 'checkbox', name, checked: Boolean(checked), ...attrs }), el('span', {}, label));
}
export const link = (text, href, style = '') => el('a', { href, class: style }, text);
export const button = (text, onClick, style = 'secondary') => el('button', { type: 'button', class: style, onClick }, text);
export const panel = (title, ...content) => el('section', { class: 'panel' }, title && el('h2', {}, title), ...content);
export const empty = (message) => el('p', { class: 'empty' }, message);
export const loading = () => el('div', { class: 'loading', role: 'status' }, 'Loading workspace...');
export const badge = (text, kind = '') => el('span', { class: `badge ${kind}` }, text || 'Not set');
export const date = (value) =>
    value
        ? new Date(Number(value) * 1000).toLocaleString(undefined, {
              dateStyle: 'medium',
              timeStyle: 'short',
          })
        : 'Never';
export const displayName = (member) => member.name_override || member.name || member.identifier || 'Member';
export function notice(node, message, error = false) {
    node.className = `notice form-status${error ? ' error' : ''}`;
    node.setAttribute('role', error ? 'alert' : 'status');
    node.textContent = message;
}
export function form(fields, submitLabel, handler) {
    const output = el('div', { class: 'form-status', 'aria-live': 'polite' });
    const submit = el('button', { type: 'submit' }, submitLabel);
    const node = el('form', {}, el('div', { class: 'form-fields' }, fields), el('div', { class: 'form-footer' }, submit), output);
    node.addEventListener('submit', async (event) => {
        event.preventDefault();
        if (node.dataset.busy || !node.reportValidity()) return;
        node.dataset.busy = 'true';
        submit.disabled = true;
        node.setAttribute('aria-busy', 'true');
        notice(output, 'Working...');
        try {
            const message = await handler(new FormData(node), node);
            if (node.isConnected) notice(output, message || 'Changes saved.');
        } catch (error) {
            if (error.name !== 'AbortError') {
                notice(output, error.message, true);
                output.tabIndex = -1;
                output.focus();
            }
        } finally {
            delete node.dataset.busy;
            submit.disabled = false;
            node.removeAttribute('aria-busy');
        }
    });
    return node;
}
export function heading(title, subtitle, action) {
    return el(
        'div',
        { class: 'page-heading' },
        el('div', {}, el('p', { class: 'eyebrow' }, 'Member workspace'), el('h1', { tabindex: '-1' }, title), subtitle && el('p', {}, subtitle)),
        action && el('div', { class: 'actions' }, action),
    );
}
export function table(headers, rows) {
    return el(
        'div',
        { class: 'table-wrap', role: 'region', 'aria-label': headers.join(', '), tabindex: '0' },
        el(
            'table',
            {},
            el(
                'thead',
                {},
                el(
                    'tr',
                    {},
                    headers.map((text) => el('th', { scope: 'col' }, text)),
                ),
            ),
            el(
                'tbody',
                {},
                rows.map((cells) =>
                    el(
                        'tr',
                        {},
                        cells.map((cell) => el('td', {}, cell)),
                    ),
                ),
            ),
        ),
    );
}
export function safeReturn(value, fallback = '/dashboard') {
    if (
        typeof value !== 'string' ||
        !value ||
        value.length > 2048 ||
        !value.startsWith('/') ||
        value.startsWith('//') ||
        /[\\\x00-\x20\x7f]|%(?:0[0-9a-f]|1[0-9a-f]|5c|7f)/i.test(value)
    )
        return fallback;
    const url = new URL(value, location.origin);
    return url.origin === location.origin && !url.pathname.startsWith('//') && !/%2f/i.test(url.pathname) && !url.pathname.startsWith('/login') && !url.pathname.startsWith('/api/')
        ? url.pathname + url.search
        : fallback;
}
export function checkoutURL(value) {
    const url = new URL(value);
    if (url.protocol !== 'https:' || !['checkout.stripe.com', 'billing.stripe.com'].includes(url.hostname) || url.username || url.password) {
        throw new Error('The payment provider returned an invalid link. Please contact leadership.');
    }
    return url.href;
}
