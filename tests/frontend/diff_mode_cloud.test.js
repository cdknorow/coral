// On the hub the changed-files diff base is the one machine setting the UI may change, through its own narrow route; every other
// /api/settings URL (and system, webhooks, license, servers) must still be refused before it is sent.
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    globalThis.location = { protocol: 'https:', host: 'hub.example' };
    globalThis.document = { addEventListener() {}, getElementById: id => (id === 'coral-boot' ? { textContent: '{"mode":"cloud"}' } : null) };
    const m = await import(pathToFileURL(path.join(dir, 'server_base.js')).href);
    assert.strictEqual(m.isCloud(), true);
    assert.strictEqual(m.serverUrl('/api/settings/git-diff-mode', 'ws1'), '/api/remote/ws1/api/settings/git-diff-mode');
    for (const never of ['/api/settings', '/api/settings?x=1', '/api/settings/git-diff-mode/x', '/api/settings/git-diff-modes', '/api/settings/cli_path_claude',
        '/api/system/api-key', '/api/webhooks', '/api/license/status', '/api/servers']) {
        assert.strictEqual(m.serverUrl(never, 'ws1'), '/api/cloud-unavailable', never);
    }
    assert.strictEqual(m.serverUrl('/api/sessions/live', 'ws1'), '/api/remote/ws1/api/sessions/live');
    console.log('diff_mode_cloud.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
