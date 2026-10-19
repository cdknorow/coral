// The Inbox says aloud who has just started waiting (#4926): a polite live region (#inbox-live) in the dashboard's own Inbox gets "<agent> needs you: <why>" once for each agent that was not waiting at
// the last look. A redraw or refresh of the same list says nothing; agents already waiting when the page loads or the Inbox opens are in the list, not announced; an agent that stops and waits again is
// announced again; and nothing is said while the Inbox is not on screen (the toast and the tab's badge carry it there). Real isolated server, three mock agents. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const settle = (cond, label) => waitFor(cond, label, 8000);
    const S = `(window.__coralStatic || '/static/')`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => {
            window.__m.add('QA Alpha', 'A', 4, { sleeping: false, board_project: 'team-a', session_id: 'qa-a-id' });
            window.__m.add('FE Alpha', 'B', 4, { sleeping: false, board_project: 'team-a', session_id: 'fe-a-id' });
            window.__m.add('QA Beta', 'C', 4, { sleeping: false, board_project: 'team-b', session_id: 'qa-b-id' });
            return true; })()`);
        await settle(`(window.__qaState && window.__qaState.liveSessions || []).length === 3`, 'three agents in the live list');
        // the list as the feed holds it, with the agents named in `waiting` waiting for permission; then the tab and its Needs you list are told, as after each feed message
        const setWaiting = waiting => ev(`(async () => { const w = ${JSON.stringify(waiting)};
            window.__qaState.liveSessions = window.__qaState.liveSessions.map(s => ({ ...s, waiting_for_input: w.includes(s.session_id), waiting_kind: w.includes(s.session_id) ? 'permission' : '' }));
            (await import(${S} + 'inbox_tab.js')).updateInboxTab(); return true; })()`);
        const said = () => ev(`window.__said.slice()`);
        const nav = tab => ev(`window.switchNavTab(${JSON.stringify(tab)}); true`);

        // some agents wait before the Inbox is ever opened, and the Inbox is opened with them waiting
        await setWaiting(['qa-b-id']);
        await nav('hub-inbox');
        await settle(`document.querySelector('.top-nav-tab.active')?.dataset.arg === 'hub-inbox' && !document.getElementById('inbox-local').hidden && document.querySelectorAll('#inbox-needs-list .inbox-needs-item').length === 1`, 'the Inbox with one agent waiting');
        await ev(`window.__said = []; new MutationObserver(() => { const t = document.getElementById('inbox-live').textContent; if (t) window.__said.push(t); }).observe(document.getElementById('inbox-live'), { childList: true }); true`);
        check('the live region is a polite status region, there before it speaks', await ev(`(() => { const l = document.getElementById('inbox-live'); return l.getAttribute('role') === 'status' && l.getAttribute('aria-live') === 'polite'; })()`));
        check('an agent already waiting when the Inbox opens is listed but not announced', (await said()).length === 0, JSON.stringify(await said()));

        // a redraw of the same list says nothing
        for (let i = 0; i < 3; i++) await setWaiting(['qa-b-id']);
        check('refreshing the same list three times says nothing', (await said()).length === 0, JSON.stringify(await said()));

        // a new agent starts waiting: said once, for that agent only
        await setWaiting(['qa-b-id', 'qa-a-id']);
        await settle(`window.__said.length === 1`, 'the announcement');
        for (let i = 0; i < 3; i++) await setWaiting(['qa-b-id', 'qa-a-id']);
        let s = await said();
        check('a newly waiting agent is announced once, by name and why, and not again on later refreshes', s.length === 1 && s[0] === 'QA Alpha needs you: Needs input · Permission' || (s.length === 1 && /^QA Alpha needs you: /.test(s[0])), JSON.stringify(s));
        check('the announcement does not repeat the agent that was already waiting', s.length === 1 && !/QA Beta/.test(s[0]), JSON.stringify(s));

        // it stops waiting and waits again: said again
        await setWaiting(['qa-b-id']);
        check('an agent leaving the list says nothing', (await said()).length === 1, JSON.stringify(await said()));
        await setWaiting(['qa-b-id', 'qa-a-id']);
        await settle(`window.__said.length === 2`, 'the second announcement');
        s = await said();
        check('the same agent waiting again is announced again (same words, a new announcement)', s.length === 2 && s[1] === s[0], JSON.stringify(s));

        // two at once: one announcement naming both
        await setWaiting(['qa-b-id', 'qa-a-id', 'fe-a-id']);
        await settle(`window.__said.length === 3`, 'the third announcement');
        s = await said();
        check('one new agent among those waiting is announced alone', s.length === 3 && /^FE Alpha needs you: /.test(s[2]) && !/QA/.test(s[2]), JSON.stringify(s));

        // the Inbox is not on screen: quiet, and the agents that waited meanwhile are not announced when it is opened
        await nav('agents');
        await settle(`document.querySelector('.top-nav-tab.active')?.dataset.arg === 'agents'`, 'back on Agents');
        await setWaiting(['qa-b-id', 'qa-a-id', 'fe-a-id']);
        await setWaiting(['qa-a-id', 'fe-a-id']);
        check('nothing is said while the Inbox is not shown (a toast and the badge carry it there)', (await said()).length === 3, JSON.stringify(await said()));
        await setWaiting(['qa-a-id', 'fe-a-id', 'qa-b-id']);
        await nav('hub-inbox');
        await settle(`document.querySelectorAll('#inbox-needs-list .inbox-needs-item').length === 3`, 'the Inbox with three waiting');
        check('opening the Inbox lists them all and announces none of them', (await said()).length === 3, JSON.stringify(await said()));
        await setWaiting(['qa-a-id', 'fe-a-id']);
        await setWaiting(['qa-a-id', 'fe-a-id', 'qa-b-id']);
        await settle(`window.__said.length === 4`, 'an agent that started waiting while the Inbox is shown');
        check('and one that starts waiting while it is shown is announced', /^QA Beta needs you: /.test((await said())[3]), JSON.stringify(await said()));
        console.log('PASS Inbox announces newly waiting agents');
    } catch (e) {
        console.error(e && e.stack || e); results.push(false);
    } finally { try { await client.close(); } catch (_) {} }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
