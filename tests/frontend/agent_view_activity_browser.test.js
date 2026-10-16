// #3719 (from the #3714 audit): what the dashboard requests for the selected agent follows what is on screen.
//  - Chat view (the default) opens no terminal WebSocket and downloads no xterm.js; the Terminal view does,
//    and Chat view closes the socket again.
//  - While the agent's page is not the view on screen (the phone's list after Back, any other view, a hidden
//    tab) the agent's polls stop (chat, pending tool, prompt options, pane capture, Agent UI) and its terminal
//    socket closes; coming back resumes them, and reattaches the terminal if that is the view.
//  - The Agent UI panel polls every 2 s only while its tab is open.
// Network counts come from wrappers installed in the page before the app loads (fetch and WebSocket).
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const { launchStandIn } = require('./helpers/stand_in_agent');

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
const MOD = `(window.__coralStatic || '/static/')`;

async function httpJson(method, path, body) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 8000) => waitFor(expr, what, { timeout: ms, soft: true });
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    let standIn = null;
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'View Activity QA' });
        if (!standIn.sid) throw new Error('no stand-in agent: ' + standIn.diag);
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__net = []; window.__ws = [];
            const realFetch = window.fetch;
            window.fetch = function (u) { try { window.__net.push({ t: Date.now(), url: String((u && u.url) || u) }); } catch (e) { /* ignore */ } return realFetch.apply(this, arguments); };
            const RealWS = window.WebSocket;
            window.WebSocket = function (url, protocols) { const s = protocols === undefined ? new RealWS(url) : new RealWS(url, protocols); window.__ws.push({ url: String(url), s }); return s; };
            window.WebSocket.prototype = RealWS.prototype;
            for (const k of ['CONNECTING', 'OPEN', 'CLOSING', 'CLOSED']) window.WebSocket[k] = RealWS[k];
            localStorage.removeItem('coral-live-view-mode'); // the default: Chat
        })();` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        // Request counts since a moment, by kind.
        const AGENT = `/api/sessions/live/[^/]+/(chat|pending-tool|prompt-options|poll)|/api/agent/ui`;
        const count = (re, since) => ev(`window.__net.filter(r => r.t >= ${since} && new RegExp(${JSON.stringify(re)}).test(r.url)).length`);
        const now = () => ev(`Date.now()`);
        const terminalSockets = () => ev(`window.__ws.filter(w => /\\/ws\\/terminal\\//.test(w.url) && w.s.readyState <= 1).length`);
        const terminalSocketsEver = () => ev(`window.__ws.filter(w => /\\/ws\\/terminal\\//.test(w.url)).length`);
        // The vendor library (vendor/xterm/*.js, 440 KB), not the app's own small xterm_renderer.js.
        const xtermFiles = () => ev(`performance.getEntriesByType('resource').map(e => e.name).filter(n => /\\/vendor\\/xterm\\//.test(n)).map(n => n.split('/').pop().split('?')[0])`);
        const xtermLoaded = async () => (await xtermFiles()).some(n => /\.js$/.test(n));
        const showView = id => ev(`import(${MOD} + 'utils.js').then(m => { m.showView(${JSON.stringify(id)}); return true; })`);
        const quiet = async (re, ms) => { // no request of this kind for ms, then the start of the observation window
            await waitFor(`!window.__net.some(r => r.t > Date.now() - ${ms} && new RegExp(${JSON.stringify(re)}).test(r.url))`, 'in-flight polls to finish', { timeout: 6000, soft: true });
            return now();
        };

        // ── Chat view: the agent is selected, no terminal ──────────────────
        await until(`!!document.querySelector('.session-group-item[data-session-id="${standIn.sid}"]')`, 'the agent in the list');
        const t0 = await now();
        await ev(`document.querySelector('.session-group-item[data-session-id="${standIn.sid}"]').click(); true`);
        await until(`window.__net.filter(r => r.t >= ${t0} && /\\/chat\\?/.test(r.url) && /after=/.test(r.url)).length >= 2`, 'the chat poll running');
        check('Chat view: the agent\'s chat is polled', (await count('/api/sessions/live/[^/]+/chat\\?', t0)) >= 2);
        check('Chat view opens no terminal WebSocket', (await terminalSocketsEver()) === 0, String(await terminalSocketsEver()));
        check('Chat view downloads no xterm.js', !(await xtermLoaded()), JSON.stringify(await xtermFiles()));
        check('the view mode really is Chat', await ev(`document.getElementById('capture-wrapper').classList.contains('chat-mode')`));

        // ── The agent's page goes off screen (the phone's list after Back, or any other view) ──
        await showView('mobile-agent-list');
        const t1 = await quiet(AGENT, 1500);
        await sleep(3500); // sleep-ok: shows that nothing is requested for a while (no condition to wait for)
        const idle = await count(AGENT, t1);
        check('page off screen: no agent request in 3.5 s (chat, pending-tool, prompt-options, poll, agent/ui)', idle === 0, String(idle));
        check('page off screen: no terminal socket open', (await terminalSockets()) === 0);

        // ── Back on the agent's page ───────────────────────────────────────
        const t2 = await now();
        await showView('live-session-view');
        await until(`window.__net.filter(r => r.t >= ${t2} && /\\/chat\\?/.test(r.url) && /after=/.test(r.url)).length >= 1`, 'the chat poll to resume', 4000);
        check('back on the page: the chat poll resumes', (await count('/api/sessions/live/[^/]+/chat\\?', t2)) >= 1);
        check('back on the page in Chat view: still no terminal socket', (await terminalSockets()) === 0);

        // ── Terminal view: the socket and xterm.js appear, and go with the view ──
        await ev(`window.setLiveViewMode('terminal'); true`);
        await until(`window.__ws.some(w => /\\/ws\\/terminal\\//.test(w.url) && w.s.readyState <= 1)`, 'the terminal socket', 15000);
        check('Terminal view opens the terminal socket', (await terminalSockets()) === 1, String(await terminalSockets()));
        check('Terminal view loads xterm.js', await xtermLoaded());
        await showView('mobile-agent-list');
        await until(`!window.__ws.some(w => /\\/ws\\/terminal\\//.test(w.url) && w.s.readyState <= 1)`, 'the terminal socket to close', 4000);
        check('Terminal view, page off screen: the socket closes', (await terminalSockets()) === 0);
        await showView('live-session-view');
        await until(`window.__ws.some(w => /\\/ws\\/terminal\\//.test(w.url) && w.s.readyState <= 1)`, 'the terminal socket to reopen', 8000);
        check('Terminal view, back on the page: the socket reopens', (await terminalSockets()) === 1, String(await terminalSockets()));
        await ev(`window.setLiveViewMode('chat'); true`);
        await until(`!window.__ws.some(w => /\\/ws\\/terminal\\//.test(w.url) && w.s.readyState <= 1)`, 'the terminal socket to close in Chat view', 4000);
        check('back to Chat view: the socket closes', (await terminalSockets()) === 0);

        // ── A hidden tab ───────────────────────────────────────────────────
        await ev(`window.setLiveViewMode('terminal'); true`);
        await until(`window.__ws.some(w => /\\/ws\\/terminal\\//.test(w.url) && w.s.readyState <= 1)`, 'the terminal socket again', 15000);
        await ev(`Object.defineProperty(document, 'hidden', { configurable: true, get: () => true }); document.dispatchEvent(new Event('visibilitychange')); true`);
        await until(`!window.__ws.some(w => /\\/ws\\/terminal\\//.test(w.url) && w.s.readyState <= 1)`, 'the socket to close in a hidden tab', 4000);
        check('hidden tab: the terminal socket closes', (await terminalSockets()) === 0);
        await ev(`delete document.hidden; document.dispatchEvent(new Event('visibilitychange')); true`);
        await until(`window.__ws.some(w => /\\/ws\\/terminal\\//.test(w.url) && w.s.readyState <= 1)`, 'the socket to reopen when the tab shows', 8000);
        check('tab visible again: the terminal socket reopens', (await terminalSockets()) === 1);
        await ev(`window.setLiveViewMode('chat'); true`);

        // ── The Agent UI panel polls fast only while its tab is open ───────
        await ev(`window.switchAgenticTab('files', 'top'); true`);
        const t3 = await quiet('/api/agent/ui', 2500);
        await sleep(4000); // sleep-ok: shows that nothing is requested for a while (no condition to wait for)
        const closed = await count('/api/agent/ui', t3);
        check('Agent UI tab closed: at most one slow poll in 4 s', closed <= 1, String(closed));
        await ev(`window.switchAgenticTab('agent-ui', 'top'); true`);
        const t4 = await now();
        await until(`window.__net.filter(r => r.t >= ${t4} && /\\/api\\/agent\\/ui/.test(r.url)).length >= 3`, 'the Agent UI poll', 7000);
        const open = await count('/api/agent/ui', t4);
        check('Agent UI tab open: it polls every 2 s', open >= 3, String(open));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        if (standIn) await standIn.stop().catch(() => {});
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
