/* Cloud Hub access for the selected agent (#3068). A server the hub (or the
 * machine's own setting) limits to View drops terminal input without an error,
 * so the dashboard decides up front: every send path refuses with a "Blocked:"
 * message and keeps the draft, keystrokes typed into the terminal are dropped,
 * and the terminal header shows a read-only chip. The state comes from the
 * Servers list (serverAccess) or from a {type: "hub_read_only"} frame the hub
 * sends on the terminal socket. Outside the Cloud Hub nothing here applies. */

import { state } from './state.js';
import { isCloud, sessionServer } from './server_base.js';
// utils.js is loaded on demand: these message builders stay importable from
// pure-node tests (utils touches localStorage when it loads).
const toast = (...args) => import('./utils.js').then(u => u.showToast(...args));

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
        ? 'Change it in Coral on that machine (Settings > Coral Hub).'
        : 'Raise it in Servers.';
    const need = label(required) ? ` needs ${label(required)}` : ' is read-only through the Cloud Hub';
    return `Blocked: ${a.label || 'this server'}${need}${parts.length ? ` (${parts.join(' · ')})` : ''}. ${fix}`;
}

/** Header chip text, e.g. "Read-only: Hub: View". */
export function readOnlyChipText(a) {
    const m = a.limitedBy === 'machine' ? label(a.effective) || label(a.local) : label(a.hub) || label(a.effective);
    if (!m) return 'Read-only';
    return a.limitedBy === 'machine' ? `Read-only: This machine: ${m}` : `Read-only: Hub: ${m}`;
}

function renderChip() {
    const chip = document.getElementById('hub-readonly-chip');
    if (!chip) return;
    const ro = state.currentSession && state.currentSession.hubReadOnly;
    chip.hidden = !ro;
    chip.textContent = ro ? ro.chip : '';
    chip.title = ro ? ro.message : '';
}

/** Mark `session` read-only with access record `a` (null clears it). Only the
 *  session still on screen is updated. */
export function setHubReadOnly(session, a, required = 'interact', fromFrame = false) {
    if (!session || state.currentSession !== session) return;
    session.hubReadOnly = a ? { message: accessBlockedMessage(a, required), chip: readOnlyChipText(a), fromFrame, hubLimits: a.limitedBy !== 'machine' } : null;
    renderChip();
}

/** Look up the selected agent's server access and update the chip. */
export async function refreshHubAccess(session) {
    if (!session || session.type !== 'live' || !isCloud()) { setHubReadOnly(session, null); return; }
    const server = sessionServer(session);
    if (!server || server === 'local') { setHubReadOnly(session, null); return; }
    const { serverAccess } = await import('./cloud_servers.js');
    const a = await serverAccess(server);
    setHubReadOnly(session, a && !a.mayInteract ? a : null);
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
    if (!session || !isCloud()) return false;
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
        ? { action: { label: 'Open Servers', onClick: () => import('./cloud_servers.js').then(m => m.showCloudServers()) } }
        : {};
    toast(ro.message, true, 6000, opts);
}

let _lastKeyToast = 0;
/** Synchronous check for keystrokes typed into the terminal (uses the known state). */
export function hubBlocksKeystrokes() {
    const ro = state.currentSession && state.currentSession.hubReadOnly;
    if (!ro) return false;
    if (Date.now() - _lastKeyToast > 5000) { _lastKeyToast = Date.now(); showBlocked(ro); }
    return true;
}
