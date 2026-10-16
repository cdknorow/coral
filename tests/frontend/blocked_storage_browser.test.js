// A browser that blocks site storage must still start the dashboard (#4116, Security's storage audit #4113). With window.localStorage and
// sessionStorage throwing a SecurityError on access (what a blocked-storage browser does), the dashboard starts, shows an agent, opens
// the side panel and its tabs, and raises no unhandled error or rejection; and what it would have stored is kept in memory for the
// page load. A second pass makes writes throw (storage full) with reads working.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BLOCK = `
(() => {
  const deny = () => { throw new DOMException('The operation is insecure.', 'SecurityError'); };
  Object.defineProperty(window, 'localStorage', { configurable: true, get: deny });
  Object.defineProperty(window, 'sessionStorage', { configurable: true, get: deny });
})();`;
const FULL = `
(() => {
  const real = Storage.prototype.setItem;
  Storage.prototype.setItem = function () { throw new DOMException('quota', 'QuotaExceededError'); };
})();`;
const ERRORS = `
window.__errs = [];
window.addEventListener('error', e => window.__errs.push('error: ' + e.message));
window.addEventListener('unhandledrejection', e => window.__errs.push('rejection: ' + (e.reason && e.reason.message || e.reason)));`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        for (const [name, script] of [['blocked (access throws)', BLOCK], ['full (writes throw)', FULL]]) {
            const added = await Page.addScriptToEvaluateOnNewDocument({ source: ERRORS + script + PRELOAD });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`window.__m.add('store-bot', 'A', 2, { sleeping: false }); true`);
            await sleep(600); // sleep-ok: the first live list renders from several answers; no single completion event
            await ev(`window.selectLiveSession('store-bot', 'claude', 'store-bot-id', 'local'); true`);
            await ev(`(() => { const p = document.getElementById('agentic-state'); if (p.classList.contains('collapsed')) window.toggleAgenticPanel(true); return true; })()`);
            await waitFor(`document.getElementById('agentic-tab-files').getBoundingClientRect().width > 0`, 'side panel shows', 8000);
            for (const tab of ['tasks', 'notes', 'files']) {
                await ev(`window.switchAgenticTab(${JSON.stringify(tab)}, 'top'); true`);
                assert.equal(await ev(`document.getElementById('agentic-tab-${tab}').classList.contains('active')`), true, `${name}: ${tab} opens`);
            }
            assert.equal(await ev(`!!document.querySelector('.session-group-item, #live-sessions-list li')`), true, `${name}: the agent list rendered`);
            const errs = await ev(`window.__errs`);
            assert.deepEqual(errs, [], `${name}: no unhandled errors: ${JSON.stringify(errs)}`);
            // What would have been stored is kept for this page load.
            const kept = await ev(`import((window.__coralStatic || '/static/') + 'safe_storage.js').then(m => { m.browserLocal.set('coral-probe', '1'); m.browserSession.set('coral-probe-s', '2'); return [m.browserLocal.get('coral-probe'), m.browserSession.get('coral-probe-s'), m.browserLocal.keys().includes('coral-probe')]; })`);
            assert.deepEqual(kept, ['1', '2', true], `${name}: a value written is read back from memory`);
            await ev(`import((window.__coralStatic || '/static/') + 'safe_storage.js').then(m => { m.browserLocal.remove('coral-probe'); return true; })`);
            assert.equal(await ev(`import((window.__coralStatic || '/static/') + 'safe_storage.js').then(m => m.browserLocal.get('coral-probe'))`), null);
            await Page.removeScriptToEvaluateOnNewDocument({ identifier: added.identifier });
            console.log(`   ✓ ${name}: the dashboard starts, shows an agent and its tabs, no unhandled errors; writes kept in memory`);
        }
        console.log('\nPASS the dashboard starts with browser storage blocked');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
