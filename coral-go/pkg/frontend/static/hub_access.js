/* Cloud Hub access for the selected agent (#3068). A server the hub (or the
 * machine's own setting) limits to View drops terminal input without an error,
 * so the dashboard decides up front: every send path refuses with a "Blocked:"
 * message and keeps the draft, keystrokes typed into the terminal are dropped,
 * and the terminal header shows a read-only chip. The state comes from the
 * Servers list (serverAccess) or from a {type: "hub_read_only"} frame the hub
 * sends on the terminal socket. Outside the Cloud Hub nothing here applies. */

import { importOnce } from './import_once.js';
import { state } from './state.js';
import { isCloud, sessionServer } from './server_base.js';
import { sessionUnreachable, unreachableReason, backoffReset } from './remote_health.js';
import { setPill } from './dom_write.js';
// utils.js is loaded on demand: these message builders stay importable from
// pure-node tests (utils touches localStorage when it loads).
const toast = (...args) => importOnce('./utils.js').then(u => u.showToast(...args));

const LABEL = { view: 'View', interact: 'Interact', launch: 'Launch' };
// Only known modes are named: a value the server sent is never echoed.
const label = m => LABEL[m] || null;

/** "Blocked: ..." text for an action needing `required`, from an access record
 *  {hub, local, effective, limitedBy, label}. Absent fields are left out. */
export function accessBlockedMessage(a, required = 'interact') {
    const parts = [];
    if (label(a.hub)) parts.push(`Hub: ${label(a.hub)}`);
    if (label(a.local)) parts.push(`This machine allows: ${label(a.local)}`);
    if (label(a.effective)) parts.push(`Effective: ${label(a.effective)}`);
    const fix = a.limitedBy === 'machine'
        ? 'Change it in Coral on that machine (Settings > Network and Hub).'
        : 'Choose a higher Hub access in Servers.';
    const need = label(required) ? ` needs ${label(required)}` : ' is read-only through the Coral Hub';
    return `Blocked: ${a.label || 'this server'}${need}${parts.length ? ` (${parts.join(' · ')})` : ''}. ${fix}`;
}

// Launch-level actions on a whole team or board (#3443): through the Coral Hub, killing a whole team
// and deleting a board need Launch on that machine (#3433). The menus draw from the cached Servers
// list; until it is loaded they show the action (the hub still refuses) and redraw once it is.
let _cloudServers = null;
let _warming = null;
// Warm the list only until it has loaded once (DevOps' #3443 note): a failed load, or a machine
// the list leaves out, must not make every menu redraw ask again and redraw again. The redraw
// event goes out only when this server's answer actually changed. A list a mode change marked
// stale is asked for again the same way, and its old answer is what callers see meanwhile.
function _warmAccess(server) {
    if (_warming || (_cloudServers && _cloudServers.serverListSettled())) return;
    const before = _cloudServers ? JSON.stringify(_cloudServers.cachedServerAccess(server)) : 'null';
    _warming = importOnce('./cloud_servers.js')
        .then(m => { _cloudServers = m; return m.serverAccess(server); })
        .then(a => { if (JSON.stringify(a) !== before) document.dispatchEvent(new CustomEvent('coral:server-access-loaded')); })
        .catch(() => {})
        .finally(() => { _warming = null; });
}

/** Why this account cannot do an action needing `tier` ('interact' or 'launch') on `server` through the
 *  Coral Hub: the "Blocked: … needs Interact/Launch" text, or '' when it can, outside the hub, or when
 *  that is not known yet (the list is asked for, and the menus redraw once it is in). While a stale
 *  list is asked for again, its last answer stands, so nothing flickers to "allowed" (#5004). */
export function blockedReason(server, tier) {
    if (!isCloud() || !server || server === 'local') return '';
    const a = _cloudServers ? _cloudServers.cachedServerAccess(server) : null;
    _warmAccess(server);
    if (!a) return '';
    return (tier === 'launch' ? a.mayLaunch : a.mayInteract) ? '' : accessBlockedMessage(a, tier);
}

/** Why this account cannot kill a whole team or delete a board on `server` through the Coral Hub
 *  (both need Launch): the reason, or '' (see blockedReason). */
export function launchBlockedReason(server) { return blockedReason(server, 'launch'); }

