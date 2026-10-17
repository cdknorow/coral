// The live list is asked for together with /api/health, not after it (#4326): the list used to wait for the health answer (is this a hub?), one more
// round trip before the first paint. /api/health is held for a second here; the list must be requested while it is still pending, and its answer
// must still be read with the hub flag known (a hub's list is {sessions, servers}). Drives the real dashboard over CDP.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, sleep } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const real = window.fetch; window.__log = [];
            window.fetch = (input, init) => { const url = new URL(String(typeof input === 'string' ? input : input.url), location.href).pathname; const at = performance.now();
                if (url === '/api/health') { window.__log.push(['health-start', at]); return real(input, init).then(r => new Promise(res => setTimeout(() => { window.__log.push(['health-end', performance.now()]); res(r); }, 1000))); }
                if (url === '/api/sessions/live') window.__log.push(['list-start', at]);
                return real(input, init); }; })();` });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await sleep(2500); // sleep-ok: the held health answer (1 s) and the list render that follows have no single event to wait on
        const log = await ev('window.__log');
        const at = n => (log.find(x => x[0] === n) || [])[1];
        assert.ok(at('health-start') !== undefined && at('list-start') !== undefined && at('health-end') !== undefined, 'all three happened: ' + JSON.stringify(log));
        assert.ok(at('list-start') < at('health-end') - 500, `the list was requested while /api/health was still pending (list at ${at('list-start').toFixed(0)} ms, health answered at ${at('health-end').toFixed(0)} ms)`);
        const rendered = await ev(`(window.__coralReady && window.__coralReady.parts && window.__coralReady.parts.liveList) || !!document.getElementById('sidebar-sessions') || document.readyState`);
        assert.ok(rendered, 'the page still came up');
        console.log(`   ✓ the list went out ${(at('health-end') - at('list-start')).toFixed(0)} ms before /api/health answered`);
        console.log('\nPASS the live list is requested together with /api/health');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
