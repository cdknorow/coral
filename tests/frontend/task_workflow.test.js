const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server');
(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`(async () => {
            const {state} = await import('/static/state.js');
            const tasks = await import('/static/tasks.js');
            window.testTasks = tasks;
            state.currentSession = {name:'workflow-browser', board_project:'workflow-browser', type:'live'};
            await tasks.showCreateTaskModal();
            document.getElementById('create-task-title').value = 'Build candidate';
            document.getElementById('create-task-workflow').value = 'Build → Test → Release';
            document.getElementById('create-task-stage').value = 'Build';
            document.getElementById('create-task-outputs').value = 'build';
            document.getElementById('create-task-instructions').value = 'Use revision-specific evidence.';
            await tasks.submitCreateTask();
            window.testTaskID = state.currentBoardTasks.find(t => t.title === 'Build candidate').id;
            tasks.showTaskDetailModal(window.testTaskID);
        })()`);
        assert.ok(await ev(`document.getElementById('task-detail-content').textContent.includes('Use revision-specific evidence.')`));
        assert.ok(await ev(`document.getElementById('task-detail-content').textContent.includes('Completion results are immutable')`));
        await ev(`testTasks.completeBoardTask(testTaskID); testTasks._doCompleteTask(testTaskID)`);
        assert.ok(await ev(`document.body.textContent.includes('required output')`), 'missing artifact is rejected');
        await ev(`document.getElementById('task-artifact-name').value = 'build';
            document.getElementById('task-artifact-content').value = '<img src=x onerror="window.artifactXSS=true">';
            document.getElementById('task-artifact-revision').value = 'rev-42';
            testTasks._doCompleteTask(testTaskID)`);
        await ev(`testTasks.showTaskDetailModal(testTaskID)`);
        assert.ok(await ev(`document.getElementById('task-detail-content').textContent.includes('rev-42')`));
        assert.equal(await ev(`Boolean(window.artifactXSS)`), false, 'artifacts render as text');
        const result = await ev(`fetch('/api/board/workflow-browser/tasks/' + testTaskID).then(r => r.json())`);
        assert.equal(result.workflow.outcome, 'success');
        assert.equal(result.workflow.artifacts[0].name, 'build');
        await ev(`(async()=>{
            const r=await fetch('/api/board/workflow-browser/tasks',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({title:'Missing candidate consumer',created_by:'tester',blocked_by:[{task_id:testTaskID,required_artifacts:['candidate']}]})});
            if(!r.ok)throw new Error(await r.text());const child=await r.json();
            await testTasks.loadBoardTasks('workflow-browser');testTasks.showTaskDetailModal(child.id);
        })()`);
        assert.ok(await ev(`document.getElementById('task-detail-content').textContent.includes('missing required artifacts: candidate')`));
        assert.equal(await ev(`document.querySelector('#task-detail-content .task-dep-status').textContent`),'unmet');

        await ev(`(async () => {
            const {state} = await import('/static/state.js');
            const r = await fetch('/api/sessions/launch', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({working_dir:'/tmp',agent_type:'terminal',display_name:'Personal workflow browser'})});
            if (!r.ok) throw new Error('launch failed');
            const session = await r.json();
            const resolved = await fetch('/api/sessions/'+session.session_id+'/resolve').then(r=>r.json());
            session.name = resolved.name; window.personalSession = session;
            state.currentSession = {...session,type:'live',board_project:null};
            const create = async body => {
                const r = await fetch('/api/agent/tasks',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({session_id:session.session_id,...body})});
                if (!r.ok) throw new Error(await r.text()); return r.json();
            };
            const build = await create({title:'Personal build',workflow:{required_outputs:['build']}});
            const test = await create({title:'Personal test',blocked_by:[build.id]});
            window.personalBuildID=build.id; window.personalTestID=test.id;
            await testTasks.loadAgentTasks(session.name,session.session_id);
            testTasks.showAgentTaskDetailModal(build.id);
            testTasks.completeBoardTask(build.id,true);
            document.getElementById('task-artifact-name').value='build';
            document.getElementById('task-artifact-content').value='Personal candidate';
            document.getElementById('task-artifact-revision').value='personal-browser-rev';
            await testTasks._doCompleteTask(build.id,true);
            testTasks.showAgentTaskDetailModal(build.id);
        })()`);
        assert.ok(await ev(`document.getElementById('task-detail-content').textContent.includes('personal-browser-rev')`));
        const personal = await ev(`fetch('/api/agent/tasks/'+personalTestID+'?session_id='+personalSession.session_id).then(r=>r.json())`);
        assert.equal(personal.status, 'pending');
        await ev(`fetch('/api/sessions/live/'+encodeURIComponent(personalSession.name)+'/kill',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({agent_type:'terminal',session_id:personalSession.session_id})})`);
        console.log('PASS workflow creation, default instructions, required artifacts, completion, and safe rendering');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
