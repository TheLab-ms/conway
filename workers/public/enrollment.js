import qrcode from './vendor/qrcode.mjs';
import { api, state, el, check, link, button, panel, form, heading, notice, date, safeReturn } from './lib.js';

class QRCode extends HTMLElement {
    set value(value) {
        const qr = qrcode(0, 'M');
        qr.addData(value);
        qr.make();
        const count = qr.getModuleCount();
        const scale = 6;
        const quiet = 4;
        const canvas = el('canvas', {
            width: (count + quiet * 2) * scale,
            height: (count + quiet * 2) * scale,
            role: 'img',
            'aria-label': 'Enrollment QR code. The same link is available below.',
        });
        const context = canvas.getContext('2d');
        context.fillStyle = '#ffffff';
        context.fillRect(0, 0, canvas.width, canvas.height);
        context.fillStyle = '#000000';
        for (let row = 0; row < count; row++)
            for (let column = 0; column < count; column++) {
                if (qr.isDark(row, column)) context.fillRect((column + quiet) * scale, (row + quiet) * scale, scale, scale);
            }
        this.replaceChildren(canvas);
    }
}
customElements.define('qr-code', QRCode);

export function kiosk(app, signal) {
    let timer;
    let idleTimer;
    let expiryTimer;
    let generation = 0;
    const scanner = el('input', {
        class: 'kiosk-scanner',
        type: 'text',
        'aria-label': 'Fob scanner',
        tabindex: '-1',
        autocomplete: 'off',
        inputmode: 'none',
        spellcheck: 'false',
    });
    const status = el('div', { class: 'notice', role: 'status', 'aria-live': 'polite' });
    const claimPanel = el('div');
    const interactive = (target) =>
        target instanceof Element &&
        target.closest(
            'input, textarea, select, button, a, summary, [contenteditable]:not([contenteditable="false"]), [role="button"], [role="textbox"], [tabindex]:not([tabindex="-1"])',
        );
    const focus = () => {
        if (!signal.aborted && scanner.isConnected && !interactive(document.activeElement)) scanner.focus({ preventScroll: true });
    };
    const stop = () => {
        generation++;
        clearTimeout(timer);
        clearTimeout(idleTimer);
        clearTimeout(expiryTimer);
    };
    const reset = (message = '', error = false) => {
        stop();
        scanner.value = '';
        claimPanel.replaceChildren();
        notice(status, message, error);
        focus();
    };
    const submit = async () => {
        clearTimeout(idleTimer);
        if (signal.aborted || !scanner.value) return;
        const value = scanner.value;
        reset();
        const current = generation;
        try {
            const fob = Number(value);
            if (!/^\d+$/.test(value) || !Number.isSafeInteger(fob) || fob < 1 || fob > 4294967295) throw new Error('Fob not recognized. Scan again.');
            notice(status, 'Reading fob...');
            const claim = await api('/api/kiosk/claims', {
                method: 'POST',
                body: { fob_id: fob },
                signal,
            });
            if (signal.aborted || current !== generation) return;
            const url = new URL(claim.url, location.origin);
            if (url.origin !== location.origin || url.pathname !== '/fobs/bind') throw new Error('The server returned an invalid enrollment link. Please contact leadership.');
            const qr = el('qr-code');
            qr.value = url.href;
            const expires = el('p', { class: 'hint' }, `Expires ${date(claim.expires)}.`);
            const copyStatus = el('p', { class: 'hint', role: 'status' });
            const copy = button('Copy enrollment link', async () => {
                try {
                    await navigator.clipboard.writeText(url.href);
                    copyStatus.textContent = 'Enrollment link copied.';
                } catch {
                    copyStatus.textContent = 'Clipboard unavailable. Scan the QR code with your phone instead.';
                }
            });
            claimPanel.replaceChildren(
                el(
                    'section',
                    { class: 'panel qr-panel' },
                    el('h2', {}, 'Link your fob'),
                    el('p', {}, 'Scan the QR code with your phone. Sign in, then confirm to link your fob.'),
                    qr,
                    link('Open enrollment link', url.href, 'claim-link'),
                    expires,
                    el(
                        'div',
                        { class: 'actions' },
                        copy,
                        button('Done', () => reset()),
                    ),
                    copyStatus,
                ),
            );
            notice(status, '');
            expiryTimer = setTimeout(() => reset('Code expired. Scan your fob again.'), Math.max(0, Number(claim.expires) * 1000 - Date.now()));
            const poll = async () => {
                if (signal.aborted || current !== generation) return;
                try {
                    const result = await api(`/api/kiosk/claims/${encodeURIComponent(claim.token)}`, {
                        signal,
                    });
                    if (signal.aborted || current !== generation) return;
                    if (result.claimed) {
                        reset('Fob linked. Ready for the next scan.');
                        return;
                    }
                    notice(status, '');
                } catch (error) {
                    if (signal.aborted || current !== generation) return;
                    if ([401, 403, 404, 410].includes(error.status)) {
                        reset(error.message, true);
                        return;
                    }
                    notice(status, error.message, true);
                }
                timer = setTimeout(poll, 2500);
            };
            timer = setTimeout(poll, 1500);
        } catch (error) {
            if (!signal.aborted && current === generation) reset(error.message, true);
        }
    };
    const idle = () => {
        clearTimeout(idleTimer);
        if (scanner.value) idleTimer = setTimeout(submit, 1000);
    };
    scanner.addEventListener('input', idle, { signal });
    document.addEventListener(
        'keydown',
        (event) => {
            if (signal.aborted || !scanner.isConnected || event.defaultPrevented || event.isComposing || event.ctrlKey || event.metaKey || event.altKey) return;
            if (event.target !== scanner && interactive(event.target)) return;
            if (event.key === 'Enter') {
                if (!scanner.value) return;
                event.preventDefault();
                void submit();
            } else if (event.key.length === 1 && event.target !== scanner) {
                event.preventDefault();
                scanner.value += event.key;
                focus();
                idle();
            }
        },
        { signal },
    );
    document.addEventListener(
        'focusin',
        (event) => {
            if (event.target !== scanner && interactive(event.target)) {
                clearTimeout(idleTimer);
                scanner.value = '';
            }
        },
        { signal },
    );
    document.addEventListener('click', focus, { signal });
    window.addEventListener('focus', focus, { signal });
    // The router inserts this view and focuses its heading after kiosk() returns.
    const focusTimer = setTimeout(focus, 0);
    signal.addEventListener(
        'abort',
        () => {
            stop();
            clearTimeout(focusTimer);
        },
        { once: true },
    );
    return el(
        'div',
        { class: 'narrow kiosk' },
        scanner,
        el('h1', { tabindex: '-1' }, 'Scan a key fob'),
        el('p', {}, 'Use the fob reader to link a fob to your account.'),
        status,
        claimPanel,
        link('Sign waiver', '/waiver', 'button secondary'),
    );
}

