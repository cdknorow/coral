/* Connect to Coral Hub (the cloud connector): Settings > Coral Hub and the first-launch
 * screen (#3221) share this one implementation. Each renders into a container whose parts
 * carry data-cloud roles (status, code, link, usercode, connect, cancel, disconnect,
 * command-row, command, copy, ask-row (the Connect screen's question), mode-row, max-mode, mode-desc, auto-accept-row,
 * allow-auto-accept, hub-cross-team, hub-operator-row, hub-operator, hub-operator-msg, hub-operator-actions,
 * hub-operator-actions-list); a container may
 * leave any of them out.
 *
 * Talks only to this server's /api/cloud/* routes. Every value from the server is
 * written with textContent / setAttribute, never as HTML, and the verification link
 * is only used if it is an http(s) URL. The server token is never sent to the page. */

import { copyText } from './utils.js';
import { importOnce } from './import_once.js';

const STATUS_URL = '/api/cloud/status';
// 'Let Coral Hub act as the Operator' (#3377): a local-only owner setting. The route answers only
// on this computer (404 through the hub), so the row is shown only when it answers 200.
const HUB_OPERATOR_URL = '/api/system/hub-operator';
 const MODE_HELP = {
    view: 'Read everything: every agent\'s terminal output and chat, all board chat on every team, files, tasks and boards. Nothing can be typed, changed or started from the web.',
    interact: 'Also type into agents that are already running (send text, keys, answer prompts), post on boards, change tasks and notes, and rename or archive agents. Typing can run commands: an agent with shell tools acts on it. It can also change a team\'s working mode.',
    launch: 'Also start, stop, sleep, wake and restart agents (one agent or a whole team), browse folders to choose where, pause boards, delete board messages and boards, and turn on auto-accept. Launching runs code on this computer as you.',
};
// The one line under the Hub access select: what the chosen level means in a few words (the full text, MODE_HELP, is behind the select's (i)).
 const MODE_SHORT = { view: 'Read only.', interact: 'Can type into running agents.', launch: 'Can start agents: runs code on this computer as you.' };
// Said when this page is not the dashboard on the computer running Coral (the cloud routes
// answer 403): connecting has to happen there.
 const ELSEWHERE_TEXT = 'Connecting has to happen on the computer running Coral. On that computer, run: coral cloud connect';
 const CONNECT_COMMAND = 'coral cloud connect';
// The command a server with no hub is started with (the OFF_TEXT's first step), with a Copy button beside it (UI audit #5589, finding 21: a command to run in a terminal was text only).
 const START_COMMAND = 'coral --cloud-url https://app.coralhub.dev';
// In Settings the sentence says it once and the row holds the whole sequence, which Copy takes (UI audit #5589); the connect dialog keeps the sentence the command line says too (#3285).
 const OFF_SHORT = "Start Coral with the Hub address, then connect:";
// Said when this server has no hub (a dev build, or --cloud-url off); the CLI says the same (#3285).
 const OFF_TEXT = "Coral Hub isn't configured on this server. Start Coral with --cloud-url https://app.coralhub.dev (or CORAL_CLOUD_URL=https://app.coralhub.dev), then run: coral cloud connect";

const ENROLLED = ['connecting', 'connected', 'retrying', 'revoked', 'key_unreadable', 'upgrade_required'];
const LIVE = ['enrolling', 'connecting', 'retrying'];

function post(method, url, body) {
    return fetch(url, {
        method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body || {}),
    });
}

const cleanLabel = v => String(v == null ? '' : v).replace(/[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2066-\u2069]/g, '').trim().slice(0, 80); // display text: no control or bidi characters, bounded

/** "Turned on from Coral Hub by <user>, <time>" / "Turned off on this computer, <time>" from the status; '' when it was never changed. The state is the switch's own: the last change set it. */
export function autoAcceptChangedText(st) {
    const c = st && st.allow_hub_auto_accept_changed;
    if (!c || typeof c !== 'object' || (c.by !== 'hub' && c.by !== 'local')) return '';
    const d = typeof c.at === 'string' ? new Date(c.at) : null;
    const at = d && !Number.isNaN(d.getTime()) ? d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : '';
    const what = st.allow_hub_auto_accept === false ? 'Turned off' : 'Turned on';
    const who = c.by === 'hub' ? `from Coral Hub by ${cleanLabel(c.user) || 'a Coral Hub user'}` : 'on this computer';
    return `${what} ${who}${at ? ', ' + at : ''}`;
}

