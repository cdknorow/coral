// A past session's Activity in History, searched (#4785 on #4779): the shared list bar above the session's events, over GET /api/sessions/history/{id}/events. With nothing asked for the tab shows
// the session's events as before (its own type filter beside the bar); a search (text, a type, a day range, the oldest-first order) shows the server's results in place of them: every event
// of the session, 50 at a time with "Load more", the count beside the box ("70 events", "30+ events" when the server stopped at its bound), said once to a screen reader after the person
// changes a filter (and only once the count is known, however slow the answer). A search belongs to the session on screen: another session starts clean. Real isolated server; a stand-in
// agent's session has the events (the route reads them by session id, as it does for an ended one).
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. Exit 0 / 1.
'use strict';
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const { launchStandIn } = require('./helpers/stand_in_agent');
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}
let SID = '';
// 70 edits, 30 reads, 10 prompts, 10 notifications: 120 in all, in this order (only these event types can be posted by an agent).
const EVENTS = [
    ...Array.from({ length: 70 }, (_, i) => ({ event_type: 'tool_use', tool_name: 'Edit', summary: `Edit file-${String(i).padStart(2, '0')}.go` })),
    ...Array.from({ length: 30 }, (_, i) => ({ event_type: 'tool_use', tool_name: 'Read', summary: `Read notes-${i}.md` })),
    ...Array.from({ length: 10 }, (_, i) => ({ event_type: 'prompt_submit', tool_name: '', summary: `Fix the login bug ${i}` })),
    ...Array.from({ length: 10 }, (_, i) => ({ event_type: 'notification', tool_name: '', summary: `Heads up ${i}` })),
];

async function httpJson(method, path, body, headers = {}) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json', ...headers }, body: body === undefined ? undefined : JSON.stringify(body) });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

