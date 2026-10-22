/* Coral Hub on first launch and on the home screen (#3221).

   - First launch: after the EULA, the dashboard opens "Connect to Coral Hub" (app.coralhub.dev)
     with the real connect flow inline (cloud_connect.js, the same code as Settings > Coral
     Hub): Connect → link and code → live status → once connected, how much the hub may do
     → Done. "Skip for now" keeps Coral local. "Run your own hub" (coral --hub) is the
     secondary option, with a short explainer. The server marks a new install
     welcome_pending; the answer is saved as welcome_choice, so another browser does not ask
     again. Settings > About reopens it. It opens only when this build has a hub configured
     and the server was started to offer it (a hub URL passed explicitly, or --cloud-connect
     prompt or auto: offer_at_launch, #3459); otherwise Coral Hub waits on the home screen and
     in Settings.
   - Home: a short section about Coral Hub and running your own hub, which the user can
     hide (home_editions_hidden).
   Neither appears in the Coral Hub itself or in an agent popout. */

import { state } from './state.js';
import { isCloud } from './server_base.js';
import { isPopout } from './popout.js';
import { escapeAttr, escapeHtml, showToast } from './utils.js';
import { switchRow } from './switch_row.js';
import { importOnce } from './import_once.js';
import { trapTab } from './dialog_focus.js';

 const OWN_HUB_GUIDE_URL = 'https://subgentic.github.io/coral/multi-server-hub/';

function _available() {
    return !isCloud() && !isPopout();
}

// How long the first-launch check waits for a connector still loading (#3285): tries × waitMs.
// Tests seed window.__coralEditionsLoadingWaitMs before the app runs to make it quick.
const LOADING_TRIES = 10;
const loadingWaitMs = () => Number(window.__coralEditionsLoadingWaitMs) || 300;

const _pending = () => String((state.settings || {}).welcome_pending) === 'True';

async function _saveSettings(values) {
    Object.assign(state.settings || (state.settings = {}), values);
    try {
        await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(values) });
    } catch { /* kept for this page; the server asks again next launch */ }
}

/** After settings load: the first-launch screen when due (and a hub is configured), and the home section. */
export async function initEditions() {
    syncHomeEditions();
    if (!_available() || !_pending()) return;
    let st = null;
    try {
        // The connector may still be loading its stored enrollment (#3285): ask again briefly.
        for (let i = 0; i < LOADING_TRIES; i++) {
            const r = await fetch('/api/cloud/status');
            st = r.ok ? await r.json() : null;
            if (!st || !st.loading) break;
            await new Promise(res => setTimeout(res, loadingWaitMs()));
        }
    } catch { return; }
    // 'off': this build has no hub to connect to (a dev build, or --cloud-url off). Still
    // loading after ~3 s (a startup credential retrying the hub can take longer): it may be
    // enrolled, so don't ask now; welcome_pending stays and the next launch asks.
    if (st && (st.state === 'off' || st.loading)) return;
    // Already connected (with coral cloud connect, before the dashboard was opened): that
    // answers the first-launch question.
    if (st && ['connected', 'connecting', 'retrying'].includes(st.state)) {
        await _saveSettings({ welcome_choice: 'connected', welcome_pending: 'False' });
        return;
    }
    // Not offered at launch (the default, #3459): no popup. welcome_pending stays, so a
    // server later started with the offer still asks once.
    if (!st || !st.offer_at_launch) return;
    showHubSetup();
}

// ── The screen ───────────────────────────────────────────────────────────

let _returnFocus = null;
let _panel = null;

function _modal() {
    let m = document.getElementById('editions-modal');
    if (m) return m;
    m = document.createElement('div');
    m.id = 'editions-modal';
    m.className = 'modal editions-modal';
    m.setAttribute('role', 'dialog');
    m.setAttribute('aria-modal', 'true');
    m.setAttribute('aria-labelledby', 'editions-title');
    m.style.display = 'none';
    document.body.appendChild(m);
    // Escape is this screen's only while it is the topmost thing: a dialog opened over it (another .modal after it in the page, or an open <dialog>) has the Escape, as everywhere else
    const topmost = () => ![...document.querySelectorAll('.modal')].some(o => o !== m && o.style.display !== 'none' && o.getClientRects().length > 0 && (m.compareDocumentPosition(o) & Node.DOCUMENT_POSITION_FOLLOWING)) && !document.querySelector('dialog[open]');
    m.addEventListener('keydown', e => {
        if (e.key === 'Escape' && topmost()) { e.preventDefault(); _dismiss('skipped'); }
        if (e.key === 'Tab') trapTab(m, e);
    });
    // Escape answers "Skip for now" wherever the focus is: while the screen is open, a page-level handler in the capture phase takes it before the generic modal close does
    // (that one closes any .modal without saving the answer, so the question came back at the next launch). The screen now opens after the connect flow has loaded, a little later
    // than other start-up steps that move the focus, so the focus is not always inside it when the key is pressed.
    document.addEventListener('keydown', e => {
        if (e.key !== 'Escape' || m.style.display === 'none' || e.defaultPrevented || e.isComposing || !topmost()) return;
        e.preventDefault(); e.stopPropagation(); _dismiss('skipped');
    }, true);
    return m;
}

