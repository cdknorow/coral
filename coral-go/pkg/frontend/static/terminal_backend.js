/* Settings > Terminal backend (#3634): tmux keeps agents running across Coral restarts but any
 * agent can reach other agents' terminals through its socket; pty isolates agents, which end when
 * Coral restarts. One Coral runs one backend, chosen when it starts, so a change applies at the
 * next start: Apply asks first, puts the running agents to sleep when going to pty, saves, and
 * restarts Coral where it can restart itself. Wording: Security's (#3632, with its #3634 edits).
 * Under pty, agents still running in Coral's old tmux server get a banner with End them. None of
 * this exists through Coral Hub. */

import { showToast } from './utils.js';
import { isCloud } from './server_base.js';
import { isSignedOut } from './overload.js';
import { showConfirmModal } from './render.js';
import { state } from './state.js';
import { browserSession } from './safe_storage.js';

let _status = null; // GET /api/system/terminal-backend
let _autoWakeShown = null; // the auto-wake values as the dialog opened (#3636)

const OS_NOTE = {
    mac: "macOS: with pty, other programs normally cannot reach Coral's terminals; with tmux they can, through the socket. Neither backend separates Coral from programs running with your full user rights; for that, run agents under a separate account.",
    linux: "Linux: with pty, a program running as you can still reach Coral's terminals through /proc today, so pty protects agents from tmux-style access, not from every program running as you.",
};

function osNote() {
    const p = `${navigator.userAgentData?.platform || navigator.platform || ''}`.toLowerCase();
    return p.includes('linux') ? OS_NOTE.linux : OS_NOTE.mac;
}

const $ = id => document.getElementById(id);

async function fetchStatus() {
    const resp = await fetch('/api/system/terminal-backend');
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return resp.json();
}

/** The backend the next start uses as things stand (what is saved, else the default). */
function nextBackend(st) {
    if (st.forced) return st.running;
    if (!st.choice) return 'pty';
    return st.next || st.saved || 'pty'; // the server says what the next start runs (the default is pty, #6064)
}

function picked() {
    const r = document.querySelector('input[name="terminal-backend"]:checked');
    return r ? r.value : null;
}

function renderPending() {
    const st = _status;
    const note = $('tb-pending');
    if (!st || !note) return;
    const want = picked();
    if (st.forced) {
        note.textContent = `This run was started with --backend ${st.running}, which wins over this setting.`;
        note.hidden = false;
    } else if (want && want !== st.running) {
        note.textContent = `Switches to ${want} when Coral restarts. Apply asks first.`;
        note.hidden = false;
    } else {
        note.hidden = true;
    }
}

/** Fill the Settings row (called as the dialog opens). */
export async function loadTerminalBackendRow() {
    const row = $('settings-terminal-backend');
    if (!row) return;
    if (isCloud()) { row.hidden = true; return; }
    try {
        _status = await fetchStatus();
    } catch {
        row.hidden = true;
        return;
    }
    row.hidden = false;
    $('tb-now').innerHTML = '';
    $('tb-now').append('Running now: ', Object.assign(document.createElement('b'), { textContent: _status.running }));
    $('tb-choice').hidden = !_status.choice;
    $('tb-fixed').hidden = !!_status.choice;
    // Waking the agents that were running when Coral stopped (#3636): a pty Coral only.
    const aw = $('tb-autowake');
    if (aw) {
        aw.hidden = _status.running !== 'pty';
        const st = state.settings || {};
        const on = String(st.auto_wake_after_restart ?? 'true') !== 'false';
        const cap = Number.parseInt(st.auto_wake_cap, 10);
        $('tb-autowake-on').checked = on;
        $('tb-autowake-cap').value = cap >= 1 && cap <= 50 ? cap : 8;
        _autoWakeShown = aw.hidden ? null : { on, cap: Number($('tb-autowake-cap').value) };
    }
    if (_status.choice) {
        const next = nextBackend(_status);
        for (const r of document.querySelectorAll('input[name="terminal-backend"]')) {
            r.checked = r.value === next;
            if (!r._tbWired) { r._tbWired = true; r.addEventListener('change', renderPending); }
        }
        // Each choice says it in a line; the whole explanation (Security's wording) opens under it.
        for (const b of document.querySelectorAll('#settings-terminal-backend .tb-learn')) {
            if (b._tbWired) continue;
            b._tbWired = true;
            b.addEventListener('click', () => {
                const target = document.getElementById(b.getAttribute('aria-controls'));
                if (!target) return;
                target.hidden = !target.hidden;
                b.setAttribute('aria-expanded', String(!target.hidden));
                b.textContent = target.hidden ? 'Learn more' : 'Show less';
            });
        }
        // The operating system's security note is the end of each backend's own Learn more, not a third control under both (UI/UX's #5985 note): said once there, whole.
        for (const d of document.querySelectorAll('#settings-terminal-backend .tb-d')) {
            if (!d.querySelector('.tb-d-os')) d.append(' ', Object.assign(document.createElement('span'), { className: 'tb-d-os', textContent: osNote() }));
        }
        renderPending();
    }
}

