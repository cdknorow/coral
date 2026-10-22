// The generic "hasn't done anything yet" pill is hidden only beside the signed-out card, which is for a Claude agent on this computer (#5852): a status that says Claude is not signed in
// (applySigninStatus) must not hide the pill of another agent that has not started, here a Codex agent. Drives Chrome over CDP against the isolated dev server; fetch and the coral WebSocket are stubbed.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const SID = require('crypto').randomUUID();
(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: `
            (function(){ const Real = window.WebSocket; function Dead(u){ this.url=String(u); this.readyState=0; } Dead.prototype.send=function(){}; Dead.prototype.close=function(){}; Dead.prototype.addEventListener=function(){}; Dead.prototype.removeEventListener=function(){};
              function W(u,p){ if (/\\/ws\\//.test(String(u))) return new Dead(u); return p===undefined? new Real(u): new Real(u,p); } W.prototype=Real.prototype; W.CONNECTING=0; W.OPEN=1; W.CLOSING=2; W.CLOSED=3; window.WebSocket=W; })();
            window.__sessions = [{ name: 'cx', display_name: 'Codex', agent_type: 'codex', session_id: '${SID}', working_directory: '/repo', not_started: true }]; window.__origFetch = window.fetch.bind(window);
            window.fetch = (url, opts) => { const u = String(url).replace(/^https?:\\/\\/[^/]+/, ''), path = u.split('?')[0], m = ((opts && opts.method) || 'GET').toUpperCase();
              const json = b => Promise.resolve(new Response(JSON.stringify(b), { status: 200, headers: { 'Content-Type': 'application/json' } }));
              if (m !== 'GET') return json({ ok: true });
              if (path === '/api/sessions/live') return json(window.__sessions);
              if (/^\\/api\\/sessions\\/live\\/[^/]+\\/chat$/.test(path)) return json({ messages: [], total: 0, has_more: false, pending: null });
              if (/^\\/api\\/board\\//.test(path)) return json({});
              return window.__origFetch(url, opts); };` });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
        await ev(`window.switchNavTab('agents'); window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); true`);
        await ev(`Promise.resolve(window.selectLiveSession('cx', 'codex', ${JSON.stringify(SID)})).then(() => { window.setLiveViewMode('chat'); return true; })`);
        await waitFor(`(() => { const b = document.getElementById('waiting-banner'); return !!b && !b.hidden && b.getBoundingClientRect().height > 0; })()`, 'the pill for a not-started codex agent', 15000);
        // Claude is not signed in on this computer: a codex agent's pill must stay
        await ev(`import((window.__coralStatic || '/static/') + 'signin_line.js').then(m => { m.applySigninStatus({ claude_signed_in: false }); return true; })`);
        await require('./helpers/wait').nextFrame(ev); // the pill is hidden at once (setPill), so two frames are enough to see it go if it were going to
        const shown = await ev(`(() => { const b = document.getElementById('waiting-banner'); return !!b && !b.hidden && b.getBoundingClientRect().height > 0; })()`);
        assert.equal(shown, true, 'a Codex agent keeps its pill when Claude is not signed in');
        console.log('PASS another agent keeps its pill');
    } finally { await client.close(); }
})().catch(e => { console.error(e && e.message || e); process.exit(1); });
