// Cloud Hub View mode (#3068): when the hub (or the machine's own setting) limits a server to View, the hub
// drops terminal input without an error, so the dashboard must refuse up front. No /send or /keys, no
// terminal-socket frame, no false 'Sent:' toast, the draft is kept, an error toast says why (with
// 'Open Servers' when the hub is the limit), keystrokes typed into the terminal are dropped, and the
// terminal header shows a read-only chip. A {type:'hub_read_only'} frame on the terminal socket does the
// same. Interact servers, unknown servers and failed lookups are unaffected.
//
// Cloud mode is the hub's boot flag injected into Coral's own page (as in cloud_csp_browser); the
// Servers list, the agents' endpoints and the terminal socket are in-page fakes.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

// Installed after the harness PRELOAD: /api/servers from window.__qaServers, and a fake terminal socket.
const CLOUD_PRELOAD = `(() => {
    window.__qaServers = { rows: [], delay: 0, fail: false, resolved: [], patches: [] };
    window.__wsSent = []; window.__wsTerm = null;
    const inner = window.fetch;
    window.fetch = (url, opts) => {
        const path = new URL(String(url), location.origin).pathname;
        if (path.startsWith('/api/servers/') && opts && opts.method === 'PATCH') { // a Servers-tab change: applied to the stub rows (#3138)
            const q = window.__qaServers, body = JSON.parse(opts.body || '{}'), id = decodeURIComponent(path.slice('/api/servers/'.length));
            q.patches.push({ id, body });
            const r = q.rows.find(x => x.id === id);
            if (r && body.access_mode) r.access_mode = body.access_mode;
            return Promise.resolve(new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'content-type': 'application/json' } }));
        }
        if (new URL(String(url), location.origin).pathname === '/api/servers') {
            const q = window.__qaServers;
            const body = q.fail ? new Response('{}', { status: 503 }) : new Response(JSON.stringify({ servers: q.rows }), { status: 200, headers: { 'content-type': 'application/json' } });
            return new Promise(r => setTimeout(() => { q.resolved.push(performance.now()); r(body); }, q.delay));
        }
        return inner(url, opts);
    };
    const Real = window.WebSocket;
    function FakeTerm(u) {
        this.url = String(u); this.readyState = 0; this.binaryType = 'blob';
        window.__wsTerm = this;
        setTimeout(() => { this.readyState = 1; this.onopen && this.onopen({}); }, 0);
    }
    // As the server does (#3276): every terminal_input carries an id, and the server acknowledges it; "Sent" waits for the acks.
    FakeTerm.prototype.send = function (d) {
        window.__wsSent.push(typeof d === 'string' ? d : '[binary]');
        try {
            const m = typeof d === 'string' ? JSON.parse(d) : null;
            // A message to an agent is one composer_send frame (#4587), answered the same way once submitted.
            if (m && (m.type === 'terminal_input' || m.type === 'composer_send') && Number.isInteger(m.id)) setTimeout(() => window.__wsInject(JSON.stringify({ type: 'terminal_input_ack', id: m.id })), 0);
        } catch (e) { /* not JSON: nothing to acknowledge */ }
    };
    FakeTerm.prototype.close = function () { this.readyState = 3; };
    FakeTerm.prototype.addEventListener = function () {}; FakeTerm.prototype.removeEventListener = function () {};
    window.__wsInject = data => window.__wsTerm && window.__wsTerm.onmessage && window.__wsTerm.onmessage({ data });
    function W(u, p) {
        const s = String(u);
        if (/\\/ws\\/terminal/.test(s)) return new FakeTerm(s);
        return p === undefined ? new Real(u) : new Real(u, p);
    }
    W.prototype = Real.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3;
    window.WebSocket = W;
})();`;