async function post(path) {
    const resp = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
    let body = {};
    try { body = await resp.json(); } catch { /* not JSON */ }
    return { ok: resp.ok, status: resp.status, body };
}

async function saveAndRestart(want) {
    const resp = await fetch('/api/system/terminal-backend', {
        method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ backend: want }),
    });
    let body = {};
    try { body = await resp.json(); } catch { /* not JSON */ }
    if (!resp.ok) {
        showToast(body.error ? `Terminal backend not changed: ${body.error}` : 'Could not change the terminal backend', true);
        return false;
    }
    if (body.can_restart) {
        const r = await post('/api/system/restart');
        if (r.ok) { showToast(`Restarting Coral on ${want}…`); return true; }
    }
    showToast(`Saved: quit Coral and start it again to switch to ${want}.`);
    return true;
}

function confirmWith(title, message, yesLabel) {
    return new Promise(resolve => {
        const yes = $('confirm-modal-yes');
        const before = yes ? yes.textContent : '';
        const restore = () => { const b = $('confirm-modal-yes'); if (b) b.textContent = before; };
        showConfirmModal(title, message, () => { restore(); resolve(true); }, () => { restore(); resolve(false); });
        const b = $('confirm-modal-yes');
        if (b) b.textContent = yesLabel;
    });
}

/** Apply a changed backend choice (after the rest of Settings is saved). Resolves to true when
 *  nothing changed or the change went through, false when the user cancelled or it failed. */
async function saveAutoWake() {
    if (!_autoWakeShown) return true;
    const on = $('tb-autowake-on').checked;
    const cap = Number($('tb-autowake-cap').value);
    if (on === _autoWakeShown.on && cap === _autoWakeShown.cap) return true;
    if (!Number.isInteger(cap) || cap < 1 || cap > 50) {
        showToast('Wake up to: a whole number from 1 to 50', true);
        return false;
    }
    const resp = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ auto_wake_after_restart: on, auto_wake_cap: cap }) });
    if (!resp.ok) {
        let body = {};
        try { body = await resp.json(); } catch { /* not JSON */ }
        showToast(body.error || 'Could not save the wake setting', true);
        return false;
    }
    state.settings = { ...(state.settings || {}), auto_wake_after_restart: String(on), auto_wake_cap: String(cap) };
    _autoWakeShown = { on, cap };
    return true;
}

/** True when the backend choice or the wake setting in the dialog differs from what it opened with: Save has something to apply
 *  beyond the plain settings (the same tests applyTerminalBackendChange and saveAutoWake make). */
export function hasUnsavedBackendChange() {
    if (_autoWakeShown && ($('tb-autowake-on').checked !== _autoWakeShown.on || Number($('tb-autowake-cap').value) !== _autoWakeShown.cap)) return true;
    const st = _status;
    if (!st || !st.choice || st.forced || isCloud()) return false;
    const want = picked();
    return Boolean(want) && want !== nextBackend(st);
}

