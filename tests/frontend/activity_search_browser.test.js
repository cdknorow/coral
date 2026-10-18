// The Activity panel's search (#4785): the shared list bar above the agent's events, over the events search of #4745. With nothing asked for the panel is the live feed, as before; a search
// (text, a type, a day range, the oldest-first order) shows the server's results in place of the feed: every event of the agent, 50 at a time with "Load more", the count beside the box
// ("70 events", "70+ events" when the server stopped at its bound) and said once to a screen reader (the hidden status region) after the person changes a filter, never again for a feed that
// moves under them. A search belongs to the agent on screen: another agent starts clean. Real isolated server; the events are made here over the API.
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
const OTHER = 'other-' + Date.now().toString(36);
let AGENT = '', SID = ''; // the stand-in agent's name and session id (only an agent's own token may post its events)
// 70 edits, 30 reads, 10 prompts, 10 notifications: 120 in all, in this order (so the newest is the last notification; only these event types can be posted).
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
    const settle = (cond, label) => waitFor(cond, label, { timeout: 10000, diag: `({ bar: !!document.querySelector('#event-search-bar .list-bar-search'), hostHidden: (document.getElementById('event-search-bar') || {}).hidden, rows: document.querySelectorAll('#agentic-state-events .event-item').length, events: (window.T && T.state.currentAgentEvents || []).length, sess: window.T && T.state.currentSession && T.state.currentSession.name, empty: (document.querySelector('#agentic-state-events') || {}).textContent })` });
    const type = async (sel, text) => { await ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); e.focus(); e.select && e.select(); return true; })()`); await Input.insertText({ text }); };
    const pick = (sel, value) => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); e.value = ${JSON.stringify(value)}; e.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
    const rows = () => ev(`[...document.querySelectorAll('#agentic-state-events .event-item .event-summary')].map(e => e.textContent)`);
    const count = () => ev(`(document.querySelector('#event-search-bar .list-bar-count') || {}).textContent`);
    const live = () => ev(`(document.querySelector('#event-search-bar .list-bar-live') || {}).textContent`);
    const clearAll = () => ev(`(() => { const b = document.querySelector('#event-search-bar .list-bar-chip-all') || document.querySelector('#event-search-bar .list-bar-chip'); if (b) b.click(); return true; })()`).then(() => pick('#ev-sort', 'newest')); // the sort is kept by Clear all: back to the feed's order
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Activity Search QA' });
        if (!standIn.sid) throw new Error('no stand-in agent: ' + standIn.diag);
        SID = standIn.sid;
        // The events are stored under the session's own name (the agent's working folder), which is the live list's name for it, not the launch's session name.
        const liveList = await (await fetch(BASE + '/api/sessions/live')).json().catch(() => []);
        AGENT = ((Array.isArray(liveList) ? liveList : liveList.sessions || []).find(x => x.session_id === SID) || {}).name || standIn.name;
        for (const e of EVENTS) { const r = await httpJson('POST', `/api/sessions/live/${encodeURIComponent(AGENT)}/events`, { ...e, session_id: SID }, { 'X-Coral-Session-Token': standIn.token }); if (r.status >= 300) throw new Error('seed ' + r.status); }
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window._coralSetLiveSessions === 'function'`, 'the app hooks', { timeout: 10000 });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const T = window.T = { state: (await import(st + 'state.js')).state, as: await import(st + 'agentic_state.js') };
            T.state.currentSession = { name: '${AGENT}', type: 'live', session_id: '${SID}', agent_type: 'claude' };
            T.as.switchAgenticTab('activity', 'top');
            document.getElementById('agentic-state').classList.remove('collapsed');
            await T.as.loadAgentEvents('${AGENT}', '${SID}');
            return true; })()`);
        await settle(`!!document.querySelector('#event-search-bar .list-bar-search') && document.querySelectorAll('#agentic-state-events .event-item').length === 50`, 'the bar and the live feed (the newest 50)');

        console.log('The live feed, with nothing asked for');
        check('the bar is above the feed, and it is the only filter button (the older tool-group menu is gone)', (await ev(`!document.getElementById('event-search-bar').hidden && document.querySelectorAll('#agentic-panel-activity .list-bar-filter-btn').length === 1 && !document.querySelector('.filter-dropdown-btn, #event-filters')`)));
        check('the feed shows the newest 50 events, as before', (await rows()).length === 50 && (await rows())[0].startsWith('Heads up 9'), (await rows())[0]);
        check('the count says nothing yet, and so does the status region', (await count()) === '' && (await live()) === '', JSON.stringify([await count(), await live()]));

        console.log('A text search covers every event of the agent');
        await type('#ev-search', 'edit file');
        await settle(`document.querySelector('#event-search-bar .list-bar-count').textContent === '70 events'`, 'the count of the matches');
        let r = await rows();
        check('the results are the matches, newest first, 50 of them with a Load more', r.length === 50 && r[0] === 'Edit file-69.go' && (await ev(`!!document.querySelector('#agentic-state-events .event-more')`)), `${r.length} rows, first ${r[0]}`);
        await settle(`document.querySelector('#event-search-bar .list-bar-live').textContent === '70 events'`, 'the status region says the settled count');
        check('a screen reader is told the count once, after the change', (await live()) === '70 events', await live());
        await ev(`document.querySelector('#agentic-state-events .event-more').click(); true`);
        await settle(`document.querySelectorAll('#agentic-state-events .event-item').length === 70`, 'the second page');
        r = await rows();
        check('Load more adds the rest, in order, and the button goes', r.length === 70 && r[69] === 'Edit file-00.go' && !(await ev(`!!document.querySelector('#agentic-state-events .event-more')`)), `${r.length} rows, last ${r[69]}`);

        console.log('The feed moving under a search does not touch it, nor is it announced again');
        const before = await rows();
        await ev(`window.__liveWrites = 0; new MutationObserver(m => { window.__liveWrites += m.length; }).observe(document.querySelector('#event-search-bar .list-bar-live'), { childList: true, characterData: true, subtree: true }); true`);
        await ev(`T.state.currentAgentEvents = [{ id: 99999, event_type: 'tool_use', tool_name: 'Bash', summary: 'a new live event', created_at: new Date().toISOString() }, ...T.state.currentAgentEvents]; T.as.renderEventTimeline(); true`);
        await ev(`T.as.loadAgentEvents('${AGENT}', '${SID}')`);
        const after = await rows();
        check('the results are the same rows after the feed redraws', JSON.stringify(after) === JSON.stringify(before) && !after.includes('a new live event'), `${after.length} rows`);
        check('the status region was not written again', (await ev(`window.__liveWrites`)) === 0, String(await ev(`window.__liveWrites`)));

        console.log('A type, a day range and the order');
        await clearAll();
        await settle(`document.querySelectorAll('#agentic-state-events .event-item').length === 50 && !document.querySelector('#agentic-state-events .event-more')`, 'the live feed back');
        await pick('#ev-type', 'prompt');
        await settle(`document.querySelector('#event-search-bar .list-bar-count').textContent === '10 events'`, 'the prompts');
        r = await rows();
        check('Type: Prompts shows the 10 prompts', r.length === 10 && r.every(s => s.startsWith('Fix the login bug')), r.join(' | '));
        check('and a chip names it', (await ev(`[...document.querySelectorAll('#event-search-bar .list-bar-chip-text')].map(e => e.textContent).join()`)) === 'Type: Prompts');
        await pick('#ev-type', 'permission');
        await settle(`document.querySelector('#agentic-state-events .event-empty')?.textContent === 'No events match'`, 'no stop events');
        check('a type with no events (no permission request was made) says so, and the count is 0 events', (await ev(`document.querySelector('#agentic-state-events .event-empty').textContent`)) === 'No events match' && (await count()) === '0 events', await count());
        await pick('#ev-type', 'prompt');
        await pick('#ev-sort', 'oldest');
        await settle(`document.querySelector('#agentic-state-events .event-summary')?.textContent === 'Fix the login bug 0'`, 'oldest first');
        check('Oldest first reverses the results', (await rows())[9] === 'Fix the login bug 9');
        const day = d => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
        const today = day(new Date()), yesterday = day(new Date(Date.now() - 86400000));
        await ev(`document.querySelector('#ev-to').value = ${JSON.stringify(yesterday)}; document.querySelector('#ev-to').dispatchEvent(new Event('change', { bubbles: true })); true`);
        await settle(`document.querySelector('#agentic-state-events .event-empty')?.textContent === 'No events match'`, 'nothing until yesterday');
        check('Until yesterday leaves nothing (the events are from today)', (await count()) === '0 events', await count());
        await ev(`document.querySelector('#ev-to').value = ${JSON.stringify(today)}; document.querySelector('#ev-to').dispatchEvent(new Event('change', { bubbles: true })); true`);
        await settle(`document.querySelectorAll('#agentic-state-events .event-item').length === 10`, 'until today');
        check('Until today includes today (the last day picked is included)', (await count()) === '10 events', await count());
        await ev(`document.querySelector('#ev-from').value = ${JSON.stringify(today)}; document.querySelector('#ev-from').dispatchEvent(new Event('change', { bubbles: true })); true`);
        await settle(`document.querySelectorAll('#agentic-state-events .event-item').length === 10`, 'from today');
        check('From today until today keeps them', (await count()) === '10 events');

        console.log('A search the server cut says N+');
        await clearAll(); await settle(`document.querySelectorAll('#agentic-state-events .event-item').length === 50`, 'the feed');
        await ev(`(() => { const real = window.fetch; window.__cut = real; window.fetch = async (u, o) => { const r = await real(u, o); if (/\\/events\\?/.test(String(u)) && /[?&]q=/.test(String(u))) { const d = await r.json(); d.truncated = true; return new Response(JSON.stringify(d), { status: 200, headers: { 'content-type': 'application/json' } }); } return r; }; return true; })()`);
        await type('#ev-search', 'read notes');
        await settle(`document.querySelector('#event-search-bar .list-bar-count').textContent === '30+ events'`, 'the cut count');
        check('the count says 30+ events', (await count()) === '30+ events', await count());
        await ev(`window.fetch = window.__cut; true`);

        console.log('The tool groups are in the same Filters menu and narrow the rows loaded, with or without a search');
        await clearAll(); await settle(`document.querySelectorAll('#agentic-state-events .event-item').length === 50`, 'the feed');
        check('the menu has a Show box for each tool group, all ticked', (await ev(`document.querySelectorAll('#event-search-bar .list-bar-kept input:checked').length`)) === 15);
        await ev(`document.querySelector('#event-search-bar .list-bar-filter-btn').click(); true`);
        const lay = await ev(`(() => { const r = id => document.getElementById(id).closest('label').getBoundingClientRect(), t = r('ev-type'), a = r('ev-g-read'), b = r('ev-g-write'), c = r('ev-g-edit'); return { typeFull: t.width > a.width * 1.8, pair: Math.abs(a.top - b.top) < 2 && b.left > a.left, next: c.top > a.top + 10, half: a.width > 40 && a.width < t.width * 0.6 }; })()`);
        check('the Show boxes go two to a line (the other fields a line each)', lay.typeFull && lay.pair && lay.next && lay.half, JSON.stringify(lay));
        await ev(`document.querySelector('#event-search-bar .list-bar-filter-btn').click(); true`);
        await ev(`window.__evReqs = 0; const r0 = window.fetch; window.__r0 = r0; window.fetch = (u, o) => { if (/\\/events\\?/.test(String(u))) window.__evReqs++; return r0(u, o); }; true`);
        await ev(`document.getElementById('ev-g-read').click(); true`);
        await settle(`document.querySelectorAll('#agentic-state-events .event-item').length === 20`, 'the feed without Read');
        check('Show Read unticked hides the 30 Reads of the 50 loaded, nothing is asked of the server', (await rows()).every(s => !s.startsWith('Read ')) && (await ev(`window.__evReqs`)) === 0, `${(await rows()).length} rows, ${await ev(`window.__evReqs`)} requests`);
        check('a chip names it and the Filters button counts it', (await ev(`[...document.querySelectorAll('#event-search-bar .list-bar-chip-text')].map(e => e.textContent).join()`)) === 'Hidden: Read' && (await ev(`document.querySelector('#event-search-bar .list-bar-filter-btn').textContent`)) === 'Filters (1)');
        check('the page keeps the hidden group in its Set', (await ev(`[...T.state.eventFiltersHidden].join()`)) === 'read');
        await type('#ev-search', 'read notes');
        await settle(`document.querySelector('#event-search-bar .list-bar-count').textContent === '30 events'`, 'the 30 reads');
        check('a search with Read hidden: the server counts 30, the rows are filtered out, and it says so', (await ev(`document.querySelector('#agentic-state-events .event-empty')?.textContent`)) === 'All events filtered out', await count());
        await ev(`window.__evReqs = 0; document.getElementById('ev-g-read').click(); true`);
        await settle(`document.querySelectorAll('#agentic-state-events .event-item').length === 30`, 'the reads back');
        check('ticking Read again shows the 30 matches, with no new request for it (the search is not run again)', (await rows()).every(s => s.startsWith('Read notes')) && (await count()) === '30 events' && (await ev(`window.__evReqs`)) === 0, String(await ev(`window.__evReqs`)));
        await ev(`window.fetch = window.__r0; true`);
        await clearAll(); await settle(`document.querySelectorAll('#agentic-state-events .event-item').length === 50`, 'the feed');
        await ev(`document.getElementById('ev-g-read').click(); true`);
        await settle(`document.querySelectorAll('#agentic-state-events .event-item').length === 20`, 'the feed without Read again');
        await ev(`document.querySelector('#event-search-bar .list-bar-chip').click(); true`);
        await settle(`document.querySelectorAll('#agentic-state-events .event-item').length === 50`, 'the chip clears it');
        check('removing the chip shows every group again', (await ev(`T.state.eventFiltersHidden.size`)) === 0);
        await ev(`T.state.eventFiltersHidden.add('bash'); T.as.renderEventTimeline(); true`); // the page's own Set changed from outside the bar
        check('the bar follows the page\'s Set: a chip and an unticked box', (await ev(`[...document.querySelectorAll('#event-search-bar .list-bar-chip-text')].map(e => e.textContent).join()`)) === 'Hidden: Bash' && (await ev(`document.getElementById('ev-g-bash').checked`)) === false);
        await ev(`T.state.eventFiltersHidden.clear(); T.as.renderEventTimeline(); true`);

        console.log('A search belongs to the agent on screen');
        await ev(`document.activeElement && document.activeElement.blur(); true`); // choosing another agent is a click elsewhere: the box is not being typed in (a field being typed in is left as typed)
        await ev(`T.state.currentSession = { name: '${OTHER}', type: 'live', session_id: 'sid-${OTHER}', agent_type: 'claude' }; T.state.currentAgentEvents = []; T.as.renderEventTimeline(); true`);
        await settle(`document.querySelector('#ev-search').value === ''`, 'the other agent');
        check('another agent starts with an empty bar and its own feed', (await ev(`document.querySelector('#ev-search').value`)) === '' && (await count()) === '' && (await ev(`document.querySelectorAll('#event-search-bar .list-bar-chip').length`)) === 0);
        await ev(`T.state.currentSession = { name: '${AGENT}', type: 'live', session_id: '${SID}', agent_type: 'claude' }; T.as.renderEventTimeline(); true`);
        check('and going back does not bring the old search back', (await ev(`document.querySelector('#ev-search').value`)) === '');

        console.log('A search with an error');
        await ev(`(() => { const real = window.fetch; window.__real2 = real; window.fetch = async (u, o) => (/\\/events\\?/.test(String(u)) ? new Response(JSON.stringify({ error: 'q can\\'t contain a secret' }), { status: 400, headers: { 'content-type': 'application/json' } }) : real(u, o)); return true; })()`);
        await type('#ev-search', 'anything');
        await settle(`/secret/.test(document.querySelector('#agentic-state-events .event-empty')?.textContent || '')`, 'the error');
        check('the server\'s refusal is shown in the list, not swallowed', true, await ev(`document.querySelector('#agentic-state-events .event-empty').textContent`));
        await ev(`window.fetch = window.__real2; true`);
    } finally { if (standIn) await standIn.stop().catch(() => {}); await client.close(); }
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(failed ? `\n${failed} FAILED` : '\nALL PASS');
    process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error(e); process.exit(1); });