export function bind(app) {
    const token = new URLSearchParams(location.search).get('token');
    if (!token || !/^[a-f0-9]{64}$/.test(token))
        return el(
            'div',
            { class: 'narrow' },
            heading('Link your fob'),
            panel('Enrollment link needed', el('p', {}, 'Scan a new QR code at the kiosk.'), link('Back to billing', '/billing', 'button secondary')),
        );
    if (!state.session.member) {
        const target = safeReturn(location.pathname + location.search);
        return el(
            'div',
            { class: 'narrow' },
            heading('Link your fob', 'Sign in on your phone to link this fob.'),
            panel(
                'Sign in to continue',
                el('p', {}, 'The enrollment link expires shortly. If it expires while you sign in, create a new code at the kiosk.'),
                link('Continue with Discord', '/login/discord?return_to=' + encodeURIComponent(target), 'button'),
            ),
        );
    }
    return el(
        'div',
        { class: 'narrow' },
        heading('Link your access fob', 'Only claim a code created for the fob in your possession.'),
        panel(
            'Confirm fob enrollment',
            el('p', {}, `Link this fob to ${state.session.member.discord_username || 'your account'}. This does not change your membership or waiver status.`),
            state.session.member.fob_id &&
                el('p', { class: 'notice warn' }, `Your account already has fob ${state.session.member.fob_id}. Confirm only if you intend to replace it.`),
            form(
                [
                    check('This is my fob, and I want to link it to my membership.', 'confirm', false, {
                        required: true,
                    }),
                ],
                'Link fob to my membership',
                async () => {
                    await api('/api/fobs/bind', { method: 'POST', body: { token } });
                    await app.refreshSession();
                    app.navigate('/billing', 'Your access fob is now linked.', true);
                },
            ),
        ),
    );
}