/** Enrolled, but the tunnel is down and retrying (the hub approved the code, then was
 *  unreachable): the server is connected to the hub as far as the user is concerned. A
 *  'retrying' without an enrollment (no server id or label) is still not connected. */
export function enrolledRetrying(st) {
    return !!st && st.state === 'retrying' && !!(st.server_id || st.label);
}

/** The secret key that seals the Hub token and the remote servers' API keys is refused (#3698):
 *  what a re-key discards, said before the Operator confirms. */
export function rekeyText(sk) {
    const lost = [];
    if (sk.hub_url) lost.push('the Coral Hub connection to ' + sk.hub_url + ' (connect again afterwards, and remove this computer in Coral Hub)');
    if (sk.remote_server_keys) lost.push(sk.remote_server_keys === 1 ? "1 remote server's API key (enter it again)" : sk.remote_server_keys + " remote servers' API keys (enter them again)");
    const why = sk.exposed ? 'Other accounts on this computer could read the secret key file, so what it protects must be treated as known.'
        : "Coral can't use its secret key file" + (sk.message ? ' (' + sk.message + ')' : '') + '.';
    return why + ' Replacing it makes a new key and discards ' + (lost.length ? lost.join(' and ') : 'nothing stored yet') + '.';
}

export function describe(st) {
    switch (st.state) {
        case 'off':
            return OFF_TEXT;
        case 'idle':
            if (codeExpired(st)) return EXPIRED_TEXT + HUB_ACCESS_URL;
            // The two failures of starting a connection are sentences for a person (#5962); the seconds until the next try are counted down beside them (syncRetry), not baked into the words.
            if (st.last_error_code === 'hub_unreachable') return 'Coral could not reach Coral Hub. Check your connection and try again.';
            if (st.last_error_code === 'rate_limited') return 'Coral Hub is busy. Try again in a moment.';
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
 const HUB_ACCESS_URL = 'https://coralhub.dev/hub.html';
 const EXPIRED_TEXT = "The code expired before it was approved. If you couldn't sign in at app.coralhub.dev, Coral Hub is in closed beta: request access at ";

/** An idle status after a code expired (last_error_code, #3285). */
 function codeExpired(st) {
    return st.state === 'idle' && st.last_error_code === 'code_expired';
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
        this.enrolled = undefined; // unknown until the first status: the Operator row is read then
        // The dashboard's confirm dialog (render.js is already loaded in the dashboard; imported
        // here only when needed, so this module stays light). Tests replace it.
        this.confirm = async (title, message, onYes) => (await importOnce('./render.js')).showConfirmModal(title, message, onYes);
    }

    part(role) { return this.root.querySelector(`[data-cloud="${role}"]`); }

    say(text) {
        const s = this.part('status');
        if (s) s.textContent = text;
    }

    /** After a failed start the server says how many seconds until Connect may ask again (retry_in_s): "Try again in 27 s" counts down beside the sentence (a plain element that is not a live region, so a
     *  screen reader hears the sentence once, not every second), and Connect is off until it reaches 0. */
    syncRetry(st) {
        clearInterval(this.retryTick);
        const status = this.part('status'), btn = this.part('connect');
        let el = this.root.querySelector('[data-cloud="retry"]');
        const secs = st.state === 'idle' ? Math.max(0, Math.floor(Number(st.retry_in_s) || 0)) : 0;
        if (!secs) { if (el) el.remove(); if (btn) btn.disabled = false; return; }
        if (!el && status) { el = document.createElement('div'); el.dataset.cloud = 'retry'; el.className = (status.className || '') + ' cloud-retry'; el.setAttribute('aria-live', 'off'); status.insertAdjacentElement('afterend', el); }
        const until = Date.now() + secs * 1000;
        const paint = () => {
            const left = Math.max(0, Math.ceil((until - Date.now()) / 1000));
            if (el) el.textContent = left ? `Try again in ${left} s` : '';
            if (btn) btn.disabled = left > 0;
            if (!left) { clearInterval(this.retryTick); if (el) el.remove(); }
        };
        paint();
        this.retryTick = setInterval(paint, 1000); // interval-ok: a one-second on-screen countdown (it asks nothing), cleared when it ends, on a redraw and on stop
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
        // A server with no hub says what to run: the command is in the row, with Copy (a person copies it into a terminal).
        const startRow = this.part('command-row');
        if (startRow && st.state === 'off') {
            const all = this.commandsInRow ? `${START_COMMAND}\n${CONNECT_COMMAND}` : START_COMMAND;
            show('command-row', true);
            const c = this.part('command'); if (c) c.textContent = all;
            this.command = all;
            if (this.commandsInRow) this.say(OFF_SHORT);
        }
        else { show('command-row', false); if (this.command && this.command.startsWith(START_COMMAND)) this.command = null; }
        show('connect', st.state === 'idle' || st.state === 'revoked');
        this.syncRetry(st);
        // While the code is shown: Cancel stops the enrollment (never undoes a connection).
        // A container without a Cancel keeps offering Disconnect, which also stops it.
        const hasCancel = !!this.part('cancel');
        show('cancel', enrolling);
        show('disconnect', ENROLLED.includes(st.state) || (enrolling && !hasCancel));
        show('mode-row', !!st.configured);
        const sk = st.secret_key;
        show('rekey-row', !!sk && sk.state === 'refused');
        const rt = this.part('rekey-text');
        if (rt) rt.textContent = sk ? rekeyText(sk) : '';
        this.secretKey = sk || null;
        this.last = st;
        let mode = st.max_mode || 'view';
        // The Connect screen's one question, "What may Coral Hub do on this computer?" (ask-row, radios named by data-cloud-mode): before the connection the answer is held here and goes with Connect
        // in the same call (Launch preselected for an install that never chose; an install keeps what it saved); once connecting or connected it is the saved cap and a change saves at once.
        const ask = this.part('ask-row');
        this.asking = false;
        if (ask) {
            this.asking = st.state === 'idle' || st.state === 'revoked';
            if (this.asking) { if (!this.pick) this.pick = st.max_mode_saved ? mode : 'launch'; mode = this.pick; } else this.pick = null;
            ask.hidden = !st.configured || st.state === 'off';
            for (const r of ask.querySelectorAll('input[data-cloud-mode]')) r.checked = r.value === mode;
        }
        const sel = this.part('max-mode');
        const unchosen = st.max_mode_saved === false;
        if (sel) {
            // A computer that never chose (max_mode_saved false) is at View until it connects, but the Connect screen offers Launch: Settings says "Not chosen yet" instead of a plain View that contradicts it.
            // Once a choice is saved both show it. The placeholder is an option of its own, taken away as soon as a choice is saved.
            let ph = sel.querySelector('option[data-unchosen]');
            // A computer connected from the command line never chose either (no answer was sent): it is connected, so the words are not "until you connect".
            const linked = st.state !== 'idle' && st.state !== 'off';
            const phText = linked ? 'Not chosen yet (View)' : 'Not chosen yet (View until you connect)';
            if (unchosen && !ph) { ph = document.createElement('option'); ph.value = ''; ph.dataset.unchosen = '1'; sel.insertBefore(ph, sel.firstChild); }
            else if (!unchosen && ph) ph.remove();
            if (ph) ph.textContent = phText;
            sel.value = unchosen ? '' : mode;
        }
        const desc = this.part('mode-desc');
        const full = this.part('mode-full');
        if (full) full.textContent = MODE_HELP[mode] + ' Coral Hub\'s owner can limit this further for each server, never raise it.';
        if (desc) desc.textContent = unchosen ? (st.state !== 'idle' && st.state !== 'off' ? 'Coral Hub can only view until you choose here.' : 'Chosen when you connect. Until then Coral Hub can only view.') : MODE_SHORT[mode];
        // Auto-accept from the hub (#3182): a local kill switch, shown only with Launch (the owner's opt-in),
        // on unless the owner turned it off here. Only changeable on this computer.
        show('auto-accept-row', mode === 'launch');
        const allow = this.part('allow-auto-accept');
        if (allow) allow.checked = this.asking && this.pickAuto !== undefined ? this.pickAuto : st.allow_hub_auto_accept !== false;
        // Who last changed it (#5847, from status allow_hub_auto_accept_changed {by: hub|local, user, at}): a local Off that Coral Hub turned on again is visible to the person at this computer. Nothing is said until it was changed.
        const changed = this.part('auto-accept-changed');
        if (changed) { const t = autoAcceptChangedText(st); changed.textContent = t; show('auto-accept-changed', mode === 'launch' && !!t); }
        // A computer linked before it recorded its owner (#6108): say so, and offer Confirm owner, which waits a minute for the owner's first owner-only request through the Hub.
        const ownerMissing = st.owner_label_missing === true;
        show('owner-row', ownerMissing);
        // Look for the last owner the Hub showed when the owner went missing and every time Settings is opened (the panel outlives the dialog).
        // ... and on a status load more than half a minute after the last look while the dialog stays open (the person may have used Coral Hub meanwhile).
        if (ownerMissing && (ownerMissing !== this.ownerMissing || this.ownerRefresh || Date.now() - (this.ownerSeenAt || 0) > 30000)) { this.ownerSeenAt = Date.now(); this.loadOwnerCandidate(); }
        if (ownerMissing || !this.ownerMissing) this.ownerRefresh = false;
        this.ownerMissing = ownerMissing;
        const confirmed = this.part('owner-confirmed');
        if (confirmed) {
            const at = typeof st.owner_adopted_at === 'string' ? new Date(st.owner_adopted_at) : null;
            confirmed.textContent = at && Number.isFinite(at.getTime()) ? `Owner confirmed ${at.toLocaleString()}.` : '';
            confirmed.hidden = !confirmed.textContent;
        }
        // Token redaction for hub viewers (#3560): on unless the owner turned it off here.
        const redact = this.part('redact-tokens');
        if (redact) redact.checked = st.redact_tokens !== false;
        // Relaunching for an update from the hub (#5280, #5383): on unless the owner turned it off here.
        const hubRelaunch = this.part('hub-relaunch');
        if (hubRelaunch) hubRelaunch.checked = st.hub_relaunch_updates === true;
        // Cross-team switches from the hub (#5572): the owner may turn a team's messaging and file sharing on or off unless this lock is off (the server reads an unset or unreadable lock as off).
        const hubCrossTeam = this.part('hub-cross-team');
        if (hubCrossTeam) hubCrossTeam.checked = st.hub_cross_team_settings !== false;
        // The Hub link came or went (connect, disconnect, revoke): the server clears the
        // Operator setting with it, so read it again.
        const enrolled = ENROLLED.includes(st.state);
        if (enrolled !== this.enrolled) { this.enrolled = enrolled; this.loadHubOperator(); }
        this.onState(st);
    }

    // Confirm owner (#6108): the one account the Hub named as the owner in the last hour is shown, so the person knows who it will trust; pressing the button arms the adoption for a minute, and
    // the first owner-only page the owner opens through Coral Hub in that minute is recorded as the owner. Status is polled until the owner is confirmed or the minute ends.
    async loadOwnerCandidate() {
        const cand = this.part('owner-candidate');
        const say = this.part('owner-status');
        if (!cand) return;
        cand.hidden = true; if (say) say.textContent = '';
        try {
            const r = await fetch('/api/cloud/owner-label');
            if (!r.ok) return;
            const d = await r.json();
            const host = typeof d.hub === 'string' ? d.hub : '';
            if (typeof d.candidate === 'string' && /^hub-user-[0-9a-f]{8}$/.test(d.candidate)) {
                const seen = Date.parse(d.candidate_seen_at);
                const ago = Number.isFinite(seen) ? `, seen ${Math.max(0, Math.round((Date.now() - seen) / 60000))} min ago` : '';
                cand.textContent = `The last owner Coral Hub showed was ${d.candidate}${host ? ` (${host})` : ''}${ago}. Confirm owner trusts the account that opens an owner-only page next.`;
                cand.hidden = false;
            } else if (host) { cand.textContent = `Confirm owner trusts the account that opens an owner-only page next through ${host}.`; cand.hidden = false; }
        } catch { /* the row keeps its text */ }
    }

    async confirmOwner() {
        const say = this.part('owner-status');
        const btn = this.part('owner-record');
        if (btn) btn.disabled = true;
        const fail = text => { if (say) say.textContent = text; if (btn) btn.disabled = false; };
        let reply = null;
        try {
            const r = await fetch('/api/cloud/owner-label/arm', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
            if (r.status === 429) return fail('Pressed a few times already. Try again later, or link this computer again.');
            if (!r.ok) return fail(r.status === 409 ? 'Coral Hub is not connected right now, or the owner is already recorded.' : 'Could not start. Link this computer again instead.');
            reply = await r.json();
        } catch { return fail('Could not start. Link this computer again instead.'); }
        const until = Date.parse(reply && reply.armed_until);
        const tick = async () => {
            const left = Number.isFinite(until) ? Math.round((until - Date.now()) / 1000) : 0;
            await this.load();
            if (!this.ownerMissing) { if (say) say.textContent = ''; if (btn) btn.disabled = false; return; }
            if (left <= 0) return fail('Time is up. Press Confirm owner and open an owner-only page through Coral Hub within a minute.');
            if (say) say.textContent = `Now open Artifacts or Saved through Coral Hub, as the owner (${left} s left).`;
            this.ownerTimer = setTimeout(tick, 2000);
        };
        clearTimeout(this.ownerTimer);
        if (say) say.textContent = 'Now open Artifacts or Saved through Coral Hub, as the owner (60 s left).';
        this.ownerTimer = setTimeout(tick, 2000);
    }

    // 'Don't let Coral Hub act as the Operator' (#3404, an opt-out). Only this computer's own
    // answer carries 'linked' (through the hub the route answers read-only, without it), so
    // the row shows only here, while a Hub link exists or the opt-out is on (it outlives the link).
    async loadHubOperator() {
        const row = this.part('hub-operator-row');
        if (!row) return;
        let s = null;
        try {
            const r = await fetch(HUB_OPERATOR_URL);
            if (r.ok) s = await r.json();
        } catch { /* hidden */ }
        const local = !!s && typeof s.linked === 'boolean';
        const disabled = !!s && !!s.disabled;
        row.hidden = !local || !(s.linked || disabled);
        const box = this.part('hub-operator');
        if (local && box) box.checked = disabled;
        this.showHubOperatorActions(local ? s.recent_actions : null);
        this.sayHubOperator('');
    }

    // The newest board writes made through Coral Hub: as the Operator, as a Coral Hub user's chat post, or from a hub
    // that sent no Hub user (method, path, board, Hub user, status).
    // All text, never HTML: these come from requests made through the hub.
    showHubOperatorActions(actions) {
        const box = this.part('hub-operator-actions');
        const list = this.part('hub-operator-actions-list');
        if (!box || !list) return;
        const items = Array.isArray(actions) ? actions.slice(0, 20) : [];
        box.hidden = !items.length;
        list.replaceChildren(...items.map(a => {
            const li = document.createElement('li');
            const when = a.time || a.created_at || a.at || '';
            const t = when ? new Date(when) : null;
            li.textContent = [t && !isNaN(t) ? t.toLocaleString() : '', String(a.method || ''), String(a.path || ''),
                a.project ? `(${a.project})` : '', String(a.hub_user || ''), a.status ? `→ ${a.status}` : ''].filter(Boolean).join(' ');
            return li;
        }));
    }

    sayHubOperator(text) {
        const m = this.part('hub-operator-msg');
        if (m) { m.textContent = text; m.hidden = !text; }
    }

    async setHubOperator(disabled) {
        const box = this.part('hub-operator');
        try {
            const r = await post('PUT', HUB_OPERATOR_URL, { disabled });
            if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'Request failed');
            const s = await r.json();
            if (box) box.checked = s.disabled !== undefined ? !!s.disabled : !s.enabled;
            this.sayHubOperator('');
        } catch (e) {
            if (box) box.checked = !disabled;
            this.sayHubOperator('Could not change it: ' + (e.message || 'request failed'));
        }
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

    // Not the dashboard on the computer running Coral. The server says so itself (code
    // local_dashboard_required, with the command to run there); any other 403 gets the same
    // sentence, since the cloud routes are refused only off that computer.
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

    // Replace a refused secret key (#3698), after a confirmation that lists what is discarded.
    async rekey() {
        if (!this.secretKey) return;
        await this.confirm('Replace the secret key', rekeyText(this.secretKey) + ' This cannot be undone.', async () => {
            const r = await post('POST', '/api/cloud/rekey', { confirm: true });
            let out = null;
            try { out = await r.json(); } catch { /* below */ }
            if (r.ok && out && out.status) {
                this.render(out.status);
                this.say('The secret key was replaced. ' + (out.discarded_hub_enrollment ? 'Connect to Coral Hub again. ' : '') +
                    (out.discarded_remote_server_keys ? "Enter the remote servers' API keys again." : ''));
                return;
            }
            if (r.status === 403) { this.elsewhere(out); return; }
            this.say((out && out.error) || 'The secret key could not be replaced');
        });
    }

    bind() {
        if (this.root.dataset.cloudBound) return;
        this.root.dataset.cloudBound = '1';
        // With the question on screen the answer goes in the Connect call itself (the server saves it before the enrollment starts).
        this.part('connect')?.addEventListener('click', () => {
            const ask = this.part('ask-row'), allow = this.part('allow-auto-accept');
            const answer = ask && !ask.hidden && this.pick ? { mode: this.pick, ...(this.pick === 'launch' && allow ? { allow_auto_accept: allow.checked } : {}) } : undefined;
            this.pickAuto = undefined;
            this.act('POST', '/api/cloud/connect', answer);
        });
        this.part('ask-row')?.addEventListener('change', e => {
            const r = e.target && e.target.closest && e.target.closest('input[data-cloud-mode]');
            if (!r) return;
            if (this.asking) { this.pick = r.value; if (this.last) this.render(this.last); } else this.act('PUT', '/api/cloud/max-mode', { mode: r.value });
        });
        this.part('disconnect')?.addEventListener('click', () => this.act('POST', '/api/cloud/disconnect'));
        this.part('cancel')?.addEventListener('click', () => this.cancel());
        this.part('rekey')?.addEventListener('click', () => this.rekey());
        this.part('copy')?.addEventListener('click', async () => {
            const btn = this.part('copy');
            const ok = await copyText(this.command || CONNECT_COMMAND);
            btn.textContent = ok ? 'Copied' : 'Copy failed';
            setTimeout(() => { btn.textContent = 'Copy'; }, 1500);
        });
        this.part('max-mode')?.addEventListener('change', (e) => { if (e.target.value) this.act('PUT', '/api/cloud/max-mode', { mode: e.target.value }); }); // the "Not chosen yet" placeholder saves nothing
        this.part('allow-auto-accept')?.addEventListener('change', (e) => {
            if (this.asking) { this.pickAuto = e.target.checked; return; } // held until Connect, which sends it
            this.act('PUT', '/api/cloud/allow-auto-accept', { allow: e.target.checked });
        });
        this.part('owner-record')?.addEventListener('click', () => this.confirmOwner());
        this.part('redact-tokens')?.addEventListener('change', (e) => this.act('PUT', '/api/cloud/redact-tokens', { redact: e.target.checked }));
        this.part('hub-relaunch')?.addEventListener('change', (e) => this.act('PUT', '/api/cloud/hub-relaunch', { allow: e.target.checked }));
        this.part('hub-cross-team')?.addEventListener('change', (e) => this.act('PUT', '/api/cloud/hub-cross-team', { allow: e.target.checked }));
        this.part('hub-operator')?.addEventListener('change', (e) => this.setHubOperator(e.target.checked));
    }

    // Cancel an enrollment in progress (#3285).
    async cancel() {
        const r = await post('POST', '/api/cloud/connect/cancel');
        if (r.ok) { await this.load(); return; }
        if (r.status === 403) { this.elsewhere(await r.json().catch(() => null)); return; }
        let msg = 'Request failed';
        try { msg = (await r.json()).error || msg; } catch { /* keep default */ }
        this.say(msg);
    }

    stop() { clearTimeout(this.timer); clearTimeout(this.ownerTimer); clearInterval(this.retryTick); }
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
    if (root && !root._cloudPanel) { const p = new CloudPanel(root); p.commandsInRow = true; root._cloudPanel = p; p.bind(); }
}

export async function loadCloudStatus() {
    const root = document.getElementById('settings-cloud');
    if (!root) return;
    if (!root._cloudPanel) initCloudConnect();
    root._cloudPanel.ownerRefresh = true; // Settings was opened: look for the last owner the Hub showed again (the panel outlives the dialog, #6108)
    await root._cloudPanel.load();
}