/** Header chip text, e.g. "Read-only: Hub: View". */
 function readOnlyChipText(a) {
    const m = a.limitedBy === 'machine' ? label(a.effective) || label(a.local) : label(a.hub) || label(a.effective);
    if (!m) return 'Read-only';
    return a.limitedBy === 'machine' ? `Read-only: This machine: ${m}` : `Read-only: Hub: ${m}`;
}

 const VIEW_ONLY_PLACEHOLDER = 'View only through Coral Hub';

function renderChip() {
    const chip = document.getElementById('hub-readonly-chip');
    const ro = state.currentSession && state.currentSession.hubReadOnly;
    if (chip) {
        chip.hidden = !ro;
        chip.textContent = ro ? ro.chip : '';
        chip.title = ro ? ro.message : '';
        // On a phone the chip is truncated: a tap (or Enter) says the whole reason (#3411).
        chip.tabIndex = ro ? 0 : -1;
    }
    applyViewOnly(!!ro);
}

// A server only open for viewing looks read-only too (#3411): the composer is disabled with a
// short note, Send is greyed out, and the terminal stops offering "Click to type". Every send
// path still refuses on its own (hubBlocked); this is what the user sees before trying.
function applyViewOnly(on) {
    const pane = document.getElementById('command-pane');
    if (pane) pane.classList.toggle('hub-view-only', on);
    document.getElementById('xterm-container')?.classList.toggle('hub-view-only', on);
    const input = document.getElementById('command-input');
    if (!input) return;
    if (on) {
        if (input.dataset.hubDisabled) return;
        // An input another state already disabled (asleep) stays that state's to undo.
        if (input.disabled) return;
        input.dataset.hubDisabled = '1';
        input.dataset.hubPlaceholder = input.placeholder;
        input.disabled = true;
        input.placeholder = VIEW_ONLY_PLACEHOLDER;
        // A disabled box is not in the tab order and a placeholder is not a name: the name says why it is off
        input.dataset.hubAriaLabel = input.getAttribute('aria-label') || '';
        input.setAttribute('aria-label', VIEW_ONLY_PLACEHOLDER);
    } else if (input.dataset.hubDisabled) {
        input.disabled = false;
        input.placeholder = input.dataset.hubPlaceholder || input.placeholder;
        if (input.dataset.hubAriaLabel) input.setAttribute('aria-label', input.dataset.hubAriaLabel); else input.removeAttribute('aria-label');
        delete input.dataset.hubDisabled;
        delete input.dataset.hubPlaceholder;
        delete input.dataset.hubAriaLabel;
    }
}

if (typeof document !== 'undefined') {
    const sayReason = e => {
        const chip = e.target && e.target.closest && e.target.closest('#hub-readonly-chip');
        if (!chip || chip.hidden || !chip.title) return;
        if (e.type === 'keydown' && e.key !== 'Enter' && e.key !== ' ') return;
        if (e.type === 'keydown') e.preventDefault();
        toast(chip.title, true, 8000);
    };
    document.addEventListener('click', sayReason);
    document.addEventListener('keydown', sayReason);
}

/** Mark `session` read-only with access record `a` (null clears it). Only the
 *  session still on screen is updated. */
export function setHubReadOnly(session, a, required = 'interact', fromFrame = false) {
    if (!session || state.currentSession !== session) return;
    const was = !!session.hubReadOnly;
    session.hubReadOnly = a ? { message: accessBlockedMessage(a, required), chip: readOnlyChipText(a), fromFrame, hubLimits: a.limitedBy !== 'machine', server: sessionServer(session) } : null;
    renderChip();
    // The terminal sets or follows the pane's size by this (#3422).
    if (was !== !!session.hubReadOnly) document.dispatchEvent(new CustomEvent('coral:hub-access-changed'));
}

/** Look up the selected agent's server access and update the chip. */
export async function refreshHubAccess(session) {
    if (!session || session.type !== 'live' || !isCloud()) { setHubReadOnly(session, null); return; }
    const server = sessionServer(session);
    if (!server || server === 'local') { setHubReadOnly(session, null); return; }
    const { serverAccess } = await importOnce('./cloud_servers.js');
    const a = await serverAccess(server);
    // A hub_read_only frame on the terminal socket is authoritative until the agent
    // is reselected: a lookup that resolves after it (a slow or cached Servers list)
    // never clears it (#3125).
    if (session.hubReadOnly && session.hubReadOnly.fromFrame) return;
    setHubReadOnly(session, a && !a.mayInteract ? a : null);
}