(async () => {
    const { client, Page, ev, waitFor, sleep, PRELOAD, install, reqs, clearReqs } = await connect();
    const { Fetch, Network } = client;

    const row = (id, access_mode, local_mode) => ({ id, label: 'Box ' + id.toUpperCase(), online: true, access_mode, local_mode, last_seen: '2026-10-13T00:00:00Z' });
    // The hub sends the same servers in /api/servers and in the live list's servers summary (state.servers).
    const setServers = (rows, extra = {}) => ev(`(() => { Object.assign(window.__qaServers, { rows: ${JSON.stringify(rows)}, delay: 0, fail: false }, ${JSON.stringify(extra)}); T.state.servers = ${JSON.stringify(rows.map(r => ({ id: r.id, label: r.label, status: 'online' })))}; return true; })()`);
    // The access lookup is cached per tab for 15 s, so each case uses its own server id.
    const select = async (name, server) => {
        await ev(`(async () => { await window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(name + '-id')}, ${JSON.stringify(server)}); return true; })()`);
        // The agent's terminal socket opens and the access lookup lands (the chip holds still).
        await waitFor(`!!window.__wsTerm && window.__wsTerm.url.includes(${JSON.stringify(name + '-id')}) && window.__wsTerm.readyState === 1`, 'the terminal socket', 3000).catch(() => {});
        await w.waitStable(`JSON.stringify((c => c ? [c.hidden, c.textContent] : null)(document.getElementById('hub-readonly-chip')))`, 'the access chip', 200).catch(() => {});
    };
    const chip = () => ev(`(() => { const c = document.getElementById('hub-readonly-chip'); return c ? { shown: !c.hidden && getComputedStyle(c).display !== 'none', text: c.textContent, title: c.title, role: c.getAttribute('role') } : null; })()`);
    const toasts = () => ev(`[...document.querySelectorAll('.toast, .notification-toast')].map(t => ({ text: (t.querySelector('.toast-message') || t).textContent.trim(), role: t.getAttribute('role'), action: t.querySelector('.toast-action')?.textContent.trim() || null }))`);
    const clearToasts = () => ev(`document.querySelectorAll('.toast, .notification-toast').forEach(t => t.remove()); true`);
    const sends = async () => {
        const r = (await reqs()).filter(x => x.kind === 'send' || x.kind === 'keys');
        const frames = await ev(`window.__wsSent.filter(f => f !== '[binary]' && !/^\\{"type":"(resize|ping|hello|auth)/.test(f))`);
        return { posts: r.length, frames };
    };
    const clearSends = async () => { await clearReqs(); await ev(`window.__wsSent.length = 0; true`); };
    const typeDraft = text => ev(`(() => { const i = document.getElementById('command-input'); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
    // After Enter: a refusal toast or a send happens, then nothing more is sent for 300 ms (a send that should not
    // happen would have started by then). Condition waits (#3118).
    const w = require('./helpers/wait').bind(ev);
    const afterEnter = async () => {
        await waitFor(`document.querySelectorAll('.toast, .notification-toast').length > 0 || window.__wsSent.length > 0`, 'a refusal or a send', 3000).catch(() => {});
        let last = JSON.stringify(await sends()), since = Date.now(); const end = since + 8000;
        while (Date.now() - since < 300 && Date.now() < end) { await sleep(25); const now = JSON.stringify(await sends()); if (now !== last) { last = now; since = Date.now(); } }
    };
    const pressEnter = () => ev(`document.getElementById('command-input').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })); true`);
    const draft = () => ev(`document.getElementById('command-input').value`);

    // Every terminal write path, run against the selected agent.
    const allPaths = () => ev(`(async () => {
        const settle = () => new Promise(r => setTimeout(r, 120)); // sleep-ok: some paths aren't awaited; let each one's async guard finish
        await window.sendCommand(); await settle();
        window.sendQuickCommand('/compact'); await settle();
        window.executeMacro('/status'); await settle();
        await window.sendRawKeys(['Escape']); await settle();
        await window.sendRawKeys(['Up'], { silent: true }); await settle();
        window.cycleModeToggle?.(); await settle();
        await window.sendBoardProtocol?.(); await settle();
        await window.sendCommandWithTeam?.(); await settle();
        await window.resendInputPrompt?.(); await settle();
        // Keystrokes typed into the terminal.
        const { getTerminal } = await import((window.__coralStatic||'/static/')+'xterm_renderer.js');
        const term = getTerminal();
        if (term && term.input) term.input('typed-in-terminal');
        await settle();
        return !!(term && term.input);
    })()`);

    try {
        await Page.enable();
        await Network.enable();
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: CLOUD_PRELOAD });
        await Fetch.enable({ patterns: [
            { urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' },
            { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
        ] });
        safePaused(client, async e => {
            if (e.request.url.endsWith('/auth/session')) {
                return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                    body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
            }
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace('"mode":"local"', '"mode":"cloud"');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await install();
        assert.equal(await ev(`JSON.parse(document.getElementById('coral-boot').textContent).mode`), 'cloud', 'cloud mode boot flag');
        await ev(`localStorage.setItem('coral-live-view-mode', 'terminal'); true`);

        console.log('1. Hub View: every send path refused, draft kept, chip, Open Servers');
        await setServers([row('m1', 'view', 'launch'), row('m2', 'interact', 'view'), row('m3', 'interact', 'launch'), row('m4', 'interact', 'launch')]);
        await ev(`window.__m.add('ro-hub', 'H', 5, { sleeping: false, server: 'm1' }); window.__m.add('ro-mach', 'M', 5, { sleeping: false, server: 'm2' }); window.__m.add('rw', 'W', 5, { sleeping: false, server: 'm3' }); window.__m.add('frame', 'F', 5, { sleeping: false, server: 'm4' }); window.__m.add('unknown', 'U', 5, { sleeping: false, server: 'm9' }); true`);
        await select('ro-hub', 'm1');
        let c = await chip();
        assert.ok(c && c.shown, 'chip shown for a View server');
        assert.equal(c.text, 'Read-only: Hub: View');
        assert.equal(c.role, 'status');
        const hubMsg = 'Blocked: Box M1 needs Interact (Hub: View · This machine allows: Launch · Effective: View). Choose a higher Hub access in Servers.';
        assert.equal(c.title, hubMsg, 'chip title is the full message');
        await clearSends(); await clearToasts();
        await typeDraft('hello there');
        await pressEnter();
        await afterEnter();
        let s = await sends();
        assert.equal(s.posts, 0, 'Enter: no /send or /keys');
        assert.deepEqual(s.frames, [], 'Enter: nothing written to the terminal socket');
        assert.equal(await draft(), 'hello there', 'Enter: the draft is kept');
        let t = await toasts();
        assert.ok(!t.some(x => /^Sent/.test(x.text)), `no 'Sent' toast (${JSON.stringify(t)})`);
        const blocked = t.find(x => x.text === hubMsg);
        assert.ok(blocked, `refusal toast with the exact message (${JSON.stringify(t)})`);
        assert.equal(blocked.role, 'alert');
        assert.equal(blocked.action, 'Open Servers');
        await ev(`[...document.querySelectorAll('.toast .toast-action')].find(b => b.textContent.trim() === 'Open Servers').click(); true`);
        // 'Open Servers' opens the Servers tab at the refused server's row (#3070).
        await waitFor(`(() => { const v = document.getElementById('servers-view'); return !!v && getComputedStyle(v).display !== 'none' && document.activeElement && document.activeElement.id === 'server-row-m1'; })()`, 'Servers tab focused on the refused server', 3000);
        await ev(`window.switchNavTab('agents'); true`);
        await select('ro-hub', 'm1');
        await clearSends(); await clearToasts();
        const typed = await allPaths();
        s = await sends();
        assert.equal(s.posts, 0, `no /send or /keys from any path (got ${s.posts})`);
        assert.deepEqual(s.frames, [], `no terminal frames from any path or typing (got ${JSON.stringify(s.frames)})`);
        assert.equal(await draft(), 'hello there', 'the draft survives every path (quick command, macro, protocol, reminder, resend)');
        t = await toasts();
        assert.ok(!t.some(x => /^Sent/.test(x.text)), 'still no Sent toast');
        console.log(`   ✓ Enter + 9 paths${typed ? ' + typing' : ''}: 0 posts, 0 frames, draft kept, exact Blocked toast with Open Servers, chip '${c.text}'`);

        // Chat view opens no terminal socket (#3719), so a typed command and a quick key go through POST /send and /keys;
        // a View-only hub must refuse them exactly as it refuses the socket.
        console.log('1b. Chat view (no terminal socket, POST /send and /keys the only route): the same refusal');
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        await select('ro-hub', 'm1');
        assert.ok(await ev(`document.getElementById('capture-wrapper').classList.contains('chat-mode')`), 'the agent is shown in Chat view');
        await clearSends(); await clearToasts();
        await typeDraft('chat view draft');
        await pressEnter(); await afterEnter();
        const typedInChat = await allPaths();
        s = await sends();
        assert.equal(s.posts, 0, `Chat view: no /send or /keys from any path (got ${s.posts})`);
        assert.deepEqual(s.frames, [], 'Chat view: no terminal frames either');
        assert.equal(await draft(), 'chat view draft', 'Chat view: the draft survives');
        t = await toasts();
        assert.ok(t.some(x => x.text === hubMsg), `Chat view: the exact Blocked toast (${JSON.stringify(t)})`);
        assert.ok(!t.some(x => /^Sent/.test(x.text)), 'Chat view: no Sent toast');
        console.log(`   ✓ Chat view: Enter + 9 paths${typedInChat ? ' + typing' : ''}: 0 posts, 0 frames, draft kept, exact Blocked toast`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'terminal'); true`);

        console.log('2. Machine-limited: different text, no Open Servers');
        await select('ro-mach', 'm2');
        c = await chip();
        assert.equal(c.text, 'Read-only: This machine: View');
        await clearSends(); await clearToasts();
        await typeDraft('machine draft'); await pressEnter(); await afterEnter();
        s = await sends();
        assert.equal(s.posts + s.frames.length, 0, 'machine-limited: nothing sent');
        t = await toasts();
        const mach = t.find(x => /^Blocked: Box M2/.test(x.text));
        assert.ok(mach && /Change it in Coral on that machine \(Settings > Network and Hub\)\.$/.test(mach.text), `machine text (${JSON.stringify(t)})`);
        assert.equal(mach.action, null, 'no Open Servers when the machine is the limit');
        assert.equal(await draft(), 'machine draft');
        console.log(`   ✓ '${c.text}', machine text, no action button, draft kept`);

        console.log('3. Interact server: sends as before, no chip');
        await select('rw', 'm3');
        c = await chip();
        assert.ok(!c.shown, 'no chip on an Interact server');
        await clearSends(); await clearToasts();
        await typeDraft('go ahead'); await pressEnter(); await afterEnter();
        s = await sends();
        assert.ok(s.posts === 1 || s.frames.some(f => f.includes('go ahead')), `sent through the socket or /send (posts ${s.posts}, frames ${JSON.stringify(s.frames)})`);
        assert.equal(await draft(), '', 'draft cleared after a real send');
        // Over the socket "Sent" shows once the send is accepted (no terminal_gone in the window, #3218).
        await waitFor(`[...document.querySelectorAll('.toast .toast-message')].some(t => /^Sent/.test(t.textContent.trim()))`, 'the Sent toast', 3000).catch(() => {});
        assert.ok((await toasts()).some(x => /^Sent/.test(x.text)), 'Sent toast after a real send');
        console.log(`   ✓ sent (${s.posts ? 'POST /send' : 'socket'}), draft cleared, Sent toast`);

        console.log('4. Unknown server and failed lookup never block');
        await select('unknown', 'm9');
        assert.ok(!(await chip()).shown, 'unknown server: no chip');
        await clearSends(); await typeDraft('to unknown'); await pressEnter(); await afterEnter();
        s = await sends();
        assert.ok(s.posts === 1 || s.frames.some(f => f.includes('to unknown')), 'unknown server: sent (the hub still enforces)');
        console.log('   ✓ unknown server sends');

        console.log('5. hub_read_only frame on the socket: same state, sticky, odd frames ignored');
        // The list says Interact for m4 from the start (before the first lookup caches it), so only the frame
        // can make it read-only.
        await setServers([row('m1', 'view', 'launch'), row('m2', 'interact', 'view'), row('m3', 'interact', 'launch'), row('m4', 'interact', 'launch')]);
        await select('frame', 'm4');
        assert.ok(!(await chip()).shown, 'Interact in the list: no chip before the frame');
        await ev(`window.__wsInject('not json at all'); window.__wsInject('{"type":"some_future_thing"}'); window.__wsInject('42'); true`);
        await ev(`window.__wsInject(JSON.stringify({ type: 'hub_read_only', required: 'interact', hub_mode: 'view' })); true`);
        await sleep(100);
        c = await chip();
        assert.ok(c.shown && c.text === 'Read-only: Hub: View', `frame: chip (${JSON.stringify(c)})`);
        await clearSends(); await clearToasts();
        await typeDraft('after frame'); await pressEnter(); await afterEnter();
        s = await sends();
        assert.equal(s.posts + s.frames.length, 0, 'frame: nothing sent although the list now says Interact (sticky until reselect)');
        assert.equal(await draft(), 'after frame');
        const termText = await ev(`(async () => { const t = (await import((window.__coralStatic||'/static/')+'xterm_renderer.js')).getTerminal(); if (!t) return ''; const b = t.buffer.active; let s = ''; for (let i = 0; i < b.length; i++) s += b.getLine(i).translateToString(true) + '\\n'; return s; })()`);
        assert.ok(!/hub_read_only|some_future_thing|not json/.test(termText), 'control frames are never written to the terminal');
        await ev(`window.__wsInject(JSON.stringify({ type: 'hub_read_only', required: 'sudo-everything', hub_mode: '<b>root</b>' })); true`);
        await clearToasts(); await pressEnter(); await afterEnter();
        t = await toasts();
        assert.ok(t.some(x => x.text === 'Blocked: Box M4 is read-only through the Coral Hub. Choose a higher Hub access in Servers.'), `unknown enum: generic text, nothing echoed (${JSON.stringify(t)})`);
        assert.ok(!t.some(x => /sudo-everything|root/.test(x.text)), 'no server-sent value echoed');
        // Reselecting clears the frame state; the list says Interact now.
        await select('frame', 'm4');
        assert.ok(!(await chip()).shown, 'reselect clears the frame state');
        console.log('   ✓ frame → chip + refusal (sticky), junk/unknown frames ignored, nothing echoed, reselect clears');

        console.log('6. A frame that arrives before the access lookup returns is not undone by it');
        await setServers([row('m1', 'view', 'launch'), row('m2', 'interact', 'view'), row('m3', 'interact', 'launch'), row('m5', 'interact', 'launch')], { delay: 600 });
        // A fresh cache answers an unknown server at once (null), which would clear the state BEFORE the
        // frame and never race it. Expire it so the selection's lookup is a real, slow request (#3125).
        await ev(`(async () => { (await import((window.__coralStatic||'/static/')+'cloud_servers.js')).invalidateServerAccess(); return true; })()`);
        await ev(`window.__m.add('race', 'R', 5, { sleeping: false, server: 'm5' }); true`);
        await ev(`window.selectLiveSession('race', 'claude', 'race-id', 'm5'); true`);
        await waitFor(`window.__wsTerm && window.__wsTerm.url.includes('race-id') && window.__wsTerm.readyState === 1`, 'race socket', 3000);
        const frameAt = await ev(`(() => { window.__wsInject(JSON.stringify({ type: 'hub_read_only', required: 'interact', hub_mode: 'view' })); return performance.now(); })()`);
        assert.ok(await ev(`window.__qaServers.resolved.every(t => t < ${frameAt}) && document.getElementById('hub-readonly-chip') && !document.getElementById('hub-readonly-chip').hidden`), 'the frame lands while the lookup is still pending');
        // The slow lookup (Interact) resolves after the frame; then give its continuation a moment to run.
        await waitFor(`window.__qaServers.resolved.some(t => t > ${frameAt})`, 'the Servers lookup resolving after the frame', 5000);
        await sleep(100); // sleep-ok: let the lookup's continuation run before checking it changed nothing
        c = await chip();
        assert.ok(c.shown, `the hub's frame still holds after the slower Servers lookup resolves (${JSON.stringify(c)})`);
        console.log('   ✓ frame state survives a late Interact lookup');

        console.log('7. The user choosing Launch in the Servers tab re-decides the open agent (no reselect needed)');
        // (a) The owner chose View and the machine allows Launch: choosing Launch -> effective Launch -> the lock clears.
        await setServers([row('m6', 'view', 'launch'), row('m7', 'interact', 'view')]);
        await ev(`window.__m.add('raised', 'R', 3, { sleeping: false, server: 'm6' }); true`);
        await ev(`window.selectLiveSession('raised', 'claude', 'raised-id', 'm6'); true`);
        await waitFor(`window.__wsTerm && window.__wsTerm.url.includes('raised-id') && window.__wsTerm.readyState === 1`, 'raised socket', 3000);
        await ev(`window.__wsInject(JSON.stringify({ type: 'hub_read_only', required: 'interact', hub_mode: 'view' })); window.__sessBefore = T.state.currentSession; true`);
        await waitFor(`!!(T.state.currentSession.hubReadOnly && T.state.currentSession.hubReadOnly.fromFrame)`, 'the frame lock', 3000);
        await ev(`window.switchNavTab('servers'); true`);
        await waitFor(`!!document.querySelector('#server-row-m6 .cs-launch select')`, 'the Hub access select for m6', 5000);
        await ev(`(() => { const s = document.querySelector('#server-row-m6 .cs-launch select'); s.value = 'launch'; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await waitFor(`T.state.currentSession.hubReadOnly === null`, 'the lock clearing after choosing Launch (up to what the computer allows)', 5000);
        const a = await ev(`({ same: T.state.currentSession === window.__sessBefore, chipHidden: document.getElementById('hub-readonly-chip').hidden, patches: window.__qaServers.patches })`);
        assert.ok(a.same, 'the same session object: no reselect');
        assert.ok(a.chipHidden, 'the read-only chip is gone');
        assert.deepEqual(a.patches, [{ id: 'm6', body: { access_mode: 'launch' } }], 'one PATCH: m6 -> launch, no confirmation dialog');
        console.log('   ✓ choosing Launch on a computer that allows it clears the frame lock on the open agent, no reselect');

        // (b) The machine's own limit keeps the effective mode at View: the lock stays, now saying why.
        await ev(`window.switchNavTab('agents'); window.__qaServers.patches = []; true`);
        await ev(`window.__m.add('capped', 'C', 3, { sleeping: false, server: 'm7' }); true`);
        await ev(`window.selectLiveSession('capped', 'claude', 'capped-id', 'm7'); true`);
        await waitFor(`window.__wsTerm && window.__wsTerm.url.includes('capped-id') && window.__wsTerm.readyState === 1`, 'capped socket', 3000);
        await ev(`window.__wsInject(JSON.stringify({ type: 'hub_read_only', required: 'interact', hub_mode: 'view' })); true`);
        await waitFor(`!!(T.state.currentSession.hubReadOnly && T.state.currentSession.hubReadOnly.fromFrame)`, 'the frame lock on m7', 3000);
        await ev(`window.switchNavTab('servers'); true`);
        await waitFor(`!!document.querySelector('#server-row-m7 .cs-launch select')`, 'the Access select for m7', 5000);
        await ev(`(() => { const s = document.querySelector('#server-row-m7 .cs-launch select'); s.value = 'launch'; s.dispatchEvent(new Event('change')); return true; })()`);
        await waitFor(`window.__qaServers.patches.length === 1 && T.state.currentSession.hubReadOnly && /This machine/.test(T.state.currentSession.hubReadOnly.chip)`, 'the lock re-explained by the machine limit', 5000);
        const b = await ev(`({ ro: T.state.currentSession.hubReadOnly, chip: document.getElementById('hub-readonly-chip').textContent })`);
        assert.equal(b.ro.fromFrame, true, 'still frame-sticky: a later lookup still cannot clear it');
        assert.equal(b.chip, 'Read-only: This machine: View', 'the chip names the machine limit');
        assert.match(b.ro.message, /Change it in Coral on that machine/, 'the refusal says where to change it');
        console.log('   ✓ machine-limited (effective View): the lock stays and names the machine limit');

        console.log('\nAll hub read-only checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