async function run() {
    let standIn = null;
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Input, Emulation } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 600)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    const settle = (cond, label) => waitFor(cond, label, { timeout: 10000 });
    const type = async (sel, text) => { await ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); e.focus(); e.select && e.select(); return true; })()`); await Input.insertText({ text }); };
    const pick = (sel, value) => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); e.value = ${JSON.stringify(value)}; e.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
    const rows = () => ev(`[...document.querySelectorAll('#history-events-list .event-item .event-summary')].map(e => e.textContent)`);
    const count = () => ev(`(document.querySelector('#history-event-search-bar .list-bar-count') || {}).textContent`);
    const region = () => ev(`(document.querySelector('#history-event-search-bar .list-bar-live') || {}).textContent`);
    const clearAll = () => ev(`(() => { const b = document.querySelector('#history-event-search-bar .list-bar-chip-all') || document.querySelector('#history-event-search-bar .list-bar-chip'); if (b) b.click(); return true; })()`).then(() => pick('#hev-sort', 'newest'));
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'History Events Search QA' });
        if (!standIn.sid) throw new Error('no stand-in agent: ' + standIn.diag);
        SID = standIn.sid;
        for (const e of EVENTS) { const r = await httpJson('POST', `/api/sessions/live/${encodeURIComponent(standIn.name)}/events`, { ...e, session_id: SID }, { 'X-Coral-Session-Token': standIn.token }); if (r.status >= 300) throw new Error('seed ' + r.status); }
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window._coralSetLiveSessions === 'function'`, 'the app hooks', { timeout: 10000 });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        // The History session view is shown by selecting a past session; the events route reads by session id, so the view is made visible here and its events loaded as a selection does.
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            window.T = { ht: await import(st + 'history_tabs.js') };
            document.getElementById('history-session-view').style.display = 'flex';
            document.getElementById('history-tab-activity').classList.add('active');
            await T.ht.loadHistoryEvents('${SID}');
            return true; })()`);
        await settle(`!!document.querySelector('#history-event-search-bar .list-bar-search') && document.querySelectorAll('#history-events-list .event-item').length === 120`, 'the bar and the session\'s 120 events');

        console.log('The session\'s events, with nothing asked for');
        check('the bar is above the list and is its only filter button (the older tool-group menu is gone), and the events are all there', (await ev(`!document.getElementById('history-event-search-bar').hidden && document.querySelectorAll('#history-tab-activity .list-bar-filter-btn').length === 1 && !document.querySelector('.filter-dropdown-btn, #history-event-filters')`)) && (await rows()).length === 120);
        check('the count says nothing yet, and so does the status region', (await count()) === '' && (await region()) === '', JSON.stringify([await count(), await region()]));

        console.log('A text search covers every event of the session');
        await type('#hev-search', 'edit file');
        await settle(`document.querySelector('#history-event-search-bar .list-bar-count').textContent === '70 events'`, 'the count of the matches');
        let r = await rows();
        check('the results are the matches, newest first, 50 of them with a Load more', r.length === 50 && r[0] === 'Edit file-69.go' && (await ev(`!!document.querySelector('#history-events-list .event-more')`)), `${r.length} rows, first ${r[0]}`);
        await settle(`document.querySelector('#history-event-search-bar .list-bar-live').textContent === '70 events'`, 'the status region says the settled count');
        await ev(`document.querySelector('#history-events-list .event-more').click(); true`);
        await settle(`document.querySelectorAll('#history-events-list .event-item').length === 70`, 'the second page');
        r = await rows();
        check('Load more adds the rest, in order, and the button goes', r.length === 70 && r[69] === 'Edit file-00.go' && !(await ev(`!!document.querySelector('#history-events-list .event-more')`)), `${r.length} rows, last ${r[69]}`);

        console.log('A type, the order, a day range');
        await clearAll();
        await settle(`document.querySelectorAll('#history-events-list .event-item').length === 120 && !document.querySelector('#history-events-list .event-more')`, 'the whole list back');
        await pick('#hev-type', 'prompt');
        await settle(`document.querySelector('#history-event-search-bar .list-bar-count').textContent === '10 events'`, 'the prompts');
        r = await rows();
        check('Type: Prompts shows the 10 prompts', r.length === 10 && r.every(s => s.startsWith('Fix the login bug')), r.join(' | '));
        await pick('#hev-sort', 'oldest');
        await settle(`document.querySelector('#history-events-list .event-summary')?.textContent === 'Fix the login bug 0'`, 'oldest first');
        check('Oldest first reverses the results', (await rows())[9] === 'Fix the login bug 9');
        const day = d => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
        const today = day(new Date()), yesterday = day(new Date(Date.now() - 86400000));
        await ev(`document.querySelector('#hev-to').value = ${JSON.stringify(yesterday)}; document.querySelector('#hev-to').dispatchEvent(new Event('change', { bubbles: true })); true`);
        await settle(`document.querySelector('#history-events-list .event-empty')?.textContent === 'No events match'`, 'nothing until yesterday');
        check('Until yesterday leaves nothing, and says so', (await count()) === '0 events' && (await ev(`document.querySelector('#history-events-list .event-empty').textContent`)) === 'No events match', await count());
        await ev(`document.querySelector('#hev-to').value = ${JSON.stringify(today)}; document.querySelector('#hev-to').dispatchEvent(new Event('change', { bubbles: true })); true`);
        await settle(`document.querySelectorAll('#history-events-list .event-item').length === 10`, 'until today');
        check('Until today includes today (the last day picked is included)', (await count()) === '10 events', await count());

        console.log('A search the server cut says N+');
        await clearAll(); await settle(`document.querySelectorAll('#history-events-list .event-item').length === 120`, 'the list');
        await ev(`(() => { const real = window.fetch; window.__cut = real; window.fetch = async (u, o) => { const r = await real(u, o); if (/\\/events\\?/.test(String(u)) && /[?&]q=/.test(String(u))) { const d = await r.json(); d.truncated = true; return new Response(JSON.stringify(d), { status: 200, headers: { 'content-type': 'application/json' } }); } return r; }; return true; })()`);
        await type('#hev-search', 'read notes');
        await settle(`document.querySelector('#history-event-search-bar .list-bar-count').textContent === '30+ events'`, 'the cut count');
        check('the count says 30+ events', (await count()) === '30+ events', await count());
        await ev(`window.fetch = window.__cut; true`);

        console.log('The status region waits for a count, however slow the answer');
        await clearAll(); await settle(`document.querySelectorAll('#history-events-list .event-item').length === 120`, 'the list');
        await ev(`(() => { const real = window.fetch; window.__slow = real; window.fetch = async (u, o) => { if (/\\/events\\?/.test(String(u)) && /[?&]q=/.test(String(u))) await new Promise(r => setTimeout(r, 1300)); return real(u, o); }; return true; })()`); // sleep-ok: the answer must come later than the region's settle (500 ms)
        await ev(`window.__writes = 0; new MutationObserver(m => { window.__writes += m.length; }).observe(document.querySelector('#history-event-search-bar .list-bar-live'), { childList: true, characterData: true, subtree: true }); true`);
        await type('#hev-search', 'heads up');
        await settle(`document.querySelector('#history-events-list .event-empty')?.textContent === 'Searching…'`, 'the search under way');
        await ev(`new Promise(r => setTimeout(r, 800))`); // sleep-ok: longer than the region's settle, shorter than the answer: the region has been asked to speak with no count yet
        check('while the answer is awaited there is no count and the region is not written (not with an empty count)', (await count()) === '' && (await ev(`window.__writes`)) === 0, JSON.stringify([await count(), await ev(`window.__writes`)]));
        await settle(`document.querySelector('#history-event-search-bar .list-bar-live').textContent === '10 events'`, 'the region says the count once it is known');
        check('and it says it, once the answer is in', (await count()) === '10 events');
        await ev(`window.fetch = window.__slow; true`);

        console.log('The tool groups are in the same Filters menu and narrow the rows loaded, with or without a search');
        await clearAll(); await settle(`document.querySelectorAll('#history-events-list .event-item').length === 120`, 'the list');
        check('the menu has a Show box for each tool group, all ticked', (await ev(`document.querySelectorAll('#history-event-search-bar .list-bar-kept input:checked').length`)) === 15);
        await ev(`window.__evReqs = 0; const r0 = window.fetch; window.__r0 = r0; window.fetch = (u, o) => { if (/\\/events\\?/.test(String(u))) window.__evReqs++; return r0(u, o); }; true`);
        await ev(`document.getElementById('hev-g-read').click(); true`);
        await settle(`document.querySelectorAll('#history-events-list .event-item').length === 90`, 'the list without Read');
        check('Show Read unticked hides the 30 Reads, nothing is asked of the server', (await rows()).every(s => !s.startsWith('Read ')) && (await ev(`window.__evReqs`)) === 0, `${(await rows()).length} rows`);
        check('a chip names it, the Filters button counts it, and the page keeps it in its Set', (await ev(`[...document.querySelectorAll('#history-event-search-bar .list-bar-chip-text')].map(e => e.textContent).join()`)) === 'Hidden: Read' && (await ev(`document.querySelector('#history-event-search-bar .list-bar-filter-btn').textContent`)) === 'Filters (1)' && (await ev(`[...window.__coral.historyFilterHidden].join()`)) === 'read');
        await type('#hev-search', 'read notes');
        await settle(`document.querySelector('#history-event-search-bar .list-bar-count').textContent === '30 events'`, 'the 30 reads');
        check('a search with Read hidden: the server counts 30, the rows are filtered out, and it says so', (await ev(`document.querySelector('#history-events-list .event-empty')?.textContent`)) === 'All events filtered out', await count());
        await ev(`window.__evReqs = 0; document.getElementById('hev-g-read').click(); true`);
        await settle(`document.querySelectorAll('#history-events-list .event-item').length === 30`, 'the reads back');
        check('ticking Read again shows the 30 matches, with no new request (the search is not run again)', (await rows()).every(s => s.startsWith('Read notes')) && (await count()) === '30 events' && (await ev(`window.__evReqs`)) === 0, String(await ev(`window.__evReqs`)));
        await ev(`window.fetch = window.__r0; true`);
        await clearAll(); await settle(`document.querySelectorAll('#history-events-list .event-item').length === 120`, 'the list');
        await ev(`document.getElementById('hev-g-read').click(); true`);
        await settle(`document.querySelectorAll('#history-events-list .event-item').length === 90`, 'without Read again');
        await ev(`document.querySelector('#history-event-search-bar .list-bar-chip').click(); true`);
        await settle(`document.querySelectorAll('#history-events-list .event-item').length === 120`, 'the chip clears it');
        check('removing the chip shows every group again', (await ev(`window.__coral.historyFilterHidden.size`)) === 0);
        await ev(`window.__coral.historyFilterHidden.add('bash'); T.ht.loadHistoryEvents('${SID}').then(() => true)`); // opening a session clears the page's Set: the bar follows it
        await settle(`document.querySelectorAll('#history-events-list .event-item').length === 120`, 'the session opened again');
        check('the bar follows the page\'s Set (a session opened clears it): no chip, every box ticked', (await ev(`document.querySelectorAll('#history-event-search-bar .list-bar-chip').length`)) === 0 && (await ev(`document.querySelectorAll('#history-event-search-bar .list-bar-kept input:checked').length`)) === 15);

        console.log('A search belongs to the session on screen');
        await ev(`document.activeElement && document.activeElement.blur(); T.ht.loadHistoryEvents('another-session-id').then(() => true)`);
        await settle(`document.querySelector('#hev-search').value === ''`, 'the other session');
        check('another session starts with an empty bar and its own list', (await count()) === '' && (await ev(`document.querySelectorAll('#history-event-search-bar .list-bar-chip').length`)) === 0);
    } finally { if (standIn) await standIn.stop().catch(() => {}); await client.close(); }
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(failed ? `\n${failed} FAILED` : '\nALL PASS');
    process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error(e); process.exit(1); });