// The connect flow (cloud_connect.js, 21 KB) loads the first time the screen opens, not with the page (#5818); _onState only runs once it has mounted.
let _cc = null;

/** Open the screen: 'connect' (Connect to Coral Hub) or 'own-hub' (the explainer). */
export async function showHubSetup(step = 'connect') {
    if (!_available()) return;
    if (step !== 'own-hub' && !_cc) {
        try { _cc = await importOnce('./cloud_connect.js'); } catch { // a page left open across an update can no longer fetch it: say so; the next press tries again
            showToast('Could not load the connect screen. Reload the page.', true);
            return;
        }
    }
    const m = _modal();
    if (m.style.display === 'none') _returnFocus = document.activeElement;
    _panel?.stop();
    _panel = null;
    m.innerHTML = step === 'own-hub' ? _ownHubHtml() : _connectHtml();
    delete m.dataset.focused;
    m.style.display = 'flex';
    if (step !== 'own-hub') _panel = _cc.mountCloudConnect(m.querySelector('.editions-cloud'), { onState: _onState });
    (m.querySelector('[data-editions-focus]:not([hidden])') || m.querySelector('button:not([hidden])'))?.focus();
}

// The one question the Connect screen asks (the full wording is in Settings): the short line says what the choice lets Coral Hub do.
const ASK_MODES = [
    ['view', 'View', 'Look at agents, boards and files. Nothing can be typed or started from the web.'],
    ['interact', 'Interact', 'View, plus type into running agents, answer their prompts, post on boards and change tasks.'],
    ['launch', 'Launch', 'Interact, plus start, stop and restart agents, choose folders and manage boards. Launching runs code on this computer as you.'],
];

function _connectHtml() {
    return `<div class="modal-content editions-content">
        <h2 id="editions-title" class="editions-title">Connect to Coral Hub</h2>
        <p class="editions-sub">Use your agents from any browser, including your phone, at app.coralhub.dev. Coral Hub is in an invite-only beta.</p>
        <div class="editions-cloud">
            <div class="editions-status" data-cloud="status" role="status" aria-live="polite">Loading…</div>
            <div class="editions-code" data-cloud="code" hidden>
                <div>Open <a data-cloud="link" target="_blank" rel="noopener noreferrer"></a> — your code should already be filled in:</div>
                <div class="editions-usercode" data-cloud="usercode"></div>
            </div>
            <div class="cloud-command-row" data-cloud="command-row" hidden><code data-cloud="command"></code><button type="button" class="btn btn-small" data-cloud="copy">Copy</button></div>
            <fieldset class="editions-ask" data-cloud="ask-row" hidden>
                <legend id="editions-ask-q">What may Coral Hub do on this computer?</legend>
                ${ASK_MODES.map(([value, name, text]) => `<label class="editions-ask-opt"><input type="radio" name="editions-mode" value="${escapeAttr(value)}" data-cloud-mode aria-describedby="editions-ask-${escapeAttr(value)}"><span class="editions-ask-text"><span class="editions-ask-name">${escapeHtml(name)}</span><span class="editions-ask-desc" id="editions-ask-${escapeAttr(value)}">${escapeHtml(text)}</span></span></label>`).join('')}
                <div data-cloud="auto-accept-row" hidden>
                    ${switchRow({ label: 'Coral Hub can turn on auto-accept', labelClass: 'editions-auto-accept', attrs: ' data-cloud="allow-auto-accept"', desc: 'Agents it launches act without asking for permission each time.', descId: 'editions-ask-auto', descClass: 'editions-ask-desc', descTag: 'div' })}
                </div>
                <div class="editions-ask-note">You can change this any time in Settings › Network and Hub.</div>
            </fieldset>
            <div class="editions-cloud-actions">
                <button type="button" class="btn btn-primary editions-connect" data-cloud="connect" data-editions-focus hidden>Connect</button>
                <button type="button" class="btn" data-cloud="cancel" hidden>Cancel</button>
            </div>
        </div>
        <div class="modal-actions editions-actions">
            <button type="button" class="btn btn-link editions-own" data-editions="own-hub">Run your own hub instead</button>
            <span class="editions-actions-spacer"></span>
            <button type="button" class="btn" data-editions="skip">Skip for now</button>
            <button type="button" class="btn btn-primary" data-editions="done" hidden>Done</button>
        </div>
    </div>`;
}

