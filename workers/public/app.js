import { api, state, el, link, button, panel, loading, heading, safeReturn } from './lib.js';
import * as member from './member.js';
import * as admin from './admin.js';
import { kiosk, bind } from './enrollment.js';

class ConwayApp extends HTMLElement {
    connectedCallback() {
        this.routeVersion = 0;
        this.handlePopstate = () => this.render();
        this.handleClick = (event) => {
            const anchor = event.target.closest('a');
            if (
                !anchor ||
                event.defaultPrevented ||
                event.button !== 0 ||
                event.metaKey ||
                event.ctrlKey ||
                event.shiftKey ||
                event.altKey ||
                anchor.target ||
                anchor.hasAttribute('download')
            )
                return;
            const url = new URL(anchor.href);
            if (url.origin !== location.origin || url.pathname.startsWith('/api/') || url.pathname.startsWith('/login/') || url.hash) return;
            event.preventDefault();
            this.navigate(url.pathname + url.search);
        };
        window.addEventListener('popstate', this.handlePopstate);
        this.addEventListener('click', this.handleClick);
        this.boot();
    }

    disconnectedCallback() {
        this.controller?.abort();
        window.removeEventListener('popstate', this.handlePopstate);
        this.removeEventListener('click', this.handleClick);
    }

    async boot() {
        this.replaceChildren(loading());
        try {
            const [session, config] = await Promise.all([api('/api/session'), api('/api/config')]);
            state.session = session;
            state.config = config;
            await this.render();
        } catch (error) {
            this.replaceChildren(
                el(
                    'main',
                    { id: 'main', class: 'workspace narrow' },
                    heading('Unable to load', 'Please try again.'),
                    el('p', { class: 'notice error', role: 'alert' }, error.message),
                    button('Try again', () => this.boot()),
                ),
            );
        }
    }

    async refreshSession() {
        state.session = await api('/api/session');
    }

    navigate(path, message = '', replace = false) {
        const target = safeReturn(path, '/');
        history[replace ? 'replaceState' : 'pushState']({}, '', target);
        this.render(message);
    }

    shell(path, message) {
        const current = state.session.member;
        const navigation = current ? [['Billing', '/billing']] : [];
        if (current?.leadership) navigation.push(['Leadership', '/admin/members']);
        const nav = el(
            'nav',
            { class: 'primary-nav', 'aria-label': 'Main navigation' },
            navigation.map(([text, href]) =>
                el(
                    'a',
                    {
                        href,
                        'aria-current': path === href || (text === 'Leadership' && path.startsWith('/admin')) ? 'page' : null,
                    },
                    text,
                ),
            ),
        );
        if (current)
            nav.append(
                button('Sign out', async (event) => {
                    const control = event.currentTarget;
                    control.disabled = true;
                    try {
                        await api('/api/logout', { method: 'POST' });
                        await this.refreshSession();
                        this.navigate('/', 'You have been signed out.', true);
                    } catch (error) {
                        this.alert(error.message);
                        control.disabled = false;
                    }
                }),
            );
        else nav.append(link('Sign in with Discord', '/login/discord?return_to=' + encodeURIComponent(safeReturn(path + location.search)), 'button secondary'));
        const banner = el('div', { class: 'global-notice notice', role: 'status', 'aria-live': 'polite' }, message);
        this.banner = banner;
        this.content = el('div', {}, loading());
        this.replaceChildren(
            el('header', { class: 'site-header' }, el('div', { class: 'header-inner' }, nav)),
            el('main', { id: 'main', class: 'workspace', tabindex: '-1' }, banner, current?.leadership && path.startsWith('/admin') && admin.adminNav(path), this.content),
        );
    }

    alert(message) {
        this.banner.className = 'global-notice notice error';
        this.banner.setAttribute('role', 'alert');
        this.banner.textContent = message;
        this.banner.tabIndex = -1;
        this.banner.focus();
    }

    async render(message = '') {
        this.controller?.abort();
        this.controller = new AbortController();
        const signal = this.controller.signal;
        const version = ++this.routeVersion;
        let path = location.pathname.replace(/\/$/, '') || '/';
        if (path === '/admin') {
            history.replaceState({}, '', '/admin/members');
            path = '/admin/members';
        }
        if (path === '/' && state.session.member) {
            history.replaceState({}, '', '/billing');
            path = '/billing';
        }
        this.shell(path, message);
        const host = this.content;
        host.setAttribute('aria-busy', 'true');
        try {
            let view;
            const publicPaths = ['/', '/kiosk', '/fobs/bind'];
            if (!state.session.member && !publicPaths.includes(path)) {
                const target = safeReturn(path + location.search);
                view = el(
                    'div',
                    { class: 'narrow' },
                    heading('Sign in', 'Use Discord to view your membership.'),
                    panel(
                        'Members only',
                        el('p', {}, 'You will return to this page after signing in.'),
                        link('Continue with Discord', '/login/discord?return_to=' + encodeURIComponent(target), 'button'),
                    ),
                );
            } else if (path.startsWith('/admin') && !state.session.member?.leadership) {
                view = el('div', {}, heading('Leadership access required'), link('Back to billing', '/billing', 'button secondary'));
            } else if (path === '/') view = member.welcome();
            else if (path === '/profile') view = await member.profile(this, signal);
            else if (path === '/waiver') view = await member.waiver(this, signal);
            else if (path === '/billing' || path === '/discounts') view = await member.billing(this, signal);
            else if (path === '/admin/members') view = await admin.members(this, signal);
            else if (/^\/admin\/members\/(new|\d+)$/.test(path)) view = await admin.memberEditor(this, path.split('/').pop(), signal);
            else if (path === '/admin/config') view = await admin.configEditor(this, signal);
            else if (path === '/admin/waiver') view = await admin.waiverEditor(this, signal);
            else if (path === '/admin/events' || path === '/admin/swipes') view = await admin.activity(this, path.split('/').pop(), signal);
            else if (path === '/admin/jobs') view = await admin.jobs(this, signal);
            else if (path === '/kiosk') view = kiosk(this, signal);
            else if (path === '/fobs/bind') view = bind(this);
            else view = el('div', {}, heading('Page not found'), link('Go home', '/', 'button'));
            if (version !== this.routeVersion) return;
            host.replaceChildren(view);
            const title = host.querySelector('h1');
            document.title = `${title?.textContent || 'Membership'} | ${state.config.site_name || 'Conway'}`;
            window.scrollTo({ top: 0, behavior: 'instant' });
            title?.focus({ preventScroll: true });
        } catch (error) {
            if (signal.aborted || version !== this.routeVersion) return;
            host.replaceChildren(
                heading('Could not load this page', 'Please try again.'),
                el('p', { class: 'notice error', role: 'alert' }, error.message),
                el(
                    'div',
                    { class: 'actions' },
                    button('Try again', () => this.render()),
                    [401, 403].includes(error.status) &&
                        link('Sign in again', '/login/discord?return_to=' + encodeURIComponent(safeReturn(path + location.search)), 'button secondary'),
                ),
            );
        } finally {
            if (version === this.routeVersion) host.removeAttribute('aria-busy');
        }
    }
}
customElements.define('conway-app', ConwayApp);
