// First run (#5835): the card of an agent that has not started (it may be at a startup prompt: trusting the folder, signing in) is readable from its first line and says what to do. The
// notice pill floats over the top of the agent view; the card, the first thing in an empty chat, used to sit under it. Held, at 1280 and 390 px: the pill does not cover the card, the pill
// and the card name the terminal as a tab to open (not "below"), the card has the Open terminal button, and the first-message advice. fetch and the coral WebSocket are stubbed.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const SID = require('crypto').randomUUID();
const SESSIONS = [{ name: 'first-agent', display_name: 'Agent', agent_type: 'claude', session_id: SID, working_directory: '/repo/first', not_started: true }];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: `
            (function(){ const Real = window.WebSocket; function Dead(u){ this.url=String(u); this.readyState=0; } Dead.prototype.send=function(){}; Dead.prototype.close=function(){}; Dead.prototype.addEventListener=function(){}; Dead.prototype.removeEventListener=function(){};
              function W(u,p){ if (/\\/ws\\//.test(String(u))) return new Dead(u); return p===undefined? new Real(u): new Real(u,p); } W.prototype=Real.prototype; W.CONNECTING=0; W.OPEN=1; W.CLOSING=2; W.CLOSED=3; window.WebSocket=W; })();
            window.__sessions = ${JSON.stringify(SESSIONS)}; window.__origFetch = window.fetch.bind(window);
            window.fetch = (url, opts) => { const u = String(url).replace(/^https?:\\/\\/[^/]+/, ''), path = u.split('?')[0], m = ((opts && opts.method) || 'GET').toUpperCase();
              const json = b => Promise.resolve(new Response(JSON.stringify(b), { status: 200, headers: { 'Content-Type': 'application/json' } }));
              if (m !== 'GET') return json({ ok: true });
              if (path === '/api/sessions/live') return json(window.__sessions);
              if (/^\\/api\\/sessions\\/live\\/[^/]+\\/chat$/.test(path)) return json({ messages: [], total: 0, has_more: false, pending: null });
              if (/^\\/api\\/board\\//.test(path)) return json({});
              return window.__origFetch(url, opts); };` });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
            await ev(`window.switchNavTab('agents'); window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); true`);
            await ev(`Promise.resolve(window.selectLiveSession('first-agent', 'claude', ${JSON.stringify(SID)})).then(() => { window.setLiveViewMode('chat'); return true; })`);
            await waitFor(`!!document.querySelector('#live-history-messages > .chat-needs-input')`, `${w}: the card`, 15000);
            await waitFor(`(() => { const b = document.getElementById('waiting-banner'); return !!b && !b.hidden && b.getBoundingClientRect().height > 0; })()`, `${w}: the pill`, 15000);
            const g = await ev(`(() => { const b = document.getElementById('waiting-banner').getBoundingClientRect(), c = document.querySelector('#live-history-messages > .chat-needs-input'), r = c.getBoundingClientRect();
                return { pillBottom: Math.round(b.bottom), cardTop: Math.round(r.top), pill: document.getElementById('waiting-banner').textContent.trim(), card: c.textContent.replace(/\\s+/g, ' ').trim(), open: !!c.querySelector('[data-on-click="terminal-view-open"]') }; })()`);
            assert.ok(g.cardTop >= g.pillBottom, `${w}: the pill (bottom ${g.pillBottom}) does not cover the card (top ${g.cardTop})`);
            assert.ok(!/below/.test(g.pill) && /Open its terminal/.test(g.pill), `${w}: the pill names the terminal as something to open: ${g.pill}`);
            assert.match(g.card, /Open the terminal, answer it there, then come back and type your first message/);
            assert.ok(g.open, `${w}: the card has Open terminal`);
            console.log(`   ok ${w}: pill ${g.pillBottom}, card ${g.cardTop}`);
        }
        console.log('PASS the first-run card is readable and says what to do');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
