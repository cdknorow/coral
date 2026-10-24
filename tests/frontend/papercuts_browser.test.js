// UX papercuts batch (#3152):  run via run_cdp_suites.mjs
// Docs error + Retry; a launch with a missing directory marks the field; Chats
// with nothing open has its own empty state; the phone nav strip has no
// scrollbar and fades the edge with more tabs; the team-send button is a
// labelled icon; the API key is masked full width with Show/Copy; the agent
// search placeholder fits its box; the Servers tab's Local row and an
// unreachable row; opening a view before the first list load is not re-opened.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Fetch, Emulation } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    let failDocs = 0;        // answer /api/agent-docs with 500 this many times
    let emptyDocs = false;   // answer /api/agent-docs with an empty list
    let holdList = null;     // a paused /api/sessions/live request
    let holdNextList = false;
    let fakeHub = false;     // answer /api/health as a hub (phase 15)
    let heldReleased = false; // the held list has been answered: from then on every list is a hub's
    safePaused(client, async e => {
        const u = new URL(e.request.url);
        const json = (code, body) => Fetch.fulfillRequest({ requestId: e.requestId, responseCode: code, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify(body)).toString('base64') });
        // Docs: fail as asked, then answer with a one-page list (a test build
        // embeds no agent docs, so the real endpoint cannot be the success case).
        if (u.pathname === '/api/agent-docs') {
            if (failDocs > 0) { failDocs--; return json(500, { error: 'boom' }); }
            if (emptyDocs) return json(200, []);
            return json(200, [{ name: 'README', title: 'Readme' }]);
        }
        if (u.pathname === '/api/agent-docs/README') return json(200, { name: 'README', title: 'Readme', content: '# Hello docs' });
        if (u.pathname === '/api/health' && fakeHub) return json(200, { status: 'ok', version: 'test', hub: true });
        if (u.pathname === '/api/sessions/live' && holdNextList) {
            holdNextList = false;
            holdList = e.requestId;
            return;
        }
        // A hub answers EVERY list as {sessions, servers}. The app asks again shortly after the first list lands (under
        // load ~100 ms later, inside the phase-15 wait); a plain array from the test server reset the server list to none
        // and hid the Servers tab: the flake.
        if (u.pathname === '/api/sessions/live' && fakeHub && heldReleased) return json(200, { sessions: [], servers: [{ id: 'r1', label: 'Remote', status: 'online' }] });
        return Fetch.continueRequest({ requestId: e.requestId });
    });
    const open = async (width, height, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: 'about:blank' }); await Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady('startup', 'liveList');
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Fetch.enable({ patterns: [{ urlPattern: '*/api/agent-docs*', requestStage: 'Request' }, { urlPattern: '*/api/sessions/live*', requestStage: 'Request' }, { urlPattern: '*/api/health*', requestStage: 'Request' }] });

        // ── (5) Docs: a failed list shows an error with Retry, which loads it.
        await open(1280, 800, false);
        failDocs = 1;
        await ev(`switchNavTab('docs'); true`);
        await waitFor(`!!document.querySelector('#docs-content .docs-error[role=alert] button')`, 'the docs error with Retry', { timeout: 8000 });
        assert.match(await ev(`document.querySelector('#docs-content .docs-error').textContent`), /Could not load the documentation/);
        assert.equal(await ev(`document.querySelectorAll('#docs-nav-list li').length`), 0, 'no list yet');
        await ev(`document.querySelector('#docs-content .docs-error button').click(); true`);
        await waitFor(`document.querySelectorAll('#docs-nav-list li').length > 0 && !document.querySelector('#docs-content .docs-error')`, 'the docs list after Retry', { timeout: 8000, diag: `({ li: document.querySelectorAll('#docs-nav-list li').length, content: document.getElementById('docs-content').textContent.slice(0, 120) })` });
        console.log('PASS (5) Docs: error + Retry, then the list loads');
        // An empty list says so instead of spinning forever.
        emptyDocs = true;
        await ev(`switchNavTab('agents'); true`);
        await open(1280, 800, false);
        await ev(`switchNavTab('docs'); true`);
        await waitFor(`/No documentation found/.test(document.getElementById('docs-content').textContent)`, 'the empty docs message', { timeout: 8000 });
        emptyDocs = false;
        await open(1280, 800, false);
        console.log('PASS (5) Docs: an empty list says No documentation found');

        // ── (7) Chats with nothing open: its own empty state, not the Agents home.
        await ev(`switchNavTab('history'); true`);
        await waitFor(`getComputedStyle(document.getElementById('chats-empty-view')).display !== 'none'`, 'the chats empty state');
        assert.equal(await ev(`getComputedStyle(document.getElementById('welcome-screen')).display`), 'none', 'not the Agents home');
        await ev(`switchNavTab('agents'); true`);
        await waitFor(`getComputedStyle(document.getElementById('welcome-screen')).display !== 'none'`, 'the Agents home on Agents');
        console.log('PASS (7) Chats: own empty state; Agents keeps its home');

        // ── (6) Launch with a missing directory: 400 and the field is marked.
        await ev(`showLaunchModal()`);
        await waitFor(`typeof window._selectLaunchType === 'function' && !!document.getElementById('launch-dir')`, 'the launch dialog');
        await ev(`window._selectLaunchType('agent'); document.getElementById('launch-dir').value = '/definitely/not/a/real/dir'; true`);
        await ev(`launchSession()`);
        await waitFor(`document.getElementById('launch-dir').getAttribute('aria-invalid') === 'true'`, 'the directory field marked invalid', { timeout: 8000 });
        const field = await ev(`(() => { const i = document.getElementById('launch-dir'); return { focused: document.activeElement === i, title: i.title, cls: i.classList.contains('input-invalid') }; })()`);
        assert.ok(field.focused && field.cls, 'marked and focused: ' + JSON.stringify(field));
        assert.match(field.title, /directory not found/);
        await ev(`(() => { const i = document.getElementById('launch-dir'); i.value = '/tmp'; i.dispatchEvent(new Event('input')); })()`);
        assert.equal(await ev(`document.getElementById('launch-dir').hasAttribute('aria-invalid')`), false, 'typing clears the mark');
        // Browse sets the value and fires 'change' (browser.js): that clears it too, and the old title comes back.
        const before = await ev(`document.getElementById('launch-dir').title`);
        await ev(`(() => { const i = document.getElementById('launch-dir'); i.value = '/definitely/not/a/real/dir'; return true; })()`);
        await ev(`launchSession()`);
        await waitFor(`document.getElementById('launch-dir').getAttribute('aria-invalid') === 'true'`, 'marked again', { timeout: 8000 });
        await ev(`(() => { const i = document.getElementById('launch-dir'); i.value = '/tmp'; i.dispatchEvent(new Event('change')); })()`);
        assert.deepEqual(await ev(`(() => { const i = document.getElementById('launch-dir'); return [i.hasAttribute('aria-invalid'), i.classList.contains('input-invalid'), i.title]; })()`), [false, false, before], "Browse's change clears the mark and restores the title");
        // Closing the dialog clears a mark left on it.
        await ev(`document.getElementById('launch-dir').value = '/definitely/not/a/real/dir'; true`);
        await ev(`launchSession()`);
        await waitFor(`document.getElementById('launch-dir').getAttribute('aria-invalid') === 'true'`, 'marked once more', { timeout: 8000 });
        await ev(`hideLaunchModal(); true`);
        assert.equal(await ev(`document.getElementById('launch-dir').hasAttribute('aria-invalid')`), false, 'closing the dialog clears the mark');
        console.log('PASS (6) Launch: missing directory -> field marked, cleared on edit');

        // ── (12) Settings API key: masked, full width, Show and Copy.
        // GET /api/system/api-key needs a signed-in browser (#4290): these tabs have no sign-in, so the answer is faked, as a signed-in one and as a refused one.
        await ev(`window.__realFetch = window.fetch; window.__keyAnswer = 200; window.fetch = (u, o) => new URL(String(u), location.origin).pathname === '/api/system/api-key'
            ? Promise.resolve(window.__keyAnswer === 200 ? new Response(JSON.stringify({ key: 'papercuts-test-key-0123456789' }), { status: 200, headers: { 'content-type': 'application/json' } }) : new Response('{"error":"dashboard_auth_required"}', { status: window.__keyAnswer, headers: { 'content-type': 'application/json' } }))
            : window.__realFetch(u, o); true`);
        await ev(`showSettingsModal()`);
        await waitFor(`(() => { const i = document.getElementById('settings-api-key'); return i && i.value && i.value !== 'Loading...'; })()`, 'the API key', { timeout: 8000 });
        const key = await ev(`(() => { const i = document.getElementById('settings-api-key'); const row = i.closest('.settings-api-key-row'); const show = document.getElementById('settings-api-key-show'); const copy = document.getElementById('settings-api-key-copy');
            return { type: i.type, readOnly: i.readOnly, w: i.getBoundingClientRect().width, rowW: row.getBoundingClientRect().width, showHidden: show.hidden, copyDisabled: copy.disabled }; })()`);
        assert.equal(key.type, 'password', 'masked');
        assert.ok(key.readOnly);
        assert.equal(key.showHidden, false); assert.equal(key.copyDisabled, false);
        assert.ok(key.w >= key.rowW - 1, `the key takes the row's full width (${Math.round(key.w)} of ${Math.round(key.rowW)})`);
        await ev(`document.getElementById('settings-api-key-show').click(); true`);
        assert.deepEqual(await ev(`[document.getElementById('settings-api-key').type, document.getElementById('settings-api-key-show').textContent, document.getElementById('settings-api-key-show').getAttribute('aria-pressed')]`), ['text', 'Hide', 'true']);
        await ev(`hideSettingsModal(); true`);
        // No sign-in: the server answers 401; the field says how to see the key (text, controls off), not "Not configured".
        await ev(`window.__keyAnswer = 401; showSettingsModal(); true`);
        await waitFor(`/Sign in/.test(document.getElementById('settings-api-key').value)`, 'the sign-in text', { timeout: 8000 });
        const refused = await ev(`(() => { const i = document.getElementById('settings-api-key'); return { value: i.value, type: i.type, showHidden: document.getElementById('settings-api-key-show').hidden, copyDisabled: document.getElementById('settings-api-key-copy').disabled }; })()`);
        assert.deepEqual(refused, { value: 'Sign in to see the API key (run: coral open)', type: 'text', showHidden: true, copyDisabled: true });
        await ev(`hideSettingsModal(); window.fetch = window.__realFetch; true`);
        console.log('PASS (12) API key: masked full width, Show/Hide, Copy enabled; a 401 says to sign in');

        // ── (13) The agent search placeholder fits the box at its narrowest.
        const fit = await ev(`(() => { const i = document.getElementById('agent-search-input'); const cs = getComputedStyle(i); const box = i.closest('.sidebar-search'); const min = parseFloat(getComputedStyle(box).minWidth);
            const c = document.createElement('canvas').getContext('2d'); c.font = cs.font; const text = c.measureText(i.placeholder).width;
            return { placeholder: i.placeholder, label: i.getAttribute('aria-label'), text, room: min - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight) - parseFloat(cs.borderLeftWidth) - parseFloat(cs.borderRightWidth) }; })()`);
        assert.equal(fit.label, 'Search agents', 'the accessible name keeps saying what it searches');
        assert.ok(fit.text <= fit.room, `placeholder "${fit.placeholder}" (${fit.text.toFixed(1)}px) fits the box's minimum width (${fit.room.toFixed(1)}px)`);
        console.log(`PASS (13) search placeholder "${fit.placeholder}" fits at the minimum width`);

        // ── (14) Servers tab (a hub): the Local row and an unreachable row.
        const rows = await ev(`(async () => {
            const S = window.__coralStatic || '/static/';
            const { state } = await import(S + 'state.js');
            const real = window.fetch;
            window.fetch = (u, o) => (new URL(String(u), location.origin).pathname === '/api/servers' && (!o || !o.method || o.method === 'GET'))
                ? Promise.resolve(new Response(JSON.stringify([
                    { id: 'local', label: 'Local', local: true, status: 'online' },
                    { id: 'r1', label: 'Remote', url: 'https://r1.example:8420', status: 'unreachable', last_error: 'dial tcp: connection refused', last_seen: new Date(Date.now() - 600000).toISOString() },
                  ]), { status: 200, headers: { 'content-type': 'application/json' } }))
                : real(u, o);
            state.hub = true; state.localVersion = '9.9.9';
            state.servers = [{ id: 'r1', label: 'Remote', status: 'unreachable' }];
            (await import(S + 'servers_tab.js')).updateServersTabVisibility();
            window.switchNavTab('servers');
            for (let i = 0; i < 100 && !document.querySelector('#server-row-r1'); i++) await new Promise(r => setTimeout(r, 50));
            const text = sel => (document.querySelector(sel)?.textContent || '').replace(/\\s+/g, ' ').trim();
            const out = { local: text('#server-row-local .servers-row-meta'), r1meta: text('#server-row-r1 .servers-row-meta'), r1hint: text('#server-row-r1 .servers-row-hint') };
            window.fetch = real;
            return out;
        })()`);
        assert.match(rows.local, /^This hub · Coral 9\.9\.9 · \d+ agents?$/, 'the Local row shows its version and agent count: ' + rows.local);
        assert.match(rows.r1meta, / · last contact /, 'an unreachable row says last contact: ' + rows.r1meta);
        assert.equal(rows.r1hint, 'Is Coral running on that machine? Check the URL.');
        console.log('PASS (14) Servers: Local row version + agents; unreachable row explains and says last contact');

        // ── (15) A view the user opens while the first list loads is not opened
        // again by the deep-link restore (it only restores the hash the page was
        // opened with). A hub with one remote, so the Servers tab stays available.
        fakeHub = true;
        holdNextList = true;
        // The /ws/coral feed is the test server's (not a hub), and its first full coral_update sets the server list
        // to none. Mark when that snapshot has been applied, so the held list (with a remote) lands after it.
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            const W = window.WebSocket;
            window.WebSocket = class extends W { constructor(u, p) { super(u, p);
                if (/\\/ws\\/coral/.test(String(u))) this.addEventListener('message', e => { if (/"type":"coral_update"/.test(String(e.data))) setTimeout(() => { window.__feedFull = true; }); }); } };
        })()` });
        await Page.navigate({ url: 'about:blank' }); await Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window.switchNavTab === 'function'`, 'the dashboard');
        for (let i = 0; i < 100 && !holdList; i++) await new Promise(r => setTimeout(r, 50)); // sleep-ok: polling the CDP-side hold
        assert.ok(holdList, 'the first live list request is held');
        await ev(`(() => {
            window.switchNavTab('servers');               // the user, before the list is in (#servers)
            const real = window.switchNavTab;
            window.__reopenCalls = [];
            window.switchNavTab = (...a) => { if (a[1] && a[1].fromHistory) window.__reopenCalls.push(JSON.stringify(a)); return real(...a); };
            return true;
        })()`);
        assert.equal(await ev(`location.pathname`), '/servers');
        await waitFor(`window.__feedFull === true`, "the feed's first full update applied");
        await Fetch.fulfillRequest({ requestId: holdList, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
            body: Buffer.from(JSON.stringify({ sessions: [], servers: [{ id: 'r1', label: 'Remote', status: 'online' }] })).toString('base64') });
        holdList = null; heldReleased = true;
        await coralReady('startup', 'liveList');
        await new Promise(r => setTimeout(r, 300)); // sleep-ok: a re-open would land right after the list
        assert.equal(await ev(`document.getElementById('nav-tab-servers').hidden`), false, 'still a hub with a remote: the tab stays');
        assert.deepEqual(await ev(`window.__reopenCalls`), [], 'the first list load does not re-open the view the user already chose');
        fakeHub = false; heldReleased = false;
        console.log('PASS (15) a hash set before the first list load is not restored again');

        // ── (10)(11) Phone: team-send button. (The nav strip and its edge fade are gone: the phone has a bottom tab bar, nav_tabs_browser, #3785.)
        await open(390, 844, true);
        // (#7063) the team reminder is behind Send options, as on a desktop: a caret with a name, and the item inside it says what it sends
        const team = await ev(`(() => { const b = document.querySelector('.mobile-send-options-btn'); const i = document.querySelector('.mobile-send-menu .send-menu-item'); return b && { label: b.getAttribute('aria-label'), title: b.title, icon: !!b.querySelector('.material-icons'), text: b.textContent.replace('expand_more', '').trim(), item: i && i.textContent.trim(), action: i && i.getAttribute('data-on-click') }; })()`);
        assert.ok(team, 'the Send options button exists');
        assert.equal(team.label, 'Send options');
        assert.ok(team.icon, 'an icon, not "+T"');
        assert.equal(team.text, '', 'no cryptic text on it');
        assert.equal(team.item, 'Send with team reminder');
        assert.equal(team.action, 'send-menu-team');
        console.log(`PASS (10)(11) phone: Send options holds the team reminder`);
    } finally {
        await Fetch.disable().catch(() => {});
        await client.close();
    }
})().catch(e => {
    console.error('FAIL ' + e.message);
    process.exitCode = 1;
});
