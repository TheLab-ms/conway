import qrcode from './vendor/qrcode.mjs';
import { api, state, el, field, check, link, button, panel, form, heading, notice, date, safeReturn } from './lib.js';

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
    let generation = 0;
    const claimPanel = el('div', { class: 'section-gap' });
    const stop = () => {
        generation++;
        clearTimeout(timer);
    };
    signal.addEventListener('abort', stop, { once: true });
    const enrollmentForm = form(
        [
            field('Fob ID', 'fob_id', '', {
                type: 'number',
                min: 1,
                max: 4294967295,
                step: 1,
                required: true,
                autocomplete: 'off',
                hint: 'Scan or enter the numeric ID of the fob you want to enroll.',
            }),
        ],
        'Create enrollment QR',
        async (data, node) => {
            stop();
            claimPanel.replaceChildren();
            const fob = Number(data.get('fob_id'));
            if (!Number.isSafeInteger(fob) || fob < 1 || fob > 4294967295) throw new Error('Enter a valid fob ID from 1 to 4294967295.');
            const claim = await api('/api/kiosk/claims', {
                method: 'POST',
                body: { fob_id: fob },
                signal,
            });
            if (signal.aborted) return;
            const url = new URL(claim.url, location.origin);
            if (url.origin !== location.origin || url.pathname !== '/fobs/bind') throw new Error('The server returned an invalid enrollment link. Please contact leadership.');
            const current = generation;
            const qr = el('qr-code');
            qr.value = url.href;
            const status = el('div', { class: 'notice', role: 'status', 'aria-live': 'polite' }, 'Waiting for the member to link this fob...');
            const expires = el('p', { class: 'hint' }, `Expires ${date(claim.expires)}. Keep this code private.`);
            const copyStatus = el('p', { class: 'hint', role: 'status' });
            const copy = button('Copy enrollment link', async () => {
                try {
                    await navigator.clipboard.writeText(url.href);
                    copyStatus.textContent = 'Enrollment link copied.';
                } catch {
                    copyStatus.textContent = 'Clipboard unavailable. Select and copy the link below instead.';
                }
            });
            const reset = button('Enroll another fob', () => {
                stop();
                claimPanel.replaceChildren();
                node.reset();
                node.querySelector('input').focus();
            });
            claimPanel.replaceChildren(
                el(
                    'section',
                    { class: 'panel qr-panel' },
                    el('h2', {}, `Link fob ${fob}`),
                    el('p', {}, 'Scan this code with your own phone. Sign in with Discord, then confirm the link.'),
                    qr,
                    link(url.href, url.href, 'claim-link'),
                    expires,
                    el('div', { class: 'actions' }, copy, reset),
                    copyStatus,
                    status,
                ),
            );
            const poll = async () => {
                if (signal.aborted || current !== generation) return;
                if (Number(claim.expires) * 1000 <= Date.now()) {
                    claimPanel.replaceChildren(panel('Enrollment expired', el('p', {}, 'This code can no longer be used. Create a new enrollment code to try again.'), reset));
                    return;
                }
                try {
                    const result = await api(`/api/kiosk/claims/${encodeURIComponent(claim.token)}`, {
                        signal,
                    });
                    if (signal.aborted || current !== generation) return;
                    if (result.claimed) {
                        claimPanel.replaceChildren(
                            panel(
                                'Fob linked',
                                el('p', { class: 'notice', role: 'status' }, `Fob ${fob} has been claimed. Building access still depends on the member's membership status.`),
                                reset,
                            ),
                        );
                        return;
                    }
                    notice(status, 'Waiting for the member to link this fob...');
                } catch (error) {
                    if (signal.aborted || current !== generation) return;
                    notice(status, error.message, true);
                    if ([401, 403, 404, 410].includes(error.status)) {
                        qr.remove();
                        claimPanel.querySelector('.claim-link')?.remove();
                        copy.remove();
                        return;
                    }
                }
                timer = setTimeout(poll, 2500);
            };
            timer = setTimeout(poll, 1500);
            return 'Enrollment code ready. Complete linking on your own device.';
        },
    );
    return el(
        'div',
        { class: 'narrow' },
        heading('A key to your space.', 'Fob enrollment / trusted on-site kiosk'),
        panel(
            'Enroll an access fob',
            el(
                'p',
                { class: 'muted' },
                'This page works only from a configured, trusted kiosk network address. Members should sign in on their own phone, never on the shared kiosk.',
            ),
            enrollmentForm,
        ),
        claimPanel,
    );
}

export function bind(app) {
    const token = new URLSearchParams(location.search).get('token');
    if (!token || !/^[a-f0-9]{64}$/.test(token))
        return el(
            'div',
            { class: 'narrow' },
            heading('Link your fob'),
            panel(
                'Enrollment link needed',
                el('p', {}, 'Scan a fresh QR code at the space enrollment kiosk. The link must contain a valid enrollment token.'),
                link('Back to dashboard', '/dashboard', 'button secondary'),
            ),
        );
    if (!state.session.member) {
        const target = safeReturn(location.pathname + location.search);
        return el(
            'div',
            { class: 'narrow' },
            heading('Your fob, your membership.', 'Sign in on your own device to securely claim this access fob.'),
            panel(
                'Sign in to continue',
                el('p', {}, 'The enrollment link expires shortly. If it expires while you sign in, create a new code at the kiosk.'),
                state.session.signup
                    ? link('Finish creating membership', '/signup?return_to=' + encodeURIComponent(target), 'button')
                    : link('Continue with Discord', '/login/discord?return_to=' + encodeURIComponent(target), 'button'),
            ),
        );
    }
    return el(
        'div',
        { class: 'narrow' },
        heading('Link your access fob', 'Only claim a code created for the fob in your possession.'),
        panel(
            'Confirm fob enrollment',
            el('p', {}, `This fob will be linked to ${state.session.member.name || 'your membership'}. Linking a fob does not change your membership or waiver status.`),
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
                    app.navigate('/dashboard', 'Your access fob is now linked.', true);
                },
            ),
        ),
    );
}
