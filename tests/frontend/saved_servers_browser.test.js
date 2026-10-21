// Save for Later across servers in hub mode (#4551): on a hub with a registered server ("Box B", through the hub's proxy) a message of its agents and boards is saved to THAT
// server's /api/saved; the Saved tab merges this server's list with every online registered server's (newest saved first), each item naming its server; a server that does not
// answer is one line, not an error; a jump opens the remote agent or board; Done, Reopen and Remove reach the right server. This server is the isolated test server; the registered
// one is a stand-in for /api/remote/box-b/api/saved* (the real route through a real hub is Go's TestHubE2E_SavedMessagesOnARegisteredServer), kept in the page with its own ids.
//
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const hex = i => i.toString(16).padStart(20, '0');

// The registered server: its own saved store (ids from 100, so they never equal this server's), reachable or not.
const REMOTE = `(() => {
    const prev = window.fetch;
    const store = { items: [], next: 100, up: true, calls: [], boardGone: new Set() };
    window.__box = store;
    const json = (b, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } });
    window.fetch = async (url, opts = {}) => {
        const u = new URL(String(url), location.origin);
        const m = u.pathname.match(/^\\/api\\/remote\\/box-b(\\/.*)$/);
        if (!m) return prev(url, opts);
        const path = m[1], method = (opts.method || 'GET').toUpperCase();
        store.calls.push(method + ' ' + path + u.search);
        if (!store.up) return json({ error: 'remote unreachable', server: 'box-b' }, 502);
        if (path === '/api/saved' && method === 'GET') {
            const q = u.searchParams, lim = q.get('limit');
            const open = store.items.filter(i => !i.done_at).length, done = store.items.length - open;
            if (lim === '0') return json({ open, done, items: [] });
            let items = store.items.slice();
            if (q.get('kind')) items = items.filter(i => i.kind === q.get('kind'));
            if (q.get('conv')) items = items.filter(i => i.ref === q.get('conv'));
            items.sort((a, b) => b.saved_at.localeCompare(a.saved_at));
            return json({ open, done, items: items.map(i => ({ ...i, reason: i.kind === 'board' && store.boardGone.has(i.key) ? 'gone' : '' })) }); // the server marks the gone ones itself (#5802)
        }
        if (path === '/api/saved' && method === 'POST') {
            const b = JSON.parse(opts.body);
            let it = store.items.find(i => i.kind === b.kind && i.ref === b.ref && i.key === b.key);
            if (!it) { it = { id: store.next++, ...b, saved_at: new Date().toISOString(), done_at: null }; store.items.push(it); }
            return json(it);
        }
        const one = path.match(/^\\/api\\/saved\\/(\\d+)$/);
        if (one && method === 'PATCH') { const it = store.items.find(i => i.id === Number(one[1])); if (!it) return json({ error: 'gone' }, 404); it.done_at = JSON.parse(opts.body).done ? new Date().toISOString() : null; return json(it); }
        if (one && method === 'DELETE') { store.items = store.items.filter(i => i.id !== Number(one[1])); return json({ ok: true }); }
        const bm = path.match(/^\\/api\\/board\\/([^/]+)\\/messages\\/all$/);
        if (bm && u.searchParams.has('id')) return store.boardGone.has(u.searchParams.get('id')) ? json({ error: 'not found' }, 404) : json({ messages: [] });
        return prev(url, opts);
    };
})()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install, select } = await connect();
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const C = `document.getElementById('live-history-messages')`;
    // This server answers the chat's keys too (the harness's mock does not): a key from the message's absolute index, and ?find_key= answered from it.
    const MOCK = `(() => {
        const prev = window.fetch;
        window.fetch = async (url, opts = {}) => {
            const u = String(url);
            const m = u.match(/\\/api\\/sessions\\/live\\/([^/?]+)\\/chat\\?(.*)$/);
            if (!m) return prev(url, opts);
            const p = new URLSearchParams(m[2]);
            const total = window.__m.byPathSeg(m[1]).msgs.length;
            if (p.has('find_key')) {
                const k = p.get('find_key'); const idx = /^[0-9a-f]{20}$/.test(k) ? parseInt(k, 16) : -1;
                return new Response(JSON.stringify({ found: idx >= 0 && idx < total, index: Math.max(idx, 0), total }), { status: 200, headers: { 'content-type': 'application/json' } });
            }
            const resp = await prev(url, opts);
            const body = await resp.clone().json().catch(() => null);
            if (!body || !body.messages) return resp;
            const start = Number.isInteger(body.start) ? body.start : (Number(p.get('after') || 0));
            body.messages = body.messages.map((x, i) => ({ ...x, key: (start + i).toString(16).padStart(20, '0') }));
            return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } });
        };
    })()`;
    const shot = async (name) => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
        await sleep(350); // sleep-ok: the sidebar's tab strip fades; a picture mid-fade shows two tabs lit
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };
    const items = () => ev(`[...document.querySelectorAll('#saved-list .saved-item')].map(li => ({ id: li.dataset.id, src: li.querySelector('.src').textContent, server: (li.querySelector('.server') || {}).textContent || null, kind: li.querySelector('.kind').textContent, hasOpen: !!li.querySelector('.saved-open') }))`);
    const openSaved = async () => {
        if (await ev(`document.getElementById('nav-tab-hub-inbox').classList.contains('active')`)) { await ev(`window.switchNavTab('agents'); true`); await waitFor(`document.querySelector('.top-nav-tab.active').dataset.arg === 'agents'`, 'the page left', 4000); }
        await ev(`(() => { const b = document.getElementById('saved-btn'); if (b.getAttribute('aria-pressed') === 'true') b.click(); b.click(); })(); true`); // the page is left first when it is the one showing (the bookmark would close it)
        await waitFor(`document.getElementById('saved-ui').getClientRects().length > 0 && document.querySelectorAll('#saved-list .saved-item, #saved-list .saved-note, #saved-list .saved-empty').length > 0`, 'the Saved list', 8000, `({ nav: document.querySelector('.top-nav-tab.active') && document.querySelector('.top-nav-tab.active').dataset.arg, uiIn: document.getElementById('saved-ui').parentElement.id, view: getComputedStyle(document.getElementById('saved-view')).display, rects: document.getElementById('saved-ui').getClientRects().length, items: document.querySelectorAll('#saved-list > li').length, scope: document.body.getAttribute('data-saved-scope'), heading: document.getElementById('saved-scope').textContent })`);
    };
    const refresh = async () => { await ev(`import((window.__coralStatic || '/static/') + 'saved.js').then(m => (document.getElementById('saved-view').contains(document.getElementById('saved-ui')) ? m.showSavedView() : m.showSaved())).then(() => true)`); await sleep(300); }; // sleep-ok: item checks draw their chips as each answer arrives (the refresh reads the page on home, the sidebar's tab for an agent)

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat')`);
        await ev(MOCK);
        await ev(REMOTE);
        for (const it of ((await api('GET', '/api/saved')).body.items || [])) await api('DELETE', `/api/saved/${it.id}`);
        // A hub with one registered server, online; an agent on each.
        await ev(`(() => { T.state.hub = true; T.state.servers = [{ id: 'box-b', label: 'Box B', status: 'online' }];
            window.__m.add('lc', 'L', 30, { sleeping: false }); window.__m.add('rb', 'R', 30, { sleeping: false, server: 'box-b' }); T.render.renderLiveSessions(T.state.liveSessions); })()`);

        console.log('1. A message of an agent on the registered server is saved to that server (through the proxy), and nowhere else');
        await select('rb', 'box-b');
        await waitFor(`${C}.querySelectorAll('[data-msg-key]').length > 5`, 'the remote agent\'s messages', 8000);
        const remoteBtn = `${C}.querySelector('[data-msg-key="${hex(20)}"] .chat-save-btn')`;
        assert.equal(await ev(`!!${remoteBtn} && !${remoteBtn}.hidden`), true, 'a Save button on a remote agent\'s message');
        await ev(`${remoteBtn}.click(); true`);
        await waitFor(`${remoteBtn}.getAttribute('aria-pressed') === 'true'`, 'saved', 6000);
        const box = await ev(`window.__box.items.map(i => ({ kind: i.kind, ref: i.ref, key: i.key }))`);
        assert.deepEqual(box, [{ kind: 'agent', ref: 'rb-id', key: hex(20) }], 'the registered server has it');
        assert.equal((await api('GET', '/api/saved')).body.items.length, 0, 'this server does not');

        console.log('2. A message of this server\'s own agent goes to this server');
        await select('lc');
        await waitFor(`${C}.querySelectorAll('[data-msg-key]').length > 5`, 'the local agent\'s messages', 8000);
        await ev(`${C}.querySelector('[data-msg-key="${hex(22)}"] .chat-save-btn').click(); true`);
        await waitFor(`${C}.querySelector('[data-msg-key="${hex(22)}"] .chat-save-btn').getAttribute('aria-pressed') === 'true'`, 'saved', 6000);
        assert.equal((await api('GET', '/api/saved')).body.items.length, 1);
        assert.equal(await ev(`window.__box.items.length`), 1, 'the registered server still has only its own');

        console.log('3. The Saved tab merges both, each item naming its server; the count adds them');
        await ev(`window.__box.items.push({ id: 101, kind: 'board', ref: 'box-board', key: '7', agent_type: '', title: 'box-board', snippet: 'a board message on Box B', said_at: '', saved_at: new Date(Date.now() + 5000).toISOString(), done_at: null }); true`);
        await ev(`window.selectLiveSession('', '', '', 'local'); true`); // home: the list is of the agent or team on screen (#4792), and the merge across servers is home's
        await openSaved();
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 3`, 'three items', 8000);
        const merged = await items();
        assert.deepEqual(merged.map(i => i.server).sort(), ['Box B', 'Box B', 'This computer']);
        assert.equal(merged[0].src, 'box-board', 'newest saved first: the board item saved last');
        assert.equal(await ev(`document.querySelector('#saved-btn .saved-count').textContent`), '3', 'the top bar counts both servers');
        assert.equal(await ev(`document.getElementById('saved-count-open').textContent`), '(3)');
        await shot('saved-servers-merged');

        console.log('4. A server that does not answer is one line, not an error; this server\'s items stay');
        await ev(`window.__box.up = false; true`);
        await refresh();
        await waitFor(`document.querySelectorAll('#saved-list .saved-note').length === 1`, 'the one-line notice', 6000);
        const note = await ev(`document.querySelector('#saved-list .saved-note').textContent`);
        assert.ok(note.includes('Box B') && /did not answer/.test(note), note);
        assert.equal(await ev(`document.querySelectorAll('#saved-list .saved-item').length`), 1, 'only this server\'s item');
        assert.equal(await ev(`document.querySelector('.toast.error, .toast-error') ? 1 : 0`), 0, 'no error toast');
        await shot('saved-servers-down');
        await ev(`window.__box.up = true; true`);
        await refresh();
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 3 && !document.querySelector('#saved-list .saved-note')`, 'back to three', 6000);

        console.log('5. Done, Reopen and Remove go to the right server');
        const boxItem = () => ev(`[...document.querySelectorAll('#saved-list .saved-item')].find(li => li.dataset.id === 'box-b:101')`);
        assert.ok(await boxItem(), 'the board item of Box B is listed by its own id');
        await ev(`document.querySelector('#saved-list [data-id="box-b:101"] .saved-done').click(); true`);
        await waitFor(`window.__box.items.find(i => i.id === 101).done_at !== null`, 'Done reached Box B', 6000);
        assert.equal((await api('GET', '/api/saved')).body.items.filter(i => i.done_at).length, 0, 'nothing of this server\'s was changed');
        await ev(`document.getElementById('saved-tab-done').click(); true`);
        await waitFor(`!!document.querySelector('#saved-list [data-id="box-b:101"]')`, 'it in the Done list', 6000);
        await ev(`document.querySelector('#saved-list [data-id="box-b:101"] .saved-done').click(); true`);
        await waitFor(`window.__box.items.find(i => i.id === 101).done_at === null`, 'Reopen reached Box B', 6000);
        await ev(`document.getElementById('saved-tab-open').click(); true`);
        await waitFor(`!!document.querySelector('#saved-list [data-id="box-b:101"] .saved-remove')`, 'it back in Open', 6000);
        await ev(`document.querySelector('#saved-list [data-id="box-b:101"] .saved-remove').click(); true`);
        await waitFor(`!window.__box.items.some(i => i.id === 101)`, 'Remove reached Box B', 6000);
        assert.equal((await api('GET', '/api/saved')).body.items.length, 1, 'this server\'s item is still there');

        console.log('6. A jump opens the remote agent at the message, and a remote board by its server key');
        await ev(`window.selectLiveSession('', '', '', 'local'); true`); // away from the remote agent, home: the merge across servers is home's (#4792)
        await openSaved();
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 2`, 'two items', 8000);
        await ev(`document.querySelector('#saved-list [data-id^="box-b:"] .saved-open').click(); true`);
        await waitFor(`T.state.currentSession && T.state.currentSession.session_id === 'rb-id' && !!${C}.querySelector('.chat-search-target')`, 'the remote agent open at the message', 12000);
        assert.equal(await ev(`T.state.currentSession.server`), 'box-b');
        assert.equal(await ev(`${C}.querySelector('.chat-search-target').dataset.msgKey`), hex(20));
        // A remote board's item opens that board under its server's key (a spy: the board's own view is not what is checked here).
        await ev(`window.__box.items.push({ id: 102, kind: 'board', ref: 'box-board', key: '9', agent_type: '', title: 'box-board', snippet: 'another', said_at: '', saved_at: new Date().toISOString(), done_at: null }); window.__opened = null; window.selectBoardProject = (k, id) => { window.__opened = [k, id]; }; true`);
        await openSaved();
        await refresh();
        await waitFor(`!!document.querySelector('#saved-list [data-id="box-b:102"] .saved-open')`, 'the board item', 6000);
        await ev(`document.querySelector('#saved-list [data-id="box-b:102"] .saved-open').click(); true`);
        await waitFor(`window.__opened !== null`, 'the board opened', 6000);
        assert.deepEqual(await ev(`window.__opened`), ['@box-b/box-board', 9]);

        console.log('6b. A registered server is another machine: a bookmark with a hostile or malformed id is never listed and never becomes part of a request');
        await ev(`window.__fetched = []; (() => { const prev = window.fetch; window.fetch = (u, o) => { window.__fetched.push(String(u)); return prev(u, o); }; })();
            for (const id of ['../../../../servers/x', '%2e%2e/%2e%2e/%2e%2e/%2e%2e/servers/x', -5, 1.5, 0, '12', null]) window.__box.items.push({ id, kind: 'board', ref: 'evil-board', key: '5', agent_type: '', title: 'evil', snippet: 'hostile ' + String(id), said_at: '', saved_at: new Date().toISOString(), done_at: null });
            window.__box.items.push({ id: 110, kind: 'board', ref: 'fine-board', key: '5', agent_type: '', title: 'fine', snippet: 'a usable item', said_at: '', saved_at: new Date().toISOString(), done_at: null }); true`);
        await refresh();
        await waitFor(`!!document.querySelector('#saved-list [data-id="box-b:110"]')`, 'the usable item', 6000);
        const listed = await ev(`[...document.querySelectorAll('#saved-list .saved-item')].map(li => li.querySelector('.snip').textContent)`);
        assert.ok(!listed.some(t => t.startsWith('hostile')), 'no hostile item is listed: ' + JSON.stringify(listed));
        await ev(`document.querySelector('#saved-list [data-id="box-b:110"] .saved-done').click(); true`);
        await waitFor(`window.__box.items.find(i => i.id === 110).done_at !== null`, 'Done on the usable item', 6000);
        const sent = await ev(`window.__fetched.filter(u => /\\.\\.|%2e|%252e|\\/servers\\//i.test(u))`);
        assert.deepEqual(sent, [], 'no request carried a hostile id');
        await ev(`window.__box.items = window.__box.items.filter(i => i.id === 102 || i.id === 103 || i.id === 110); true`);
        // In a narrow panel the server name wraps under the title instead of being cut (and its full name is the tooltip).
        await ev(`window.selectLiveSession('', '', '', 'local'); true`); // home: the server names are of the merged list (#4792)
        await ev(`document.getElementById('agentic-state').style.width = '300px'; true`);
        await refresh();
        await waitFor(`!!document.querySelector('#saved-list .server')`, 'a server label');
        const narrow = await ev(`(() => { const li = document.querySelector('#saved-list [data-id^="box-b:"]'); const s = li.querySelector('.server'), t = li.querySelector('.src'); const r = li.getBoundingClientRect();
            return { title: s.title, text: s.textContent, under: s.getBoundingClientRect().top >= t.getBoundingClientRect().bottom - 1, inside: s.getBoundingClientRect().right <= r.right + 1 && t.getBoundingClientRect().right <= r.right + 1, clipped: s.scrollWidth > s.clientWidth + 1 }; })()`);
        assert.deepEqual({ title: narrow.title === narrow.text && !!narrow.text, under: narrow.under, inside: narrow.inside, clipped: narrow.clipped }, { title: true, under: true, inside: true, clipped: false }, JSON.stringify(narrow));
        await shot('saved-servers-narrow');
        await ev(`document.getElementById('agentic-state').style.width = ''; true`);

        console.log('6c. A phone at 200% text: the wrapped server name does not touch the kind chip');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await ev(`document.documentElement.style.fontSize = '32px'; document.getElementById('agentic-state').style.width = ''; true`);
        await sleep(400); // sleep-ok: the layout settles over frames after a viewport change; there is no completion event
        await ev(`window.toggleAgenticPanel(true); window.switchAgenticTab('saved', 'top'); true`);
        await refresh();
        await waitFor(`document.querySelectorAll('#saved-list .saved-item .server').length >= 2`, 'items with server names', 6000);
        const gap = await ev(`[...document.querySelectorAll('#saved-list .saved-item')].map(li => { const k = li.querySelector('.kind').getBoundingClientRect(), s = li.querySelector('.server').getBoundingClientRect();
            const overlap = !(s.bottom <= k.top || s.top >= k.bottom || s.right <= k.left || s.left >= k.right); return { overlap, gap: Math.round(Math.max(s.top - k.bottom, k.top - s.bottom)), wrapped: s.top >= k.bottom - 1 }; })`);
        assert.ok(gap.length >= 2 && gap.every(g => !g.overlap), 'the server name overlaps the kind chip: ' + JSON.stringify(gap));
        await shot('saved-servers-phone-200');
        await ev(`document.documentElement.style.fontSize = ''; window.toggleAgenticPanel(false); true`);
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await sleep(300); // sleep-ok: as above

        console.log('7. A deleted message on a remote board is Gone; an ended agent on a remote server is kept and says it cannot be opened from here');
        await ev(`window.__box.boardGone.add('9'); window.__box.items.push({ id: 103, kind: 'agent', ref: 'ended-b', key: '${hex(3)}', agent_type: 'claude', title: 'old agent', snippet: 'from an agent that ended', said_at: '', saved_at: new Date().toISOString(), done_at: null }); true`);
        await openSaved();
        await refresh();
        await waitFor(`!!document.querySelector('#saved-list [data-id="box-b:102"].gone') && !!document.querySelector('#saved-list [data-id="box-b:103"] .why:not([hidden])')`, 'Gone and the cannot-open line', 8000);
        const ended = await ev(`(() => { const li = document.querySelector('#saved-list [data-id="box-b:103"]'); return { open: !!li.querySelector('.saved-open'), remove: !!li.querySelector('.saved-remove'), snippet: li.querySelector('.snip').textContent, why: li.querySelector('.why').textContent }; })()`);
        assert.deepEqual({ open: ended.open, remove: ended.remove, snippet: ended.snippet }, { open: false, remove: true, snippet: 'from an agent that ended' });
        assert.ok(/Box B/.test(ended.why) && /cannot be opened/.test(ended.why), ended.why);
        console.log('   ✓');
    } finally {
        await require('./helpers/saved_cleanup').clearSaved(api).catch(() => {}); // the bookmarks this suite saved are taken off again (#4916)
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
