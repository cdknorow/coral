// #3698: Settings > Coral Hub offers to replace a refused secret key, says what that discards,
// asks first, and only then posts {"confirm": true}. jsdom, no browser: node cloud_rekey.test.mjs
import assert from 'node:assert';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { JSDOM } from 'jsdom';

const here = path.dirname(fileURLToPath(import.meta.url));
const dir = path.resolve(here, '../../coral-go/pkg/frontend/static');
const dom = new JSDOM(`<!doctype html><body>
  <div id="panel">
    <div data-cloud="status"></div>
    <button data-cloud="connect" hidden></button><button data-cloud="disconnect" hidden></button>
    <div data-cloud="rekey-row" hidden><div data-cloud="rekey-text"></div><button data-cloud="rekey">Replace the secret key</button></div>
  </div>
  <div id="confirm-modal" style="display:none"><h3 id="confirm-modal-title"></h3><p id="confirm-modal-message"></p><button id="confirm-modal-yes">Yes</button></div>
</body>`, { url: 'http://127.0.0.1:8420/' });
for (const k of ['window', 'document', 'navigator', 'localStorage', 'location', 'sessionStorage', 'requestAnimationFrame', 'getComputedStyle', 'HTMLElement', 'Node', 'Event', 'CustomEvent', 'MutationObserver']) {
    Object.defineProperty(globalThis, k, { value: dom.window[k], configurable: true, writable: true });
}

const refused = { state: 'refused', exposed: true, remote_server_keys: 2, hub_url: 'https://hub.example.com' };
let status = { state: 'idle', configured: true, hub_url: 'https://hub.example.com' };
const posts = [];
globalThis.fetch = async (url, opts = {}) => {
    const method = opts.method || 'GET';
    if (method === 'POST' && url === '/api/cloud/rekey') {
        posts.push(JSON.parse(opts.body));
        status = { state: 'idle', configured: true, hub_url: 'https://hub.example.com' };
        return { ok: true, status: 200, json: async () => ({ ok: true, discarded_remote_server_keys: 2, discarded_hub_enrollment: true, status }) };
    }
    return { ok: true, status: 200, json: async () => status };
};

const m = await import(pathToFileURL(path.join(dir, 'cloud_connect.js')).href);
const tick = () => new Promise((r) => setTimeout(r, 20));
const part = (role) => document.querySelector(`[data-cloud="${role}"]`);

// The text says why and lists what is lost.
const text = m.rekeyText(refused);
assert.match(text, /Other accounts on this computer could read/);
assert.match(text, /https:\/\/hub\.example\.com/);
assert.match(text, /2 remote servers' API keys/);
assert.match(m.rekeyText({ state: 'refused', remote_server_keys: 1, message: 'bad file' }), /bad file.*1 remote server's API key/s);
assert.match(m.rekeyText({ state: 'refused', remote_server_keys: 0 }), /nothing stored yet/);

// A usable key: no row.
const panel = m.mountCloudConnect(document.getElementById('panel'));
// The dashboard's confirm dialog, reduced to what this test needs (render.js pulls the whole app).
panel.confirm = async (title, message, onYes) => {
    document.getElementById('confirm-modal-title').textContent = title;
    document.getElementById('confirm-modal-message').textContent = message;
    document.getElementById('confirm-modal-yes').onclick = onYes;
    document.getElementById('confirm-modal').style.display = 'flex';
};
await tick();
assert.strictEqual(part('rekey-row').hidden, true);

// A refused key: the row, with what is lost.
status = { ...status, secret_key: refused };
panel.load();
await tick();
assert.strictEqual(part('rekey-row').hidden, false);
assert.match(part('rekey-text').textContent, /2 remote servers' API keys/);

// The button asks first; nothing is sent until Yes.
part('rekey').click();
await tick();
assert.strictEqual(document.getElementById('confirm-modal').style.display, 'flex');
assert.match(document.getElementById('confirm-modal-message').textContent, /cannot be undone/);
assert.strictEqual(posts.length, 0);
document.getElementById('confirm-modal-yes').click();
await tick();
assert.deepStrictEqual(posts, [{ confirm: true }]);
assert.strictEqual(part('rekey-row').hidden, true, 'gone once replaced');
assert.match(part('status').textContent, /secret key was replaced/);
console.log('cloud_rekey: ok');
process.exit(0);
