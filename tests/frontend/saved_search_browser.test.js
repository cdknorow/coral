// The Saved for later panel's search, filters and order (#4778), against the real isolated server (q and the order are #4745's, kind and conv were there):
//   - a bar sits between the Open / Done tabs and the list: search, Filters (Type, From) and Sort (Newest saved, Oldest saved);
//   - a search or a filter asks the server (title and snippet), so it covers every saved message: "k of N" for the tab, chips, "No saved messages match this search.";
//   - Type (agent chat, board message) and From (a conversation, by its title) narrow it; Oldest saved reverses it; the Open and Done tabs and their totals are not changed by a search;
//   - Clear all brings the plain list back. Desktop and phone.
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. Exit 0 / 1.
'use strict';
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}
const hex = i => i.toString(16).padStart(20, '0');

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Input, Emulation } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    const exceptions = [];
    Runtime.exceptionThrown(e => exceptions.push((((e.exceptionDetails || {}).exception || {}).description || 'exception').slice(0, 200)));
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    const settle = (cond, label) => waitFor(cond, label, { timeout: 8000 });
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const snap = () => ev(`(() => { const bar = document.getElementById('saved-bar'); const rows = [...document.querySelectorAll('#saved-list > li:not(.saved-empty)')];
        return { shown: !!bar.firstChild && bar.getClientRects().length > 0, count: (bar.querySelector('.list-bar-count') || {}).textContent || '', chips: [...bar.querySelectorAll('.list-bar-chip-text')].map(e => e.textContent), titles: rows.map(r => r.textContent.replace(/\\s+/g, ' ').slice(0, 80)),
            empty: (document.querySelector('#saved-list > .saved-empty') || {}).textContent || null, tabs: [document.getElementById('saved-count-open').textContent, document.getElementById('saved-count-done').textContent], over: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
    const type = async (text) => { await ev(`document.getElementById('sv-search').focus(); true`); await Input.insertText({ text }); };
    // A filter select's options arrive with the saved list (setOptions), a moment after the rows. Picking a value it does not hold yet sets nothing, so the
    // pick waits for the option itself, not for a chip or a count (#5216: the phone run once picked before sv-a-1 existed). A failure says which it was.
    const pickOption = async (id, value) => {
        const state = () => ev(`(() => { const s = document.getElementById(${JSON.stringify(id)}); return { options: s ? [...s.options].map(o => o.value) : null, value: s && s.value, chip: document.querySelector('#saved-bar .list-bar-chip-text')?.textContent || null }; })()`);
        await settle(`(() => { const s = document.getElementById(${JSON.stringify(id)}); return !!s && [...s.options].some(o => o.value === ${JSON.stringify(value)}); })()`, `the option ${value} in ${id}`)
            .catch(async e => { throw new Error(`${e.message} | case: the option was not built | ${JSON.stringify(await state())}`); });
        await pick(id, value);
        return state;
    };
    const pick = (id, value) => ev(`(() => { const s = document.getElementById(${JSON.stringify(id)}); s.value = ${JSON.stringify(value)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);

    try {
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window._coralSetLiveSessions === 'function'`, 'the app hooks', { timeout: 10000 });
        for (const it of (await api('GET', '/api/saved')).body.items) await api('DELETE', `/api/saved/${it.id}`);
        // Six saved messages: three from two agents' chats, three from two boards; one of each kind done.
        const made = [];
        const add = async (body) => { const r = await api('POST', '/api/saved', body); if (r.status >= 300) throw new Error('save ' + JSON.stringify(r)); made.push(r.body); return r.body; };
        await add({ kind: 'agent', ref: 'sv-a-1', key: hex(1), agent_type: 'claude', title: 'Frontend Dev', snippet: 'the retry budget is per request' });
        await add({ kind: 'agent', ref: 'sv-a-1', key: hex(2), agent_type: 'claude', title: 'Frontend Dev', snippet: 'a second note about the dashboard' });
        await add({ kind: 'agent', ref: 'sv-a-2', key: hex(3), agent_type: 'claude', title: 'Backend Dev', snippet: 'the retry helper returns errBudget' });
        await add({ kind: 'board', ref: 'team-x', key: '11', title: 'team-x', snippet: 'rollout checklist, retry budget again' });
        await add({ kind: 'board', ref: 'team-x', key: '12', title: 'team-x', snippet: 'an unrelated board note' });
        await add({ kind: 'board', ref: 'team-y', key: '13', title: 'team-y', snippet: 'failover drill notes' });
        const list = (await api('GET', '/api/saved')).body.items;
        const done = list.find(i => i.ref === 'sv-a-2'); await api('PATCH', `/api/saved/${done.id}`, { done: true });
        const done2 = list.find(i => i.ref === 'team-y'); await api('PATCH', `/api/saved/${done2.id}`, { done: true });

        for (const [dev, w, h, mobile] of [['desktop', 1280, 800, false], ['phone', 390, 844, true]]) {
            console.log(dev);
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await waitFor(`typeof window._coralSetLiveSessions === 'function'`, 'the app hooks', { timeout: 10000 });
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`);
            await settle(`document.querySelectorAll('#saved-list > li:not(.saved-empty)').length === 4`, 'the four open messages').catch(async e => { throw new Error(e.message + ' | ' + JSON.stringify(await ev(`({ rows: document.querySelectorAll('#saved-list > li').length, text: document.getElementById('saved-list').textContent.slice(0, 200), status: document.getElementById('saved-status').textContent, bar: !!document.getElementById('saved-bar').firstChild })`)) + ' | ' + exceptions.join(' ; ')); });
            let s = await snap();
            check(`${dev}: the bar is between the tabs and the list; the plain list is the 4 open ones (6 saved: 4 open, 2 done)`, s.shown && s.titles.length === 4 && s.count === '4 messages' && s.chips.length === 0 && s.tabs.join() === '(4),(2)', JSON.stringify([s.shown, s.titles.length, s.count, s.tabs]));

            await type('retry');
            await settle(`document.querySelector('#saved-bar .list-bar-count').textContent === '2 of 4'`, 'the search');
            s = await snap();
            check(`${dev}: "retry" matches two of the open ones (titles and snippets), with a chip; the tabs' totals stay`, s.titles.length === 2 && s.chips.join() === 'Search: retry' && s.tabs.join() === '(4),(2)', JSON.stringify([s.titles.length, s.count, s.chips, s.tabs]));
            await ev(`document.getElementById('saved-tab-done').click(); true`);
            await settle(`document.querySelector('#saved-bar .list-bar-count').textContent === '1 of 2'`, 'the Done tab');
            s = await snap();
            check(`${dev}: the Done tab has its own match (1 of 2: the Backend Dev note)`, s.titles.length === 1 && /Backend Dev/.test(s.titles[0]), JSON.stringify(s.titles));
            await ev(`document.getElementById('saved-tab-open').click(); true`);
            await type('zzz-nothing');
            await settle(`!!document.querySelector('#saved-list > .saved-empty')`, 'the empty line');
            s = await snap();
            check(`${dev}: nothing matching says so`, s.empty === 'No saved messages match this search.' && s.shown, JSON.stringify([s.empty, s.count]));
            await ev(`document.querySelector('#saved-bar .list-bar-clear-q').click(); true`);
            await settle(`document.querySelectorAll('#saved-list > li:not(.saved-empty)').length === 4`, 'the plain list again');

            await pick('sv-kind', 'board');
            await settle(`document.querySelector('#saved-bar .list-bar-chip-text')?.textContent === 'Type: Board message' && document.querySelector('#saved-bar .list-bar-count').textContent === '2 of 4'`, 'the board filter');
            s = await snap();
            check(`${dev}: Type: board message leaves the two open board messages`, s.titles.length === 2 && s.titles.every(t => /team-x/.test(t)) && s.chips.join() === 'Type: Board message', JSON.stringify([s.titles, s.chips]));
            await pick('sv-kind', '');
            const convState = await pickOption('sv-conv', 'sv-a-1');
            await settle(`document.querySelector('#saved-bar .list-bar-chip-text')?.textContent === 'From: Frontend Dev' && document.querySelector('#saved-bar .list-bar-count').textContent === '2 of 4'`, 'From a conversation')
                .catch(async e => { throw new Error(`${e.message} | case: the option existed but the pick had no effect | ${JSON.stringify(await convState())}`); });
            s = await snap();
            check(`${dev}: From a conversation (by its title) leaves that chat's two, and the chip reads the title`, s.titles.length === 2 && s.titles.every(t => /Frontend Dev/.test(t)) && s.chips.join() === 'From: Frontend Dev', JSON.stringify([s.titles, s.chips]));
            await ev(`document.querySelector('#saved-bar .list-bar-filter-btn').click(); document.querySelector('#saved-bar .list-bar-clear-all').click(); true`);
            await settle(`document.querySelector('#saved-bar .list-bar-count').textContent === '4 messages'`, 'Clear all');

            const order = () => ev(`[...document.querySelectorAll('#saved-list > li:not(.saved-empty)')].map(r => r.querySelector('.snip').textContent.replace(/\\s+/g, ' ').slice(0, 40))`); // the snippets: a row's Gone sentence is said once for the list (#7122), so the whole text differs by row
            const newest = await order();
            await pick('sv-sort', 'oldest');
            await settle(`(() => { const r = document.querySelector('#saved-list > li:not(.saved-empty)'); return !!r && !/rollout checklist|unrelated/.test(r.textContent); })()`, 'oldest first');
            const oldest = await order();
            check(`${dev}: Oldest saved reverses the order`, JSON.stringify(oldest) === JSON.stringify([...newest].reverse()), JSON.stringify([newest[0], oldest[0]]));
            await pick('sv-sort', 'newest');
            s = await snap();
            check(`${dev}: nothing scrolls sideways`, s.over <= 0, String(s.over));
        }
        check('no page exceptions', exceptions.length === 0, exceptions.join(' | '));
    } catch (e) {
        check('test ran', false, e.stack || String(e));
    } finally {
        await require('./helpers/saved_cleanup').clearSaved(api).catch(() => {}); // the bookmarks this suite saved are taken off again whichever way it ended (#4916, #5083)
        await client.close();
    }
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
}

run().catch(e => { console.error(e); process.exit(1); });
