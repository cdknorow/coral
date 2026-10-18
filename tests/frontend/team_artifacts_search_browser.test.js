// The Team Artifacts panel's search box, filters, sort and Load more (#4573), all asked of the server (a search covers every artifact, not just the loaded page): the search is debounced and
// sent as q=, with a clear (x) and the count ("1 of 14"); the filter menu (type, origin, uploaded by, kept, dates) and its chips and Clear all; the sort; Load more with the cursor and the
// keyboard focus kept; the last search and filters remembered per board; a phone (the search on its own row, 44 px targets, 200% text fits); the keyboard; and a registered server's board through the
// hub's proxy. The route is Backend's #4572 (q, kind, origin, by, kept, since, until, sort, cursor; the reply adds total and next_cursor), stood in for by a page-side stub that applies the
// contract to 14 artifacts in pages of 5, and records every request.
//
// Run against an isolated dev server. CORAL_SCREENSHOT_DIR=<dir> saves screenshots.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

const HEX = n => n.toString(16).padStart(2, '0').repeat(32);
const TEAM = 'ta-search-' + Date.now().toString(36);
// 14 artifacts: ids 1..14, newest first by id; kinds, origins, names and one file kept.
const DATA = (() => {
    const kinds = [['image/png', 'png'], ['application/pdf', 'pdf'], ['text/plain', 'txt'], ['application/zip', 'zip']];
    const names = ['alpha', 'bravo', 'charlie', 'delta', 'echo', 'foxtrot', 'golf', 'hotel', 'india', 'juliet', 'kilo', 'lima', 'zebra-plan', 'mike'];
    return names.map((n, i) => {
        const [mt, ext] = kinds[i % 4];
        const source = i % 5 === 3 ? 'received' : i % 5 === 4 ? 'shared' : i % 7 === 6 ? 'unlisted' : 'task';
        return { id: HEX(i + 1), name: `${n}.${ext}`, media_type: mt, size: (i + 1) * 100 + (i % 3) * 7, task_id: 100 + i, task_title: i === 11 ? 'Quarterly numbers' : 'Task ' + (i + 1),
            created_at: new Date(Date.UTC(2026, 9, 1 + (13 - i), 10, 0, 0)).toISOString(), uri: 'coral://artifacts/' + HEX(i + 1), available: true, inline: false, reference_count: 1,
            source: source === 'unlisted' ? 'task' : source, provenance: source === 'unlisted' ? 'unverified' : undefined, produced_by: i % 2 ? 'Dev One' : 'Dev Two', sent_by: source === 'received' || source === 'shared' ? 'Dev One' : undefined,
            kept: i === 5 };
    });
})();

