/* Settings > Terminal backend (#3634): tmux keeps agents running across Coral restarts but any
 * agent can reach other agents' terminals through its socket; pty isolates agents, which end when
 * Coral restarts. One Coral runs one backend, chosen when it starts, so a change applies at the
 * next start: Apply asks first, puts the running agents to sleep when going to pty, saves, and
 * restarts Coral where it can restart itself. Wording: Security's (#3632, with its #3634 edits).
 * Under pty, agents still running in Coral's old tmux server get a banner with End them. None of
 * this exists through Coral Hub. */

import { showToast } from './utils.js';
import { isCloud } from './server_base.js';
import { showConfirmModal } from './render.js';

let _status = null; // GET /api/system/terminal-backend

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
    return st.saved || 'tmux';
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
    if (_status.choice) {
        const next = nextBackend(_status);
        for (const r of document.querySelectorAll('input[name="terminal-backend"]')) {
            r.checked = r.value === next;
            if (!r._tbWired) { r._tbWired = true; r.addEventListener('change', renderPending); }
        }
        $('tb-note').textContent = osNote();
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
export async function applyTerminalBackendChange() {
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
