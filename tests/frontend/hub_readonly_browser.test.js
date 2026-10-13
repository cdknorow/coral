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
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

// Installed after the harness PRELOAD: /api/servers from window.__qaServers, and a fake terminal socket.
const CLOUD_PRELOAD = `(() => {
    window.__qaServers = { rows: [], delay: 0, fail: false };
    window.__wsSent = []; window.__wsTerm = null;
    const inner = window.fetch;
    window.fetch = (url, opts) => {
        if (new URL(String(url), location.origin).pathname === '/api/servers') {
            const q = window.__qaServers;
            const body = q.fail ? new Response('{}', { status: 503 }) : new Response(JSON.stringify({ servers: q.rows }), { status: 200, headers: { 'content-type': 'application/json' } });
            return new Promise(r => setTimeout(() => r(body), q.delay));
        }
        return inner(url, opts);
    };
    const Real = window.WebSocket;
    function FakeTerm(u) {
        this.url = String(u); this.readyState = 0; this.binaryType = 'blob';
        window.__wsTerm = this;
        setTimeout(() => { this.readyState = 1; this.onopen && this.onopen({}); }, 0);
    }
    FakeTerm.prototype.send = function (d) { window.__wsSent.push(typeof d === 'string' ? d : '[binary]'); };
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
        await sleep(250); // the terminal socket opens and the access lookup lands
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
    const pressEnter = () => ev(`document.getElementById('command-input').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })); true`);
    const draft = () => ev(`document.getElementById('command-input').value`);

    // Every terminal write path, run against the selected agent.
    const allPaths = () => ev(`(async () => {
        const settle = () => new Promise(r => setTimeout(r, 120));
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
        const { getTerminal } = await import('/static/xterm_renderer.js');
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
        Fetch.requestPaused(async e => {
            if (e.request.url.endsWith('/auth/session')) {
                return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                    body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
            }
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace('{"mode":"local"}', '{"mode":"cloud"}');
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
        const hubMsg = 'Blocked: Box M1 needs Interact (Hub: View · This machine allows: Launch · Effective: View). Raise it in Servers.';
        assert.equal(c.title, hubMsg, 'chip title is the full message');
        await clearSends(); await clearToasts();
        await typeDraft('hello there');
        await pressEnter();
        await sleep(400);
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

        console.log('2. Machine-limited: different text, no Open Servers');
        await select('ro-mach', 'm2');
        c = await chip();
        assert.equal(c.text, 'Read-only: This machine: View');
        await clearSends(); await clearToasts();
        await typeDraft('machine draft'); await pressEnter(); await sleep(400);
        s = await sends();
        assert.equal(s.posts + s.frames.length, 0, 'machine-limited: nothing sent');
        t = await toasts();
        const mach = t.find(x => /^Blocked: Box M2/.test(x.text));
        assert.ok(mach && /Change it in Coral on that machine \(Settings > Coral Hub\)\.$/.test(mach.text), `machine text (${JSON.stringify(t)})`);
        assert.equal(mach.action, null, 'no Open Servers when the machine is the limit');
        assert.equal(await draft(), 'machine draft');
        console.log(`   ✓ '${c.text}', machine text, no action button, draft kept`);

        console.log('3. Interact server: sends as before, no chip');
        await select('rw', 'm3');
        c = await chip();
        assert.ok(!c.shown, 'no chip on an Interact server');
        await clearSends(); await clearToasts();
        await typeDraft('go ahead'); await pressEnter(); await sleep(600);
        s = await sends();
        assert.ok(s.posts === 1 || s.frames.some(f => f.includes('go ahead')), `sent through the socket or /send (posts ${s.posts}, frames ${JSON.stringify(s.frames)})`);
        assert.equal(await draft(), '', 'draft cleared after a real send');
        assert.ok((await toasts()).some(x => /^Sent/.test(x.text)), 'Sent toast after a real send');
        console.log(`   ✓ sent (${s.posts ? 'POST /send' : 'socket'}), draft cleared, Sent toast`);

        console.log('4. Unknown server and failed lookup never block');
        await select('unknown', 'm9');
        assert.ok(!(await chip()).shown, 'unknown server: no chip');
        await clearSends(); await typeDraft('to unknown'); await pressEnter(); await sleep(600);
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
        await typeDraft('after frame'); await pressEnter(); await sleep(400);
        s = await sends();
        assert.equal(s.posts + s.frames.length, 0, 'frame: nothing sent although the list now says Interact (sticky until reselect)');
        assert.equal(await draft(), 'after frame');
        const termText = await ev(`(async () => { const t = (await import('/static/xterm_renderer.js')).getTerminal(); if (!t) return ''; const b = t.buffer.active; let s = ''; for (let i = 0; i < b.length; i++) s += b.getLine(i).translateToString(true) + '\\n'; return s; })()`);
        assert.ok(!/hub_read_only|some_future_thing|not json/.test(termText), 'control frames are never written to the terminal');
        await ev(`window.__wsInject(JSON.stringify({ type: 'hub_read_only', required: 'sudo-everything', hub_mode: '<b>root</b>' })); true`);
        await clearToasts(); await pressEnter(); await sleep(300);
        t = await toasts();
        assert.ok(t.some(x => x.text === 'Blocked: Box M4 is read-only through the Cloud Hub. Raise it in Servers.'), `unknown enum: generic text, nothing echoed (${JSON.stringify(t)})`);
        assert.ok(!t.some(x => /sudo-everything|root/.test(x.text)), 'no server-sent value echoed');
        // Reselecting clears the frame state; the list says Interact now.
        await select('frame', 'm4');
        assert.ok(!(await chip()).shown, 'reselect clears the frame state');
        console.log('   ✓ frame → chip + refusal (sticky), junk/unknown frames ignored, nothing echoed, reselect clears');

        console.log('6. A frame that arrives before the access lookup returns is not undone by it');
        await setServers([row('m1', 'view', 'launch'), row('m2', 'interact', 'view'), row('m3', 'interact', 'launch'), row('m5', 'interact', 'launch')], { delay: 600 });
        await ev(`window.__m.add('race', 'R', 5, { sleeping: false, server: 'm5' }); true`);
        await ev(`window.selectLiveSession('race', 'claude', 'race-id', 'm5'); true`);
        await waitFor(`window.__wsTerm && window.__wsTerm.url.includes('race-id') && window.__wsTerm.readyState === 1`, 'race socket', 3000);
        await ev(`window.__wsInject(JSON.stringify({ type: 'hub_read_only', required: 'interact', hub_mode: 'view' })); true`);
        await sleep(900); // the slow lookup (Interact) resolves after the frame
        c = await chip();
        assert.ok(c.shown, `the hub's frame still holds after the slower Servers lookup resolves (${JSON.stringify(c)})`);
        console.log('   ✓ frame state survives a late Interact lookup');

        console.log('\nAll hub read-only checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
