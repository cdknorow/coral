// On the hub the New Agent dialog's CLI check (#4607, coral-go#4) is the one /api/system route that reaches the machine: serverUrl lets
// GET /api/system/cli-check through for `type` (a known agent type) and `source=cli_recheck`, as the tunnel's allowlist does
// (pkg/tunnel/routes_query.go), and still refuses everything else under /api/system before it is sent, a `binary` path included.
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    globalThis.location = { protocol: 'https:', host: 'hub.example' };
    globalThis.document = { addEventListener() {}, getElementById: id => (id === 'coral-boot' ? { textContent: '{"mode":"cloud"}' } : null) };
    const m = await import(pathToFileURL(path.join(dir, 'server_base.js')).href);
    assert.strictEqual(m.isCloud(), true);
    const NEVER = '/api/cloud-unavailable';
    for (const type of ['claude', 'agy', 'antigravity', 'gemini', 'codex', 'pi']) {
        assert.strictEqual(m.serverUrl(`/api/system/cli-check?type=${type}`, 'ws1'), `/api/remote/ws1/api/system/cli-check?type=${type}`, type);
        assert.strictEqual(m.serverUrl(`/api/system/cli-check?type=${type}&source=cli_recheck`, 'ws1'), `/api/remote/ws1/api/system/cli-check?type=${type}&source=cli_recheck`, type + ' recheck');
    }
    // Refused: a binary path, any other parameter, a repeated one, an unknown or missing type, another source, another path under it, another route under /api/system.
    for (const bad of ['/api/system/cli-check', '/api/system/cli-check?binary=/bin/sh&type=claude', '/api/system/cli-check?type=claude&binary=%2Fbin%2Fsh', '/api/system/cli-check?type=claude&x=1',
        '/api/system/cli-check?type=claude&type=codex', '/api/system/cli-check?type=bash', '/api/system/cli-check?type=', '/api/system/cli-check?type=Claude', '/api/system/cli-check?type=claude&source=other',
        '/api/system/cli-check?type=claude&source=', '/api/system/cli-check?type=claude&source=cli_recheck&source=cli_recheck', '/api/system/cli-check/x?type=claude', '/api/system/cli-check/?type=claude',
        '/api/system/cli-checks?type=claude', '/api/system/cli-check?type=claude#frag', '/api/system/status', '/api/system/api-key',
        '/api/system/cli-check?type=claude%00', '/api/system/cli-check?type=claude%0a']) {
        assert.strictEqual(m.serverUrl(bad, 'ws1'), NEVER, 'refused: ' + bad);
    }
    // A local Coral (or the hub's own machine) is not behind the proxy and is not filtered here.
    assert.strictEqual(m.serverUrl('/api/system/cli-check?binary=x&type=claude', 'local'), '/api/system/cli-check?binary=x&type=claude');
    assert.strictEqual(m.cloudCliCheckAllowed('/api/system/cli-check?type=codex&source=cli_recheck'), true);
    assert.strictEqual(m.cloudCliCheckAllowed('/api/system/cli-check?type=codex&binary=x'), false);
    console.log('cli_check_cloud.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
