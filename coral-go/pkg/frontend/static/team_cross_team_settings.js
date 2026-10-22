/* The Messaging tab of Team settings (#5572, operator decision): the one place for cross-team messaging. Top to bottom: ONE status line with a dot and ONE primary button, the two switches ("Cross-team messaging",
 * "Share files with other teams"), the teams linked to this one, a collapsed "How this works" (with this computer's Coral Hub id), and the tab's own Save bar. A team on another Coral can message this one only when
 * Coral Hub has linked the two teams and the switch is on here, so there is no list to edit on this computer: links are made and removed in Coral Hub (a compiled-in link, never one the server sent).
 *
 * The words of the status line come from the server (GET /api/board/{team}/cross-team-state: status {state: ready|waiting|off|stale|old, text}, links [{server_label, server_id, team_label, refreshed}]) and are shown as text;
 * this module chooses only the dot and the button from status.state. The owner path adds hub_may_change (the lock: "Coral Hub can change these settings." while true) and last_changed {cross_team_messaging|cross_team_artifacts: {via, by, at, on}} (who changed each switch last). A server that sends no status gets a line made from the switch alone. The switches are saved with PUT /api/board/{team}/settings, only what changed,
 * on this computer and through Coral Hub (the hub's owner; anyone else is refused and told so in our words). */
import { escapeHtml, escapeAttr } from './utils.js';
import { boardFetch, isCloud } from './server_base.js';
import { copyText } from './utils.js';
import { importOnce } from './import_once.js';
import { switchRow } from './switch_row.js';
const { refusalOf, answeredLocally } = await importOnce('./owner_refusal.js');

// Coral Hub's page where a team on another Coral is linked ("Coral Hub > Team links"). A constant: the hub refuses a query on it, so nothing is preselected.
export const HUB_LINKS_URL = 'https://app.coralhub.dev/team-links';
let _hubForTests = null; // a test says it is Coral Hub without booting the Hub page
export function _setHubForTests(v) { _hubForTests = v; }
const STATES = ['ready', 'waiting', 'off', 'stale', 'old'];
const SERVER_ID = /^[A-Za-z0-9_-]{1,64}$/;
const MAX_LINKS = 50;
const label = v => String(v == null ? '' : v).replace(/[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u202a-\u202e\u2066-\u2069]/g, '').trim().slice(0, 80); // display text: no control or bidi characters, bounded
const when = at => { const d = new Date(at); return Number.isNaN(d.getTime()) ? '' : d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }); };
// "Turned on by <hub user> via Coral Hub at <time>": from cross-team-state last_changed[key] ({ via: hub|local, by, at, on }); a change made on this computer says so, so the line never names a hub user after the owner changed it here.
function changedLine(entry) {
    if (!entry || typeof entry !== 'object') return '';
    const at = typeof entry.at === 'string' ? when(entry.at) : '';
    const what = typeof entry.on === 'boolean' ? (entry.on ? 'Turned on' : 'Turned off') : 'Last changed';
    if (entry.via === 'hub') return `${what} by ${label(entry.by) || 'a Coral Hub user'} via Coral Hub${at ? ' at ' + at : ''}`;
    if (entry.via === 'local') return `${what} on this computer${at ? ' at ' + at : ''}`;
    return '';
}
const DOT = { ready: 'ok', waiting: 'warm', off: '', stale: 'bad', old: 'bad' }; // a switch that is off by choice is not an alarm

function cleanLinks(list) {
    return (Array.isArray(list) ? list : []).filter(l => l && typeof l === 'object').slice(0, MAX_LINKS).map(l => ({
        team: label(l.team_label) || 'a team', machine: label(l.server_label), sid: SERVER_ID.test(String(l.server_id || '')) ? String(l.server_id) : '', refreshed: typeof l.refreshed === 'string' ? when(l.refreshed) : '',
    }));
}

