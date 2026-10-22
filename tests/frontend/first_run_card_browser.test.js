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
              if (path === '/api/system/status' && opts && opts.cache === 'no-store') window.__freshStatus = (window.__freshStatus || 0) + 1;
              if (path === '/api/system/status') return window.__origFetch(url, opts).then(r => r.json()).then(d => { if (window.__signedIn !== undefined) d.claude_signed_in = window.__signedIn; else delete d.claude_signed_in; return json(d); });
              if (path === '/api/system/cli-check') { window.__cliAsks = (window.__cliAsks || 0) + 1; return json(window.__cli === undefined ? { found: true } : { found: true, signed_in: window.__cli }); } // the CLI's own status command, as the card asks it (#6070)
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
            await ev(`import((window.__coralStatic || '/static/') + 'session_header_updates.js').then(m => { m.updateWaitingIndicator(window.__sessions[0]); return new Promise(r => setTimeout(() => r(true), 1800)); })`); // the page's own timer, longer than the pill's 1.5 s show delay
            const g = await ev(`(() => { const b = document.getElementById('waiting-banner').getBoundingClientRect(), c = document.querySelector('#live-history-messages > .chat-needs-input'), r = c.getBoundingClientRect();
                return { pillHeight: Math.round(b.height), cardTop: Math.round(r.top), card: c.textContent.replace(/\\s+/g, ' ').trim(), open: !!c.querySelector('[data-on-click="terminal-view-open"]') }; })()`);
            assert.ok(g.pillHeight === 0, `${w}: the card is the one message: no pill beside it (height ${g.pillHeight})`);
            assert.match(g.card, /Open the terminal, answer it there, then come back and type your first message/);
            assert.ok(g.open, `${w}: the card has Open terminal`);
            const t = await ev(`(() => { const b = document.querySelector('#live-history-messages > .chat-needs-input [data-on-click="terminal-view-open"]'), cs = getComputedStyle(b); return { h: Math.round(b.getBoundingClientRect().height), underline: cs.textDecorationLine === 'underline' && cs.textDecorationColor !== 'rgba(0, 0, 0, 0)' && !/transparent/.test(cs.textDecorationColor) }; })()`);
            assert.ok(t.underline, `${w}: Open terminal is underlined at rest, so it reads as the action`);
            assert.ok(t.h >= (mobile ? 44 : 24), `${w}: Open terminal is a big enough target (${t.h})`);
            console.log(`   ok ${w}: card ${g.cardTop}, no pill`);
        }

        // Claude's sign-in as the server says it (#5852, after #5838's check): not signed in -> the card says so; signed in or not said -> the generic words
        console.log('2. the card says "Claude is not signed in" when the status says so');
        const card = () => ev(`(() => { const c = document.querySelector('#live-history-messages > .chat-needs-input'); return c ? { text: c.textContent.replace(/\\s+/g, ' ').trim(), open: !!c.querySelector('[data-on-click="terminal-view-open"]') } : null; })()`);
        const withStatus = async v => { await ev(`window.__signedIn = ${v === undefined ? 'undefined' : v}; import((window.__coralStatic || '/static/') + 'signin_line.js').then(m => m.askSigninSoon ? null : null).then(() => fetch('/api/system/status', { cache: 'no-store' }).then(r => r.json()).then(d => import((window.__coralStatic || '/static/') + 'signin_line.js').then(m => { m.applySigninStatus(d); return true; })))`); await ev(`import((window.__coralStatic || '/static/') + 'live_chat.js').then(m => m.refreshLiveHistory()).then(() => true)`); };
        await withStatus(false);
        await waitFor(`/Claude is not signed in/.test((document.querySelector('#live-history-messages > .chat-needs-input') || {}).textContent || '')`, 'the signed-out card', 10000);
        let c = await card();
        assert.match(c.text, /Sign-in needed.*Claude is not signed in.*Open the terminal and sign in, then check again./); assert.ok(c.open, 'Open terminal stays');
        assert.ok(!/startup prompt/.test(c.text), 'not the generic words');
        await waitFor(`document.getElementById('waiting-banner').hidden`, 'the generic pill gone beside the signed-out card', 8000);
        assert.ok(await ev(`document.getElementById('waiting-banner').hidden`), 'the signed-out card is the one message: the "hasn\'t done anything yet" pill is not shown with it');
        // and a later update of the same agent (the WebSocket tick) does not bring it back: wait out the pill's show delay in the page, then look
        await ev(`import((window.__coralStatic || '/static/') + 'session_header_updates.js').then(m => { m.updateWaitingIndicator(window.__sessions[0]); return new Promise(r => setTimeout(() => r(true), 1800)); })`); // the page's own timer, not a sleep of the test: longer than the pill's 1.5 s show delay
        assert.ok(await ev(`document.getElementById('waiting-banner').hidden`), 'a live update does not bring the generic pill back beside the signed-out card');
        await withStatus(true);
        await waitFor(`/startup prompt/.test((document.querySelector('#live-history-messages > .chat-needs-input') || {}).textContent || '')`, 'the generic card when signed in', 10000);
        assert.ok(!/not signed in/.test((await card()).text));
        await withStatus(undefined);
        await waitFor(`/startup prompt/.test((document.querySelector('#live-history-messages > .chat-needs-input') || {}).textContent || '')`, 'the generic card when not said', 10000);
        assert.ok(!/not signed in/.test((await card()).text));
        console.log('   ok signed out / signed in / not said');
        // claude_signed_in is THIS computer's Claude: an agent on another server gets the generic card, and nothing is asked for it
        console.log('3. an agent on another server never gets the sign-in card');
        await ev(`window.__sessions[0].server = 'other'; window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); true`);
        await withStatus(false);
        await waitFor(`/startup prompt/.test((document.querySelector('#live-history-messages > .chat-needs-input') || {}).textContent || '')`, 'the generic card for another server', 10000);
        assert.ok(!/not signed in/.test((await card()).text), 'the local Claude being signed out says nothing about the other server');
        await withStatus(undefined);
        await ev(`window.__freshStatus = 0; true`);
        for (let i = 0; i < 3; i++) await ev(`import((window.__coralStatic || '/static/') + 'live_chat.js').then(m => m.refreshLiveHistory()).then(() => true)`);
        assert.equal(await ev(`window.__freshStatus`), 0, 'a card for another server asks for no status');
        assert.ok(!/not signed in/.test((await card()).text));

        // #6070 (a): no status answer yet (a fresh install: no background check has run), so the card asks the CLI's own status command and shows the sign-in card at once, as Codex's does
        console.log('4. a signed-out Claude with no status answer yet gets the sign-in card from the CLI check');
        await ev(`const t0 = Date.now.bind(Date); Date.now = () => t0() + 60000; window.__sessions[0].server = undefined; window.__cli = false; window.__cliAsks = 0; window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); true`);
        await withStatus(undefined);
        await waitFor(`/Claude is not signed in/.test((document.querySelector('#live-history-messages > .chat-needs-input') || {}).textContent || '')`, 'the sign-in card from the CLI check alone', 10000);
        c = await card();
        assert.match(c.text, /Sign-in needed.*Claude is not signed in.*Open the terminal and sign in, then check again./); assert.ok(c.open, 'Open terminal stays');
        assert.equal(await ev(`window.__cliAsks`), 1, 'the CLI is asked once, not on every look');
        for (let i = 0; i < 3; i++) await ev(`import((window.__coralStatic || '/static/') + 'live_chat.js').then(m => m.refreshLiveHistory()).then(() => true)`);
        assert.equal(await ev(`window.__cliAsks`), 1, 'later looks at the card do not ask again');

        // #6070 (c): the "Claude is not signed in" line is about Claude: not on a Codex agent's page, nor an agent on another computer; on a Claude agent's page and off agent pages it stays
        console.log('5. the Claude sign-in line shows on Claude, not on a Codex agent');
        const lineShown = () => ev(`(() => { const l = document.getElementById('claude-signin-line'); return !l.hidden && l.getClientRects().length > 0; })()`);
        await withStatus(false);
        await waitFor(`!document.getElementById('claude-signin-line').hidden`, 'the line for a signed-out Claude', 10000);
        assert.ok(await lineShown(), 'on the Claude agent: shown');
        const CODEX = require('crypto').randomUUID();
        await ev(`window.__sessions.push({ name: 'codex-agent', display_name: 'Codex agent', agent_type: 'codex', session_id: ${JSON.stringify(CODEX)}, working_directory: '/repo/c' }); window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); Promise.resolve(window.selectLiveSession('codex-agent', 'codex', ${JSON.stringify(CODEX)})).then(() => true)`);
        await waitFor(`document.body.dataset.agentCli === 'codex'`, 'the Codex agent open', 10000);
        assert.ok(!(await lineShown()), 'on a Codex agent: not shown, though Claude is signed out');
        await ev(`Promise.resolve(window.selectLiveSession('first-agent', 'claude', ${JSON.stringify(SID)})).then(() => true)`);
        await waitFor(`document.body.dataset.agentCli === 'claude'`, 'the Claude agent open again', 10000);
        assert.ok(await lineShown(), 'back on the Claude agent: shown again');
        await ev(`window.__sessions[0].server = 'other'; window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); Promise.resolve(window.selectLiveSession('first-agent', 'claude', ${JSON.stringify(SID)}, 'other')).then(() => true)`);
        await waitFor(`document.body.dataset.agentCli === 'remote'`, 'an agent on another computer open', 10000);
        assert.ok(!(await lineShown()), 'a Claude on another computer: not shown (this computer\'s Claude says nothing about it)');
        await ev(`import((window.__coralStatic || '/static/') + 'view_switch.js').then(m => { m.showView('welcome-screen'); return true; })`);
        assert.equal(await ev(`document.body.dataset.agentCli`), '', 'off an agent page: no CLI');
        assert.ok(await lineShown(), 'off the agent pages the line still says background jobs wait');

        console.log('PASS the first-run card is readable and says what to do');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
