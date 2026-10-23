/* Settings > Network and Hub > Setup code (#6410, Security's #6408 rank 1 option A): the one-time code another device asks for (the sign-in page, "Link this computer to Coral Hub") shown where the person is,
 * not only in a terminal ('coral setup-code'). It is the same code and expiry; the route answers only a dashboard signed in on this computer, so the row is not there anywhere else, through Coral Hub, and
 * once a password is set (there is no code then). The row is made here, when there is a code, and the code is read only while the Network and Hub pane is in view: it is kept nowhere and written nowhere. */
import { copyText } from './utils.js';
import { serverFetch } from './server_base.js';

const $ = id => document.getElementById(id);
let _code = '';
let _wired = false;
let _token = 0; // a later read replaces an earlier one that is still in flight

const minutes = s => Math.max(1, Math.round(Number(s) / 60));

function row() {
    let r = $('settings-setup-code-row');
    if (r) return r;
    const anchor = $('settings-cloud')?.closest('.settings-row');
    if (!anchor) return null;
    r = document.createElement('div');
    r.className = 'settings-row settings-row-wide';
    r.id = 'settings-setup-code-row';
    r.innerHTML = '<dt id="settings-setup-code-label">Setup code</dt><dd>'
        + '<div class="settings-cloud-usercode-row"><div id="settings-setup-code" class="settings-mono-input settings-cloud-usercode" aria-labelledby="settings-setup-code-label"></div>'
        + '<button type="button" class="btn btn-icon" id="settings-setup-code-copy" aria-label="Copy the setup code" title="Copy the setup code"><span class="material-icons" aria-hidden="true">content_copy</span></button></div>'
        + '<div id="settings-setup-code-hint" class="settings-hint" role="status"></div></dd>';
    r.querySelector('#settings-setup-code-copy').addEventListener('click', copy);
    anchor.before(r);
    return r;
}

function remove() {
    _code = '';
    $('settings-setup-code-row')?.remove();
}

/** Read the code and show the row, or take it away when the server has none for this page. */
export async function loadSetupCode() {
    const mine = ++_token;
    try {
        const r = await serverFetch('local', '/api/auth/setup-code/show', { cache: 'no-store' });
        if (mine !== _token) return;
        if (!r.ok) { remove(); return; }
        const d = await r.json();
        if (mine !== _token) return;
        if (!d || typeof d.code !== 'string' || !d.code) { remove(); return; }
        const el = row();
        if (!el) return;
        _code = d.code;
        $('settings-setup-code').textContent = d.code;
        const m = minutes(d.expires_in);
        $('settings-setup-code-hint').textContent = `For linking or signing in from another device. Valid for about ${m} more minute${m === 1 ? '' : 's'}; it works once.`;
    } catch { if (mine === _token) remove(); }
}

/** Called when Settings opens: the code is read whenever the Network and Hub pane comes into view, and now if it is. */
export function initSetupCode() {
    if (!_wired) {
        _wired = true;
        document.addEventListener('coral-settings-group', e => { if (e.detail && e.detail.id === 'network') loadSetupCode(); });
    }
    const pane = $('settings-pane-network');
    if (pane && !pane.hidden) loadSetupCode();
}

async function copy() {
    if (!_code) return;
    const ok = await copyText(_code);
    $('settings-setup-code-hint').textContent = ok ? 'Copied.' : 'Could not copy: select the code and copy it.';
}