export async function applyTerminalBackendChange() {
    if (!(await saveAutoWake())) return false;
    const st = _status;
    if (!st || !st.choice || st.forced || isCloud()) return true;
    const want = picked();
    if (!want || want === nextBackend(st)) return true;
    if (want === 'pty' && st.running === 'tmux') {
        const n = st.running_agents || 0;
        const ok = await confirmWith('Switch to pty?',
            (n ? `Switching to pty ends the ${n} running agent${n === 1 ? '' : 's'}. Coral puts them to sleep first, then restarts on pty. `
                + 'Wake them from the dashboard afterwards: they resume their conversation, but a tool that is running now is not continued. '
                : 'No agents are running. Coral restarts on pty now. ')
            + 'After this, agents end whenever Coral restarts or upgrades.',
            n ? `Sleep ${n} agent${n === 1 ? '' : 's'} and restart on pty` : 'Restart on pty');
        if (!ok) return false;
        if (n) {
            const r = await post('/api/sessions/live/sleep-all');
            if (!r.ok) { showToast('Could not put the agents to sleep: nothing changed', true); return false; }
        }
        return saveAndRestart('pty');
    }
    if (want === 'tmux' && st.running === 'pty') {
        const ok = await confirmWith('Switch to tmux?',
            "Agents will keep running across Coral restarts after this. Any agent will be able to reach other agents' terminals. "
            + 'Coral restarts on tmux now; your sleeping agents wake as usual.', 'Restart on tmux');
        if (!ok) return false;
        return saveAndRestart('tmux');
    }
    // Going back to the backend this run already uses: just save it.
    return saveAndRestart(want);
}

// ── Agents left in Coral's old tmux server (under pty) ───────────────────

export async function checkLeftoverTmux() {
    const banner = $('leftover-tmux-banner');
    if (!banner || isCloud()) return;
    let st;
    try { st = await fetchStatus(); } catch { return; }
    const n = st.running === 'pty' ? (st.leftover_tmux_agents || 0) : 0;
    banner.hidden = n === 0;
    if (!n) return;
    $('leftover-tmux-count').textContent = `${n} agent${n === 1 ? ' is' : 's are'} still running in Coral's old tmux server.`;
}

export async function endLeftoverTmux() {
    const ok = await confirmWith('End the agents left in the old tmux server?',
        "They stop at once (a tool that is running is not finished). Then wake them here: they resume their conversation on pty.", 'End them');
    if (!ok) return;
    const r = await post('/api/system/terminal-backend/end-leftover');
    if (!r.ok) { showToast(r.body.error || 'Could not end them', true); return; }
    showToast(`Ended ${r.body.ended} agent${r.body.ended === 1 ? '' : 's'}: wake them from the list`);
    checkLeftoverTmux();
}

// ── The built-in terminal became the default (#6064) ─────────────────────
// One line, once, for an install that ran on the old tmux default; dismissing it is kept in the server's settings.

export async function checkDefaultNotice() {
    const banner = $('default-notice-banner');
    if (!banner || isCloud()) return;
    let st;
    try { st = await fetchStatus(); } catch { return; }
    banner.hidden = !st.default_notice;
}

export async function dismissDefaultNotice() {
    const r = await post('/api/system/terminal-backend/notice-seen');
    if (!r.ok) { showToast(r.body.error || 'Could not dismiss this', true); return; }
    const banner = $('default-notice-banner');
    if (banner) banner.hidden = true;
}

// ── More agents to wake than Coral wakes by itself (#3636) ───────────────

async function autoWakeStatus() {
    const resp = await fetch('/api/system/auto-wake');
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return resp.json();
}

function showAutoWakeBanner(st) {
    const banner = $('auto-wake-banner');
    if (!banner) return;
    const n = (st && st.state === 'pending') ? st.pending.length : 0;
    banner.hidden = n === 0;
    if (n) $('auto-wake-count').textContent = `${n} agents were running when Coral stopped.`;
}

/** After a start, look for the over-the-cap question (the pass runs a few seconds in). */
export function watchAutoWake() {
    if (isCloud()) return;
    let tries = 0;
    const look = async () => {
        if (isSignedOut()) return; // the wait every poller keeps while signed out; the page reloads when the user signs in (a stray request landed in auth_redirect's quiet window)
        tries++;
        let st;
        try { st = await autoWakeStatus(); } catch { return; }
        showAutoWakeBanner(st);
        if ((st.state === '' || st.state === 'waking') && tries < 6) setTimeout(look, 5000); // sleep-ok: the pass starts 5 s after Coral
    };
    setTimeout(look, 6000); // sleep-ok: the pass starts 5 s after Coral
}

