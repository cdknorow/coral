// QA #5774: the shared Inbox phase 1 against the REAL server (no stubbed fetch): Tasks for you lists the Operator's tasks and expand shows the task, a reply posts and the
// creator is notified; Saved for later "Read the whole message" for a board message and an agent message (gone and not_reachable), and a token in a saved message comes back masked.
// Everything the page shows is what the real routes answered; the data is seeded through the same routes.
'use strict';
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => { const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const results = [];
    const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined && !ok ? ' — ' + String(detail).slice(0, 400) : ''}`); };
    const note = s => console.log(`[NOTE] ${s}`);
    const P = 'inboxreal';
    const GH = 'ghp_' + 'aB3dE5fG7h'.repeat(3) + 'AbCdEf', AWS = 'AKIA' + 'QWERTYUIOPASDFGH';
    const api = (m, p, b) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(m)}, headers: { 'Content-Type': 'application/json' }, body: ${b === undefined ? 'undefined' : JSON.stringify(JSON.stringify(b))} }).then(async r => ({ status: r.status, data: await r.json().catch(() => null) }))`);
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        const loaded = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await loaded;
        await coralReady();

        // ---- seed through the real routes
        for (const who of ['Orchestrator', 'Worker']) await api('POST', `/api/board/${P}/subscribe`, { subscriber_id: who, job_title: who });
        const t1 = await api('POST', `/api/board/${P}/tasks`, { title: 'Decide on the release notes', priority: 'high', subscriber_id: 'Orchestrator', assigned_to: 'Operator',
            body: 'Please read **this** and answer:\n\n- is the wording ok\n- <script>window.__pwn=1</script> and a [link](javascript:window.__pwn=2)\n\nA token ' + GH });
        check('seed: a task for the Operator on a real board', t1.status === 201 || t1.status === 200, JSON.stringify(t1));
        await api('POST', `/api/board/${P}/tasks`, { title: 'Not for the Operator', priority: 'low', subscriber_id: 'Orchestrator', assigned_to: 'Worker' });
        const mk = async content => { const r = await api('POST', `/api/board/${P}/messages`, { subscriber_id: 'Orchestrator', content }); return (r.data && (r.data.message ? r.data.message.id : r.data.id)) || 0; };
        const plan = 'Release plan, the whole text. ' + 'A paragraph of the plan that goes on. '.repeat(20) + '\nSecret: ' + GH + ' and key ' + AWS + '\nThe end.';
        const planId = await mk(plan);
        const save = async o => { const r = await api('POST', '/api/saved', o); return r.data && r.data.id; };
        await save({ kind: 'board', ref: P, key: String(planId), title: 'Release plan', snippet: plan.slice(0, 60) });
        await save({ kind: 'board', ref: P, key: '987654', title: 'A message that is gone', snippet: 'was here' });
        await save({ kind: 'agent', ref: '11111111-2222-4333-8444-555566667777', agent_type: 'claude', key: 'a'.repeat(20), title: 'An agent of another computer', snippet: 'was there' });
        const hugeId = await mk('0123456789abcdef'.repeat(15 * 1024) + ' end');
        if (hugeId) await save({ kind: 'board', ref: P, key: String(hugeId), title: 'One unbroken run of 240 KiB', snippet: 'x' });

        // ---- the Inbox tab: Tasks for you
        await ev(`localStorage.removeItem('coral.inbox.tab'); document.getElementById('nav-tab-hub-inbox').click(); true`);
        await waitFor(`!!document.getElementById('inbox-local') && !document.getElementById('inbox-local').hidden`, 'the Inbox', 10000);
        await ev(`document.getElementById('inbox-tab-tasks').click(); true`);
        await waitFor(`[...document.querySelectorAll('#inbox-tasks-host .ibx-task-title')].some(e => e.textContent === 'Decide on the release notes')`, 'the Operator task in the list', 10000);
        const titles = await ev(`[...document.querySelectorAll('#inbox-tasks-host .ibx-task-title')].map(e => e.textContent)`);
        check('Tasks for you lists the Operator\'s task from the real server, not the Worker\'s', titles.includes('Decide on the release notes') && !titles.includes('Not for the Operator'), JSON.stringify(titles));
        const idx = titles.indexOf('Decide on the release notes');
        await ev(`document.querySelectorAll('#inbox-tasks-host .ibx-task-head')[${idx}].click(); true`);
        await waitFor(`!!document.querySelectorAll('#inbox-tasks-host .ibx-task-body')[${idx}].querySelector('.md')`, 'the task text', 10000);
        const body = await ev(`(() => { const r = document.querySelectorAll('#inbox-tasks-host .ibx-task-body')[${idx}]; const tags = new Set(); r.querySelectorAll('.md *').forEach(e => tags.add(e.tagName.toLowerCase()));
            return { text: r.textContent, tags: [...tags], hrefs: [...r.querySelectorAll('a')].map(a => a.getAttribute('href')), pwn: window.__pwn === undefined ? null : window.__pwn }; })()`);
        check('expand: the real task text is shown, rendered (a list, bold)', /Please read this and answer/.test(body.text) && body.tags.includes('ul') && body.tags.includes('strong'), JSON.stringify(body).slice(0, 300));
        check('expand: raw HTML and a javascript: link in the task are text; nothing ran', !body.tags.some(t => ['script', 'img', 'iframe'].includes(t)) && !body.hrefs.some(h => /^javascript:/i.test(h || '')) && body.pwn === null, JSON.stringify(body));
        note(`the task text on this computer shows the token as stored: ${body.text.includes(GH) ? 'UNMASKED (as the task route answers)' : 'masked'}`);

        // reply: through the real reply route, the creator is notified
        await ev(`(() => { const t = document.querySelectorAll('#inbox-tasks-host .ibx-reply-input')[${idx}]; t.value = 'Wording is fine, ship it. Token ' + ${JSON.stringify(GH)}; t.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        await ev(`document.querySelectorAll('#inbox-tasks-host .ibx-send')[${idx}].click(); true`);
        await waitFor(`/Reply (sent|saved)|could not|refus|verified|HTTP/i.test(document.querySelectorAll('#inbox-tasks-host .ibx-reply-status')[${idx}].textContent)`, 'the answer of the real reply route', 15000);
        const status = await ev(`document.querySelectorAll('#inbox-tasks-host .ibx-reply-status')[${idx}].textContent`);
        check('reply: the real route accepted it and the creator (Orchestrator) was notified', status === 'Reply sent. Orchestrator notified.', status);
        const all = await api('GET', `/api/board/${P}/messages/all?limit=200`);
        const raw = JSON.stringify(all.data);
        check('reply: it is on the board as the Operator\'s message', /ship it/.test(raw) && /Operator/.test(raw), raw.slice(0, 200));
        check('reply: the token typed in it is not stored on the board as typed', !raw.includes(GH), 'the token is on the board');
        const notifiedMsg = await ev(`(async () => { const r = await fetch('/api/board/${P}/messages?subscriber_id=Orchestrator&all=1&limit=50'); const d = await r.json().catch(() => null); return JSON.stringify(d).slice(0, 2000); })()`);
        check('reply: the creator\'s own unread messages hold the reply (it reached the creator)', /ship it/.test(notifiedMsg), notifiedMsg.slice(0, 200));

        // ---- Saved for later: Read the whole message
        await ev(`document.getElementById('inbox-tab-saved').click(); true`);
        await waitFor(`document.querySelectorAll('#saved-list .ibx-saved-toggle').length >= 2`, 'a control on each saved item', 10000);
        const items = await ev(`[...document.querySelectorAll('#saved-list .saved-item')].map((li, i) => ({ i, title: li.textContent.replace(/\\s+/g, ' ').slice(0, 120), toggle: !!li.querySelector('.ibx-saved-toggle'), gone: li.classList.contains('gone') }))`);
        const find = t => items.findIndex(x => x.title.includes(t));
        const open = async i => {
            await ev(`document.querySelectorAll('#saved-list .saved-item')[${i}].querySelector('.ibx-saved-toggle').click(); true`);
            await waitFor(`(() => { const b = document.querySelectorAll('#saved-list .saved-item')[${i}].querySelector('.ibx-saved-toggle'); const r = document.getElementById(b.getAttribute('aria-controls')); return !r.hidden && !/Reading the message/.test(r.textContent); })()`, 'the message region', 15000);
            return ev(`(() => { const b = document.querySelectorAll('#saved-list .saved-item')[${i}].querySelector('.ibx-saved-toggle'); return document.getElementById(b.getAttribute('aria-controls')).textContent; })()`);
        };
        const planI = find('Release plan');
        const planText = planI >= 0 ? await open(planI) : '';
        check('saved board message: the whole text from the real server', planI >= 0 && /The end\./.test(planText) && planText.length > 600, planText.slice(0, 200));
        check('saved board message: the token and the key are masked in what the page shows', !planText.includes(GH) && !planText.includes(AWS) && /Secret:/.test(planText), planText.slice(planText.indexOf('Secret:'), planText.indexOf('Secret:') + 120));
        const goneI = find('A message that is gone');
        if (goneI >= 0 && items[goneI].toggle) {
            const goneText = await open(goneI);
            check('saved board message that is gone: it says so in words', /This message was deleted, so only the saved snippet is left\./.test(goneText), goneText);
        } else note(`the saved item of a deleted board message has ${goneI >= 0 ? 'no' : 'NOT SHOWN: no'} "Read the whole message" control (class gone: ${goneI >= 0 && items[goneI].gone})`);
        const agentI = find('An agent of another computer');
        const agentText = agentI >= 0 ? await open(agentI) : '';
        check('saved agent message with no transcript here: it says so in words', /This message cannot be read from here: its agent ended on another server\./.test(agentText), agentText);
        const hugeI = find('One unbroken run');
        if (hugeI >= 0) {
            const hugeText = await open(hugeI);
            note(`a saved message that is one unbroken 240 KiB run shows: ${JSON.stringify(hugeText.slice(0, 120))} (${hugeText.length} characters)`);
            check('saved very long message: the panel says it is too long to show', /too long to show here/.test(hugeText), hugeText.slice(0, 120));
        }
    } catch (e) { check('the suite ran to the end', false, e.stack || e.message); }
    await client.close();
    const bad = results.filter(r => !r).length;
    console.log(bad ? `${bad} FAILED` : `${results.length}/${results.length} passed`);
    process.exit(bad ? 1 : 0);
})();
