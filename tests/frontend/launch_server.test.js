// Pure-node unit test for the multi-server launch helpers (no browser needed):
//   node launch_server.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const dir = path.resolve(__dirname, '../../coral-go/internal/server/frontend/static');
    globalThis.location = { protocol: 'https:', host: 'hub.example' };
    const m = await import(pathToFileURL(path.join(dir, 'launch_server.js')).href);

    const servers = [
        { id: 'a', label: 'Box A', status: 'online' },
        { id: 'b', label: 'Box B', status: 'unreachable' },
        { id: 'c', label: 'Box C', status: 'unauthorized' },
        { id: 'd', label: 'Box D', status: 'key_unreadable' },
        { id: 'e', label: 'Box E', status: 'unknown' },
    ];

    // Picker visibility: hub only, and at least one non-unreachable remote.
    assert.strictEqual(m.pickerVisible(false, servers), false);
    assert.strictEqual(m.pickerVisible(true, []), false);
    assert.strictEqual(m.pickerVisible(true, [{ id: 'b', status: 'unreachable' }]), false);
    assert.strictEqual(m.pickerVisible(true, [{ id: 'local', status: 'online' }]), false);
    assert.strictEqual(m.pickerVisible(true, servers), true);
    assert.strictEqual(m.pickerVisible(true, [{ id: 'c', status: 'unauthorized' }]), true);

    // Options: local first, unusable ones disabled with a reason.
    const opts = m.pickerOptions(servers);
    assert.strictEqual(opts[0].id, 'local');
    assert.strictEqual(opts[0].disabled, false);
    const by = Object.fromEntries(opts.map(o => [o.id, o]));
    assert.strictEqual(by.a.disabled, false);
    assert.ok(by.b.disabled && /offline/.test(by.b.label));
    assert.ok(by.c.disabled && /rejected/.test(by.c.label));
    assert.ok(by.d.disabled);
    assert.strictEqual(by.e.disabled, false);

    // Block reasons / coercion: remote without hub mode or unusable never launches.
    assert.strictEqual(m.launchBlockReason('local', false, []), '');
    assert.ok(m.launchBlockReason('a', false, servers));
    assert.strictEqual(m.launchBlockReason('a', true, servers), '');
    assert.ok(m.launchBlockReason('b', true, servers));
    assert.ok(m.launchBlockReason('zzz', true, servers));
    assert.strictEqual(m.coerceSelection('b', true, servers), 'local');
    assert.strictEqual(m.coerceSelection('a', true, servers), 'a');
    assert.strictEqual(m.coerceSelection(undefined, true, servers), 'local');
    assert.strictEqual(m.coerceSelection({ type: 'click' }, true, servers), 'local');

    // Recent dirs never mix between servers; local keeps the pre-hub key.
    assert.strictEqual(m.recentDirsKey('local'), 'coral-recent-dirs');
    assert.strictEqual(m.recentDirsKey(), 'coral-recent-dirs');
    assert.strictEqual(m.recentDirsKey('a'), 'coral-recent-dirs:a');
    assert.strictEqual(m.bareBoardName('@a/team x'), 'team x');
    assert.strictEqual(m.bareBoardName('plain'), 'plain');

    // postLaunch routes to the chosen server and never falls back to local.
    const calls = [];
    const mk = (status, body) => async (server, url, opts) => {
        calls.push({ server, url, body: JSON.parse(opts.body) });
        return { ok: status >= 200 && status < 300, status, text: async () => body };
    };
    let r = await m.postLaunch('a', '/api/sessions/launch', { working_dir: '/r' }, { hub: true, servers, fetchFn: mk(200, '{"session_name":"x","session_id":"s1"}') });
    assert.ok(r.ok && r.data.session_name === 'x');
    assert.deepStrictEqual(calls[0], { server: 'a', url: '/api/sessions/launch', body: { working_dir: '/r' } });

    calls.length = 0;
    r = await m.postLaunch('b', '/api/sessions/launch', {}, { hub: true, servers, fetchFn: mk(200, '{}') });
    assert.ok(!r.ok && !r.sent && calls.length === 0, 'unreachable remote must not be called, nor local');
    r = await m.postLaunch('a', '/api/sessions/launch', {}, { hub: false, servers, fetchFn: mk(200, '{}') });
    assert.ok(!r.ok && calls.length === 0, 'hub off: remote launch refused, nothing sent');

    // Proxy rejection (502 JSON), non-JSON gateway failure, thrown network error: error shown, not ok.
    r = await m.postLaunch('a', '/p', {}, { hub: true, servers, fetchFn: mk(502, '{"error":"remote rejected API key","server":"a"}') });
    assert.ok(!r.ok && /Box A: remote rejected API key/.test(r.error));
    r = await m.postLaunch('a', '/p', {}, { hub: true, servers, fetchFn: mk(504, '<html>') });
    assert.ok(!r.ok && /HTTP 504/.test(r.error));
    r = await m.postLaunch('a', '/p', {}, { hub: true, servers, fetchFn: async () => { throw new Error('boom'); } });
    assert.ok(!r.ok && /boom/.test(r.error) && /Box A/.test(r.error));
    // 200 with an error body (the launch handler reports CLI-not-found this way).
    r = await m.postLaunch('a', '/p', {}, { hub: true, servers, fetchFn: mk(200, '{"error":"claude CLI not found"}') });
    assert.ok(!r.ok && /CLI not found/.test(r.error));
    // Local: 403 is the demo limit, error text unprefixed.
    r = await m.postLaunch('local', '/p', {}, { hub: false, servers: [], fetchFn: mk(403, '{"error":"limit"}') });
    assert.ok(!r.ok && r.demoLimit && r.error === 'limit');
    r = await m.postLaunch('local', '/p', {}, { hub: false, servers: [], fetchFn: mk(200, '{"error":"nope"}') });
    assert.ok(!r.ok && !r.demoLimit && r.error === 'nope');

    // Recognising the launched agent in the merged list (same name on another server does not count).
    const live = [{ name: 'x', server: 'local', session_id: 's0' }, { name: 'y', server: 'a', session_id: 's2', board_project: 'T' }];
    assert.strictEqual(m.launchedVisible(live, 'a', { sessionName: 'x' }), false);
    assert.strictEqual(m.launchedVisible(live, 'a', { sessionName: 'y' }), true);
    assert.strictEqual(m.launchedVisible(live, 'a', { sessionId: 's2' }), true);
    assert.strictEqual(m.launchedVisible(live, 'a', { boardName: 'T' }), true);
    assert.strictEqual(m.launchedVisible(live, 'local', { boardName: 'T' }), false);

    // waitForLaunched polls until visible, and gives up on schedule.
    let list = [], n = 0;
    const seen = await m.waitForLaunched('a', { sessionName: 'new' }, async () => { n++; if (n === 2) list = [{ name: 'new', server: 'a' }]; },
        { delays: [1, 1, 1, 1], getSessions: () => list, sleep: async () => {} });
    assert.ok(seen && n === 2);
    n = 0; list = [];
    const none = await m.waitForLaunched('a', { sessionName: 'new' }, async () => { n++; },
        { delays: [1, 1, 1], getSessions: () => list, sleep: async () => {} });
    assert.ok(!none && n === 3);

    console.log('launch_server.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
