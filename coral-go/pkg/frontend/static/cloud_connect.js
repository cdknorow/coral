/* Connect to Coral Hub (the cloud connector): Settings > Coral Hub and the first-launch
 * screen (#3221) share this one implementation. Each renders into a container whose parts
 * carry data-cloud roles (status, code, link, usercode, connect, cancel, disconnect,
 * command-row, command, copy, mode-row, max-mode, mode-desc, auto-accept-row,
 * allow-auto-accept); a container may leave any of them out.
 *
 * Talks only to this server's /api/cloud/* routes. Every value from the server is
 * written with textContent / setAttribute, never as HTML, and the verification link
 * is only used if it is an http(s) URL. The server token is never sent to the page. */

import { copyText } from './utils.js';

const STATUS_URL = '/api/cloud/status';
export const MODE_HELP = {
    view: 'Read everything: every agent\'s terminal output and chat, all board chat on every team, files, tasks and boards. Nothing can be typed, changed or started from the web.',
    interact: 'Also type into agents that are already running (send text, keys, answer prompts) and change tasks, notes and the board. Typing can run commands: an agent with shell tools acts on it. It can also change board settings, including turning on cross-team messaging.',
    launch: 'Also start, wake and restart agents and browse folders to choose where. Launching runs code on this computer as you.',
};
// Said when this page is not the dashboard on the computer running Coral (the cloud routes
// answer 403): connecting has to happen there.
export const ELSEWHERE_TEXT = 'Connecting has to happen on the computer running Coral. On that computer, run: coral cloud connect';
export const CONNECT_COMMAND = 'coral cloud connect';
// Said when this server has no hub (a dev build, or --cloud-url off); the CLI says the same (#3285).
export const OFF_TEXT = "Coral Hub isn't configured on this server. Start Coral with --cloud-url https://app.coralai.ai (or CORAL_CLOUD_URL=https://app.coralai.ai), then run: coral cloud connect";

const ENROLLED = ['connecting', 'connected', 'retrying', 'revoked', 'key_unreadable', 'upgrade_required'];
const LIVE = ['enrolling', 'connecting', 'retrying'];

function post(method, url, body) {
    return fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body || {}),
    });
}

/** Enrolled, but the tunnel is down and retrying (the hub approved the code, then was
 *  unreachable): the server is connected to the hub as far as the user is concerned. A
 *  'retrying' without an enrollment (no server id or label) is still not connected. */
export function enrolledRetrying(st) {
    return !!st && st.state === 'retrying' && !!(st.server_id || st.label);
}

