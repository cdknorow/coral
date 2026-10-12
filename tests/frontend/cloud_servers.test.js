// Pure-node test for the Servers dialog's access-mode logic:  node cloud_servers.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    globalThis.location = { protocol: 'https:', host: 'hub.example', origin: 'https://hub.example' };
    globalThis.document = { addEventListener() {}, getElementById() { return null; } };
    const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
    const m = await import(pathToFileURL(path.join(dir, 'cloud_servers.js')).href);

    assert.strictEqual(m.lowerMode('launch', 'interact'), 'interact');
    assert.strictEqual(m.lowerMode('view', 'launch'), 'view');
    assert.strictEqual(m.lowerMode('interact', undefined), 'interact', 'unknown local mode does not limit');
    assert.strictEqual(m.lowerMode('interact', 'bogus'), 'interact');
    assert.strictEqual(m.lowerMode('bogus', 'launch'), 'launch');

    assert.strictEqual(m.hubMode({ access_mode: 'interact' }), 'interact');
    assert.strictEqual(m.hubMode({ allow_launch: true }), 'launch', 'legacy hub');
    assert.strictEqual(m.hubMode({ allow_launch: false }), 'view');
    assert.strictEqual(m.hubMode({}), 'view', 'the default is view');
    assert.strictEqual(m.hubMode({ access_mode: 'root' }), 'view', 'unknown value never widens');

    assert.strictEqual(m.effectiveMode({ access_mode: 'launch', local_mode: 'view' }), 'view');
    assert.strictEqual(m.effectiveMode({ access_mode: 'view', local_mode: 'launch' }), 'view');

    assert.match(m.launchNote({ access_mode: 'launch', local_mode: 'view' }), /Limited by this machine's local setting/);
    assert.strictEqual(m.launchNote({ access_mode: 'interact', local_mode: 'launch' }), '');
    assert.strictEqual(m.launchNote({ access_mode: 'view' }), '');

    for (const k of ['view', 'interact', 'launch']) assert.ok(m.MODE_INFO[k].text.length > 20);
    assert.match(m.MODE_INFO.interact.text, /run commands/);
    assert.match(m.MODE_INFO.interact.text, /delete boards/);
    assert.match(m.MODE_INFO.interact.text, /board settings, including turning on cross-team messaging/);
    const cl = await import(pathToFileURL(path.join(dir, 'cloud.js')).href);
    for (const ok of ['/api/license/status', '/api/system/privacy', '/api/agent/ui', '/api/agent/ui/x']) assert.ok(cl.NO_HUB_ENDPOINT.test(ok), ok);
    for (const no of ['/api/themes/variables', '/api/themes', '/api/themes/Dark', '/api/settings', '/api/tags', '/api/views', '/api/servers', '/api/remote/x/api/license/status', '/api/sessions/live']) assert.ok(!cl.NO_HUB_ENDPOINT.test(no), no);
    assert.match(m.refusalMessage(403, { error: 'local_mode', mode: 'view' }), /Limited by this machine's local setting \(View\)/);
    assert.match(m.refusalMessage(403, { error: 'local_mode', mode: 'x<b>' }), /lower mode/, 'unknown mode text is never echoed');
    assert.match(m.refusalMessage(403, { error: 'access_mode', required: 'interact' }), /Servers page/);
    assert.strictEqual(m.refusalMessage(403, { error: 'csrf' }), '');
    assert.strictEqual(m.refusalMessage(404, { error: 'local_mode' }), '');
    assert.strictEqual(m.refusalMessage(403, null), '');
    // The wrapper answers machine-only URLs itself and sends nothing; everything else passes through.
    {
        const sent = [];
        globalThis.window = globalThis;
        globalThis.location = { protocol: 'https:', host: 'hub.example', origin: 'https://hub.example', href: 'https://hub.example/' };
        globalThis.fetch = async u => { sent.push(String(u)); return new Response('{}', { status: 200 }); };
        cl.installCloudFetch();
        for (const [u, init] of [['/api/cloud-unavailable', undefined], ['/api/cloud-unavailable?x=1', { method: 'POST', body: '{}' }], ['/api/license/status', undefined]]) {
            const r = await fetch(u, init);
            assert.strictEqual(r.status, 404, u);
        }
        assert.deepStrictEqual(sent, [], 'nothing was sent for machine-only URLs');
        assert.strictEqual((await fetch('/api/servers')).status, 200);
        assert.deepStrictEqual(sent, ['/api/servers']);
    }
    // serverMayInteract: the lower of hub and machine mode decides; unknown servers and failed lookups never block.
    {
        let servers = [{ id: 'v', access_mode: 'view', local_mode: 'launch' }, { id: 'i', access_mode: 'interact', local_mode: 'launch' },
                       { id: 'm', access_mode: 'launch', local_mode: 'view' }, { id: 'l', access_mode: 'launch', local_mode: 'launch' }];
        let calls = 0;
        globalThis.fetch = async () => { calls++; return new Response(JSON.stringify({ servers }), { status: 200 }); };
        assert.strictEqual(await m.serverMayInteract('v'), false, 'hub View');
        assert.strictEqual(await m.serverMayInteract('m'), false, 'machine limited to View');
        assert.strictEqual(await m.serverMayInteract('i'), true);
        assert.strictEqual(await m.serverMayInteract('l'), true);
        assert.strictEqual(await m.serverMayInteract('unknown'), true, 'unknown server never blocks');
        assert.strictEqual(calls, 1, 'cached');
    }
    console.log('cloud_servers.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