/** Wake them: each through the per-agent wake route, 2 s plus up to 1 s apart. */
export async function wakePendingAgents() {
    let st;
    try { st = await autoWakeStatus(); } catch { return; }
    const pending = (st.pending || []).slice();
    const detail = $('auto-wake-detail');
    for (const btn of [$('auto-wake-go'), $('auto-wake-dismiss')]) if (btn) btn.disabled = true;
    let woken = 0;
    for (let i = 0; i < pending.length; i++) {
        if (i) await new Promise(r => setTimeout(r, 2000 + Math.random() * 1000)); // sleep-ok: the stagger between wakes
        if (detail) detail.textContent = `Waking ${i + 1} of ${pending.length}…`;
        try {
            const resp = await fetch(`/api/sessions/live/${encodeURIComponent(pending[i].session_id)}/wake`, { method: 'POST' });
            const data = await resp.json().catch(() => ({}));
            if (data.ok) woken++;
        } catch { /* the next one */ }
    }
    for (const btn of [$('auto-wake-go'), $('auto-wake-dismiss')]) if (btn) btn.disabled = false;
    showToast(`Woke ${woken} of ${pending.length} agents`);
    try { showAutoWakeBanner(await autoWakeStatus()); } catch { /* keep it */ }
}

/** Not now: they stay asleep, and can be woken from the list. */
export async function dismissAutoWake() {
    const resp = await fetch('/api/system/auto-wake/dismiss', { method: 'POST' });
    if (!resp.ok) { showToast('Could not dismiss this', true); return; }
    showAutoWakeBanner(await resp.json());
}

// ── Agents on an older hook set (#3665) ──────────────────────────────────
// Claude reads its hooks once, at launch: an agent started before a hook-set change keeps the old
// set until it is resumed. The banner lists them (or ones not heard from since Coral started) and
// resumes each through Sleep then Wake: the same session, its conversation and its settings.

const HOOKS_DISMISS_KEY = 'coral.hooksBanner.dismissed';
let _hooksWired = false;

async function hooksStatus() {
    const resp = await fetch('/api/system/hooks-status');
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return resp.json();
}

function hooksDismissed() {
    try { return browserSession.get(HOOKS_DISMISS_KEY) === '1'; } catch { return false; }
}

function wireHooksBanner() {
    if (_hooksWired) return;
    _hooksWired = true;
    $('hooks-go')?.addEventListener('click', () => resumeAllOnCurrentHooks());
    $('hooks-dismiss')?.addEventListener('click', () => {
        try { browserSession.set(HOOKS_DISMISS_KEY, '1'); } catch { /* it just hides for now */ }
        const banner = $('hooks-banner');
        if (banner) banner.hidden = true;
    });
    $('hooks-toggle')?.addEventListener('click', () => {
        const panel = $('hooks-panel'), btn = $('hooks-toggle');
        if (!panel || !btn) return;
        panel.hidden = !panel.hidden;
        btn.setAttribute('aria-expanded', String(!panel.hidden));
        btn.textContent = panel.hidden ? 'Show agents' : 'Hide agents';
    });
    $('hooks-list')?.addEventListener('click', ev => {
        const btn = ev.target.closest('button[data-session-id]');
        if (btn) resumeOneOnCurrentHooks(btn.dataset.sessionId, btn.dataset.name || 'the agent');
    });
}

/** What Coral can truthfully say about one agent's hook set. An agent on a hook set from before the revision
 *  existed never reports one, so "older" without a revision is all the server can tell; "unknown" is an agent
 *  with no revision recorded and no event heard since Coral started, which may well be current. */
 function hooksStateText(agent, current) {
    if (agent.status === 'unknown') return 'started before hook versions were tracked';
    if (agent.rev > 0) return `older hooks (rev ${agent.rev}, current is ${current})`;
    return 'older hooks (no version reported)';
}