function _ownHubHtml() {
    return `<div class="modal-content editions-content">
        <h2 id="editions-title" class="editions-title">Run your own hub</h2>
        <p class="editions-sub">One dashboard for the agents on all your machines. Free and self-hosted.</p>
        <ol class="editions-steps">
            <li>On the machine you'll use as the hub, start Coral with <code>coral --hub</code>.</li>
            <li>On each other machine, start Coral and copy its API key from Settings.</li>
            <li>In the hub's <strong>Servers</strong> tab, add each machine with its address and key.</li>
        </ol>
        <a class="editions-guide" href="${escapeAttr(OWN_HUB_GUIDE_URL)}" target="_blank" rel="noopener">Read the guide<span class="material-icons" aria-hidden="true">open_in_new</span></a>
        <div class="modal-actions editions-actions">
            <button type="button" class="btn" data-editions="back">Back</button>
            <span class="editions-actions-spacer"></span>
            <button type="button" class="btn btn-primary" data-editions="done-own" data-editions-focus>Go to the dashboard</button>
        </div>
    </div>`;
}

// Each status: once connected, Done replaces Skip (the question "What may Coral Hub do on this computer?" is answered before Connect, and can still be changed here). Enrolled but reconnecting counts as connected: the hub approved this server,
// and the access choice applies when the tunnel comes back. The first status moves focus to
// Connect (it is hidden until then).
function _onState(st) {
    const m = document.getElementById('editions-modal');
    if (!m) return;
    const connected = !!st && (st.state === 'connected' || (_cc && _cc.enrolledRetrying(st)));
    const q = sel => m.querySelector(sel);
    const skip = q('[data-editions="skip"]'), done = q('[data-editions="done"]');
    const focusWasOnSkip = skip && document.activeElement === skip;
    if (skip) skip.hidden = connected;
    if (done) done.hidden = !connected;
    if (connected && focusWasOnSkip) done.focus();
    if (!m.dataset.focused) {
        m.dataset.focused = '1';
        const connect = q('[data-cloud="connect"]');
        if (connect && !connect.hidden) connect.focus();
    }
}

function _close() {
    _panel?.stop();
    _panel = null;
    const m = document.getElementById('editions-modal');
    if (m) { m.style.display = 'none'; m.innerHTML = ''; }
    if (_returnFocus && document.contains(_returnFocus)) _returnFocus.focus();
    _returnFocus = null;
}

/** Close the screen; on first launch this answers it (welcome_choice). */
function _dismiss(choice) {
    if (_pending()) _saveSettings({ welcome_choice: choice, welcome_pending: 'False' });
    _close();
}

// ── The home section ─────────────────────────────────────────────────────

/** Show the home section unless hidden (or in the Coral Hub / a popout). */
 function syncHomeEditions() {
    const el = document.getElementById('editions-home');
    if (!el) return;
    el.hidden = !_available() || String((state.settings || {}).home_editions_hidden) === 'True';
}

// A server enrolled with the hub's old host (#3546, hub_moved; the header indicator reports the
// status) is not offered Coral Hub as if it had never connected: the home section's Coral Hub
// item says it must be connected again, set as text. Any other status restores the offer.
document.addEventListener('coral:welcome-built', syncHomeEditions); // a screen built after the settings loaded (the phone builds it on first show, #4497)
document.addEventListener('coral:cloud-status', e => {
    const copy = document.querySelector('#editions-home [data-editions="home-hub"] .editions-home-copy');
    if (!copy) return;
    const st = e.detail || {};
    const moved = st.state === 'idle' && (st.hub_moved || st.last_error_code === 'hub_moved');
    if (copy.dataset.offer === undefined) copy.dataset.offer = copy.innerHTML; // the template's own offer
    if (!moved) {
        if (copy.dataset.moved) { copy.innerHTML = copy.dataset.offer; delete copy.dataset.moved; }
        return;
    }
    let host = '';
    try { host = new URL(String(st.hub_url || '')).host; } catch { /* no host */ }
    const title = document.createElement('strong');
    title.textContent = 'Coral Hub moved';
    const text = document.createElement('span');
    text.textContent = `Connect this server again${host ? ` at ${host}` : ''}: run coral cloud connect --force on this computer.`;
    copy.replaceChildren(title, ' ', text); // the space keeps the two apart for a screen reader
    copy.dataset.moved = '1';
});

// ── Actions ──────────────────────────────────────────────────────────────

document.addEventListener('click', e => {
    const t = e.target && e.target.closest && e.target.closest('[data-editions]');
    if (!t) return;
    const action = t.dataset.editions;
    if (action === 'skip') _dismiss('skipped');
    else if (action === 'done') _dismiss('connected');
    else if (action === 'own-hub') showHubSetup('own-hub');
    else if (action === 'back') showHubSetup('connect');
    else if (action === 'done-own') _dismiss('own-hub');
    else if (action === 'open') { window.hideSettingsModal?.(); showHubSetup('connect'); }
    else if (action === 'home-hub') showHubSetup('connect');
    else if (action === 'home-own') showHubSetup('own-hub');
    else if (action === 'hide-home') { _saveSettings({ home_editions_hidden: 'True' }); syncHomeEditions(); }
    else return;
    e.preventDefault();
});