// The stand-in route: GET /api/board/<team>/artifacts (and through /api/remote/box-b/...), pages of 5, every request recorded in window.__req.
const STUB = `(() => {
    const R = window.fetch;
    window.__req = [];
    window.__data = ${JSON.stringify(DATA)};
    const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
    window.fetch = (u, o = {}) => {
        const url = new URL(String(u), location.origin);
        const m = url.pathname.match(/^(?:\\/api\\/remote\\/([^/]+))?\\/api\\/board\\/([^/]+)\\/artifacts$/);
        if (m && (o.method || 'GET').toUpperCase() === 'GET') {
            const q = url.searchParams;
            window.__req.push({ remote: m[1] || null, project: decodeURIComponent(m[2]), query: Object.fromEntries(q.entries()) });
            const queried = ['q', 'kind', 'origin', 'by', 'kept', 'since', 'until', 'sort', 'cursor'].some(k => q.has(k));
            let items = window.__data.slice();
            if (q.get('q')) { const t = q.get('q').toLowerCase(); items = items.filter(i => [i.name, i.task_title, i.produced_by, i.sent_by, i.media_type].some(x => (x || '').toLowerCase().includes(t))); }
            if (q.get('kind')) { const k = q.get('kind'); items = items.filter(i => (i.media_type.startsWith('image/') ? 'image' : i.media_type === 'application/pdf' ? 'pdf' : i.media_type.startsWith('text/') ? 'text' : 'other') === k); }
            if (q.get('origin')) { const o2 = q.get('origin'); items = items.filter(i => (i.provenance === 'unverified' ? 'unlisted' : i.source) === o2); }
            if (q.get('by')) { const b = q.get('by').toLowerCase(); items = items.filter(i => (i.produced_by || i.sent_by || '').toLowerCase() === b); }
            if (q.get('kept')) items = items.filter(i => i.kept === (q.get('kept') === 'true'));
            if (q.get('since')) items = items.filter(i => i.created_at >= new Date(q.get('since')).toISOString());
            if (q.get('until')) items = items.filter(i => i.created_at < new Date(q.get('until')).toISOString());
            const sort = q.get('sort') || 'newest';
            items.sort((a, b) => sort === 'oldest' ? a.created_at.localeCompare(b.created_at) : sort === 'name' ? a.name.localeCompare(b.name) : sort === 'size' ? b.size - a.size : b.created_at.localeCompare(a.created_at));
            const start = q.has('cursor') ? Number(atob(q.get('cursor'))) : Number(q.get('offset') || 0);
            const page = items.slice(start, start + 5);
            const more = start + 5 < items.length;
            const body = { project: decodeURIComponent(m[2]), artifacts: page.map(({ kept, ...rest }) => rest), has_more: more, truncated: false };
            if (queried) { body.total = items.length; body.all = window.__data.length; if (more) body.next_cursor = btoa(String(start + 5)); }
            return json(body);
        }
        if (url.pathname === '/api/system/artifact-store/kept') return json({ kept: [HEX(6)] });
        if (url.pathname.startsWith('/api/') && !url.pathname.startsWith('/api/saved')) return json({});
        return R(u, o);
    };
    return true;
})()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a debounce is measured in real time; there is no completion event for "nothing was sent"
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await sleep(350); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }; // sleep-ok: tab fades
    const press = async (key, code, vk) => { const b = { key, code, windowsVirtualKeyCode: vk }; await Input.dispatchKeyEvent({ type: 'keyDown', ...b, ...(key === 'Enter' ? { text: '\r' } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...b }); };
    const type = async text => { for (const ch of text) { await Input.dispatchKeyEvent({ type: 'keyDown', key: ch, text: ch }); await Input.dispatchKeyEvent({ type: 'keyUp', key: ch }); } };
    const names = () => ev(`[...document.querySelectorAll('tr.team-artifact-row .team-artifact-name')].map(e => e.textContent)`);
    const reqs = () => ev(`window.__req.length`);
    const last = () => ev(`window.__req[window.__req.length - 1]`);
    const settle = async n => waitFor(`window.__req.length >= ${n} && document.getElementById('agentic-panel-team-artifacts-view').getAttribute('aria-busy') === 'false'`, `request ${n} answered`, 8000);
    // The page also holds the agent list's bar (agent_list_bar.js, #4744), built from the same classes: the Team Artifacts bar is looked for inside its panel.
    const count = () => ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-count').textContent`);
    const open = async (width = 1200, height = 900) => {
        await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(STUB);
    };
    const showTeam = async (session = {}) => {
        await ev(`(async () => { const {state} = await import((window.__coralStatic||'/static/')+'state.js'); const files = await import((window.__coralStatic||'/static/')+'changed_files.js');
            state.currentSession = Object.assign({ type: 'live', name: 'ta-ui', session_id: 'ta-a', board_project: ${JSON.stringify(TEAM)} }, ${JSON.stringify(session)});
            const {showView} = await import((window.__coralStatic||'/static/')+'utils.js'); showView('live-session-view');
            const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px'; files.initFileSearch(); files.syncFilesViewerSession(); return true; })()`);
        await ev(`localStorage.removeItem('coral-agentic-tab-top'); document.getElementById('agentic-tab-team-artifacts').click(); true`);
        await waitFor(`!!document.querySelector('#agentic-panel-team-artifacts .list-bar-search') && document.querySelectorAll('tr.team-artifact-row').length > 0`, 'the list and its search bar', 8000);
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await open();
        await ev(`localStorage.clear()`);
        await showTeam();

        console.log('1. The first load asks for the newest first with the server\'s totals, shows a page of 5 and Load more');
        let r = await last();
        assert.deepEqual(r.query.sort, 'newest'); assert.ok(!r.query.q && !r.query.cursor && r.query.offset === '0', JSON.stringify(r.query));
        assert.equal((await names()).length, 5); assert.equal(await count(), '14 artifacts');
        assert.equal(await ev(`!!document.querySelector('.team-artifacts-more')`), true);
        await shot('ta-search-list');

        console.log('2. Search is the server\'s (it finds a file that is not on the loaded page) and debounced (typing sends one request after the pause)');
        const before = await reqs();
        await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-search').focus(); true`);
        await type('zeb');
        await sleep(100); // sleep-ok: inside the debounce
        assert.equal(await reqs(), before, 'nothing is sent while the person is still typing');
        await type('ra');
        await settle(before + 1);
        await sleep(400); // sleep-ok: well past the debounce: only one request may have been made for the five keys
        assert.equal(await reqs(), before + 1, 'five keystrokes, one request');
        r = await last(); assert.equal(r.query.q, 'zebra');
        assert.deepEqual(await names(), ['zebra-plan.png'.replace('.png', '.' + DATA[12].name.split('.')[1])]);
        assert.equal(await count(), '1 of 14');
        assert.equal(await ev(`document.activeElement === document.querySelector('#agentic-panel-team-artifacts .list-bar-search')`), true, 'the focus stays in the search box while the list is rebuilt');
        assert.equal(await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-search').value`), 'zebra');
        await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-clear-q').click(); true`);
        await settle(before + 2);
        assert.equal((await last()).query.q, undefined); assert.equal((await names()).length, 5); assert.equal(await count(), '14 artifacts');
        assert.equal(await ev(`document.activeElement === document.querySelector('#agentic-panel-team-artifacts .list-bar-search')`), true, 'clearing returns to the box');

        console.log('3. The filters: type, origin, uploaded by, kept, dates; each is a request parameter and a chip; a chip removes its filter; Clear all removes the rest');
        const menu = async () => { if (await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-menu').hidden`)) await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-filter-btn').click(); true`); };
        const pickSel = async (id, v) => { await menu(); const n = await reqs(); await ev(`(() => { const s = document.getElementById('${id}'); s.value = '${v}'; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); await settle(n + 1); };
        await pickSel('ta-kind', 'image');
        r = await last(); assert.equal(r.query.kind, 'image');
        assert.ok((await names()).every(n => n.endsWith('.png')), JSON.stringify(await names())); assert.equal(await count(), '4 of 14');
        await pickSel('ta-origin', 'received'); r = await last(); assert.equal(r.query.origin, 'received');
        await pickSel('ta-kind', ''); r = await last(); assert.equal(r.query.kind, undefined);
        assert.deepEqual(await ev(`[...document.querySelectorAll('#agentic-panel-team-artifacts .list-bar-chips .list-bar-chip-text')].map(e => e.textContent)`), ['Origin: Received']);
        await pickSel('ta-origin', '');
        // uploaded by, typed (exact name, debounced like the search)
        await menu(); let n = await reqs();
        await ev(`document.getElementById('ta-by').focus(); true`); await type('Dev One'); await settle(n + 1);
        r = await last(); assert.equal(r.query.by, 'Dev One'); assert.ok((await names()).length > 0);
        // kept
        n = await reqs(); await ev(`document.getElementById('ta-kept').click(); true`); await settle(n + 1);
        r = await last(); assert.equal(r.query.kept, 'true'); assert.deepEqual(await names(), [DATA[5].name]);
        // dates: a local day, as RFC 3339 instants (until is the next local midnight)
        n = await reqs();
        await ev(`(() => { for (const [id, v] of [['ta-from', '2026-10-05'], ['ta-to', '2026-10-09']]) { const e = document.getElementById(id); e.value = v; e.dispatchEvent(new Event('change', { bubbles: true })); } return true; })()`);
        await settle(n + 2);
        r = await last();
        assert.equal(r.query.since, new Date(2026, 9, 5).toISOString()); assert.equal(r.query.until, new Date(2026, 9, 10).toISOString());
        const chipTexts = await ev(`[...document.querySelectorAll('#agentic-panel-team-artifacts .list-bar-chips .list-bar-chip-text')].map(e => e.textContent)`);
        assert.deepEqual(chipTexts, ['By: Dev One', 'Kept', '2026-10-05 to 2026-10-09'], JSON.stringify(chipTexts));
        assert.equal(await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-filter-btn').textContent`), 'Filters (3)');
        await shot('ta-search-chips');
        // a chip removes its filter
        n = await reqs(); await ev(`[...document.querySelectorAll('#agentic-panel-team-artifacts .list-bar-chip')].find(c => c.textContent.startsWith('Kept')).click(); true`); await settle(n + 1);
        assert.equal((await last()).query.kept, undefined); assert.equal(await ev(`document.getElementById('ta-kept').checked`), false);
        n = await reqs(); await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-chip-all').click(); true`); await settle(n + 1);
        r = await last(); assert.deepEqual(Object.keys(r.query).sort(), ['limit', 'offset', 'sort']); assert.equal((await names()).length, 5);
        assert.equal(await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-chips').hidden`), true);

        console.log('4. Sort: newest, oldest, name, size');
        const sortTo = async v => { const k = await reqs(); await ev(`(() => { const s = document.getElementById('ta-sort'); s.value = '${v}'; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); await settle(k + 1); };
        await sortTo('oldest'); assert.equal((await last()).query.sort, 'oldest'); assert.equal((await names())[0], DATA[13].name);
        await sortTo('name'); assert.deepEqual((await names()).slice(0, 2), [DATA[0].name, DATA[1].name]);
        await sortTo('size'); assert.equal((await names())[0], DATA[13].name, 'largest first');
        await sortTo('newest'); assert.equal((await names())[0], DATA[0].name);

        console.log('5. Load more: the next page by the cursor with the same filters and sort; the focus stays on it, and moves to the first new row when the last page is in');
        await pickSel('ta-kind', 'image'); // 4 images: one page; use no filter for paging
        await pickSel('ta-kind', '');
        n = await reqs();
        await ev(`document.querySelector('.team-artifacts-more').focus(); document.querySelector('.team-artifacts-more').click(); true`);
        await settle(n + 1);
        r = await last(); assert.ok(r.query.cursor && !r.query.offset, 'by the cursor: ' + JSON.stringify(r.query)); assert.equal(r.query.sort, 'newest');
        assert.equal((await names()).length, 10);
        assert.equal(await ev(`document.activeElement === document.querySelector('.team-artifacts-more')`), true, 'the focus stays on Load more while more pages remain');
        n = await reqs(); await ev(`document.querySelector('.team-artifacts-more').click(); true`); await settle(n + 1);
        assert.equal((await names()).length, 14); assert.equal(await ev(`!!document.querySelector('.team-artifacts-more')`), false);
        assert.equal(await ev(`(() => { const a = document.activeElement; const row = a.closest('tr.team-artifact-row'); return row ? [...row.parentElement.children].indexOf(row) : -1; })()`), 10, 'the last page: the focus is on the first new row');

        console.log('6. The last search and filters are remembered per board (and the sort), and another board starts empty');
        n = await reqs(); await ev(`document.getElementById('ta-sort').value = 'name'; document.getElementById('ta-sort').dispatchEvent(new Event('change', { bubbles: true })); true`); await settle(n + 1);
        n = await reqs(); await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-search').focus(); true`); await type('golf'); await settle(n + 1);
        const stored = await ev(`localStorage.getItem(${JSON.stringify('coral-team-artifacts-filters:' + TEAM)})`);
        assert.deepEqual(JSON.parse(stored), { q: 'golf', kind: '', origin: '', by: '', kept: false, from: '', to: '', sort: 'name' });
        await open(); await showTeam();
        assert.equal(await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-search').value`), 'golf'); r = await last(); assert.equal(r.query.q, 'golf'); assert.equal(r.query.sort, 'name');
        assert.equal(await ev(`document.getElementById('ta-sort').value`), 'name');
        await showTeam({ board_project: 'another-board' });
        await waitFor(`window.__req[window.__req.length - 1].project === 'another-board'`, 'the other board', 6000);
        assert.equal(await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-search').value`), ''); assert.equal((await last()).query.q, undefined);
        // storage that throws does not break the panel
        await ev(`(() => { const s = Storage.prototype.setItem; Storage.prototype.setItem = function () { throw new Error('quota'); }; window.__setItem = s; return true; })()`);
        n = await reqs(); await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-search').focus(); true`); await type('x'); await settle(n + 1);
        assert.equal((await last()).query.q, 'x'); await ev(`Storage.prototype.setItem = window.__setItem; true`);

        console.log('7. The keyboard: Enter in the box searches at once; the Filters button opens the menu (focus inside), Escape closes it and returns to the button; a chip is a button');
        await ev(`localStorage.clear()`); await open(); await showTeam();
        n = await reqs();
        await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-search').focus(); true`); await type('lima'); await press('Enter', 'Enter', 13);
        await settle(n + 1); assert.equal((await last()).query.q, 'lima');
        await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-filter-btn').focus(); true`); await press('Enter', 'Enter', 13);
        assert.equal(await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-filter-btn').getAttribute('aria-expanded')`), 'true');
        assert.equal(await ev(`document.activeElement.id`), 'ta-kind', 'the focus goes into the menu');
        await press('Escape', 'Escape', 27);
        assert.equal(await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-filter-btn').getAttribute('aria-expanded')`), 'false');
        assert.equal(await ev(`document.activeElement === document.querySelector('#agentic-panel-team-artifacts .list-bar-filter-btn')`), true);
        n = await reqs(); await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-chip').focus(); true`); await press('Enter', 'Enter', 13); await settle(n + 1);
        assert.equal((await last()).query.q, undefined, 'a chip removes its filter from the keyboard');

        console.log('8. A 390 px phone, touch: the search on its own row, the menu fits, every control is 44 px, nothing scrolls sideways; and at 200% text');
        for (const big of [false, true]) {
            await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
            await Emulation.setTouchEmulationEnabled({ enabled: true });
            await ev(`localStorage.clear()`); await open(390, 844); await Emulation.setTouchEmulationEnabled({ enabled: true });
            await showTeam();
            await ev(`window.toggleAgenticPanel(true); document.getElementById('agentic-state').style.width = ''; ${big ? "document.documentElement.style.fontSize = '32px';" : ''} true`);
            await sleep(400); // sleep-ok: layout settles after the viewport change
            await ev(`document.querySelector('#agentic-panel-team-artifacts .list-bar-filter-btn').click(); true`);
            await ev(`document.getElementById('ta-by').value = 'Dev One'; document.querySelector('#agentic-panel-team-artifacts .list-bar-search').value = 'lima'; true`);
            const m = await ev(`(() => { const R = e => e.getBoundingClientRect(); const dock = R(document.getElementById('agentic-state')); const search = R(document.querySelector('#agentic-panel-team-artifacts .list-bar-search')), btn = R(document.querySelector('#agentic-panel-team-artifacts .list-bar-filter-btn'));
                const small = [...document.querySelectorAll('#agentic-panel-team-artifacts .list-bar-search, .list-bar-filter-btn, .list-bar-select, .list-bar-by, .list-bar-kept, .list-bar-field input[type=date]')].filter(e => e.getClientRects().length && R(e).height < 43.5).map(e => e.id || e.className);
                const out = [...document.querySelectorAll('#agentic-panel-team-artifacts .list-bar-bar *')].filter(e => e.getClientRects().length && (R(e).right > dock.right + 1 || R(e).left < dock.left - 1)).map(e => e.id || e.className).slice(0, 4);
                return { searchAbove: search.bottom <= btn.top + 1, searchW: Math.round(search.width), dock: Math.round(dock.width), small, out, page: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
            assert.ok(m.searchAbove, `the search is on its own row: ${JSON.stringify(m)}`);
            assert.deepEqual(m.small, [], `every control is a 44 px target (${big ? '200%' : '100%'}): ${JSON.stringify(m)}`);
            assert.deepEqual(m.out, [], `nothing is cut off at the sides (${big ? '200%' : '100%'}): ${JSON.stringify(m)}`);
            assert.ok(m.page <= 0, `no sideways scroll: ${JSON.stringify(m)}`);
            await shot(big ? 'ta-search-phone-200' : 'ta-search-phone');
        }
        await Emulation.setTouchEmulationEnabled({ enabled: false });

        console.log('9. In local hub mode a registered server\'s board is asked through the proxy (and the containment check applies)');
        await ev(`localStorage.clear()`); await open(); await ev(`(async () => { const { state } = await import((window.__coralStatic||'/static/')+'state.js'); state.hub = true; state.servers = [{ id: 'box-b', label: 'Box B', status: 'online' }]; return true; })()`);
        await showTeam({ server: 'box-b', board_project: 'remote-team' });
        await waitFor(`window.__req.some(x => x.remote === 'box-b' && x.project === 'remote-team')`, 'the request through the proxy', 8000);
        assert.equal(await ev(`window.__req.some(x => x.remote === null && x.project === 'remote-team')`), false, 'nothing went to this server');
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