export function describe(st) {
    switch (st.state) {
        case 'off':
            return OFF_TEXT;
        case 'idle':
            if (codeExpired(st)) return EXPIRED_TEXT + HUB_ACCESS_URL;
            return st.last_error || ('Ready to connect to ' + st.hub_url + '. Nothing is sent until you connect.');
        case 'enrolling':
            return 'Waiting for you to approve…';
        case 'connecting':
            return 'Connecting' + (st.label ? ' as "' + st.label + '"' : '') + '…';
        case 'connected':
            return 'Connected to Coral Hub' + (st.label ? ' as ' + st.label : '') + '.';
        case 'retrying':
            if (enrolledRetrying(st)) return 'Connected to Coral Hub' + (st.label ? ' as ' + st.label : '') + ' (reconnecting…). Coral keeps working locally meanwhile.';
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

// Coral Hub never says why a code went unapproved (that would leak who is in the beta), so an
// expired code may mean the user could not sign in at all: say so, with where to ask for access.
export const HUB_ACCESS_URL = 'https://coralai.ai/hub.html';
export const EXPIRED_TEXT = "The code expired before it was approved. If you couldn't sign in at app.coralai.ai, Coral Hub is in closed beta: request access at ";

/** An idle status after a code expired (last_error_code from #3285; older servers only say it in last_error). */
export function codeExpired(st) {
    return st.state === 'idle' && (st.last_error_code === 'code_expired' || /code expired/i.test(st.last_error || ''));
}

function safeHref(u) {
    try {
        const p = new URL(u);
        return (p.protocol === 'https:' || p.protocol === 'http:') ? p.href : '';
    } catch { return ''; }
}

/** One connect panel: `root` holds the data-cloud parts; onState(st) follows every status. */
class CloudPanel {
    constructor(root, onState) {
        this.root = root;
        this.onState = onState || (() => {});
        this.timer = null;
    }

    part(role) { return this.root.querySelector(`[data-cloud="${role}"]`); }

    say(text) {
        const s = this.part('status');
        if (s) s.textContent = text;
    }

    // The expired text with the access page as a link (Connect, shown when idle, is the retry).
    sayExpired() {
        const s = this.part('status');
        if (!s) return;
        const a = document.createElement('a');
        a.href = HUB_ACCESS_URL;
        a.target = '_blank';
        a.rel = 'noopener noreferrer';
        a.textContent = HUB_ACCESS_URL;
        a.dataset.cloud = 'access-link';
        s.replaceChildren(EXPIRED_TEXT, a);
    }

    render(st) {
        this.say(describe(st));
        if (codeExpired(st)) this.sayExpired();
        const enrolling = st.state === 'enrolling';
        const code = this.part('code');
        if (code) code.hidden = !enrolling;
        if (enrolling) {
            const href = safeHref(st.verification_url);
            const a = this.part('link');
            if (a) {
                a.textContent = href || st.hub_url;
                if (href) a.setAttribute('href', href); else a.removeAttribute('href');
            }
            const uc = this.part('usercode');
            if (uc) uc.textContent = st.user_code || '';
        }
        const show = (role, on) => { const el = this.part(role); if (el) el.hidden = !on; };
        show('command-row', false);
        show('connect', st.state === 'idle' || st.state === 'revoked');
        // While the code is shown: Cancel stops the enrollment (never undoes a connection).
        // A container without a Cancel keeps offering Disconnect, which also stops it.
        const hasCancel = !!this.part('cancel');
        show('cancel', enrolling);
        show('disconnect', ENROLLED.includes(st.state) || (enrolling && !hasCancel));
        show('mode-row', !!st.configured);
        const mode = st.max_mode || 'view';
        const sel = this.part('max-mode');
        if (sel) sel.value = mode;
        const desc = this.part('mode-desc');
        if (desc) desc.textContent = MODE_HELP[mode];
        // Auto-accept from the hub (#3182): a local kill switch, shown only with Launch (the owner's opt-in),
        // on unless the owner turned it off here. Only changeable on this computer.
        show('auto-accept-row', mode === 'launch');
        const allow = this.part('allow-auto-accept');
        if (allow) allow.checked = st.allow_hub_auto_accept !== false;
        this.onState(st);
    }

    async load() {
        try {
            const r = await fetch(STATUS_URL);
            if (r.status === 403) { this.elsewhere(await r.json().catch(() => null)); return; }
            if (!r.ok) throw new Error(String(r.status));
            const st = await r.json();
            this.render(st);
            this.schedule(st);
        } catch {
            this.say('Could not read the connector status.');
            this.onState(null);
        }
    }

    // Not the dashboard on the computer running Coral. A server with #3285 says so itself
    // (code local_dashboard_required, with the command to run there); an older one only
    // answers 403, and the same sentence is said here.
    elsewhere(body) {
        const own = body && body.code === 'local_dashboard_required' && body.error;
        this.say(own ? body.error : ELSEWHERE_TEXT);
        const command = (own && body.command) || CONNECT_COMMAND;
        for (const role of ['code', 'connect', 'cancel', 'disconnect', 'mode-row']) { const el = this.part(role); if (el) el.hidden = true; }
        const row = this.part('command-row');
        if (row) {
            row.hidden = false;
            const c = this.part('command');
            if (c) c.textContent = command;
        }
        this.command = command;
        this.onState({ state: 'elsewhere' });
    }

    // Poll while something is changing and the panel is on screen.
    schedule(st) {
        clearTimeout(this.timer);
        // loading (#3285): the connector is still reading its stored enrollment, so an
        // enrolled server can look idle for a moment.
        if ((LIVE.includes(st.state) || st.loading) && this.root.offsetParent !== null) {
            this.timer = setTimeout(() => this.load(), 3000);
        }
    }

    async act(method, url, body) {
        const r = await post(method, url, body);
        if (r.ok) { this.render(await r.json()); this.schedule(await (await fetch(STATUS_URL)).json()); return; }
        if (r.status === 403) { this.elsewhere(await r.json().catch(() => null)); return; }
        let msg = 'Request failed';
        try { msg = (await r.json()).error || msg; } catch { /* keep default */ }
        this.say(msg);
    }

    bind() {
        if (this.root.dataset.cloudBound) return;
        this.root.dataset.cloudBound = '1';
        this.part('connect')?.addEventListener('click', () => this.act('POST', '/api/cloud/connect'));
        this.part('disconnect')?.addEventListener('click', () => this.act('POST', '/api/cloud/disconnect'));
        this.part('cancel')?.addEventListener('click', () => this.cancel());
        this.part('copy')?.addEventListener('click', async () => {
            const btn = this.part('copy');
            const ok = await copyText(this.command || CONNECT_COMMAND);
            btn.textContent = ok ? 'Copied' : 'Copy failed';
            setTimeout(() => { btn.textContent = 'Copy'; }, 1500);
        });
        this.part('max-mode')?.addEventListener('change', (e) => this.act('PUT', '/api/cloud/max-mode', { mode: e.target.value }));
        this.part('allow-auto-accept')?.addEventListener('change', (e) => this.act('PUT', '/api/cloud/allow-auto-accept', { allow: e.target.checked }));
    }

    // Cancel an enrollment in progress (#3285). A server from before that endpoint answers
    // 404: Disconnect stops an enrollment too.
    async cancel() {
        const r = await post('POST', '/api/cloud/connect/cancel');
        if (r.status === 404 || r.status === 405) { await this.act('POST', '/api/cloud/disconnect'); return; }
        if (r.ok) { await this.load(); return; }
        if (r.status === 403) { this.elsewhere(await r.json().catch(() => null)); return; }
        let msg = 'Request failed';
        try { msg = (await r.json()).error || msg; } catch { /* keep default */ }
        this.say(msg);
    }

    stop() { clearTimeout(this.timer); }
}

/** Mount the connect flow in `root` and load its status. Returns the panel (load(), stop()). */
export function mountCloudConnect(root, { onState } = {}) {
    if (!root) return null;
    if (root._cloudPanel) { root._cloudPanel.onState = onState || root._cloudPanel.onState; root._cloudPanel.load(); return root._cloudPanel; }
    const p = new CloudPanel(root, onState);
    root._cloudPanel = p;
    p.bind();
    p.load();
    return p;
}

// Settings > Coral Hub.
export function initCloudConnect() {
    const root = document.getElementById('settings-cloud');
    if (root && !root._cloudPanel) { const p = new CloudPanel(root); root._cloudPanel = p; p.bind(); }
}

export async function loadCloudStatus() {
    const root = document.getElementById('settings-cloud');
    if (!root) return;
    if (!root._cloudPanel) initCloudConnect();
    await root._cloudPanel.load();
}
