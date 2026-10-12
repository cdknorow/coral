/* Settings > Coral Hub (cloud connector).
 *
 * Talks only to this server's /api/cloud/* routes. Every value from the server is
 * written with textContent / setAttribute, never as HTML, and the verification link
 * is only used if it is an http(s) URL. The server token is never sent to the page. */

const STATUS_URL = '/api/cloud/status';
const MODE_HELP = {
    view: 'Read the terminal, chat, files, tasks and board. Nothing can be typed or started from the web.',
    interact: 'Also type into agents that are already running (send text, keys, answer prompts) and change tasks, notes and the board. Typing can run commands: an agent with shell tools acts on it. It can also change board settings, including turning on cross-team messaging.',
    launch: 'Also start, wake and restart agents and browse folders to choose where. Launching runs code on this computer as you.',
};
let timer = null;

const $ = (id) => document.getElementById(id);

function post(method, url, body) {
    return fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body || {}),
    });
}

function describe(st) {
    switch (st.state) {
        case 'off':
            return 'Not enabled. Start Coral with --cloud-url https://your-hub (or set CORAL_CLOUD_URL) to enable it. Until then this server makes no connection to the cloud.';
        case 'idle':
            return st.last_error || ('Ready to connect to ' + st.hub_url + '.');
        case 'enrolling':
            return 'Waiting for you to confirm the code on the hub…';
        case 'connecting':
            return 'Connecting' + (st.label ? ' as "' + st.label + '"' : '') + '…';
        case 'connected':
            return 'Connected' + (st.label ? ' as "' + st.label + '"' : '') + ' to ' + st.hub_url + '.';
        case 'retrying':
            return 'Not connected right now (' + (st.last_error || 'retrying') + '). Coral keeps working locally and will reconnect on its own.';
        case 'revoked':
            return 'The hub revoked this server, so Coral stopped retrying. Connect again to re-enroll.';
        case 'key_unreadable':
            return st.last_error;
        case 'upgrade_required':
            return st.last_error;
        default:
            return st.state;
    }
}

function safeHref(u) {
    try {
        const p = new URL(u);
        return (p.protocol === 'https:' || p.protocol === 'http:') ? p.href : '';
    } catch { return ''; }
}

function render(st) {
    $('settings-cloud-status').textContent = describe(st);
    const enrolling = st.state === 'enrolling';
    $('settings-cloud-code').hidden = !enrolling;
    if (enrolling) {
        const href = safeHref(st.verification_url);
        const a = $('settings-cloud-link');
        a.textContent = href || st.hub_url;
        if (href) a.setAttribute('href', href); else a.removeAttribute('href');
        $('settings-cloud-usercode').textContent = st.user_code || '';
    }
    const enrolled = ['connecting', 'connected', 'retrying', 'revoked', 'key_unreadable', 'upgrade_required'].includes(st.state);
    $('settings-cloud-connect').hidden = !(st.state === 'idle' || st.state === 'revoked');
    $('settings-cloud-disconnect').hidden = !(enrolled || enrolling);
    $('settings-cloud-mode-row').hidden = !st.configured;
    $('settings-cloud-max-mode').value = st.max_mode || 'view';
    $('settings-cloud-mode-desc').textContent = MODE_HELP[st.max_mode || 'view'];
}

export async function loadCloudStatus() {
    try {
        const r = await fetch(STATUS_URL);
        if (!r.ok) throw new Error(String(r.status));
        const st = await r.json();
        render(st);
        schedule(st);
    } catch {
        $('settings-cloud-status').textContent = 'Could not read the connector status.';
    }
}

// Poll while something is changing and the settings dialog is on screen.
function schedule(st) {
    clearTimeout(timer);
    const live = ['enrolling', 'connecting', 'retrying'].includes(st.state);
    if (live && $('settings-cloud') && $('settings-cloud').offsetParent !== null) {
        timer = setTimeout(loadCloudStatus, 3000);
    }
}

async function act(method, url, body) {
    const r = await post(method, url, body);
    if (r.ok) { render(await r.json()); schedule(await (await fetch(STATUS_URL)).json()); return; }
    let msg = 'Request failed';
    try { msg = (await r.json()).error || msg; } catch { /* keep default */ }
    $('settings-cloud-status').textContent = msg;
}

export function initCloudConnect() {
    const c = $('settings-cloud-connect');
    if (!c || c.dataset.bound) return;
    c.dataset.bound = '1';
    c.addEventListener('click', () => act('POST', '/api/cloud/connect'));
    $('settings-cloud-disconnect').addEventListener('click', () => act('POST', '/api/cloud/disconnect'));
    $('settings-cloud-max-mode').addEventListener('change', (e) =>
        act('PUT', '/api/cloud/max-mode', { mode: e.target.value }));
}