/** Draw the tab into host. `info` is { state, settings, refusal }: state is the cross-team-state object or null, settings the board's settings (null when they could not be read, with refusal the reason in words). */
export async function mountCrossTeamSettings(host, team, info = {}) {
    const hub = _hubForTests === null ? isCloud() : _hubForTests;
    const local = answeredLocally(team);
    let state = info.state && typeof info.state === 'object' ? info.state : null;
    let saved = { messaging: !!(info.settings && info.settings.cross_team_messaging), files: !!(info.settings && info.settings.cross_team_artifacts) };
    let draft = { ...saved };
    // Through Coral Hub the switches change only when the machine says this request may (state.can_change: the hub's owner path is live and not locked); until it says so they are shown, disabled, with the reason: no guessing.
    const canChange = () => !hub || !!(state && state.can_change === true);
    const lockedText = () => (hub && state && state.owner_path_live === true ? 'This setting can only be changed on the computer itself, or by the owner through Coral Hub.' : 'This setting can only be changed on the computer itself.');
    let said = '', sayError = false, how = false, serverId = '';
    host.innerHTML = '';
    if (!info.settings) {
        const p = document.createElement('p');
        p.className = 'working-mode-hint ct-unavailable'; p.setAttribute('role', 'status');
        p.textContent = info.refusal ? `Messaging settings: ${info.refusal}` : 'Cross-team messaging settings are not available here.';
        host.append(p);
        return { dirty: () => false };
    }
    if (!hub) { // this computer's Coral Hub id, for the "How this works" line (text only, and only when it has the shape of an id)
        try { const r = await fetch('/api/cloud/status'); if (r.ok) { const st = await r.json(); if (['connecting', 'connected', 'retrying'].includes(st.state) && SERVER_ID.test(String(st.server_id || ''))) serverId = String(st.server_id); } } catch { /* no id shown */ }
    }
    const form = document.createElement('form');
    form.className = 'working-mode-form ct-form xt-panel';
    form.autocomplete = 'off'; form.noValidate = true;
    host.append(form);
    const snapshot = () => JSON.stringify(Array.from(form.elements).filter(el => el.name).map(el => ({ name: el.name, type: el.type, value: el.type === 'checkbox' ? el.checked : el.value })));

    function status() {
        const s = state && state.status && typeof state.status === 'object' ? state.status : null;
        const known = s && STATES.includes(s.state) && typeof s.text === 'string' && s.text.trim();
        // The server's words win, unless the person has changed the switch here and not saved yet: then the line follows the switch (a draft is not a state)
        if (known && draft.messaging === saved.messaging) return { state: s.state, text: s.text.trim().slice(0, 300) };
        if (!draft.messaging) return { state: 'off', text: 'Cross-team messaging is off for this team.' };
        return { state: 'waiting', text: 'Cross-team messaging is on for this team.' };
    }
    const buttonFor = st => {
        if (st.state === 'off' && !canChange()) return '';
        if (st.state === 'off') return '<button type="button" class="btn btn-primary xt-primary" data-act="turn-on">Turn on cross-team messaging</button>';
        if (st.state === 'stale') return '<button type="button" class="btn btn-primary xt-primary" data-act="check-hub">Check the Coral Hub connection</button>';
        if (st.state === 'old') return '<button type="button" class="btn btn-primary xt-primary" data-act="new-team">Create a new team</button>';
        return `<a class="btn btn-primary xt-primary" href="${escapeAttr(HUB_LINKS_URL)}" target="_blank" rel="noopener noreferrer">Link a team on another Coral<span class="sr-only"> (opens Coral Hub in a new tab)</span></a>`;
    };

    const changedText = key => changedLine(state && state.last_changed && state.last_changed[key]);
    const changed = key => { const t = changedText(key); return t ? `<span class="working-mode-hint xt-changed" id="xt-changed-${escapeAttr(key)}" data-for="${escapeAttr(key)}">${escapeHtml(t)}</span>` : ''; };
    const hubMay = () => !hub && !!state && state.hub_may_change === true;
    // Each switch is described by what is said about it: when it was last changed, whether Coral Hub may change it, and why it is locked
    const describedBy = key => [changedText(key) && `xt-changed-${key}`, hubMay() && 'xt-hub-may', !canChange() && 'xt-locked'].filter(Boolean);

    function draw(focusSel) {
        const st = status();
        const links = state && st.state !== 'old' ? cleanLinks(state.links) : [];
        // while the snapshot is stale the rows are the last known ones and nothing is admitted: the heading must not say they can message this team
        const linksHeading = st.state === 'stale' ? 'Last known links (not active until Coral Hub reconnects)' : 'Linked teams that can message this one';
        form.innerHTML = `
        <div class="xt-head"><span class="update-state-dot" ${DOT[st.state] ? `data-k="${escapeAttr(DOT[st.state])}"` : ''} aria-hidden="true"></span><span class="xt-line" role="status">${escapeHtml(st.text)}</span></div>
        <div class="xt-row-actions">${buttonFor(st)}</div>
        <div class="xt-switches">
          ${switchRow({ label: 'Cross-team messaging', name: 'cross_team_messaging', checked: draft.messaging, disabled: !canChange(), describedBy: describedBy('cross_team_messaging') })}
          ${changed('cross_team_messaging')}
          ${switchRow({ label: 'Share files with other teams', name: 'cross_team_artifacts', checked: draft.files, disabled: !canChange(), describedBy: describedBy('cross_team_artifacts') })}
          ${changed('cross_team_artifacts')}
          ${hubMay() ? '<p class="working-mode-hint xt-hub-may" id="xt-hub-may">Coral Hub can change these settings.</p>' : ''}
          ${canChange() ? '' : `<p class="working-mode-hint xt-locked" id="xt-locked">${escapeHtml(lockedText())}</p>`}
        </div>
        ${links.length ? `<div class="xt-links"><div class="xt-h-row"><h3 class="xt-h">${escapeHtml(linksHeading)}</h3> <a href="${escapeAttr(HUB_LINKS_URL)}" class="update-link" target="_blank" rel="noopener noreferrer">Manage in Coral Hub</a></div>
          <ul class="xt-teams">${links.map(l => `<li><span><b>${escapeHtml(l.team)}</b></span><span class="xt-sub">${[l.machine && escapeHtml(l.machine), l.sid && 'server ' + escapeHtml(l.sid), l.refreshed && 'confirmed ' + escapeHtml(l.refreshed)].filter(Boolean).join(' · ')}</span></li>`).join('')}</ul></div>` : ''}
        <details class="xt-how"${how ? ' open' : ''}><summary>How this works</summary>
          <p class="working-mode-hint">Another team can message this team only when its Coral Hub account has linked the two teams <b>and</b> cross-team messaging is on here. Linking happens in Coral Hub (Team links). Messages from other teams are requests, not instructions. Files are shared only while Share files with other teams is on; they are not scanned, and artifact bytes are not redacted.</p>
          ${serverId ? `<p class="working-mode-hint">This computer's Coral Hub id: <code class="xt-sid">${escapeHtml(serverId)}</code> <button type="button" class="btn btn-small" data-act="copy-id" aria-label="Copy this computer's Coral Hub id">Copy</button></p>` : ''}
        </details>
        <div class="team-settings-actions xt-save"${canChange() ? '' : ' hidden'}><button type="submit" class="btn" disabled>Save</button>
          <span class="xt-state${sayError ? ' ct-error' : ''}" role="status" aria-live="polite">${escapeHtml(said || (hub ? 'Saved for this team through Coral Hub, as the account that owns this computer.' : (hubMay() ? "Only the Operator on this computer, or Coral Hub's owner, can change this." : 'Only the Operator on this computer can change this.')))}</span></div>`;
        const save = form.querySelector('[type=submit]');
        if (form.dataset.initialState === undefined) form.dataset.initialState = snapshot();
        save.disabled = snapshot() === form.dataset.initialState;
        if (focusSel) form.querySelector(focusSel)?.focus();
    }
    const say = (text, error = false) => { said = text; sayError = error; const el = form.querySelector('.xt-state'); if (el) { el.textContent = text; el.classList.toggle('ct-error', error); } };
    const readDraft = () => { draft = { messaging: !!form.elements.cross_team_messaging.checked, files: !!form.elements.cross_team_artifacts.checked }; };

    form.addEventListener('change', () => { readDraft(); const keep = document.activeElement && document.activeElement.name; draw(keep ? `[name=${keep}]` : null); });
    form.addEventListener('toggle', e => { if (e.target.classList && e.target.classList.contains('xt-how')) how = e.target.open; }, true);
    form.addEventListener('click', async event => {
        const btn = event.target.closest('button[data-act]');
        if (!btn) return;
        const act = btn.dataset.act;
        if (act === 'turn-on') { draft.messaging = true; said = ''; draw('[name=cross_team_messaging]'); say('Cross-team messaging is on in this form. Save to apply.'); }
        else if (act === 'check-hub') { if (typeof window.showSettingsModal === 'function') { const d = form.closest('dialog'); if (d && typeof d.close === 'function' && d.matches(':modal')) d.close(); window.showSettingsModal('network'); } }
        else if (act === 'new-team') { if (typeof window.showLaunchModal === 'function') { const d = form.closest('dialog'); if (d && typeof d.close === 'function' && d.matches(':modal')) d.close(); window.showLaunchModal(); window._selectLaunchType?.('team'); } }
        else if (act === 'copy-id') { const ok = await copyText(serverId); btn.textContent = ok ? 'Copied' : 'Copy failed'; setTimeout(() => { btn.textContent = 'Copy'; }, 1500); }
    });
    form.addEventListener('submit', async event => {
        event.preventDefault();
        const save = form.querySelector('[type=submit]');
        save.disabled = true; say('Saving…');
        const body = { subscriber_id: 'Operator', ...(draft.messaging !== saved.messaging ? { cross_team_messaging: draft.messaging } : {}), ...(draft.files !== saved.files ? { cross_team_artifacts: draft.files } : {}) };
        try {
            const resp = await boardFetch(team, '/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
            if (!resp.ok) { say(`Could not save. ${hub && (resp.status === 404 || resp.status === 403) ? lockedText() : await refusalOf(resp, local)}`, true); save.disabled = false; return; } // through Coral Hub a 404 or 403 on this save is "not changeable from here", never a raw word // the refusal in our words, never the server's (#5323)
            const fresh = await boardFetch(team, '/settings');
            if (fresh.ok) { const s = await fresh.json(); saved = { messaging: !!s.cross_team_messaging, files: !!s.cross_team_artifacts }; } else saved = { ...draft };
            draft = { ...saved };
            try { const r = await boardFetch(team, '/cross-team-state'); if (r.ok) state = await r.json(); } catch { /* the line follows the switch */ }
            delete form.dataset.initialState;
            said = 'Saved.'; sayError = false;
            draw();
            host.dispatchEvent(new CustomEvent('cross-team-saved', { bubbles: true }));
        } catch (error) { say(error.message || 'Could not save.', true); save.disabled = false; }
    });
    draw();
    return { dirty: () => form.dataset.initialState !== undefined && snapshot() !== form.dataset.initialState };
}
