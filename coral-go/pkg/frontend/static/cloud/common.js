/* Shared helpers for the Cloud Hub's standalone pages (sign-in, enroll). No
 * dependencies and no inline script, so they run under a strict CSP. */

/** Same-origin path check for ?next=: one leading "/", no scheme, no backslash, no control chars. */
export function safeNext(raw, fallback = '/') {
    if (typeof raw !== 'string' || raw === '') return fallback;
    if (raw[0] !== '/' || raw[1] === '/' || raw[1] === '\\') return fallback;
    if (/[\\\u0000-\u001f\u007f]/.test(raw) || /%2f|%5c/i.test(raw)) return fallback; // no raw or encoded slash tricks
    try {
        const u = new URL(raw, location.origin);
        if (u.origin !== location.origin) return fallback;
        return u.pathname + u.search + u.hash;
    } catch { return fallback; }
}

export async function getSession() {
    try {
        const r = await fetch('/auth/session', { credentials: 'same-origin', headers: { Accept: 'application/json' } });
        return r.ok ? await r.json() : { authenticated: false };
    } catch { return { authenticated: false }; }
}

export async function postJSON(path, body, csrf) {
    const r = await fetch(path, {
        method: 'POST', credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
        body: JSON.stringify(body),
    });
    let data = {};
    try { data = await r.json(); } catch { /* empty body */ }
    return { ok: r.ok, status: r.status, data };
}

/** Set a status line (textContent only). kind: '' | 'error' | 'ok'. */
export function setStatus(el, msg, kind = '') {
    el.textContent = msg;
    el.className = 'cloud-status' + (kind ? ' ' + kind : '');
}
