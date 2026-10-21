// When the Coral Hub connect flow (cloud_connect.js, loaded the first time its screen opens, #5818) cannot be fetched (a page left open across an update,
// whose old build is gone), the button says so in a toast instead of doing nothing (#5829), and pressing it again after the file is back opens the screen.
// Drives Chrome over CDP against the isolated dev server; the file is blocked with Network.setBlockedURLs.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Network } = client;
    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const modalOpen = () => ev(`(() => { const m = document.getElementById('editions-modal'); return !!m && m.style.display !== 'none'; })()`);
        const toasts = () => ev(`[...document.querySelectorAll('.toast')].map(t => ({ text: t.textContent, role: t.getAttribute('role'), error: t.classList.contains('error') }))`);

        console.log('1. the file cannot be fetched: a toast says so, nothing opens');
        await Network.setBlockedURLs({ urls: ['*cloud_connect.js*'] });
        await ev(`import((window.__coralStatic || '/static/') + 'editions.js').then(m => m.showHubSetup('connect')).then(() => true)`);
        await waitFor(`!!document.querySelector('.toast')`, 'the toast', 8000);
        const t = await toasts();
        assert.equal(t.length, 1, JSON.stringify(t));
        assert.match(t[0].text, /^Could not load the connect screen\. Reload the page\./);
        assert.equal(t[0].role, 'alert'); assert.equal(t[0].error, true);
        assert.equal(await modalOpen(), false, 'no half-built screen');

        console.log('2. the file is back: pressing again opens the screen');
        await Network.setBlockedURLs({ urls: [] });
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); import((window.__coralStatic || '/static/') + 'editions.js').then(m => m.showHubSetup('connect')).then(() => true)`);
        await waitFor(`(() => { const m = document.getElementById('editions-modal'); return !!m && m.style.display !== 'none' && !!m.querySelector('.editions-cloud'); })()`, 'the connect screen', 8000);
        assert.deepEqual(await toasts(), [], 'no toast when it loads');
        console.log('PASS the connect screen says when it cannot load, and loads when it can');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
