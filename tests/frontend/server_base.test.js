// Pure-node unit test for the multi-server routing helpers (no browser needed):
//   node server_base.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
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

    // A request for a registered server stays inside its proxy (#4551, Security): a value from that machine (an id, a name, a path) cannot walk it out to the hub's own routes.
    {
        const sent = [];
        globalThis.fetch = (u) => { sent.push(String(u)); return Promise.resolve({ ok: true }); };
        const hostile = ['/api/saved/../../../../servers/x', '/api/saved/%2e%2e/%2e%2e/%2e%2e/%2e%2e/servers/x', '/api/saved/%2E%2E/%2E%2E/%2E%2E/servers/x', '/api/saved/..%2f..%2f..%2fservers/x',
            '/api/saved/%252e%252e%252f%252e%252e%252f%252e%252e%252fservers', '/api/saved/..\\..\\..\\servers', '/api/saved/%2e%2e%5c%2e%2e%5c%2e%2e%5cservers', 'api/no-leading-slash', '/api/saved/%zz'];
        for (const srv of ['ws1', 'a b']) {
            for (const h of hostile) {
                assert.throws(() => m.serverUrl(h, srv), /refused a request that would leave the proxy|refused/, `serverUrl refuses ${h} for ${srv}`);
                await assert.rejects(m.serverFetch(srv, h), /refused/, `serverFetch refuses ${h} for ${srv}`);
            }
        }
        assert.deepStrictEqual(sent, [], 'nothing was sent');
        const fine = ['/api/x', '/api/saved/12', '/api/board/a%2Fb/messages?limit=1&q=../..', '/api/sessions/live/n..x/chat?after=0', '/api/artifacts/abc?v=2', '/api/board/.hidden/messages', '/api/files/a%20b/c.txt'];
        for (const f of fine) {
            assert.strictEqual(m.serverUrl(f, 'ws1'), '/api/remote/ws1' + f, `serverUrl passes ${f}`);
            await m.serverFetch('ws1', f);
        }
        assert.deepStrictEqual(sent, fine.map(f => '/api/remote/ws1' + f), 'ordinary URLs are sent as built');
        // This server is not behind the proxy: its URLs are not checked here.
        assert.strictEqual(m.serverUrl('/api/saved/../x', 'local'), '/api/saved/../x');
        // The boards' and teams' helpers go through the same check.
        assert.throws(() => m.boardFetch('@ws1/b', '/../../../../../servers/x'), /refused/);
        assert.throws(() => m.teamFetch('@ws1/b', () => '/api/teams/../../../servers'), /refused/);
        delete globalThis.fetch;
    }

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

// A hub server id is a UUID: its folders must split into server + name like any other key.
{
    const assert = require('assert');
    (async () => {
        const path = require('path');
        const { pathToFileURL } = require('url');
        globalThis.location = globalThis.location || { protocol: 'https:', host: 'hub.example', origin: 'https://hub.example' };
        globalThis.document = globalThis.document || { addEventListener() {}, getElementById() { return null; } };
        const m = await import(pathToFileURL(path.resolve(__dirname, '../../coral-go/pkg/frontend/static/server_base.js')).href);
        const id = '5295fa3b-a6c8-42b6-bd9f-0c7c88f3704e';
        assert.deepStrictEqual(m.splitKey('@' + id + '/001'), { server: id, name: '001' });
        assert.strictEqual(m.keyLabel('@' + id + '/my project'), 'my project');
        assert.deepStrictEqual(m.splitKey('@not a uuid/x'), { server: m.LOCAL_SERVER, name: '@not a uuid/x' });
        console.log('server_base uuid keys: ok');
    })().catch(e => { console.error(e); process.exit(1); });
}
