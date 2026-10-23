/* Settings > Network and Hub > Setup code (#6410, Security's #6408 rank 1 option A; #6579 part 2): the one-time code another device asks for (the sign-in page, "Link this computer to Coral Hub") shown where the person is,
 * not only in a terminal ('coral setup-code'). It is the same code and expiry; the route answers only a dashboard signed in on this computer, so the group is not there anywhere else, through Coral Hub, and
 * once a password is set (there is no code then). The group is there only while another device could use the code: Remote access is on, or a Coral Hub link is being made. The code is not on the screen until the
 * person asks ("Show setup code", a button that says whether it is open), is read aloud letter by letter, and hides itself after a minute (Settings is the screen people share in a call). It is read when the Network and
 * Hub pane comes into view and kept only in memory; the page writes it nowhere. */
import { copyText } from './utils.js';
import { serverFetch } from './server_base.js';
import { importOnce } from './import_once.js';
const { spellCode } = await importOnce('./cloud_words.js');

const $ = id => document.getElementById(id);
const SHOW_MS = 60000; // how long the code stays on the screen
let _code = '';
let _available = false; // the route has a code for this page
let _expires = 0;
let _enrolling = false; // a Coral Hub link is being made
let _wired = false;
let _token = 0; // a later read replaces an earlier one that is still in flight
let _hideTimer = 0;

const minutes = s => Math.max(1, Math.round(Number(s) / 60));
const REST_HINT = 'To sign in from another device.';

function hide(say) {
    clearTimeout(_hideTimer);
    const box = $('settings-setup-code-box'), btn = $('settings-setup-code-reveal');
    if (!box || !btn) return;
    const wasOpen = !box.hidden;
    box.hidden = true;
    $('settings-setup-code').textContent = '';
    $('settings-setup-code-spoken') && ($('settings-setup-code-spoken').textContent = '');
    btn.textContent = 'Show setup code';
    btn.setAttribute('aria-expanded', 'false');
    $('settings-setup-code-hint').textContent = wasOpen && say ? 'Setup code hidden.' : REST_HINT;
}

function show() {
    const box = $('settings-setup-code-box'), btn = $('settings-setup-code-reveal');
    if (!_code || !box || !btn) return;
    $('settings-setup-code').textContent = _code;
    const spoken = $('settings-setup-code-spoken'); if (spoken) spoken.textContent = `Setup code: ${spellCode(_code)}`;
    box.hidden = false;
    btn.textContent = 'Hide setup code';
    btn.setAttribute('aria-expanded', 'true');
    const m = minutes(_expires);
    $('settings-setup-code-hint').textContent = `For linking or signing in from another device. Valid for about ${m} more minute${m === 1 ? '' : 's'}; it works once. It hides itself in a minute.`;
    clearTimeout(_hideTimer);
    _hideTimer = setTimeout(() => hide(true), SHOW_MS); // timeout-ok: the code leaves the screen by itself
}

/** The group is shown only while another device could use the code. */
function syncGroup() {
    const group = $('net-group-setup');
    if (!group) return;
    const remote = !!$('settings-remote-access-enabled')?.checked;
    const want = _available && (remote || _enrolling);
    if (!want && !group.hidden) hide(false);
    group.hidden = !want;
}

/** The note under the Remote access switch: a change waits for Save, and turning it on says what it then lets in. */
function syncRemoteNote() {
    const box = $('settings-remote-access-enabled'), note = $('net-remote-note');
    if (!box || !note) return;
    const saved = box.dataset.saved === '1';
    const pending = box.dataset.saved !== undefined && box.checked !== saved;
    note.textContent = !pending ? '' : box.checked
        ? 'Applies when you press Save settings. Other devices on your network can then reach Coral; they need your password or a setup code.'
        : 'Applies when you press Save settings.';
}

/** Read the code (it is not shown until asked for), or take the group away when the server has none for this page. */
export async function loadSetupCode() {
    const mine = ++_token;
    try {
        const r = await serverFetch('local', '/api/auth/setup-code/show', { cache: 'no-store' });
        if (mine !== _token) return;
        if (!r.ok) { _available = false; _code = ''; syncGroup(); return; }
        const d = await r.json();
        if (mine !== _token) return;
        if (!d || typeof d.code !== 'string' || !d.code) { _available = false; _code = ''; syncGroup(); return; }
        _available = true; _code = d.code; _expires = d.expires_in;
        if (!$('settings-setup-code-box')?.hidden) show(); // an open code follows the new one
        syncGroup();
    } catch { if (mine === _token) { _available = false; _code = ''; syncGroup(); } }
}

/** Called when Settings opens: the code is read whenever the Network and Hub pane comes into view, and now if it is. */
export function initSetupCode() {
    if (!_wired) {
        _wired = true;
        document.addEventListener('coral-settings-group', e => {
            if (e.detail && e.detail.id === 'network') { loadSetupCode(); syncGroup(); syncRemoteNote(); } else hide(false);
        });
        document.addEventListener('coral-cloud-state', e => { _enrolling = !!e.detail && e.detail.state === 'enrolling'; syncGroup(); });
        document.addEventListener('coral-remote-access-status', () => { syncGroup(); syncRemoteNote(); });
        $('settings-remote-access-enabled')?.addEventListener('change', () => { syncGroup(); syncRemoteNote(); });
        $('settings-setup-code-reveal')?.addEventListener('click', () => { if ($('settings-setup-code-box').hidden) show(); else hide(true); });
        $('settings-setup-code-copy')?.addEventListener('click', copy);
    }
    const pane = $('settings-pane-network');
    if (pane && !pane.hidden) loadSetupCode();
    syncGroup(); syncRemoteNote();
}

async function copy() {
    if (!_code) return;
    const ok = await copyText(_code);
    $('settings-setup-code-hint').textContent = ok ? 'Copied.' : 'Could not copy: select the code and copy it.';
}
