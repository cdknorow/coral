/* Two-phase server enrollment.
 *
 * Phase 1 (preview): look the code up and show WHO is asking: the connecting
 * machine's IP, hostname, Coral version and time. Nothing is granted.
 * Phase 2 (approve): only an explicit click confirms, after a plain warning
 * that approving gives that machine access to this account.
 *
 * Every value from the server is rendered with textContent. */

import { safeNext, getSession, postJSON, setStatus } from './common.js';

const $ = id => document.getElementById(id);
const CODE_RE = /^[A-Z0-9]{4}-?[A-Z0-9]{4}$/;

function normalizeCode(v) {
    const c = String(v || '').toUpperCase().replace(/[^A-Z0-9]/g, '');
    return c.length === 8 ? c.slice(0, 4) + '-' + c.slice(4) : String(v || '').toUpperCase().trim();
}

function fact(dl, name, value) {
    const dt = document.createElement('dt'); dt.textContent = name;
    const dd = document.createElement('dd'); dd.textContent = value || 'unknown';
    dl.append(dt, dd);
}

function fmtTime(ts) {
    const t = Date.parse(ts);
    return Number.isNaN(t) ? 'unknown' : new Date(t).toLocaleString() + ` (${Math.max(0, Math.round((Date.now() - t) / 1000))}s ago)`;
}

(async function init() {
    const here = location.pathname + location.search;
    const s = await getSession();
    if (!s.authenticated) { location.replace('/signin?next=' + encodeURIComponent(safeNext(here, '/enroll'))); return; }

    const entry = $('entry'), review = $('review'), done = $('done');
    const status = $('status');
    const codeInput = $('code');
    let csrf = s.csrf;
    let code = '';

    let nonce = '';

    async function preview(raw) {
        code = normalizeCode(raw);
        if (!CODE_RE.test(code)) { setStatus(status, 'Enter the code shown on your machine (XXXX-XXXX).', 'error'); return; }
        setStatus(status, 'Looking up…');
        const r = await postJSON('/api/enroll/preview', { user_code: code }, csrf);
        if (r.status === 404) { setStatus(status, 'That code is invalid, expired, or already used. Start again on your machine.', 'error'); return; }
        if (r.status === 429) { setStatus(status, 'Too many attempts. Wait a few minutes and try again.', 'error'); return; }
        if (!r.ok || !r.data || !r.data.nonce) { setStatus(status, 'Could not check the code. Try again.', 'error'); return; }
        const q = r.data.requester || {};
        nonce = r.data.nonce;
        // What the hub itself observed goes first: it cannot be faked by the machine.
        const seen = $('seen'); seen.replaceChildren();
        fact(seen, 'Code', code);
        fact(seen, 'From IP address', q.ip);
        fact(seen, 'Requested', fmtTime(q.requested_at));
        const rep = $('reported'); rep.replaceChildren();
        fact(rep, 'Machine name', q.hostname);
        fact(rep, 'Coral version', q.coral_version);
        $('label').value = (q.hostname || '').slice(0, 100);
        entry.hidden = true; review.hidden = false;
        setStatus(status, '');
        $('label').focus();
    }

    $('entry-form').addEventListener('submit', e => { e.preventDefault(); preview(codeInput.value); });

    $('cancel').addEventListener('click', () => { review.hidden = true; entry.hidden = false; codeInput.value = ''; code = ''; nonce = ''; setStatus(status, ''); });

    $('approve').addEventListener('click', async () => {
        const label = $('label').value.trim();
        if (!label) { setStatus(status, 'Enter a name for this machine.', 'error'); return; }
        $('approve').disabled = true;
        setStatus(status, 'Approving…');
        const r = await postJSON('/api/enroll/confirm', { user_code: code, label, nonce }, csrf);
        if (r.ok) {
            review.hidden = true; done.hidden = false; setStatus(status, '');
        } else if (r.status === 409) {
            setStatus(status, 'You have reached the limit of connected machines. Revoke one first.', 'error'); $('approve').disabled = false;
        } else if (r.status === 404) {
            setStatus(status, 'That code is no longer valid. Start again on your machine.', 'error'); $('approve').disabled = false;
        } else if (r.status === 429) {
            setStatus(status, 'Too many attempts. Wait a few minutes and try again.', 'error'); $('approve').disabled = false;
        } else {
            setStatus(status, 'Could not confirm approval. Check whether the machine connected before trying again.', 'error'); $('approve').disabled = false;
        }
    });

    const q = new URLSearchParams(location.search).get('code');
    if (q) { codeInput.value = normalizeCode(q); preview(q); }
    else codeInput.focus();
})();
