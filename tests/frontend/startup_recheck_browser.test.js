// First run (#6048): a new agent that has not started, on a computer where its CLI is installed but signed out, gets one plain card (Claude's from the status, Codex's asked once for the card) with Check again,
// which asks the CLI's own status command now (GET /api/system/cli-check?signin=1) and clears or keeps the card by its answer. Drives Chrome over CDP; the status, the CLI check and the chat are answered by the test.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const fs = require('node:fs');
const SID = require('crypto').randomUUID();
const SESSIONS = [{ name: 'first-agent', display_name: 'Agent', agent_type: 'claude', session_id: SID, working_directory: '/repo/first', not_started: true }];
const CODEX = [{ name: 'first-codex', display_name: 'Agent', agent_type: 'codex', session_id: SID, working_directory: '/repo/first', not_started: true }];

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
              if (path === '/api/system/status' && opts && opts.cache === 'no-store') window.__freshStatus = (window.__freshStatus || 0) + 1;
              if (path === '/api/system/status') return window.__origFetch(url, opts).then(r => r.json()).then(d => { if (window.__signedIn !== undefined) d.claude_signed_in = window.__signedIn; else delete d.claude_signed_in; return json(d); });
              if (path === '/api/system/cli-check') { window.__checks = (window.__checks || []).concat(u); const o = { found: true, status: 'available', agent_type: new URLSearchParams(u.split('?')[1]).get('type') }; if (window.__cliSigned !== undefined) o.signed_in = window.__cliSigned; return json(o); }
              if (path === '/api/sessions/live') return json(window.__sessions);
              if (/^\\/api\\/sessions\\/live\\/[^/]+\\/chat$/.test(path)) return json({ messages: [], total: 0, has_more: false, pending: null });
              if (/^\\/api\\/board\\//.test(path)) return json({});
              return window.__origFetch(url, opts); };` });
        console.log('1. Claude: the signed-out card has Check again; a fresh answer clears it; no answer leaves it and says so');
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
        await ev(`window.__signedIn = false; window.switchNavTab('agents'); window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); true`);
        await ev(`Promise.resolve(window.selectLiveSession('first-agent', 'claude', ${JSON.stringify(SID)})).then(() => { window.setLiveViewMode('chat'); return true; })`);
        await waitFor(`/Claude is not signed in/.test((document.querySelector('#live-history-messages > .chat-needs-input') || {}).textContent || '')`, 'the signed-out card', 15000);
        const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); await new Promise(r => setTimeout(r, 300)); // sleep-ok: a screenshot settles layout
            fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
        await shot('signed-out-desktop');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await shot('signed-out-phone');
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        const btn = `document.querySelector('#live-history-messages > .chat-needs-input .cni-recheck')`;
        assert.equal(await ev(`${btn} && ${btn}.textContent`), 'Check again');
        assert.ok(await ev(`${btn}.getBoundingClientRect().height >= 24`), 'a big enough target');
        // still signed out: the card stays, and the CLI's own check was asked (fresh, signin=1)
        await ev(`window.__cliSigned = false; ${btn}.click(); true`);
        await waitFor(`(window.__checks || []).length === 1`, 'the check', 8000);
        assert.match(await ev(`window.__checks[0]`), /type=claude&signin=1/);
        assert.match(await ev(`document.querySelector('#live-history-messages > .chat-needs-input').textContent`), /Claude is not signed in/);
        await waitFor(`/still not signed in/.test((document.getElementById('coral-prompt-live') || {}).textContent || '')`, 'the screen reader hears the answer', 5000);
        // no answer (the CLI cannot be asked): the card is not changed to a claim the CLI did not make
        await ev(`window.__cliSigned = undefined; ${btn}.click(); true`);
        await waitFor(`window.__checks.length === 2`, 'the second check', 8000);
        assert.match(await ev(`document.querySelector('#live-history-messages > .chat-needs-input').textContent`), /Claude is not signed in|startup prompt/);
        // signed in now: the card goes back to the generic words
        await ev(`window.__cliSigned = true; (${btn} || {click(){}}).click(); true`);
        await waitFor(`/startup prompt/.test((document.querySelector('#live-history-messages > .chat-needs-input') || {}).textContent || '')`, 'the generic card once signed in', 8000);
        assert.ok(!(await ev(`!!document.querySelector('#live-history-messages > .chat-needs-input .cni-recheck')`)), 'no Check again once signed in');

        console.log('2. Codex: asked once for its card; signed out says so; Check again');
        await ev(`window.__sessions = ${JSON.stringify(CODEX)}; window.__cliSigned = false; window.__checks = []; window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); true`);
        await ev(`Promise.resolve(window.selectLiveSession('first-codex', 'codex', ${JSON.stringify(SID)})).then(() => { window.setLiveViewMode('chat'); return true; })`);
        await waitFor(`/Codex is not signed in/.test((document.querySelector('#live-history-messages > .chat-needs-input') || {}).textContent || '')`, 'the Codex card', 15000);
        assert.ok((await ev(`window.__checks`)).some(u => /type=codex&signin=1/.test(u)), 'Codex was asked, with signin=1');
        await ev(`window.__cliSigned = true; document.querySelector('#live-history-messages > .chat-needs-input .cni-recheck').click(); true`);
        await waitFor(`!/Codex is not signed in/.test((document.querySelector('#live-history-messages > .chat-needs-input') || {}).textContent || 'x')`, 'the card clears once Codex is signed in', 8000);
        console.log('PASS startup sign-in card: Claude and Codex, Check again');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
