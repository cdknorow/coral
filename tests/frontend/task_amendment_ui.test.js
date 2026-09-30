const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');

const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server');

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`(async () => {
            const {state} = await import('/static/state.js');
            const tasks = await import('/static/tasks.js');
            window.testState = state; window.testTasks = tasks;
            state.currentSession = {name:'amendment-fixture', board_project:'amendment-fixture', type:'live'};
            state.currentAgentTasks = []; state.currentSubagents = [];
            window.__requests = [];
            window.__amendMode = 'ok';
            window.__task = {id:1669,title:'Versioned task',body:'Original body',status:'in_progress',priority:'medium',assigned_to:'UI',claimed_at:'2026-09-30T07:00:00Z',revision:2,workflow:{instructions:'Original instructions',effective_instructions:'Original instructions',amendments:[{revision:2,actor:'Orchestrator',created_at:'2026-09-30T06:00:00Z',reason:'Clarify handoff',changes:{body:'Clarified body'},previous_snapshot:{body:'Original body'},effective_snapshot:{body:'Clarified body'}}]}};
            state.currentBoardTasks = [window.__task];
            window.fetch = async (url, opts={}) => {
                const body = opts.body ? JSON.parse(opts.body) : null;
                window.__requests.push({url, method:opts.method || 'GET', body});
                if (url.includes('/subscribers')) return new Response(JSON.stringify([{subscriber_id:'Operator'},{subscriber_id:'UI'}]), {status:200});
                if (url.endsWith('/tasks') && (!opts.method || opts.method === 'GET')) return new Response(JSON.stringify({tasks:state.currentBoardTasks}), {status:200});
                if (url.endsWith('/amend')) {
                    if (window.__amendMode === 'forbidden') return new Response(JSON.stringify({error:'planner authorization required'}), {status:403});
                    if (window.__amendMode === 'conflict') return new Response(JSON.stringify({error:'task revision is stale'}), {status:409});
                    return new Response(JSON.stringify(window.__task), {status:200});
                }
                if (url.includes('/complete')) return new Response(JSON.stringify({error:'task #1669 has revision 3; reread task detail before completing'}), {status:409});
                return new Response('{}', {status:200});
            };
            tasks.showTaskDetailModal(1669);
        })()`);

        const detail = await ev(`({text:document.getElementById('task-detail-content').textContent,revision:document.querySelector('[data-task-revision]')?.textContent||'',history:!!document.querySelector('.task-amendment-history')})`);
        assert.match(detail.text, /Current revision 2/);
        assert.match(detail.text, /Clarify handoff/);
        assert.equal(detail.history, true);
        fs.writeFileSync('/tmp/coral-1669-task-amendment.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));

        await ev(`testTasks.enableTaskEditMode(1669); new Promise(r=>setTimeout(r,20))`);
        await ev(`document.getElementById('task-edit-body').value='Amended body'; document.getElementById('task-edit-reason').value='Add acceptance detail'; testTasks.saveTaskEdit(1669)`);
        const amendRequest = await ev(`window.__requests.find(r=>r.url.endsWith('/amend'))`);
        assert.equal(amendRequest.method, 'PATCH');
        assert.equal(amendRequest.body.base_revision, 2);
        assert.equal(amendRequest.body.reason, 'Add acceptance detail');
        assert.equal(amendRequest.body.changes.body, 'Amended body');
        assert.equal(amendRequest.body.changes.title, undefined);
        assert.equal(await ev(`window.__requests.some(r=>r.url.endsWith('/tasks/1669') && r.method === 'PATCH')`), false);

        await ev(`testTasks.showTaskDetailModal(1669); testTasks.enableTaskEditMode(1669); new Promise(r=>setTimeout(r,20))`);
        await ev(`document.getElementById('task-edit-body').value='Unauthorized body'; document.getElementById('task-edit-reason').value='Unauthorized change'; window.__amendMode='forbidden'; testTasks.saveTaskEdit(1669); new Promise(r=>setTimeout(r,30))`);
        assert.match(await ev(`document.getElementById('task-edit-error').textContent`), /authorization/);
        await ev(`window.__amendMode='conflict'; document.getElementById('task-edit-body').value='Stale body'; document.getElementById('task-edit-reason').value='Concurrent change'; testTasks.saveTaskEdit(1669); new Promise(r=>setTimeout(r,30))`);
        assert.match(await ev(`document.getElementById('task-edit-error').textContent`), /stale/);
        await ev(`window.__amendMode='ok'`);

        await ev(`testTasks.showTaskDetailModal(1669); testTasks.completeBoardTask(1669); document.getElementById('task-complete-message').value='draft evidence'; testTasks._doCompleteTask(1669); new Promise(r=>setTimeout(r,100))`);
        assert.equal(await ev(`document.getElementById('task-complete-message').value`), 'draft evidence');
        const staleState = await ev(`({hidden:document.getElementById('task-complete-refresh').hidden,error:document.getElementById('task-complete-error').textContent,requests:window.__requests})`);
        assert.equal(staleState.hidden, false, JSON.stringify(staleState));
        const completeRequest = await ev(`window.__requests.find(r=>r.url.includes('/complete'))`);
        assert.equal(completeRequest.body.expected_revision, 2);

        await ev(`window.__task={id:1670,title:'Revision one',body:'body',status:'pending',revision:1,workflow:{instructions:'instructions'}}; testState.currentBoardTasks=[window.__task]; testTasks.showTaskDetailModal(1670); testTasks.completeBoardTask(1670); testTasks._doCompleteTask(1670); new Promise(r=>setTimeout(r,30))`);
        const revisionOne = await ev(`window.__requests.filter(r=>r.url.includes('/complete')).at(-1).body.expected_revision`);
        assert.equal(revisionOne, 1);

        await ev(`window.__task={id:1671,title:'Finished',status:'completed',revision:3,workflow:{instructions:'frozen'}}; testState.currentBoardTasks=[window.__task]; testTasks.showTaskDetailModal(1671)`);
        assert.equal(await ev(`document.getElementById('task-detail-modal-footer').textContent.includes('Edit')`), false);
        console.log(JSON.stringify({revision:2, amendment:true, staleDraftPreserved:true, revisionOneCompatibility:true, terminalImmutable:true}));
    } finally { await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
