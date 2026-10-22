// The waiting pill and the card in the chat say the same thing, so the pill is not drawn beside the card while the card is ON SCREEN (#5895); scrolled away from it (up in a long chat, a phone with no
// sidebar and only a small dot in the header) the pill is the cue again. Held, at 1280 and 390 px, for a permission wait and for a sign-in: at the bottom (card in view) no pill; at the top of a long chat
// the pill is shown with its words; back at the bottom it is gone again. fetch and the coral WebSocket are stubbed.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const ASK = require('crypto').randomUUID(), LOGIN = require('crypto').randomUUID();
const SESSIONS = [
    { name: 'ask-agent', display_name: 'Ask', agent_type: 'claude', session_id: ASK, working_directory: '/repo/b', waiting_for_input: true, waiting_kind: 'permission', waiting_summary: 'Allow the agent to run npm test?' },
    { name: 'login-agent', display_name: 'Login', agent_type: 'claude', session_id: LOGIN, working_directory: '/repo/a', waiting_for_input: true, needs_login: true, waiting_kind: 'login', waiting_reason: 'Run claude login' },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: `
            (function(){ const Real = window.WebSocket; function Dead(u){ this.url=String(u); this.readyState=0; } Dead.prototype.send=function(){}; Dead.prototype.close=function(){}; Dead.prototype.addEventListener=function(){}; Dead.prototype.removeEventListener=function(){};
              function W(u,p){ if (/\\/ws\\//.test(String(u))) return new Dead(u); return p===undefined? new Real(u): new Real(u,p); } W.prototype=Real.prototype; W.CONNECTING=0; W.OPEN=1; W.CLOSING=2; W.CLOSED=3; window.WebSocket=W; })();
            window.__sessions = ${JSON.stringify(SESSIONS)}; window.__origFetch = window.fetch.bind(window);
            const MSGS = []; for (let i = 0; i < 60; i++) MSGS.push({ type: i % 2 ? 'assistant' : 'user', content: 'Message ' + i + ' lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor.' });
            window.fetch = (url, opts) => { const u = String(url).replace(/^https?:\\/\\/[^/]+/, ''), path = u.split('?')[0], m = ((opts && opts.method) || 'GET').toUpperCase();
              const json = b => Promise.resolve(new Response(JSON.stringify(b), { status: 200, headers: { 'Content-Type': 'application/json' } }));
              if (m !== 'GET') return json({ ok: true });
              if (path === '/api/sessions/live') return json(window.__sessions);
              if (/^\\/api\\/sessions\\/live\\/[^/]+\\/chat$/.test(path)) return json({ messages: MSGS, total: MSGS.length, has_more: false, pending: null, needs_login: path.includes('login-agent') });
              if (/^\\/api\\/board\\//.test(path)) return json({});
              return window.__origFetch(url, opts); };` });
        const pill = () => ev(`(() => { const b = document.getElementById('waiting-banner'); const r = b.getBoundingClientRect(); return { display: getComputedStyle(b).display, h: Math.round(r.height), text: b.textContent.trim() }; })()`);
        const scrollTo = where => ev(`(() => { const card = document.querySelector('#live-history-messages > .chat-needs-input'); for (let e = card.parentElement; e; e = e.parentElement) if (e.scrollHeight > e.clientHeight + 50 && /auto|scroll/.test(getComputedStyle(e).overflowY)) { e.scrollTop = ${where === 'top' ? '0' : 'e.scrollHeight'}; return true; } return false; })()`);
        for (const [w, mobile, nm] of [[1280, false, 'desktop'], [390, true, 'phone']]) for (const [name, sid, words] of [['ask-agent', ASK, /waiting for input/], ['login-agent', LOGIN, /sign in/]]) {
            const tag = `${nm}, ${name}`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: 800, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
            await ev(`window.switchNavTab('agents'); window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); true`);
            await ev(`Promise.resolve(window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(sid)})).then(() => { window.setLiveViewMode('chat'); return true; })`);
            await waitFor(`document.querySelectorAll('#live-history-messages > *').length > 20 && !!document.querySelector('#live-history-messages > .chat-needs-input')`, `${tag}: the long chat and its card`, 15000);
            await ev(`import((window.__coralStatic || '/static/') + 'session_header_updates.js').then(m => { m.updateWaitingIndicator(window.__sessions.find(s => s.session_id === ${JSON.stringify(sid)})); return true; })`);
            assert.ok(await scrollTo('bottom'), `${tag}: the chat scrolls`);
            await waitFor(`document.getElementById('capture-wrapper').classList.contains('card-in-view')`, `${tag}: the card is in view`, 5000);
            await waitFor(`getComputedStyle(document.getElementById('waiting-banner')).display === 'none' || document.getElementById('waiting-banner').hidden`, `${tag}: no pill beside the card`, 5000);
            assert.ok(await scrollTo('top'));
            await waitFor(`!document.getElementById('capture-wrapper').classList.contains('card-in-view')`, `${tag}: the card is out of view`, 5000);
            await waitFor(`!document.getElementById('waiting-banner').hidden && getComputedStyle(document.getElementById('waiting-banner')).display !== 'none'`, `${tag}: scrolled away, the pill is the cue`, 5000);
            const p = await pill();
            assert.ok(p.h > 0 && words.test(p.text), `${tag}: the pill says it: ${JSON.stringify(p)}`);
            if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/pill-scrolled-${nm}-${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
            assert.ok(await scrollTo('bottom'));
            await waitFor(`getComputedStyle(document.getElementById('waiting-banner')).display === 'none'`, `${tag}: back at the card, the pill is gone again`, 5000);
            console.log(`   ok ${tag}`);
        }
        console.log('PASS the pill is the cue only while the card is out of view');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