function hooksRow(agent, current) {
    const li = document.createElement('li');
    const name = document.createElement('span');
    name.className = 'hooks-name';
    name.textContent = agent.name;
    name.title = agent.team ? `${agent.name} (${agent.team})` : agent.name;
    const status = document.createElement('span');
    status.className = 'hooks-state';
    status.textContent = hooksStateText(agent, current);
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn btn-small';
    btn.textContent = 'Resume';
    btn.title = `Resume ${agent.name} on the current hooks`;
    btn.dataset.sessionId = agent.session_id;
    btn.dataset.name = agent.name;
    li.append(name, status, btn);
    return li;
}

/** The agents grouped by team (the server sorts them by team, then name). */
function hooksGroups(agents, current) {
    const groups = new Map();
    for (const a of agents) {
        const team = a.team || 'No team';
        if (!groups.has(team)) groups.set(team, []);
        groups.get(team).push(a);
    }
    return [...groups].map(([team, list]) => {
        const section = document.createElement('section');
        section.className = 'hooks-team';
        const h = document.createElement('h4');
        h.textContent = `${team} (${list.length})`;
        const ul = document.createElement('ul');
        ul.append(...list.map(a => hooksRow(a, current)));
        section.append(h, ul);
        return section;
    });
}

function showHooksBanner(st) {
    const banner = $('hooks-banner');
    if (!banner) return;
    const agents = (st && st.agents) || [];
    banner.hidden = agents.length === 0 || hooksDismissed();
    if (!agents.length) return;
    wireHooksBanner();
    $('hooks-count').textContent = agents.length === 1 ? '1 agent runs an older hook set' : `${agents.length} agents run an older hook set`;
    $('hooks-list').replaceChildren(...hooksGroups(agents, st.current));
}

/** After a start, list the agents on an older hook set (Operator only: not through Coral Hub). */
export async function watchHooksStatus() {
    if (isCloud()) return;
    try { showHooksBanner(await hooksStatus()); } catch { /* not shown */ }
}

async function refreshHooksOf(sessionId, force) {
    const resp = await fetch(`/api/sessions/live/${encodeURIComponent(sessionId)}/refresh-hooks`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ force: !!force }),
    });
    const data = await resp.json().catch(() => ({}));
    return { status: resp.status, data };
}

/** One agent: in a turn, it asks before stopping that turn. */
async function resumeOneOnCurrentHooks(sessionId, name) {
    let r = await refreshHooksOf(sessionId, false);
    if (r.status === 409 && r.data.code === 'busy') {
        const ok = await confirmWith(`Resume ${name} now?`,
            'It is in a turn or waiting on a prompt. Resuming stops that turn; its conversation is kept.', 'Resume now');
        if (!ok) return;
        r = await refreshHooksOf(sessionId, true);
    }
    if (r.data.ok) showToast(`${name} resumed on the current hooks`);
    else showToast(r.data.error || 'Could not resume it', true);
    watchHooksStatus();
}

/** All of them, one at a time, 2 s plus up to 1 s apart; an agent in a turn is skipped and stays listed. */
 async function resumeAllOnCurrentHooks() {
    let st;
    try { st = await hooksStatus(); } catch { return; }
    const agents = (st.agents || []).slice();
    // Progress shows in the banner's one line (the detail sits in the collapsed list).
    const count = $('hooks-count');
    const countText = count ? count.textContent : '';
    const buttons = [$('hooks-go'), $('hooks-dismiss')];
    for (const btn of buttons) if (btn) btn.disabled = true;
    let resumed = 0;
    let busy = 0;
    for (let i = 0; i < agents.length; i++) {
        if (i) await new Promise(r => setTimeout(r, 2000 + Math.random() * 1000)); // sleep-ok: the stagger between resumes
        if (count) count.textContent = `Resuming ${i + 1} of ${agents.length}…`;
        try {
            const r = await refreshHooksOf(agents[i].session_id, false);
            if (r.data.ok) resumed++;
            else if (r.status === 409 && r.data.code === 'busy') busy++;
        } catch { /* the next one */ }
    }
    for (const btn of buttons) if (btn) btn.disabled = false;
    if (count) count.textContent = countText;
    showToast(busy
        ? `Resumed ${resumed}; ${busy} in a turn were skipped. Resume all again when they are idle.`
        : `Resumed ${resumed} of ${agents.length} agents on the current hooks`);
    watchHooksStatus();
}
