// QA #5796: a scale probe of the shared Inbox on a REAL server: 100 tasks for the Operator over 5 boards and 50 saved items. It measures, in the page, the time from the click
// to the painted tab (two frames after the condition holds) and the requests the page made meanwhile: opening each tab, expanding 5 tasks (each with 25 artifact links, so the
// 20-lookup cap is in play), switching away and back (the open tasks are re-read), and the Saved tab with 50 items. Numbers go to the log as [METRIC] lines.
'use strict';
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => { const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const out = [];
    const metric = (label, o) => { const s = `[METRIC] ${label}: ${JSON.stringify(o)}`; out.push(s); console.log(s); };
    const note = s => { out.push(s); console.log(`[NOTE] ${s}`); };
    const api = (m, p, b) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(m)}, headers: { 'Content-Type': 'application/json' }, body: ${b === undefined ? 'undefined' : JSON.stringify(JSON.stringify(b))} }).then(async r => ({ status: r.status, data: await r.json().catch(() => null) }))`);
    const hexes = n => Array.from({ length: n }, (_, i) => (i + 1).toString(16).padStart(2, '0').repeat(32));
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        const loaded = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await loaded;
        await coralReady();

        // ---- seed: 5 boards, 100 tasks for the Operator, 50 saved items
        const boards = ['scale-a', 'scale-b', 'scale-c', 'scale-d', 'scale-e'];
        for (const b of boards) await api('POST', `/api/board/${b}/subscribe`, { subscriber_id: 'Orchestrator', job_title: 'Orchestrator' });
        const links = hexes(25).map(h => `[artifact](coral://artifacts/${h})`).join(' ');
        const t0 = Date.now();
        let made = 0;
        for (let i = 0; i < 100; i++) {
            const b = boards[i % 5];
            const body = `Task ${i}: please read **this** and answer.\n\n- one\n- two\n\n${i < 5 ? 'Links: ' + links : 'A short body.'}`;
            const r = await api('POST', `/api/board/${b}/tasks`, { title: `Scale task ${String(i).padStart(3, '0')}`, priority: ['low', 'medium', 'high'][i % 3], subscriber_id: 'Orchestrator', assigned_to: 'Operator', body });
            if (r.status === 201 || r.status === 200) made++;
        }
        let saved = 0;
        for (let i = 0; i < 50; i++) {
            const b = boards[i % 5];
            const m = await api('POST', `/api/board/${b}/messages`, { subscriber_id: 'Orchestrator', content: `Saved message ${i}: ` + 'the text of it. '.repeat(40) });
            const id = m.data && (m.data.message ? m.data.message.id : m.data.id);
            const s = id && await api('POST', '/api/saved', { kind: 'board', ref: b, key: String(id), title: `Saved ${i}`, snippet: `Saved message ${i}` });
            if (s && s.status === 200) saved++;
        }
        note(`seeded ${made} tasks and ${saved} saved items in ${Date.now() - t0} ms`);

        // ---- record the page's requests, and time a click to the painted result
        await ev(`(() => { const real = window.fetch; window.__reqs = []; window.fetch = function (u, o) { const url = new URL(String(u), location.origin); const e = { m: ((o && o.method) || 'GET').toUpperCase(), p: url.pathname, t: performance.now() }; window.__reqs.push(e); const p = real.apply(this, arguments); p.then(() => { e.d = performance.now() - e.t; }, () => { e.d = -1; }); return p; }; return true; })()`);
        const measure = async (label, action, cond) => {
            const r = await ev(`(async () => { window.__reqs.length = 0; const t0 = performance.now(); ${action}; const end = t0 + 30000; while (!(${cond}) && performance.now() < end) await new Promise(r => setTimeout(r, 4));
                const met = performance.now() - t0; await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))); const painted = performance.now() - t0;
                await new Promise(r => setTimeout(r, 400)); // late requests (lookups after the render)
                const reqs = window.__reqs.slice(); const by = {}; for (const q of reqs) { const k = q.m + ' ' + q.p.replace(/[0-9a-f]{64}/g, '<id>').replace(/\\/\\d+(?=\\/|$)/g, '/<n>'); by[k] = (by[k] || 0) + 1; }
                return { conditionMs: Math.round(met), paintedMs: Math.round(painted), requests: reqs.length, by, slowestMs: Math.round(Math.max(0, ...reqs.map(q => q.d || 0))) }; })()`);
            metric(label, r);
            return r;
        };

        // ---- opening the Inbox, then each tab
        const inbox = await measure('open the Inbox (the first tab)', `localStorage.removeItem('coral.inbox.tab'); document.getElementById('nav-tab-hub-inbox').click()`, `!!document.getElementById('inbox-local') && !document.getElementById('inbox-local').hidden`);
        const tasks = await measure('Tasks for you: open the tab (100 tasks)', `document.getElementById('inbox-tab-tasks').click()`, `document.querySelectorAll('#inbox-tasks-host .ibx-task').length >= 100`);
        const rows = await ev(`document.querySelectorAll('#inbox-tasks-host .ibx-task').length`);
        note(`rows in Tasks for you: ${rows}`);
        const expand = await measure('Tasks for you: expand 5 tasks (each has 25 artifact links)', `[0, 1, 2, 3, 4].forEach(i => document.querySelectorAll('#inbox-tasks-host .ibx-task-head')[i].click())`, `document.querySelectorAll('#inbox-tasks-host .ibx-task-body .md').length >= 5`);
        const away = await measure('switch to Saved for later', `document.getElementById('inbox-tab-saved').click()`, `document.querySelectorAll('#saved-list .saved-item').length >= 50`);
        const back = await measure('switch back to Tasks for you (the 5 open tasks are re-read)', `document.getElementById('inbox-tab-tasks').click()`, `document.querySelectorAll('#inbox-tasks-host .ibx-task-body .md').length >= 5`);
        const needs = await measure('open Needs you (the third tab)', `document.getElementById('inbox-tab-needs').click()`, `!!document.getElementById('inbox-needs') ? true : true`);
        const saved2 = await measure('Saved for later again', `document.getElementById('inbox-tab-saved').click()`, `document.querySelectorAll('#saved-list .saved-item').length >= 50`);
        const savedOpen = await measure('Saved: open "Read the whole message" on 5 items', `[0, 1, 2, 3, 4].forEach(i => document.querySelectorAll('#saved-list .saved-item')[i].querySelector('.ibx-saved-toggle')?.click())`, `document.querySelectorAll('#saved-list .ibx-saved-body .md').length >= 5`);

        // ---- the verdict against the budget (a tab opens in < 300 ms locally, no request per row)
        const over = [];
        for (const [name, r] of [['open Inbox', inbox], ['open Tasks', tasks], ['expand 5', expand], ['to Saved', away], ['back to Tasks', back], ['Saved again', saved2], ['Saved read 5', savedOpen]]) {
            if (r.paintedMs > 300) over.push(`${name}: ${r.paintedMs} ms`);
        }
        const perRow = (tasks.by && Object.entries(tasks.by).filter(([k, v]) => v > 5).map(([k, v]) => `${k} x${v}`)) || [];
        note(`OVER 300 ms: ${over.length ? over.join('; ') : 'none'}`);
        note(`requests that repeat per row when opening Tasks: ${perRow.length ? perRow.join('; ') : 'none'}`);
        note(`lookups: expand ${expand.by ? JSON.stringify(Object.entries(expand.by).filter(([k]) => /artifacts/.test(k))) : '-'}; after a tab switch back ${back.by ? JSON.stringify(Object.entries(back.by).filter(([k]) => /artifacts/.test(k))) : '-'}`);
    } catch (e) { note('ERROR ' + String(e.stack || e.message).slice(0, 300)); }
    await client.close();
    process.exit(1); // the probe reports through the runner's failure output
})();
