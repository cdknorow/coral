/* Per-agent Auto-accept (#3182): Coral approves that agent's permission prompts for it.
 *
 * The answering happens on the server (it works with the dashboard closed and through hubs);
 * this module is the switch, the warning shown before turning it on (with an optional time limit),
 * the AUTO badges, the one-time notice when Coral turns it off, and "Stop all auto-accept".
 *
 * Session fields (live list and feed, always present): auto_accept (bool), auto_accept_until (unix
 * seconds or null), auto_accept_source ('hub' | 'local' | null), auto_accept_supported (bool),
 * auto_accept_unsupported_code and auto_accept_paused_code (fixed codes or null; the text is here).
 * POST /api/sessions/live/{session_id}/auto-accept/enable {session_id, acknowledged: true, duration_s?} (hub: Launch)
 * POST /api/sessions/live/{session_id}/auto-accept/disable {session_id} (hub: Interact)
 * POST /api/auto-accept/disable-all → {stopped}
 */
import { state } from './state.js';
import { serverFetch, serverForSession, sessionServer, isCloud } from './server_base.js';
import { showToast, escapeAttr, jsStr } from './utils.js';
import { responseErrorText } from './server_status.js';
import { scheduleLiveSessionsRender } from './render.js';
import { isPopout } from './popout.js';

const WARNING_TITLE = 'Turn on auto-accept for this agent?';
// The operator's exact words (#3182), in parts so the risk stands out and what still waits gets
// its own line (#3218). WARNING_TEXT is the whole, unchanged.
const WARNING_LEAD = 'Coral will answer “Yes” to this agent’s permission prompts for you, ';
const WARNING_RISK = 'which can include risky actions such as deleting files, force-pushing, running sudo or '
    + 'changing files outside the agent’s folder.';
const WARNING_TRUST = ' Only turn this on for a task you trust, ideally in a sandbox or a disposable worktree.';
const WARNING_WAITS = 'Questions and plan approvals still wait for you. It turns off when the agent restarts or wakes.';
export const WARNING_TEXT = WARNING_LEAD + WARNING_RISK + WARNING_TRUST + ' ' + WARNING_WAITS;

// Why Coral turned it off (auto_accept_paused_code). The server sends codes only; the words are ours.
export const PAUSED_TEXT = {
    repeat_loop: 'the same prompt repeated 5 times in a minute',
    rate_cap: 'more than 30 approvals in a minute',
    expired: 'its time limit ran out',
    mode_lowered: 'the Cloud Hub’s access dropped below Launch',
    hub_allow_off: 'Cloud Hub auto-accept was turned off on this computer',
    link_removed: 'this computer was disconnected from the Cloud Hub',
    stopped_by_owner: 'you stopped all auto-accept',
    coral_restarted: 'Coral restarted (an auto-accept turned on from the Cloud Hub ends then)',
};
export const pausedText = code => PAUSED_TEXT[code] || 'Coral turned it off';

// The time limits offered when turning it on. A hub enable must have one (the server allows at most 8 h).
const DURATIONS = [[15 * 60, '15 minutes'], [30 * 60, '30 minutes'], [3600, '1 hour'], [2 * 3600, '2 hours'], [4 * 3600, '4 hours'], [8 * 3600, '8 hours']];

// Agent types Coral cannot answer for (the server refuses them too); used until the payload says.
const UNSUPPORTED_TYPES = new Set(['pi', 'terminal']);

function supportedFor(s) {
    if (typeof s.auto_accept_supported === 'boolean') return s.auto_accept_supported;
    return !UNSUPPORTED_TYPES.has(String(s.agent_type || '').toLowerCase());
}

function unsupportedReason(s) {
    return `Not available for ${s.agent_type || 'this agent type'} yet`;
}

function findSession(sessionId, server) {
    return (state.liveSessions || []).find(s => s.session_id === sessionId && sessionServer(s) === server);
}

