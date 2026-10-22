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

    // The owner's choice is View, Interact or Launch (the hub reads a stored launch as "up to what the computer allows"; an older hub's follow is the same).
    assert.strictEqual(m.accessChoice({ access_mode: 'launch' }), 'launch');
    assert.strictEqual(m.accessChoice({ access_mode: 'follow' }), 'launch', "an older hub's follow means launch");
    assert.strictEqual(m.accessChoice({ access_mode: 'interact' }), 'interact');
    assert.strictEqual(m.accessChoice({}), 'view', 'a missing value is view');
    assert.strictEqual(m.accessChoice({ access_mode: 'root' }), 'view', 'unknown value never widens');
    assert.strictEqual(m.hubMode({ access_mode: 'interact' }), 'interact');

    assert.strictEqual(m.effectiveMode({ access_mode: 'launch', local_mode: 'view' }), 'view');
    assert.strictEqual(m.effectiveMode({ access_mode: 'launch', local_mode: 'launch' }), 'launch', 'Launch is up to what the computer allows');
    assert.strictEqual(m.effectiveMode({ access_mode: 'view', local_mode: 'launch' }), 'view');
    assert.strictEqual(m.effectiveMode({ access_mode: 'launch' }), 'view', 'a computer that has not announced counts as View');

    // What the row says (fixed words, never machine text): what the computer allows, and what applies only when lower than the choice.
    assert.strictEqual(m.launchNote({ access_mode: 'launch', local_mode: 'interact' }), 'This computer allows up to Interact; change it in Coral on that computer (Settings > Network and Hub).');
    assert.strictEqual(m.launchNote({ access_mode: 'launch', local_mode: 'launch' }), '');
    assert.strictEqual(m.launchNote({ access_mode: 'view' }), '', 'nothing announced: no machine note (the row says View until it connects)');
    assert.strictEqual(m.effectiveNote({ access_mode: 'launch', local_mode: 'interact' }), 'Effective: Interact, limited by this computer');
    assert.strictEqual(m.effectiveNote({ access_mode: 'interact', local_mode: 'launch' }), '', 'the effective value shows only when lower than the choice');
    assert.strictEqual(m.effectiveNote({ access_mode: 'view', local_mode: 'view' }), '');
    assert.strictEqual(m.limitNote, undefined, 'no "you limited" note any more');
    assert.strictEqual(m.confirmLaunch, undefined, 'no Launch confirmation: the connect-time question is the consent');

    for (const k of ['view', 'interact', 'launch']) assert.ok(m.MODE_INFO[k].text.length > 20);
    assert.match(m.MODE_INFO.interact.text, /run commands/);
    // Stopping, sleeping and waking agents (one or a whole team) and deleting boards need Launch through the hub (#3433, #3443, #4274;
    // #4526 fixed MODE_INFO, which still gave single-agent kill to Interact). TestDashboardModeTextMatchesTheRouteTable pins it to the route table.
    assert.doesNotMatch(m.MODE_INFO.interact.text, /\bkill\b|\bstop\b|delete boards|delete a board/);
    assert.match(m.MODE_INFO.launch.text, /start, stop, sleep, wake and restart agents \(one agent or a whole team\)/);
    assert.match(m.MODE_INFO.launch.text, /delete board messages and boards/);
    assert.match(m.MODE_INFO.interact.text, /working mode/);
    // Cross-team messaging is a board setting (PUT .../settings), never reachable through the hub at any mode (QA on #4526).
    for (const k of ['view', 'interact', 'launch']) assert.doesNotMatch(m.MODE_INFO[k].text, /cross-team/i);
    // cloud_connect.js keeps its own (module-local) Interact description for the machine's Cloud panel: keep it in step.
    const connect = require('fs').readFileSync(path.join(dir, 'cloud_connect.js'), 'utf8');
    const connectInteract = (connect.match(/^\s*interact: '(.*)',$/m) || [])[1] || '';
    assert.match(connectInteract, /run commands/);
    assert.match(connectInteract, /working mode/);
    assert.doesNotMatch(connectInteract, /cross-team/i);
    assert.doesNotMatch(connectInteract, /\bkill\b|\bstop\b/);
    const cl = await import(pathToFileURL(path.join(dir, 'cloud.js')).href);
    for (const ok of ['/api/system/privacy', '/api/agent/ui', '/api/agent/ui/x']) assert.ok(cl.NO_HUB_ENDPOINT.test(ok), ok);
    for (const no of ['/api/themes/variables', '/api/themes', '/api/themes/Dark', '/api/settings', '/api/tags', '/api/views', '/api/servers', '/api/remote/x/api/license/status', '/api/sessions/live']) assert.ok(!cl.NO_HUB_ENDPOINT.test(no), no);
    // C2 (#3068): refusals start with 'Blocked:', name what each side allows and who can fix it.
    assert.match(m.refusalMessage(403, { error: 'local_mode', mode: 'view' }, 'Work Mac'), /^Blocked: Work Mac .*This machine allows: View.*Change it in Coral on that machine/);
    assert.doesNotMatch(m.refusalMessage(403, { error: 'local_mode', mode: 'x<b>' }), /x<b>/, 'unknown mode text is never echoed');
    assert.match(m.refusalMessage(403, { error: 'access_mode', required: 'interact', hub_mode: 'view', local_mode: 'launch' }, 'Work Mac'),
        /^Blocked: Work Mac needs Interact \(Hub: View · This machine allows: Launch · Effective: View\)\. Choose a higher Hub access in Servers\.$/);
    assert.match(m.refusalMessage(403, { error: 'access_mode', required: 'interact', hub_mode: 'launch', local_mode: 'view' }), /^Blocked: this server .*Change it in Coral on that machine/);
    assert.doesNotMatch(m.refusalMessage(403, { error: 'access_mode', required: '<i>', hub_mode: '<b>' }), /<[ib]>/);
    assert.match(m.refusalMessage(403, { error: 'access_mode', required: 'interact' }), /^Blocked: .*needs Interact.*higher Hub access/);
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
        for (const [u, init] of [['/api/cloud-unavailable', undefined], ['/api/cloud-unavailable?x=1', { method: 'POST', body: '{}' }]]) {
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
