// Pure-node unit test for the hub sidebar/server-status helpers (no browser needed):
//   node server_status.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/internal/server/frontend/static');
    globalThis.location = { protocol: 'http:', host: 'hub.example' };
    const m = await import(pathToFileURL(path.join(dir, 'server_status.js')).href);
    const { state } = await import(pathToFileURL(path.join(dir, 'state.js')).href);

    // Hub off: nothing is rendered, whatever state.servers holds.
    state.hub = false;
    state.servers = [{ id: 'ws1', label: 'WS', status: 'unreachable' }];
    assert.strictEqual(m.showServerLabels(), false);
    assert.strictEqual(m.serverBadgeHtml('ws1'), '');
    assert.strictEqual(m.serverStripHtml(), '');
    const ents = [['b', 1], ['@ws1/a', 2]];
    assert.strictEqual(m.localFirst(ents), ents);

    // Hub on, no remotes: still identical to a standalone server.
    state.hub = true;
    state.servers = [];
    assert.strictEqual(m.showServerLabels(), false);
    assert.strictEqual(m.serverBadgeHtml('local'), '');
    assert.strictEqual(m.serverStripHtml(), '');
    state.servers = [{ id: 'local', label: 'Local', status: 'online' }];
    assert.strictEqual(m.showServerLabels(), false);

    // Hub on with remotes.
    state.servers = [{ id: 'ws1', label: 'Work <b>', status: 'online' }, { id: 'ws2', label: 'Cloud', status: 'unreachable' }, { id: 'ws3', label: 'Old', status: 'unauthorized' }];
    assert.strictEqual(m.showServerLabels(), true);
    assert.strictEqual(m.serverStatus('ws2'), 'unreachable');
    assert.strictEqual(m.serverStatus('local'), 'online');
    assert.strictEqual(m.serverStatus('nope'), 'unknown');
    const badge = m.serverBadgeHtml('ws1');
    assert.ok(badge.includes('server-online') && badge.includes('Work &lt;b&gt;') && !badge.includes('<b>'));
    assert.ok(m.serverBadgeHtml('local').includes('Local'));
    assert.notStrictEqual(m.serverBadgeHtml('ws1'), m.serverBadgeHtml('ws2'));
    const strip = m.serverStripHtml();
    assert.ok(strip.indexOf('Local') < strip.indexOf('Cloud'));
    assert.ok(strip.includes('Unreachable') && strip.includes('Key rejected'));
    assert.deepStrictEqual(m.localFirst([['@ws1/a', 1], ['b', 2], ['@ws2/c', 3], ['d', 4]]).map(e => e[0]), ['b', 'd', '@ws1/a', '@ws2/c']);

    // Hover text: status, address, agent count, last contact, error when down.
    const down = m.serverHoverText('ws2');
    assert.ok(/^Cloud: Unreachable/.test(down) && /agents?/.test(down) && /Last seen:/.test(down));
    assert.ok(/This server \(the hub\)/.test(m.serverHoverText('local')));

    // Stale sessions.
    assert.strictEqual(m.isStaleSession({ stale: true }), true);
    assert.strictEqual(m.isStaleSession({}), false);
    assert.ok(/Cloud is unreachable/.test(m.staleReason({ server: 'ws2' })));

    // Error messages.
    assert.ok(/cannot be decrypted/.test(m.describeServerError(409, { error: 'x', status: 'key_unreadable' })));
    assert.ok(/already exists/.test(m.describeServerError(409, { error: 'server id already exists' })));
    assert.ok(/rejected the API key/.test(m.describeServerError(502, { status: 'unauthorized', error: 'remote rejected API key' })));
    assert.ok(/Could not reach/.test(m.describeServerError(502, { status: 'unreachable', error: 'remote unreachable' })));
    assert.ok(/boom/.test(m.describeServerError(400, { error: 'boom' })));

    // Misc.
    assert.strictEqual(m.formatLastSeen(null), 'never');
    assert.strictEqual(m.formatLastSeen('2026-01-01T00:00:00Z', Date.parse('2026-01-01T00:05:00Z')), '5m ago');
    assert.ok(m.isValidServerId('work-1') && !m.isValidServerId('local') && !m.isValidServerId('A') && !m.isValidServerId(''));

    console.log('server_status.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
