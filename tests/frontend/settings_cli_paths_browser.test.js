// Settings > Agents > CLI paths (#7041, UI/UX's #7036 finding 1): the four path fields sit in a disclosure, and its line looked like a caption (a flex summary draws no marker), so a person who
// needed to fix "claude: command not found" could not tell the fields were there. The line now draws a chevron like the other disclosures in Settings, and the disclosure opens itself when
// Coral cannot find the default agent's CLI (and stays closed when it can). Drives Chrome over CDP through agent_view_harness.js; the CLI check's answer is stubbed.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the dialog fills after it opens
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled(mobile ? { enabled: true, maxTouchPoints: 5 } : { enabled: false });
            for (const found of [false, true]) {
                await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
                await ev(`window.__coralReady.then(() => true)`);
                await ev(`(() => { const real = window.fetch; window.fetch = (u, o) => /\\/api\\/system\\/cli-check/.test(String(u)) ? Promise.resolve(new Response(JSON.stringify({ found: ${found}, status: ${found ? "'available'" : "'missing'"}, agent_type: new URL(String(u), location.href).searchParams.get('type') }), { status: 200, headers: { 'Content-Type': 'application/json' } })) : real(u, o); return true; })()`);
                await ev(`window.showSettingsModal('agents'); true`);
                await waitFor(`!!document.querySelector('#settings-pane-agents .settings-cli-details > summary') && document.querySelector('#settings-pane-agents .settings-cli-details > summary').getClientRects().length > 0`, 'the CLI paths line', 8000);
                await sleep(500); // sleep-ok: the check answers and the disclosure opens after the dialog fills
                const tag = `${w} px, CLI ${found ? 'found' : 'missing'}`;
                const marker = await ev(`(() => { const s = document.querySelector('#settings-pane-agents .settings-cli-details > summary'); const c = getComputedStyle(s, '::before').content; return { content: c, display: getComputedStyle(s).display, listItem: getComputedStyle(s).display === 'list-item' }; })()`);
                assert.ok(marker.content && marker.content !== 'none' && marker.content !== 'normal' || marker.listItem, `${tag}: the CLI paths line draws a disclosure marker: ` + JSON.stringify(marker));
                const open = await ev(`document.querySelector('#settings-pane-agents .settings-cli-details').open`);
                assert.equal(open, !found, `${tag}: the disclosure is ${found ? 'closed' : 'open'} (open=${open})`);
                if (!found) assert.ok(await ev(`document.getElementById('settings-cli-path-claude').getClientRects().length > 0`), `${tag}: the Claude path field is on screen`);
            }
        }
        console.log('PASS settings cli paths');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
