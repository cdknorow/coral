// A link to an agent's page with ?view=terminal (coral-hub's request, #6752): /agents/<session>?view=terminal opens the agent on its terminal; the saved Chat/Terminal choice is left as it was (the terminal is for this visit);
// the address bar loses the query as the page starts. Exactly one view parameter with exactly the value terminal counts: another value, a second view or none opens the chat as before. (The parse table is routes_unit.test.mjs.)
// Drives Chrome over CDP through agent_view_harness.js with its mocked agents; the agent's session id is a UUID, as a route needs.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const SID = '5c1d7e2a-3b4f-4a60-9d81-2e3f4a5b6c7d';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD } = await connect();
    const SEED = `(() => { window.__m.add('tq-agent', 'T', 6, { sleeping: false, session_id: '${SID}' }); })();`;
    const where = () => ev(`({ path: location.pathname, search: location.search, mode: localStorage.getItem('coral-live-view-mode'),
        terminalOn: (() => { const b = document.getElementById('live-view-btn-terminal'); return !!b && (b.classList.contains('active') || b.getAttribute('aria-pressed') === 'true'); })(),
        chatOn: (() => { const b = document.getElementById('live-view-btn-chat'); return !!b && (b.classList.contains('active') || b.getAttribute('aria-pressed') === 'true'); })() })`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: SEED });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        const open = async (query, saved) => {
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await ev(`localStorage.setItem('coral-live-view-mode', ${JSON.stringify(saved)}); true`);
            await Page.navigate({ url: BASE + '/agents/' + SID + query }); await Page.loadEventFired();
            await waitFor(`!!document.getElementById('live-view-btn-terminal') && document.getElementById('live-view-btn-terminal').getClientRects().length > 0`, `the agent's page for "${query}"`, 20000);
        };

        // the link: the terminal, this visit only, the address without the query
        await open('?view=terminal', 'chat');
        await waitFor(`(() => { const b = document.getElementById('live-view-btn-terminal'); return b.classList.contains('active') || b.getAttribute('aria-pressed') === 'true'; })()`, 'the terminal to show', 15000);
        let w = await where();
        assert.equal(w.mode, 'chat', `the saved Chat/Terminal choice is untouched (${w.mode})`);
        assert.equal(w.path, '/agents/' + SID, `the path stays (${w.path})`);
        assert.equal(w.search, '', `the query is gone from the address bar (${JSON.stringify(w.search)})`);
        console.log('   ✓ ?view=terminal opens the terminal, keeps the saved choice and drops the query');

        // the saved choice is Terminal and the link is the plain page: Terminal, as before (nothing here forces Chat)
        for (const [q, label] of [['', 'no query'], ['?view=chat', 'another value'], ['?view=terminal&view=chat', 'a second view'], ['?view=Terminal', 'a different case']]) {
            await open(q, 'chat');
            await ev(`new Promise(r => setTimeout(r, 600))`); // sleep-ok: a later call to the terminal would show within this time
            w = await where();
            assert.ok(!w.terminalOn, `${label}: the chat stays (${JSON.stringify(w)})`);
            assert.equal(w.mode, 'chat', `${label}: the saved choice is untouched`);
        }
        console.log('   ✓ no query, another value, a second view and a different case open the chat');
        console.log('PASS /agents/<session>?view=terminal opens the terminal for the visit only');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
