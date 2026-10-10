/* Mobile connection flow: saved preference is not proof of effective access. */
import { state } from './state.js';

let generation = 0;
let controller = null;
let opener = null;
let saving = false;

const byId = id => document.getElementById(id);
const current = gen => gen === generation && byId('mobile-connect-modal')?.style.display !== 'none';

function clearConnection() {
    byId('mobile-connect-details').hidden = true;
    byId('mobile-connect-qr').replaceChildren();
    byId('mobile-connect-api-key').replaceChildren();
    byId('mobile-connect-url').textContent = '';
}

function button(label, action, primary = false) {
    const node = document.createElement('button');
    node.type = 'button'; node.className = primary ? 'btn btn-primary' : 'btn';
    node.textContent = label; node.addEventListener('click', action);
    return node;
}

function renderState(message, actions = [], error = false) {
    clearConnection();
    const status = byId('mobile-connect-status');
    status.textContent = message;
    status.setAttribute('role', error ? 'alert' : 'status');
    const controls = byId('mobile-connect-actions');
    const hadFocus = controls.contains(document.activeElement);
    controls.replaceChildren(...actions);
    if (hadFocus) (actions[0] || byId('mobile-connect-modal').querySelector('.modal-close-btn')).focus();
}

function failure(message) {
    renderState(message, [button('Retry', checkAccess, true), button('Cancel', hideMobileConnectModal)], true);
}

export function hideMobileConnectModal() {
    ++generation; controller?.abort(); controller = null; saving = false;
    const modal = byId('mobile-connect-modal');
    if (modal) { modal.style.display = 'none'; clearConnection(); }
    if (opener?.isConnected) opener.focus();
}

async function privacy(signal) {
    const response = await fetch('/api/system/privacy', { signal, cache: 'no-store' });
    if (!response.ok) throw new Error(`Could not check mobile access (${response.status}).`);
    const status = await response.json();
    if (typeof status.remote_access_enabled !== 'boolean' || typeof status.remote_access_effective !== 'boolean') {
        throw new Error('Could not verify the running mobile access state.');
    }
    return status;
}

function rememberStatus(status) {
    state.privacyStatus = status;
    state.settings = { ...state.settings, remote_access_enabled: status.remote_access_enabled };
    const checkbox = byId('settings-remote-access-enabled');
    if (checkbox) checkbox.checked = status.remote_access_enabled;
}

async function renderAccess(status, gen, signal) {
    if (!current(gen)) return;
    rememberStatus(status);
    if (!status.remote_access_enabled && status.remote_access_effective) {
        renderState('Mobile access is disabled in settings. Restart Coral to finish disabling access on the running server.',
            [button('Check again', checkAccess, true), button('Close', hideMobileConnectModal)]);
        return;
    }
    if (!status.remote_access_effective) {
        if (status.remote_access_enabled) {
            renderState('Mobile access is enabled in settings but is not running yet. Restart Coral to apply the change, then check again.',
                [button('Check again', checkAccess, true), button('Close', hideMobileConnectModal)]);
        } else {
            renderState('Mobile access is disabled. Enable it?',
                [button('Enable', enableMobileAccess, true), button('Cancel', hideMobileConnectModal)]);
        }
        return;
    }

    renderState('Loading mobile connection…', [button('Cancel', hideMobileConnectModal)]);
    const [networkResponse, keyResponse] = await Promise.all([
        fetch('/api/system/network-info', { signal }),
        fetch('/api/system/api-key', { signal }),
    ]);
    if (!networkResponse.ok || !keyResponse.ok) throw new Error('Could not load the mobile connection details.');
    const [network, key] = await Promise.all([networkResponse.json(), keyResponse.json()]);
    if (!current(gen)) return;
    if (network.remote_access_effective === false) throw new Error('Mobile access is not running. Check again after restarting Coral.');
    let baseUrl = window.location.origin;
    const address = network.primary || network.ips?.[0];
    if (address) baseUrl = `http://${address}:${network.port || window.location.port || '8420'}`;
    byId('mobile-connect-status').textContent = '';
    byId('mobile-connect-actions').replaceChildren();
    byId('mobile-connect-url').textContent = baseUrl;
    const image = document.createElement('img');
    image.alt = 'QR code for mobile connection';
    image.style.cssText = 'width:180px;height:180px;border-radius:8px;background:#fff;padding:8px';
    image.addEventListener('error', () => {
        if (current(gen)) failure('Could not load the mobile QR code.');
    });
    image.src = `/api/system/qr?url=${encodeURIComponent(baseUrl)}`;
    byId('mobile-connect-qr').append(image);
    const keyDisplay = byId('mobile-connect-api-key');
    keyDisplay.style.display = key.key ? '' : 'none';
    if (key.key) {
        const label = document.createElement('strong'); label.textContent = 'API Key: ';
        const code = document.createElement('code'); code.textContent = key.key; code.style.userSelect = 'all';
        const copy = button('Copy', async () => {
            try { await navigator.clipboard.writeText(key.key); copy.textContent = 'Copied!'; }
            catch { copy.textContent = 'Select key to copy'; }
        });
        copy.classList.add('btn-small'); copy.style.marginLeft = '6px';
        keyDisplay.append(label, code, copy);
        const hint = document.createElement('div');
        hint.className = 'text-secondary-sm'; hint.textContent = 'Use this key to connect securely from your phone.';
        keyDisplay.append(hint);
    }
    byId('mobile-connect-details').hidden = false;
}

async function checkAccess() {
    controller?.abort(); controller = new AbortController();
    const signal = controller.signal;
    const gen = ++generation;
    renderState('Checking mobile access…', [button('Cancel', hideMobileConnectModal)]);
    try { await renderAccess(await privacy(signal), gen, signal); }
    catch (error) { if (current(gen) && error.name !== 'AbortError') failure(error.message); }
}

async function enableMobileAccess() {
    if (saving) return;
    saving = true;
    controller?.abort(); controller = new AbortController();
    const signal = controller.signal;
    const gen = ++generation;
    renderState('Enabling mobile access…');
    try {
        const response = await fetch('/api/settings', {
            method: 'PUT', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ remote_access_enabled: true }), signal,
        });
        if (!response.ok) throw new Error(`Could not enable mobile access (${response.status}).`);
        // Re-read actual runtime state; saving a preference does not activate it.
        await renderAccess(await privacy(signal), gen, signal);
    } catch (error) {
        if (current(gen) && error.name !== 'AbortError') failure(error.message);
    } finally { if (gen === generation) saving = false; }
}

export async function showMobileConnectModal() {
    const modal = byId('mobile-connect-modal');
    if (!modal) return;
    if (modal.style.display === 'none') opener = document.activeElement;
    modal.style.display = 'flex'; saving = false;
    modal.querySelector('.modal-close-btn')?.focus();
    await checkAccess();
}
