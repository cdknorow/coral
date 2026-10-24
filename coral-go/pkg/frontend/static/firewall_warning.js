/* The Windows Firewall warning (#97) in a short form (#295): one sentence, the commands behind a disclosure with a Copy button each, and a
 * "don't show again" that the Settings row undoes. The server's text is one sentence, then a line per rule ("- Name (app) why:") followed by
 * the commands, each on a line of its own. Everything is put in the page with textContent, never as HTML. */

import { browserLocal } from './safe_storage.js';
import { fetchSystemStatus } from './system_status.js';

const DISMISSED_KEY = 'coral-firewall-warning-dismissed';
const el = (tag, cls, text) => { const e = document.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; };

/** The server's message as {summary, how, rules: [{label, commands: []}]}; commands are the lines that are not prose. */
export function parseFirewallWarning(message) {
    const parts = String(message || '').split('\n- ');
    const head = parts.shift() || '';
    // "<the risk>. Coral never makes firewall rules: ... To limit each rule ..., open PowerShell as an administrator and run:"
    const cut = head.indexOf(' Coral never makes firewall rules');
    const summary = (cut > 0 ? head.slice(0, cut) : head).trim();
    const how = cut > 0 ? head.slice(cut).trim() : '';
    const rules = parts.map(block => {
        const lines = block.split('\n').map(l => l.trim()).filter(Boolean);
        const label = (lines.shift() || '').replace(/:$/, '');
        return { label, commands: lines.filter(l => !/^Or, to remove/i.test(l)) };
    });
    return { summary, how, rules };
}

/** A key for the text, so a changed warning (another rule) is shown again after "don't show again". */
function keyOf(message) {
    let h = 0;
    for (const ch of String(message)) h = (h * 31 + ch.codePointAt(0)) | 0;
    return String(h);
}
export function firewallDismissedForGood(message) { return browserLocal.get(DISMISSED_KEY) === keyOf(message); }
export function dismissFirewallForGood(message) { browserLocal.set(DISMISSED_KEY, keyOf(message)); }
export function showFirewallAgain() { browserLocal.remove(DISMISSED_KEY); }

function copyButton(text) {
    const b = el('button', 'btn btn-small firewall-copy', 'Copy');
    b.type = 'button';
    b.addEventListener('click', async () => {
        try { await navigator.clipboard.writeText(text); b.textContent = 'Copied'; } catch { b.textContent = 'Select and copy'; }
        setTimeout(() => { b.textContent = 'Copy'; }, 2000);
    });
    return b;
}

/** The commands as a disclosure: closed, so the toast stays short. Each rule has its label and, one per line, its commands with a Copy button. */
export function firewallDetails(message, { open = false } = {}) {
    const { how, rules } = parseFirewallWarning(message);
    const details = el('details', 'firewall-details');
    details.open = open;
    details.append(el('summary', null, rules.length === 1 ? 'Show the commands' : `Show the commands (${rules.length} rules)`));
    if (how) details.append(el('p', 'firewall-how', how));
    for (const r of rules) {
        const box = el('div', 'firewall-rule');
        box.append(el('div', 'firewall-rule-label', r.label));
        for (const c of r.commands) {
            const row = el('div', 'firewall-cmd');
            row.append(el('code', null, c), copyButton(c));
            box.append(row);
        }
        details.append(box);
    }
    return details;
}

/** The Settings row, made when there is a warning to show (so a machine without one has no row at all, and the dialog's default markup is as before). */
function ensureRow() {
    let row = document.getElementById('settings-firewall-row');
    if (row) return row;
    const anchor = document.getElementById('settings-security');
    if (!anchor) return null;
    row = el('div', 'settings-row');
    row.id = 'settings-firewall-row';
    const dt = el('dt', 'toggle-dt');
    dt.append(el('span', null, 'Windows Firewall'), el('span', 'toggle-desc', 'A rule that lets other computers reach Coral on every port.'));
    const dd = el('dd');
    const body = el('div', 'settings-hint');
    body.id = 'settings-firewall-status';
    body.setAttribute('role', 'status');
    body.dataset.state = 'warn';
    dd.append(body);
    row.append(dt, dd);
    anchor.after(row);
    return row;
}

/** Settings: the row for the warning, there while the warning is, so the toast can be dismissed for good. */
export async function renderFirewallRow() {
    let message = '';
    try { const r = await fetchSystemStatus({ fresh: true }); if (r.ok) message = (await r.json()).firewall_warning || ''; } catch { /* no row */ }
    if (!message) { document.getElementById('settings-firewall-row')?.remove(); return; }
    const row = ensureRow();
    const body = row && document.getElementById('settings-firewall-status');
    if (!body) return;
    body.replaceChildren();
    body.append(el('p', 'firewall-summary', parseFirewallWarning(message).summary), firewallDetails(message));
    if (firewallDismissedForGood(message)) {
        const again = el('button', 'btn btn-small', 'Show the warning on the dashboard again');
        again.type = 'button';
        again.addEventListener('click', () => { showFirewallAgain(); again.remove(); });
        body.append(again);
    }
}
