// #3751: a dashboard tab left open across a server update. The server names its static build in X-Coral-Build on
// every /api/ answer; the page knows the build it loaded from. When they differ:
//  - a "Coral was updated" banner with a Reload button appears;
//  - a tab with nothing unsent reloads by itself (once per build, so a half-finished update cannot loop);
//  - text typed and not sent, or work a module reports as unsaved, is never reloaded over: the banner stays until
//    it is sent or saved, then the tab reloads;
//  - the Reload button reloads at once.
// "The server was updated" is made by adding a different X-Coral-Build to the answers through CDP (Fetch domain).
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const NEW_BUILD = 'ffffffffffff';

(async () => {
    const { client, Page, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const { Fetch, Network, Input } = client;
    let spoof = null; // the build the server "now" reports, or null for the real header
    let remoteSpoof = null; // the X-Coral-Build header a /api/remote/ answer carries (what a self-hosted hub's proxy used to relay), or null
    const seenBuilds = [];
    safePaused(client, async e => {
        if (!e.responseStatusCode && !e.responseErrorReason) return Fetch.continueRequest({ requestId: e.requestId });
        let headers = e.responseHeaders || [];
        const real = headers.find(h => h.name.toLowerCase() === 'x-coral-build');
        if (real) seenBuilds.push(real.value);
        const value = /\/api\/remote\//.test(e.request.url) ? remoteSpoof : spoof;
        if (value) headers = headers.filter(h => h.name.toLowerCase() !== 'x-coral-build').concat([{ name: 'X-Coral-Build', value }]);
        return Fetch.continueResponse({ requestId: e.requestId, responseCode: e.responseStatusCode, responsePhrase: e.responseStatusText || undefined, responseHeaders: headers }).catch(() => Fetch.continueRequest({ requestId: e.requestId }).catch(() => {}));
    });
    const load = async (preselect) => {
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`window.__marker = 'old-page'; window.__coralUpdateReloadDelay = 400; localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('ub-a', 'A', 3, { sleeping: false }); true`);
        if (preselect) await ev(`window.selectLiveSession('ub-a', 'claude', 'ub-a-id', 'local'); true`);
    };
    const banner = () => ev(`(() => { const b = document.getElementById('update-banner'); return b && document.getElementById('update-banner-text').textContent !== '' ? { text: b.textContent.replace(/\\s+/g, ' ').trim(), role: document.getElementById('update-banner-text').getAttribute('role'), button: [...b.querySelectorAll('button')].map(x => x.textContent.trim()) } : null; })()`);
    // Typed like a person: focus the box and insert text, which fires a trusted input event.
    const type = async (id, text) => { await ev(`document.getElementById(${JSON.stringify(id)}).focus(); true`); await Input.insertText({ text }); };
    const clear = async id => { await ev(`document.getElementById(${JSON.stringify(id)}).focus(); true`); await Input.dispatchKeyEvent({ type: 'rawKeyDown', key: 'a', commands: ['selectAll'] }); await Input.dispatchKeyEvent({ type: 'rawKeyDown', key: 'Backspace', commands: ['deleteBackward'] }); };
    const poke = () => ev(`fetch('/api/system/status').then(r => r.status)`);
    const reloaded = () => ev(`window.__marker === undefined`);
    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Fetch.enable({ patterns: [{ urlPattern: '*/api/*', requestStage: 'Response' }] });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: 'window.__coralUpdateReloadDelay = 400;' }); // every document, a reloaded one too

        console.log('1. The server names the build the page loaded from, and nothing shows');
        await load(false);
        await poke();
        const own = await ev(`(/^\\/static\\/bb?\\/([^/]+)\\/$/.exec(window.__coralStatic) || [])[1] || ''`);
        assert.ok(own, `the page loaded from a versioned prefix (${await ev('window.__coralStatic')})`);
        assert.ok(seenBuilds.length > 0 && seenBuilds.every(b => b === own), `every /api/ answer carries the page's own build (${own}; saw ${[...new Set(seenBuilds)]})`);
        await sleep(700); // sleep-ok: no banner appears in the time a reload would have fired (nothing to wait for)
        assert.equal(await banner(), null, 'no banner while the builds match');
        assert.equal(await ev(`window.__marker`), 'old-page', 'and no reload');
        console.log(`   ✓ ${seenBuilds.length} answers, all build ${own}; no banner, no reload`);

        console.log('1b. A self-hosted hub: an answer relayed from a remote server never drives the banner (#3990)');
        // The hub's own header plus the remote's reached the browser as one value, "hub, remote": never equal to the page's build.
        for (const v of [`${own}, ${own}`, `${own}, ffffffffffff`, 'ffffffffffff']) {
            remoteSpoof = v;
            assert.equal(await ev(`fetch('/api/remote/m1/api/system/status').then(r => r.headers.get('X-Coral-Build'))`), v, 'the relayed answer carries that header');
        }
        remoteSpoof = null;
        spoof = `${own}, ${own}`; await poke(); spoof = null;   // a header with two values is no build, even on the hub's own routes
        await sleep(700); // sleep-ok: no banner appears in the time a reload would have fired (nothing to wait for)
        assert.equal(await banner(), null, 'no banner from relayed answers or a two-valued header');
        assert.equal(await ev(`window.__marker`), 'old-page', 'and no reload');
        console.log('   ✓ relayed and two-valued headers are ignored');

        console.log('1c. A year-cached artifact answer that carries an old build never drives the banner (#5312)');
        spoof = 'ffffffffffff';
        const hex = 'ab'.repeat(32);
        assert.equal(await ev(`fetch('/api/artifacts/${hex}?v=2').then(r => r.headers.get('X-Coral-Build'))`), 'ffffffffffff', 'the artifact answer carries the old build');
        assert.equal(await ev(`fetch('/api/artifacts/${hex}').then(r => r.headers.get('X-Coral-Build'))`), 'ffffffffffff');
        spoof = null;
        await sleep(700); // sleep-ok: no banner appears in the time a reload would have fired (nothing to wait for)
        assert.equal(await banner(), null, 'no banner from an artifact answer');
        assert.equal(await ev(`window.__marker`), 'old-page', 'and no reload');
        assert.deepEqual(await ev(`import((window.__coralStatic || '/static/') + 'update_banner.js').then(m => [m.isArtifactContent('/api/artifacts/${hex}?v=2'), m.isArtifactContent('/api/artifacts/${hex}/comments'), m.isArtifactContent('/api/artifacts'), m.isArtifactContent('/api/system/status')])`), [true, false, false, false], 'only the bytes of an artifact are exempt');
        console.log('   ✓ an artifact answer with an old build is ignored');

        console.log('2. After an update the banner appears and an idle tab reloads by itself, once');
        await ev(`sessionStorage.removeItem('coral-update-reloaded'); true`);
        spoof = NEW_BUILD;
        await poke();
        await waitFor(`!!document.getElementById('update-banner') && document.getElementById('update-banner-text').textContent !== ''`, 'the update banner', 3000);
        const b1 = await banner();
        assert.equal(b1.role, 'status');
        assert.match(b1.text, /Coral was updated/);
        assert.deepEqual(b1.button, ['Reload']);
        await waitFor(`window.__marker === undefined`, 'the idle tab to reload by itself', 6000);
        await waitFor(`document.readyState === 'complete' && !!document.getElementById('update-banner')`, 'the reloaded page', 8000);
        assert.equal(await ev(`sessionStorage.getItem('coral-update-reloaded')`), NEW_BUILD, 'the reload is recorded for that build');
        await ev(`window.__marker = 'new-page'; window.__coralUpdateReloadDelay = 400; true`);
        await poke(); // the server still reports a different build: the banner returns, the tab does not reload again
        await waitFor(`!!document.getElementById('update-banner') && document.getElementById('update-banner-text').textContent !== ''`, 'the banner after the reload', 3000);
        await sleep(1200); // sleep-ok: shows that a second automatic reload does not happen (nothing to wait for)
        assert.equal(await ev(`window.__marker`), 'new-page', 'no second automatic reload for the same build');
        assert.match((await banner()).text, /Reload to use the new version/, 'it asks for a manual reload');
        console.log('   ✓ banner → automatic reload → banner again, no loop');

        console.log('3. Unsent text is never reloaded over; the tab reloads once it is gone');
        spoof = null;
        await load(true);
        await ev(`sessionStorage.removeItem('coral-update-reloaded'); true`);
        await waitFor(`!!document.getElementById('command-input') && !document.getElementById('command-input').disabled`, 'the composer', 8000);
        await type('command-input', 'half written reply');
        spoof = NEW_BUILD;
        await poke();
        await waitFor(`!!document.getElementById('update-banner') && document.getElementById('update-banner-text').textContent !== ''`, 'the banner with a draft', 3000);
        await sleep(1500); // sleep-ok: several reload ticks pass with the draft there; nothing may happen (no condition to wait for)
        assert.equal(await ev(`window.__marker`), 'old-page', 'the tab did not reload over the draft');
        assert.equal(await ev(`document.getElementById('command-input').value`), 'half written reply', 'the draft is intact');
        assert.match((await banner()).text, /Your unsent text is kept/, 'the banner says why it is waiting');
        await clear('command-input');
        assert.equal(await ev(`document.getElementById('command-input').value`), '', 'the box is empty');
        await waitFor(`window.__marker === undefined`, 'the reload once the draft is gone', 6000);
        console.log('   ✓ a draft holds the reload for as long as it exists, and the reload follows when it is gone');

        console.log('4. Unsaved work a module reports holds the reload too');
        spoof = null;
        await load(false);
        await ev(`sessionStorage.removeItem('coral-update-reloaded'); window.__unsaved = true; import((window.__coralStatic || '/static/') + 'update_banner.js').then(m => { m.registerUnsavedWork(() => window.__unsaved); return true; })`);
        spoof = NEW_BUILD;
        await poke();
        await waitFor(`!!document.getElementById('update-banner') && document.getElementById('update-banner-text').textContent !== ''`, 'the banner with unsaved work', 3000);
        await sleep(1500); // sleep-ok: as above
        assert.equal(await ev(`window.__marker`), 'old-page', 'no reload over unsaved work');
        await ev(`window.__unsaved = false; true`);
        await waitFor(`window.__marker === undefined`, 'the reload once the work is saved', 6000);
        console.log('   ✓ registered unsaved work holds the reload, then releases it');

        console.log('5. The Reload button reloads at once, even with a draft');
        spoof = null;
        await load(true);
        await ev(`sessionStorage.removeItem('coral-update-reloaded'); window.__coralUpdateReloadDelay = 60000; true`);
        await waitFor(`!!document.getElementById('command-input') && !document.getElementById('command-input').disabled`, 'the composer', 8000);
        await type('command-input', 'still typing');
        spoof = NEW_BUILD;
        await poke();
        await waitFor(`!!document.getElementById('update-banner') && document.getElementById('update-banner-text').textContent !== ''`, 'the banner', 3000);
        await ev(`document.querySelector('#update-banner button').click(); true`);
        await waitFor(`window.__marker === undefined`, 'the page to reload from the button', 6000);
        console.log('   ✓ the button reloads without waiting');

        console.log('\nAll update-banner checks passed');
    } finally {
        spoof = null;
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