function clock(unixSeconds) {
    return new Date(unixSeconds * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

/** "until 14:32", "from the Cloud Hub": what the chips and the menu say about an agent that has it on. */
function onDetails(s) {
    const parts = [];
    if (s.auto_accept_until) parts.push(`until ${clock(s.auto_accept_until)}`);
    if (s.auto_accept_source === 'hub') parts.push('turned on from the Cloud Hub');
    return parts.join(', ');
}

function chipTitle(s) {
    const d = onDetails(s);
    return `Auto-accept is on${d ? ` (${d})` : ''}: Coral approves this agent’s permission prompts`;
}

/** The AUTO chip for an agent's sidebar row ('' when auto-accept is off). */
export function autoAcceptChip(s) {
    if (!s || !s.auto_accept) return '';
    return `<span class="auto-accept-chip" title="${escapeAttr(chipTitle(s))}">AUTO</span>`;
}

/** The "Auto-accept prompts" switch for an agent's menu. */
export function autoAcceptMenuItem(s) {
    const sid = s.session_id;
    const on = !!s.auto_accept;
    const supported = supportedFor(s);
    const why = !supported ? unsupportedReason(s)
        : on ? onDetails(s)
        : s.auto_accept_paused_code ? `Turned off: ${pausedText(s.auto_accept_paused_code)}` : '';
    const stateText = on ? (s.auto_accept_until ? `On until ${clock(s.auto_accept_until)}` : 'On') : 'Off';
    return `<button type="button" class="overflow-menu-item auto-accept-menu-item" role="menuitemcheckbox" aria-checked="${on}"`
        + `${supported ? '' : ' disabled aria-disabled="true"'}${why ? ` title="${escapeAttr(why)}"` : ''}`
        + ` onclick="event.stopPropagation(); closeSidebarKebabs(); toggleAutoAccept('${jsStr(s.name)}', '${jsStr(sid)}')">`
        + '<svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 8.5l3 3 7-7"/></svg>'
        + `Auto-accept prompts<span class="auto-accept-state">${stateText}</span></button>`;
}

/** Ask before turning auto-accept on, with a time limit (required from the Cloud Hub, optional here).
 *  Cancel (focused), Escape and the backdrop keep it off. onConfirm(durationSeconds | null). */
export function confirmAutoAccept(name, onConfirm, onCancel, { hub = isCloud() } = {}) {
    document.querySelectorAll('dialog.auto-accept-confirm').forEach(d => d.remove());
    const dlg = document.createElement('dialog');
    dlg.className = 'auto-accept-confirm';
    dlg.setAttribute('aria-labelledby', 'auto-accept-confirm-title');
    dlg.setAttribute('aria-describedby', 'auto-accept-confirm-text');
    const h = document.createElement('h3');
    h.id = 'auto-accept-confirm-title';
    h.textContent = WARNING_TITLE;
    const who = document.createElement('p');
    who.className = 'auto-accept-confirm-agent';
    who.textContent = name;
    const p = document.createElement('div');
    p.id = 'auto-accept-confirm-text';
    const risk = document.createElement('p');
    const strong = document.createElement('strong');
    strong.textContent = WARNING_RISK;
    risk.append(WARNING_LEAD, strong, WARNING_TRUST);
    const waits = document.createElement('p');
    waits.textContent = WARNING_WAITS;
    p.append(risk, ' ', waits); // the space keeps the description one text when read as a whole
    const label = document.createElement('label');
    label.className = 'auto-accept-confirm-duration';
    label.textContent = 'Turn it off after ';
    const select = document.createElement('select');
    select.id = 'auto-accept-duration';
    if (!hub) select.append(new Option('No time limit', ''));
    for (const [secs, text] of DURATIONS) select.append(new Option(text, String(secs)));
    select.value = hub ? '3600' : '';
    label.append(select);
    const cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.className = 'btn';
    cancel.textContent = 'Cancel';
    const ok = document.createElement('button');
    ok.type = 'button';
    ok.className = 'btn auto-accept-confirm-enable';
    ok.textContent = 'Enable auto-accept';
    const actions = document.createElement('div');
    actions.className = 'auto-accept-confirm-actions';
    actions.append(cancel, ok);
    dlg.append(h, who, p, label, actions);
    let done = false;
    const finish = yes => {
        if (done) return;
        done = true;
        const duration = select.value ? Number(select.value) : null;
        if (dlg.open) dlg.close();
        dlg.remove();
        if (yes) onConfirm(duration); else if (onCancel) onCancel();
    };
    cancel.addEventListener('click', () => finish(false));
    ok.addEventListener('click', () => finish(true));
    dlg.addEventListener('cancel', e => { e.preventDefault(); finish(false); }); // Escape
    dlg.addEventListener('click', e => { if (e.target === dlg) finish(false); }); // backdrop
    document.body.appendChild(dlg);
    dlg.showModal();
    cancel.focus();
    return dlg;
}

/** Turning it on and off are separate routes, so a hub can require more access to turn it on. */
export function autoAcceptPath(sessionId, enabled) {
    return `/api/sessions/live/${encodeURIComponent(sessionId)}/auto-accept/${enabled ? 'enable' : 'disable'}`;
}

// The server's 403 codes for an enable it refuses, in words.
const REFUSED_TEXT = {
    agent_identity: 'Auto-accept can only be turned on by you, in the dashboard, not by an agent.',
    no_dashboard_provenance: 'Auto-accept can only be turned on from the dashboard.',
    hub_auto_accept_disabled: 'The owner of that computer has turned off Cloud Hub auto-accept there (in its Coral Settings).',
};

async function refusalText(resp, server) {
    if (resp.status === 403) {
        const body = await resp.clone().json().catch(() => null);
        if (body && REFUSED_TEXT[body.code]) return REFUSED_TEXT[body.code];
    }
    return responseErrorText(resp, server, 'Could not change auto-accept');
}

function applyState(s, data) {
    for (const k of ['auto_accept', 'auto_accept_until', 'auto_accept_source', 'auto_accept_paused_code']) {
        if (k in data) s[k] = data[k];
    }
}

async function setAutoAccept(name, sessionId, server, enabled, durationS = null) {
    try {
        const body = { session_id: sessionId };
        if (enabled) {
            body.acknowledged = true;
            if (durationS) body.duration_s = durationS;
        }
        const resp = await serverFetch(server, autoAcceptPath(sessionId, enabled), {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
        });
        if (!resp.ok) { showToast(await refusalText(resp, server), true); return false; }
        const data = await resp.json().catch(() => ({}));
        const s = findSession(sessionId, server);
        if (s) applyState(s, { auto_accept: enabled, ...data });
        // The open agent too (the popout's session may not be in the live list yet).
        const cur = state.currentSession;
        if (cur && cur.session_id === sessionId && cur !== s) applyState(cur, { auto_accept: enabled, ...data });
        scheduleLiveSessionsRender();
        syncAutoAcceptHeader();
        syncStopAll();
        showToast(enabled ? `Auto-accept is on for "${name}"` : `Auto-accept is off for "${name}"`);
        return true;
    } catch {
        showToast('Could not change auto-accept', true);
        return false;
    }
}

/** Flip an agent's auto-accept: off at once, on only after the warning. Resolves true when changed. */
export function toggleAutoAccept(name, sessionId) {
    const server = serverForSession(name, sessionId);
    // The live list, or the open agent (a popout may not have it in the list).
    const cur = state.currentSession;
    const s = findSession(sessionId, server) || (cur && cur.session_id === sessionId ? cur : null);
    const label = (s && (s.display_name || s.name)) || name; // what the sidebar calls it
    if (s && !supportedFor(s)) {
        showToast(unsupportedReason(s), true);
        return Promise.resolve(false);
    }
    if (s && s.auto_accept) return setAutoAccept(label, sessionId, server, false);
    return new Promise(resolve => confirmAutoAccept(label,
        duration => setAutoAccept(label, sessionId, server, true, duration).then(resolve),
        () => resolve(false)));
}

let _headerTimer = null;
/** The header control for the selected agent. While auto-accept is on: the AUTO chip ("AUTO 42m" in
 *  its last hour), a click turns it off. In the popout, which has no agent menu, it is also the switch
 *  while off: an "Auto-accept" button that asks first, like the sidebar menu. */
export function syncAutoAcceptHeader() {
    const host = document.querySelector('.terminal-header-actions');
    if (!host) return;
    let chip = document.getElementById('auto-accept-header-chip');
    const cur = state.currentSession;
    const s = cur && cur.type === 'live' ? findSession(cur.session_id, sessionServer(cur)) || cur : null;
    clearTimeout(_headerTimer);
    const offSwitch = !!s && !s.auto_accept && isPopout() && supportedFor(s);
    if (!s || (!s.auto_accept && !offSwitch)) { chip?.remove(); return; }
    if (!chip) {
        chip = document.createElement('button');
        chip.type = 'button';
        chip.id = 'auto-accept-header-chip';
        chip.addEventListener('click', () => {
            const c = state.currentSession;
            if (c) toggleAutoAccept(c.name, c.session_id).then(() => syncAutoAcceptHeader());
        });
        host.prepend(chip);
    }
    chip.setAttribute('aria-pressed', String(!!s.auto_accept));
    if (offSwitch) {
        chip.className = 'auto-accept-header-off';
        chip.textContent = 'Auto-accept';
        chip.title = s.auto_accept_paused_code ? `Turned off: ${pausedText(s.auto_accept_paused_code)}. Click to turn it on again.` : 'Turn on auto-accept for this agent (asks first)';
        chip.setAttribute('aria-label', 'Auto-accept is off. Turn it on');
        return;
    }
    chip.className = 'auto-accept-chip auto-accept-header-chip';
    chip.setAttribute('aria-label', 'Auto-accept is on. Turn it off');
    const left = s.auto_accept_until ? Math.ceil((s.auto_accept_until * 1000 - Date.now()) / 60000) : null;
    chip.textContent = left != null && left <= 60 ? `AUTO ${Math.max(left, 0)}m` : 'AUTO';
    chip.title = `${chipTitle(s)}. Click to turn it off.`;
    if (left != null) _headerTimer = setTimeout(syncAutoAcceptHeader, 30000); // sleep-ok: a minute countdown, not a wait
}

const _pausedSeen = new Set();
// Codes for something the owner did themselves: no notice (they know), only the reason on the switch.
const OWNER_CODES = new Set(['stopped_by_owner', 'hub_allow_off']);
/** Tell the user once when Coral turned an agent's auto-accept off, and keep "Stop all" in step. */
export function noteAutoAcceptPauses(sessions) {
    for (const s of sessions || []) {
        const key = `${sessionServer(s)}|${s.session_id}`;
        if (s.auto_accept_paused_code && !s.auto_accept && !OWNER_CODES.has(s.auto_accept_paused_code)) {
            if (_pausedSeen.has(key)) continue;
            _pausedSeen.add(key);
            showToast(`Auto-accept turned off for "${s.display_name || s.name}": ${pausedText(s.auto_accept_paused_code)}`, true, 8000);
        } else {
            _pausedSeen.delete(key);
        }
    }
    syncStopAll();
}

/** "Stop all auto-accept" (top-bar menu) is shown only while some agent has it on. */
export function syncStopAll() {
    const item = document.getElementById('stop-all-auto-accept');
    if (!item) return;
    const n = (state.liveSessions || []).filter(s => s.auto_accept).length;
    item.hidden = n === 0;
    item.title = n ? `Turn auto-accept off for all ${n} agent${n === 1 ? '' : 's'}` : '';
}

/** Turn auto-accept off for every agent at once, on this server and on every server the hub shows. */
export async function stopAllAutoAccept() {
    const servers = [...new Set((state.liveSessions || []).filter(s => s.auto_accept).map(sessionServer))];
    let stopped = 0, failed = 0;
    for (const server of servers) {
        try {
            const resp = await serverFetch(server, '/api/auto-accept/disable-all', { method: 'POST' });
            if (!resp.ok) { failed++; continue; }
            stopped += Number((await resp.json().catch(() => ({}))).stopped) || 0;
            for (const s of state.liveSessions || []) {
                if (sessionServer(s) === server && s.auto_accept) applyState(s, { auto_accept: false, auto_accept_until: null, auto_accept_source: null, auto_accept_paused_code: 'stopped_by_owner' });
            }
        } catch { failed++; }
    }
    scheduleLiveSessionsRender();
    syncAutoAcceptHeader();
    syncStopAll();
    if (failed) showToast(`Auto-accept stopped for ${stopped} agent${stopped === 1 ? '' : 's'}; ${failed} server${failed === 1 ? '' : 's'} could not be reached`, true);
    else showToast(`Auto-accept stopped for ${stopped} agent${stopped === 1 ? '' : 's'}`);
    return { stopped, failed };
}

window.toggleAutoAccept = toggleAutoAccept;
window.stopAllAutoAccept = stopAllAutoAccept;
