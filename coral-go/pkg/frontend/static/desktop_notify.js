/* Desktop alerts for "an agent needs input" (N1).
 *
 * Deliberately small: the page raises a Notification itself while it is open in a background tab. No service worker, no
 * push subscription, nothing stored on the server. It is off until this browser opts in, and the opt-in lives in this
 * browser's localStorage only. The text carries names, never agent output: no summary, prompt, path or tool input. */
export const DESKTOP_NOTIFY_KEY = 'coral-desktop-notify';
const MAX_NAME = 60;

function api() { return typeof Notification === 'undefined' ? null : Notification; }
function read() { try { return localStorage.getItem(DESKTOP_NOTIFY_KEY) === '1'; } catch { return false; } }
function write(on) { try { if (on) localStorage.setItem(DESKTOP_NOTIFY_KEY, '1'); else localStorage.removeItem(DESKTOP_NOTIFY_KEY); } catch { /* storage blocked */ } }

export function desktopNotifySupported() { return !!api(); }

/** On only when this browser opted in AND the browser still grants permission. */
export function desktopNotifyEnabled() {
    const N = api();
    return !!N && read() && N.permission === 'granted';
}

/** Ask for permission (call from a click) and remember the answer. Resolves to whether alerts are now on. */
export async function enableDesktopNotify() {
    const N = api();
    if (!N) return false;
    let perm = N.permission;
    if (perm === 'default') {
        try { perm = await N.requestPermission(); } catch { perm = 'denied'; }
    }
    const on = perm === 'granted';
    write(on);
    return on;
}

export function disableDesktopNotify() { write(false); }

/** A display name reduced to short plain text: control and bidi characters removed, length capped. */
export function safeName(v) {
    const s = String(v == null ? '' : v).replace(/[\u0000-\u001f\u007f-\u009f‎‏‪-‮⁦-⁩]/g, ' ').replace(/\s+/g, ' ').trim();
    return s.length > MAX_NAME ? s.slice(0, MAX_NAME - 1) + '…' : s;
}

/** The only text an alert may carry. */
export function alertText(agent, where) {
    const a = safeName(agent) || 'An agent';
    const w = safeName(where);
    return { title: 'Coral', body: w ? `${a} needs input on ${w}` : `${a} needs input` };
}

/** Raise one alert for a session that just started waiting. Only while the page is hidden. Returns the alert or null. */
export function notifyNeedsInput({ agent, where, tag, onClick }) {
    const N = api();
    if (!N || !desktopNotifyEnabled()) return null;
    if (typeof document !== 'undefined' && !document.hidden) return null; // the in-page toast covers a visible page
    const { title, body } = alertText(agent, where);
    try {
        const n = new N(title, { body, tag: tag ? `needs-input:${String(tag).slice(0, 200)}` : undefined, silent: false });
        n.onclick = () => {
            try { window.focus(); } catch { /* ignore */ }
            if (onClick) onClick();
            n.close();
        };
        return n;
    } catch { return null; }
}
