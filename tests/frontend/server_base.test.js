// Pure-node unit test for the multi-server routing helpers (no browser needed):
//   node server_base.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/internal/server/frontend/static');
    globalThis.location = { protocol: 'https:', host: 'hub.example' };
    const m = await import(pathToFileURL(path.join(dir, 'server_base.js')).href);
    const { state, sessionKey } = await import(pathToFileURL(path.join(dir, 'state.js')).href);

    assert.strictEqual(m.serverBase(), '');
    assert.strictEqual(m.serverBase('local'), '');
    assert.strictEqual(m.serverBase('ws1'), '/api/remote/ws1');
    assert.strictEqual(m.serverBase('a b'), '/api/remote/a%20b');
    assert.strictEqual(m.serverWsBase('ws1'), 'wss://hub.example/api/remote/ws1');
    assert.strictEqual(m.serverWsBase(), 'wss://hub.example');
    assert.strictEqual(m.serverUrl('/api/x', 'ws1'), '/api/remote/ws1/api/x');
    assert.strictEqual(m.serverUrl('/api/x'), '/api/x');

    // Identity: local keys stay bare (legacy storage keeps working); remote keys never collide.
    assert.strictEqual(m.identityKey('local', 'a'), 'a');
    assert.strictEqual(m.identityKey(undefined, 'a'), 'a');
    assert.notStrictEqual(m.identityKey('ws1', 'a'), m.identityKey('ws2', 'a'));
    assert.notStrictEqual(m.identityKey('ws1', 'a'), m.identityKey('local', 'a'));
    assert.deepStrictEqual(m.splitKey(m.identityKey('ws1', 'team/x')), { server: 'ws1', name: 'team/x' });
    assert.deepStrictEqual(m.splitKey('plain'), { server: 'local', name: 'plain' });
    assert.strictEqual(m.keyLabel('@ws1/alpha'), 'alpha');
    assert.ok(!/[^A-Za-z0-9_-]/.test(m.identityDomId('ws1', 'a b')));

    // Sessions on different servers with the same name are different teams/folders.
    const a = { name: 'f', board_project: 'T', server: 'ws1' }, b = { name: 'f', board_project: 'T' };
    assert.notStrictEqual(m.sessionTeamKey(a), m.sessionTeamKey(b));
    assert.strictEqual(m.sessionTeamKey(b), 'T');
    assert.ok(m.inTeam(a, '@ws1/T') && !m.inTeam(b, '@ws1/T'));
    assert.notStrictEqual(m.sessionFolderKey(a), m.sessionFolderKey(b));

    // Popout paths.
    assert.strictEqual(m.agentPath('local', 'id1'), '/agent/id1');
    assert.strictEqual(m.agentPath('ws1', 'id1'), '/agent/ws1/id1');
    assert.deepStrictEqual(m.parseAgentPath('/agent/id1'), { server: 'local', id: 'id1' });
    assert.deepStrictEqual(m.parseAgentPath('/agent/ws1/id1/'), { server: 'ws1', id: 'id1' });
    assert.strictEqual(m.parseAgentPath('/other'), null);

    // Server resolution by session id, then unique name.
    state.liveSessions = [{ name: 'n', session_id: 's1', server: 'ws1' }, { name: 'n', session_id: 's2' }];
    assert.strictEqual(m.serverForSession('n', 's1'), 'ws1');
    assert.strictEqual(m.serverForSession('n', 's2'), 'local');

    // Draft key: remote name-only sessions get a server-qualified key; local unchanged.
    assert.strictEqual(sessionKey({ type: 'live', name: 'n' }), 'live:n');
    assert.strictEqual(sessionKey({ type: 'live', name: 'n', server: 'ws1' }), 'live:@ws1/n');
    console.log('server_base: all checks passed');
})().catch(e => { console.error(e); process.exit(1); });
