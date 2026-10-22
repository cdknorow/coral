// The Create task dialog and the task detail view for the keyboard, a screen reader and both themes (#5814): a refusal in the Create dialog is spoken (role=alert) and marks the
// title; "+ Add dependency" and a dependency tag's × are buttons (they were links with no href: not reachable by Tab, and the × had no name); the task detail dialog is named
// by its task, not only "Task #1"; and the real light theme holds 4.5:1 in both dialogs (the Open chip, Complete, the Add dependency link). Focus goes to the title on open and back
// to what opened each dialog on Escape. Real board over the API. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const B = 'a11y-task-' + Date.now().toString(36);
    const api = (m, p, body) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(m)}, headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(r => r.status)`);
    const key = async (k, code, vk, modifiers = 0) => { for (const type of ['keyDown', 'keyUp']) await Input.dispatchKeyEvent({ type, key: k, code, windowsVirtualKeyCode: vk, modifiers }); };
    const shot = async n => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; require('node:fs').mkdirSync(d, { recursive: true }); require('node:fs').writeFileSync(`${d}/${n}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const open = id => ev(`getComputedStyle(document.getElementById('${id}')).display !== 'none'`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Emulation.setFocusEmulationEnabled({ enabled: true });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        for (const [id, t] of [['Boss', 'Orchestrator'], ['dev-1', 'Developer']]) assert.equal(await api('POST', `/api/board/${B}/subscribe`, { subscriber_id: id, job_title: t, session_name: id + '-s' }), 200);
        assert.equal(await api('POST', `/api/board/${B}/tasks`, { subscriber_id: 'Boss', title: 'Ship the queue', body: 'Do it.', priority: 'high', assignee: 'dev-1' }), 201);
        assert.equal(await api('POST', `/api/board/${B}/tasks`, { subscriber_id: 'Boss', title: 'Write the docs', priority: 'low' }), 201);
        await Page.navigate({ url: `${BASE}/board/${B}` }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        await waitFor(`document.querySelectorAll('#mb-messages .mb-message, #mb-messages .mb-queue-run').length >= 1`, 'the board', 15000);
        await ev(`(() => { if (document.getElementById('agentic-state').getBoundingClientRect().width < 50) document.getElementById('panel-toggle-btn').click(); document.getElementById('agentic-tab-tasks').click(); return true; })()`);
        await waitFor(`document.querySelectorAll('#agentic-panel-tasks .board-task-item').length >= 2 && document.getElementById('agentic-panel-tasks').getBoundingClientRect().width > 50`, 'the tasks', 15000);

        console.log('1. Create task: focus, a spoken refusal, a keyboard-reachable dependency picker');
        await ev(`document.querySelector('.btn-create-task').focus(); document.querySelector('.btn-create-task').click(); true`);
        await waitFor(`getComputedStyle(document.getElementById('create-task-modal')).display !== 'none'`, 'the dialog', 8000);
        assert.equal(await ev(`document.activeElement.id`), 'create-task-title', 'focus goes to the title on open');
        assert.equal(await ev(`document.getElementById('create-task-title').getAttribute('aria-required')`), 'true');
        assert.equal(await ev(`document.getElementById('create-task-error').getAttribute('role')`), 'alert', 'the refusal is an alert');
        await ev(`document.querySelector('#create-task-modal [data-on-click="m-submit-create-task"]').click(); true`);
        const err = await ev(`(() => { const e = document.getElementById('create-task-error'); const t = document.getElementById('create-task-title'); return { text: e.textContent, shown: getComputedStyle(e).display !== 'none', invalid: t.getAttribute('aria-invalid'), described: t.getAttribute('aria-describedby'), focus: document.activeElement.id }; })()`);
        assert.deepEqual(err, { text: 'Title is required', shown: true, invalid: 'true', described: 'create-task-error', focus: 'create-task-title' });
        await shot('create-error-dark');
        await ev(`const t = document.getElementById('create-task-title'); t.value = 'x'; t.dispatchEvent(new Event('input', { bubbles: true })); true`);
        assert.equal(await ev(`document.querySelector('.task-workflow-options').open`), false, 'optional workflow fields start folded');
        assert.equal(await ev(`document.getElementById('create-task-draft').checkVisibility()`), true, 'draft visibility remains a primary choice');
        await ev(`document.querySelector('.task-workflow-options > summary').focus();true`);
        await key(' ', 'Space', 32);
        assert.equal(await ev(`document.getElementById('create-task-workflow').checkVisibility()`), true, 'Space opens the workflow fields');
        const add = await ev(`(() => { const b = document.querySelector('#create-task-modal .dep-picker-add-link'); return { tag: b && b.tagName, type: b && b.type, expanded: b && b.getAttribute('aria-expanded'), controls: b && b.getAttribute('aria-controls'), tabbable: !!b && b.tabIndex >= 0 }; })()`);
        assert.deepEqual(add, { tag: 'BUTTON', type: 'button', expanded: 'false', controls: add.controls, tabbable: true }, '"+ Add dependency" is a button');
        await ev(`document.querySelector('#create-task-modal .dep-picker-add-link').focus(); true`);
        await key(' ', 'Space', 32);
        assert.equal(await ev(`document.querySelector('#create-task-modal .dep-picker-add-link').getAttribute('aria-expanded')`), 'true', 'Space opens the list');
        await ev(`document.querySelector('#create-task-modal .dep-picker-item input').click(); true`);
        const tag = await ev(`(() => { const b = document.querySelector('#create-task-modal .dep-tag .dep-tag-remove'); return b && { tag: b.tagName, label: b.getAttribute('aria-label') }; })()`);
        assert.ok(tag && tag.tag === 'BUTTON' && /^Remove dependency #\d+/.test(tag.label), 'a dependency tag\'s x is a named button: ' + JSON.stringify(tag));
        // A rejected workflow must not remain concealed after the user folds it.
        await ev(`(async () => { const d=document.querySelector('.task-workflow-options'); d.open=false;
            const original=window.fetch; window.fetch=async (u,o)=>String(u).endsWith('/tasks') && o?.method==='POST' ? new Response(JSON.stringify({error:'Required upstream artifact is missing'}),{status:400,headers:{'content-type':'application/json'}}) : original(u,o);
            try { await window.submitCreateTask(); } finally { window.fetch=original; } })()`);
        assert.equal(await ev(`document.querySelector('.task-workflow-options').open`), true, 'a server refusal reveals workflow fields');
        await ev(`(() => { const d=document.querySelector('.task-workflow-options'), f=document.getElementById('create-task-inputs'); d.open=false; f.setCustomValidity('Artifact required'); f.checkValidity(); f.setCustomValidity(''); })()`);
        assert.equal(await ev(`document.querySelector('.task-workflow-options').open`), true, 'native validation reveals the invalid field');
        await key('Escape', 'Escape', 27);
        await waitFor(`getComputedStyle(document.getElementById('create-task-modal')).display === 'none'`, 'Escape closes it', 4000);
        assert.ok(await ev(`document.activeElement.classList.contains('btn-create-task')`), 'focus goes back to + Task');

        await ev(`(async()=>{await window.showCreateTaskModal({blocked_by:[{task_id:(await import((window.__coralStatic||'/static/')+'state.js')).state.currentBoardTasks[0].id,condition:'failure',required_artifacts:['build']}],workflow:{name:'Release'}});})()`);
        assert.ok(await ev(`document.querySelector('.task-workflow-options').open && document.getElementById('create-task-condition').value==='failure' && document.getElementById('create-task-workflow').value==='Release' && document.querySelector('#create-task-deps input:checked') && document.getElementById('create-task-inputs').value==='build'`), 'preset prerequisites, condition and workflow are visible');
        await key('Escape', 'Escape', 27);
        await ev(`window.showCreateTaskModal({condition:'termination'})`);
        assert.ok(await ev(`document.querySelector('.task-workflow-options').open && document.getElementById('create-task-condition').value==='termination'`), 'a preset condition alone is visible');
        await key('Escape', 'Escape', 27);
        await ev(`window.showCreateTaskModal()`);
        assert.equal(await ev(`document.querySelector('.task-workflow-options').open`), false, 'fresh tasks reset to folded');
        await key('Escape', 'Escape', 27);

        console.log('2. the task detail dialog is named by its task');
        await ev(`document.querySelector('#agentic-panel-tasks .board-task-open').focus(); document.querySelector('#agentic-panel-tasks .board-task-open').click(); true`);
        await waitFor(`getComputedStyle(document.getElementById('task-detail-modal')).display !== 'none' && !!document.getElementById('task-detail-heading')`, 'the detail', 10000);
        const nm = await ev(`(() => { const m = document.getElementById('task-detail-modal'); const ids = m.getAttribute('aria-labelledby').split(' '); return ids.map(i => (document.getElementById(i) || {}).textContent).join(' ').replace(/\\s+/g, ' ').trim(); })()`);
        assert.ok(/Task #\d+ #\d+ · (Ship the queue|Write the docs)/.test(nm), 'the dialog is named by its number and its task: ' + nm);
        await key('Escape', 'Escape', 27);
        await waitFor(`getComputedStyle(document.getElementById('task-detail-modal')).display === 'none'`, 'Escape closes it', 4000);
        assert.ok(await ev(`document.activeElement.classList.contains('board-task-open')`), 'focus goes back to the task that was opened');

        console.log('3. both themes hold 4.5:1 in both dialogs');
        await installAudit(ev);
        for (const theme of ['dark', 'light']) {
            await setTheme(ev, theme);
            await ev(`document.querySelector('.btn-create-task').click(); true`);
            await waitFor(`getComputedStyle(document.getElementById('create-task-modal')).display !== 'none'`, 'create', 5000);
            const c = await auditRegion(ev, ['#create-task-modal']); await shot(`create-${theme}`);
            await ev(`document.querySelector('#create-task-modal .modal-close-btn').click(); document.querySelector('#agentic-panel-tasks .board-task-open').click(); true`);
            await waitFor(`!!document.getElementById('task-detail-heading') && getComputedStyle(document.getElementById('task-detail-modal')).display !== 'none'`, 'detail', 8000);
            const d = await auditRegion(ev, ['#task-detail-modal']); await shot(`detail-${theme}`);
            await ev(`document.querySelector('#task-detail-modal .modal-close-btn').click(); true`);
            assert.ok(c.checked > 0 && !c.fails.length, `${theme}: the Create task dialog holds 4.5:1: ` + JSON.stringify(c.fails).slice(0, 400));
            assert.ok(d.checked > 0 && !d.fails.length, `${theme}: the task detail holds 4.5:1: ` + JSON.stringify(d.fails).slice(0, 400));
        }
        console.log('   ✓');
    } finally { await removeBoards(ev, [B]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