/** The user changed `serverId`'s access in the Servers tab, and `a` is a fresh
 *  lookup made after it (#3138). A frame-set read-only state otherwise stays until
 *  reselect (#3125); the user's own successful change is the one thing that
 *  re-decides it: an effective mode of Interact or more clears it, anything less
 *  keeps the lock with the lookup's reason (for example the machine's own limit).
 *  A failed lookup changes nothing. If the hub still refuses, it re-sends
 *  hub_read_only on the next dropped input, so the dashboard can't stay wrongly writable. */
export function accessChanged(serverId, a) {
    const session = state.currentSession;
    if (!a || !session || session.type !== 'live' || !isCloud() || sessionServer(session) !== serverId) return;
    if (a.mayInteract) setHubReadOnly(session, null);
    else setHubReadOnly(session, a, 'interact', !!(session.hubReadOnly && session.hubReadOnly.fromFrame));
}

/** The hub said so on the terminal socket: {type: "hub_read_only", required, hub_mode}. */
export function applyHubReadOnlyFrame(frame) {
    const session = state.currentSession;
    if (!session || session.type !== 'live') return;
    // Unknown values still mean read-only; the message just stays generic.
    const hub = typeof frame.hub_mode === 'string' ? frame.hub_mode : null;
    const server = sessionServer(session);
    const row = (state.servers || []).find(s => s && s.id === server);
    setHubReadOnly(session, { hub, effective: hub, limitedBy: 'hub', label: (row && row.label) || server },
        typeof frame.required === 'string' ? frame.required : null, true);
}

/** True (after showing the reason unless silent) when the selected agent's
 *  server only allows viewing. Every terminal write path calls this first. */
export async function hubBlocked(session, { silent = false } = {}) {
    if (!session) return false;
    // The agent's server is unreachable (any hub, not only the Cloud Hub): nothing
    // can reach it, so refuse with the reason instead of a failed send (#3151).
    const down = unreachableReason(session);
    if (down) {
        if (!silent) toast(down, true, 6000);
        return true;
    }
    if (!isCloud()) return false;
    // The Servers list is cached for 15 s, so a raised mode is picked up; a
    // refusal the hub sent on the terminal socket stays until reselect.
    if (!(session.hubReadOnly && session.hubReadOnly.fromFrame)) await refreshHubAccess(session);
    const ro = session.hubReadOnly;
    if (!ro) return false;
    if (!silent) showBlocked(ro);
    return true;
}

// The refusal toast; when the hub setting is what limits, it offers the Servers dialog.
function showBlocked(ro) {
    const opts = ro.hubLimits
        ? { action: { label: 'Open Servers', onClick: () => importOnce('./servers_tab.js').then(m => m.openServersTab(ro.server)) } }
        : {};
    toast(ro.message, true, 6000, opts);
}

let _lastKeyToast = 0;
/** Synchronous check for keystrokes typed into the terminal (uses the known state). */
export function hubBlocksKeystrokes() {
    const down = unreachableReason(state.currentSession);
    if (down) {
        if (Date.now() - _lastKeyToast > 5000) { _lastKeyToast = Date.now(); toast(down, true, 6000); }
        return true;
    }
    const ro = state.currentSession && state.currentSession.hubReadOnly;
    if (!ro) return false;
    if (Date.now() - _lastKeyToast > 5000) { _lastKeyToast = Date.now(); showBlocked(ro); }
    return true;
}

// The agent-view banner for an unreachable server (#3151): the same reason as the
// agent row's title, but visible on touch screens too. Called when an agent is
// selected and on every feed update; when the server comes back it clears the
// polls' back-off so they resume at once.
let _bannerFor = null, _bannerDown = false;
export function syncUnreachableBanner() {
    const s = state.currentSession;
    const live = !!(s && s.type === 'live');
    const key = live ? `${sessionServer(s)}|${s.session_id}` : null;
    const down = live && sessionUnreachable(s);
    if (key === _bannerFor && _bannerDown && !down) backoffReset(sessionServer(s));
    _bannerFor = key; _bannerDown = down;
    const el = document.getElementById('server-unreachable-banner');
    if (!el) return;
    setPill(el, down, down ? unreachableReason(s) : ''); // an overlay pill, shown after a delay (#5287)
}
