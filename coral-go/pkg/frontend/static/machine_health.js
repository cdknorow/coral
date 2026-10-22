/* Machine health (#3446): the card and the status dot for Coral's machine_health (#3444).
 *
 * Locally the dashboard's own feed (/ws/coral) carries it: in full in coral_update, then only the
 * changed fields in coral_diff (null for one that went away). Through Coral Hub each servers[]
 * entry carries its machine's object while that machine is online and connected.
 *
 * Unknown, never a stale "ok": locally while the feed socket is disconnected or before the first
 * value; through the hub when the entry has no machine_health (the machine is offline, held or
 * reconnecting). A healthy machine that changes nothing sends nothing, so there is no age rule.
 *
 * Everything is drawn with textContent: the object holds numbers and two enums only, and a
 * value outside the enums is shown as unknown rather than echoed. */

import { state } from './state.js';
import { onBuilt } from './lazy_dom.js';

// Maps, not plain objects: a value such as "constructor" or "__proto__" must not count as known
// (QA, #3446 review).
const STATUS = new Map([['ok', 'OK'], ['busy', 'Busy'], ['stalled', 'Stalled']]);
const REASON = new Map([['cpu', 'CPU'], ['memory', 'memory'], ['disk', 'disk'], ['stall', 'not answering']]);
// The schema's top-level fields (#3444): nothing else is kept from a feed message.
const FIELDS = new Set(['status', 'reason', 'responsiveness_ms', 'coral', 'agents', 'host', 'disk']);

// Only the schema's fields of an object, as a fresh plain object.
function known(obj) {
    const out = {};
    for (const [k, v] of Object.entries(obj)) if (FIELDS.has(k)) out[k] = v;
    return out;
}

let _local = null; // this dashboard's own machine_health, as merged from the feed

/** Apply a feed message's machine_health: replace on coral_update, merge on coral_diff. */
export function applyMachineHealth(msg) {
    if (!msg) return;
    if (!('machine_health' in msg)) {
        // A full update without it: none sampled yet. Unknown, not the last value.
        if (msg.type === 'coral_update' && _local) { _local = null; document.dispatchEvent(new CustomEvent('coral:machine-health')); }
        return;
    }
    const mh = msg.machine_health;
    if (msg.type === 'coral_diff' && _local && mh && typeof mh === 'object') {
        const merged = { ..._local };
        for (const [k, v] of Object.entries(mh)) {
            if (!FIELDS.has(k)) continue;
            if (v === null) delete merged[k]; else merged[k] = v;
        }
        _local = merged;
    } else {
        _local = mh && typeof mh === 'object' ? known(mh) : null;
    }
    document.dispatchEvent(new CustomEvent('coral:machine-health'));
}

/** This dashboard's machine health, or null when unknown (no value yet, or the feed is down). */
 function localMachineHealth() {
    const ws = state.coralWs;
    if (!_local || !ws || ws.readyState !== 1) return null;
    return _local;
}

/** The status to show: ok | busy | stalled, or unknown (no value, or not one of the enums). */
 function healthStatus(h) {
    return h && typeof h.status === 'string' && STATUS.has(h.status) ? h.status : 'unknown';
}

/** One line for the status, e.g. "Busy (CPU)", "OK", "Unknown". */
 function healthStatusText(h) {
    const s = healthStatus(h);
    if (s === 'unknown') return 'Unknown';
    const r = typeof h.reason === 'string' ? REASON.get(h.reason) : undefined;
    return s !== 'ok' && r ? `${STATUS.get(s)} (${r})` : STATUS.get(s);
}

const num = v => (typeof v === 'number' && Number.isFinite(v) ? v : null);

/** Bytes as "512 MB", "1.4 GB"; '–' when unknown. */
 function formatBytes(v) {
    const b = num(v);
    if (b === null) return '–';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0, x = b;
    while (x >= 1024 && i < units.length - 1) { x /= 1024; i++; }
    return `${i >= 3 ? x.toFixed(1) : Math.round(x)} ${units[i]}`;
}

const pct = v => (num(v) === null ? '–' : `${Math.round(v)}%`);

/** The status dot: a small coloured dot with the status as its accessible name and title. */
export function healthDot(h, label = 'Machine') {
    const s = healthStatus(h);
    const dot = document.createElement('span');
    dot.className = `machine-health-dot machine-health-${s}`;
    dot.setAttribute('role', 'img');
    const text = `${label} health: ${healthStatusText(h)}`;
    dot.setAttribute('aria-label', text);
    dot.title = text;
    return dot;
}

/** The card (`online`: the machine is connected, so no reading yet is "waiting", not "not connected"): the status with its reason, then Coral, agents, load, memory, disk and responsiveness. */
export function healthCard(h, { title = 'Machine health', online = false, waitingText = 'Waiting for a health reading from this computer.' } = {}) {
    const card = document.createElement('section');
    card.className = 'machine-health-card';
    const head = document.createElement('div');
    head.className = 'machine-health-head';
    const t = document.createElement('h5');
    t.className = 'machine-health-title';
    t.textContent = title;
    const status = document.createElement('span');
    status.className = 'machine-health-status';
    status.textContent = healthStatusText(h);
    head.append(healthDot(h), t, status);
    card.appendChild(head);
    if (healthStatus(h) === 'unknown') {
        const note = document.createElement('p');
        note.className = 'machine-health-note';
        // An online machine has not relayed a reading yet: say so; "not connected" is for one that is not (the card's status must agree with the row's).
        note.textContent = online ? waitingText : 'No health reading: not connected.';
        card.appendChild(note);
        return card;
    }
    const c = h.coral || {}, a = h.agents || {}, host = h.host || {}, d = h.disk || {};
    const rows = [
        ['Coral', `${pct(c.cpu_percent)} CPU · ${formatBytes(c.rss_bytes)}`],
        ['Agents', `${num(a.count) ?? '–'} · ${pct(a.cpu_percent)} CPU · ${formatBytes(a.rss_bytes)}`],
        ['Load per core', num(host.load1_per_core) === null ? '–' : host.load1_per_core.toFixed(2) + (num(host.cpu_count) ? ` (${host.cpu_count} cores)` : '')],
        ['Memory available', num(host.mem_total) ? `${formatBytes(host.mem_available)} of ${formatBytes(host.mem_total)}` : '–'],
        ['Disk free', num(d.total_bytes) ? `${formatBytes(d.free_bytes)} of ${formatBytes(d.total_bytes)}` : '–'],
        ['Responsiveness', num(h.responsiveness_ms) === null ? '–' : `${h.responsiveness_ms} ms`],
    ];
    const dl = document.createElement('dl');
    dl.className = 'machine-health-grid';
    for (const [k, v] of rows) {
        const dt = document.createElement('dt');
        dt.textContent = k;
        const dd = document.createElement('dd');
        dd.textContent = v;
        dl.append(dt, dd);
    }
    card.appendChild(dl);
    return card;
}

// The Settings card for this machine (local dashboard only), redrawn as the feed changes.
function renderLocalCard() {
    const slot = document.getElementById('machine-health-local');
    if (!slot) return;
    slot.replaceChildren(healthCard(localMachineHealth(), { title: 'This machine' }));
}
onBuilt('settings-modal', renderLocalCard); // the card's slot is in Settings, built on first open (#4500)
if (typeof document !== 'undefined') { // a page (pure-node tests import this module too)
    document.addEventListener('coral:machine-health', renderLocalCard);
    document.addEventListener('coral:feed-state', renderLocalCard);
    renderLocalCard();
}
