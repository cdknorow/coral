// Field-level feed diffs (#3281). A dashboard served by this Coral asks /ws/coral for
// ?diff=fields; a changed agent then arrives as a partial row ("_partial": true) with only
// what changed: it merges into the row the page has (an explicit null clears a field, an
// omitted one keeps its value), the marker is never stored, and a partial row for an agent
// the page no longer has is never shown half-built: the page reloads the list instead.
//
// Expects CORAL_TEST_URL + CDP_PORT from tests/frontend/run.sh. Drives Chrome over CDP
// (chrome-remote-interface). Exit 0 / 1.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__wsUrls = []; const W = window.WebSocket;
            window.WebSocket = function (u, p) { window.__wsUrls.push(String(u)); return p === undefined ? new W(u) : new W(u, p); };
            window.WebSocket.prototype = W.prototype; Object.assign(window.WebSocket, { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 });
            const real = window.fetch.bind(window); window.__lists = 0;
            window.fetch = (u, o) => { if (/\\/api\\/sessions\\/live(\\?|$)/.test(new URL(String(u), location.href).pathname + (String(u).includes('?') ? '?' : ''))) window.__lists++; return real(u, o); };
        })();` });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await coralReady();

        const urls = await ev(`window.__wsUrls.filter(u => /\\/ws\\/coral/.test(u))`);
        assert.ok(urls.length >= 1 && urls.every(u => /[?&]diff=fields\b/.test(u)), 'the feed is asked for field diffs: ' + JSON.stringify(urls));
        console.log('   ✓ the dashboard asks /ws/coral for ?diff=fields');

        // Hold the real feed so only the messages below move the list.
        const r = await ev(`(async () => {
            const ws = await import(${S}+'websocket.js'); const { state } = await import(${S}+'state.js');
            ws.feedHooks.schedule = () => {}; const c = state.coralWs; if (c) { c.onmessage = null; c.onclose = null; try { c.close(); } catch {} }
            const row = { session_id: 'fd-1', name: 'fd-agent', agent_type: 'claude', server: 'local', working: false, summary: 'before', branch: 'main', staleness_seconds: 3 };
            ws.handleCoralMessage({ type: 'coral_update', sessions: [row] });
            ws.handleCoralMessage({ type: 'coral_diff', changed: [{ _partial: true, session_id: 'fd-1', name: 'fd-agent', agent_type: 'claude', server: 'local', working: true, summary: null }] });
            const s = state.liveSessions.find(x => x.session_id === 'fd-1');
            const merged = { working: s.working, summary: s.summary, branch: s.branch, partialStored: '_partial' in s, n: state.liveSessions.length };
            return { merged };
        })()`);
        assert.deepEqual(r.merged, { working: true, summary: null, branch: 'main', partialStored: false, n: 1 },
            'changed field applied, the null clears, the omitted field keeps its value, no marker stored');
        console.log('   ✓ a partial row merges: changed applied, null clears, omitted kept, marker not stored');
        // A partial diff that only ages an agent still skips the full sidebar render (the drift
        // optimisation): the marker is not a change.
        const drift = await ev(`(async () => { const ws = await import(${S}+'websocket.js');
            const msg = { type: 'coral_diff', changed: [{ _partial: true, session_id: 'fd-1', name: 'fd-agent', agent_type: 'claude', server: 'local', staleness_seconds: 9 }] };
            ws.handleCoralMessage(msg); return msg._skipSidebarRender === true; })()`);
        assert.equal(drift, true, 'an age-only partial diff is drift');
        console.log('   ✓ an age-only partial diff still skips the full sidebar render');

        // A whole (non-partial) new row is still taken as-is.
        const added = await ev(`(async () => { const ws = await import(${S}+'websocket.js'); const { state } = await import(${S}+'state.js');
            ws.handleCoralMessage({ type: 'coral_diff', changed: [{ session_id: 'new-2', name: 'new', agent_type: 'claude', server: 'local', working: false }] });
            return !!state.liveSessions.find(x => x.session_id === 'new-2'); })()`);
        assert.equal(added, true);
        // Last: a partial row for an agent the page lost starts a list reload (from the real server,
        // which knows none of these agents), so it runs after the cases that use fd-1.
        const g = await ev(`(async () => { const ws = await import(${S}+'websocket.js'); const { state } = await import(${S}+'state.js');
            const listsBefore = window.__lists;
            ws.handleCoralMessage({ type: 'coral_diff', changed: [{ _partial: true, session_id: 'gone-9', name: 'ghost', agent_type: 'claude', server: 'local', working: true }] });
            return { ghost: !!state.liveSessions.find(x => x.session_id === 'gone-9'), listsBefore }; })()`);
        assert.equal(g.ghost, false, 'a partial row for an agent the page lost is not shown');
        await waitFor(`window.__lists > ${g.listsBefore}`, 'the list reloaded instead');
        console.log('   ✓ a partial row for a lost agent reloads the list instead of showing half a row');

        console.log('PASS feed_field_diff_browser');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
