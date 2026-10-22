// The notice pill (#waiting-banner) over the agent view, after QA's first-run walk (#5892, #5895): a sign-in is said once, by the card in the chat (no pill for it), and the Terminal tab shows no
// waiting pill, which sat on top of the terminal's first lines; a generic "waiting for input" pill still shows over the chat. fetch and the coral WebSocket are stubbed; the terminal is the view's own.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const LOGIN = require('crypto').randomUUID(), ASK = require('crypto').randomUUID();
const SESSIONS = [
    { name: 'login-agent', display_name: 'Login', agent_type: 'claude', session_id: LOGIN, working_directory: '/repo/a', waiting_for_input: true, needs_login: true, waiting_kind: 'login', waiting_reason: 'login' },
    { name: 'ask-agent', display_name: 'Ask', agent_type: 'claude', session_id: ASK, working_directory: '/repo/b', waiting_for_input: true, waiting_kind: 'permission', waiting_summary: 'Allow?' },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD } = await connect();
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
              if (/^\\/api\\/sessions\\/live\\/[^/]+\\/chat$/.test(path)) return json({ messages: [], total: 0, has_more: false, pending: null, needs_login: path.includes('login-agent') });
              if (/^\\/api\\/board\\//.test(path)) return json({});
              return window.__origFetch(url, opts); };` });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
        await ev(`window.switchNavTab('agents'); window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); true`);
        const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; require('node:fs').mkdirSync(d, { recursive: true }); require('node:fs').writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
        const pill = () => ev(`(() => { const b = document.getElementById('waiting-banner'); const r = b.getBoundingClientRect(); return { hidden: b.hidden, display: getComputedStyle(b).display, h: Math.round(r.height), text: b.textContent.trim() }; })()`);
        const settle = () => ev(`new Promise(r => setTimeout(() => r(true), 1800))`); // the page's own timer, longer than the pill's 1.5 s show delay
        const open = async (name, sid, mode) => {
            await ev(`Promise.resolve(window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(sid)})).then(() => { window.setLiveViewMode(${JSON.stringify(mode)}); return true; })`);
            await waitFor(`!!window.__coralReady && true`, 'ready', 5000);
            await ev(`import((window.__coralStatic || '/static/') + 'session_header_updates.js').then(m => { m.updateWaitingIndicator(window.__sessions.find(s => s.session_id === ${JSON.stringify(sid)})); return true; })`);
            await settle();
        };

        console.log('1. a sign-in: the card says it, no pill');
        await open('login-agent', LOGIN, 'chat');
        await waitFor(`/needs you to sign in/.test((document.querySelector('#live-history-messages > .chat-needs-input') || {}).textContent || '')`, 'the login card', 10000);
        let p = await pill();
        assert.ok(p.hidden || p.h === 0, 'no pill beside the login card: ' + JSON.stringify(p));
        await shot('pill-login-chat');

        console.log('2. a generic wait: the pill shows over the chat');
        await open('ask-agent', ASK, 'chat');
        p = await pill();
        assert.ok(!p.hidden && p.h > 0 && /waiting for input/.test(p.text), 'the pill shows over the chat: ' + JSON.stringify(p));
        await shot('pill-ask-chat');

        console.log('3. the Terminal tab: no waiting pill on top of the terminal');
        await ev(`window.setLiveViewMode('terminal'); true`);
        await settle();
        p = await pill();
        assert.ok(p.display === 'none' && p.h === 0, 'no pill over the terminal: ' + JSON.stringify(p));
        await shot('pill-ask-terminal');
        await ev(`window.setLiveViewMode('chat'); true`);
        await settle();
        p = await pill();
        assert.ok(p.h > 0, 'and it is back over the chat: ' + JSON.stringify(p));
        console.log('PASS the sign-in is said once, and the terminal is not covered');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
