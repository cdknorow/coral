// Isolated browser regression: subscriber discovery must never clear assignment.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const base = process.env.CORAL_URL;
if (!base || !/^http:\/\/(127\.0\.0\.1|localhost):\d+\/?$/.test(base) || /:8420\/?$/.test(base)) throw Error('Use an isolated local server');
(async () => {
    const c = await CDP({port:Number(process.env.CDP_PORT || 9222)});
    const ev = async expression => {
        const r = await c.Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});
        if (r.exceptionDetails) throw Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    try {
        await c.Page.enable(); await c.Network.enable(); await c.Network.setCacheDisabled({cacheDisabled:true});
        await c.Page.navigate({url:'about:blank'}); await c.Page.loadEventFired();
        await c.Page.navigate({url:base}); await c.Page.loadEventFired();
        await ev(`(async()=>{
            window.s=(await import('/static/state.js')).state;
            window.t=await import('/static/tasks.js');
            s.currentSession={name:'fixture',board_project:'fixture',type:'live'};
            s.currentBoardTasks=[{id:1,title:'Assigned task',body:'',priority:'medium',status:'pending',assigned_to:'Orchestrator'},
                {id:2,title:'Second task',body:'',priority:'medium',status:'pending',assigned_to:'UI'}];
            window.mode='missing';window.writes=[];window.pending=[];
            window.fetch=async (url,opts={})=>{
                if(opts.method==='PATCH'){writes.push({url,body:JSON.parse(opts.body)});return new Response('{}');}
                if(url.endsWith('/subscribers')){
                    if(mode==='deferred')return new Promise(resolve=>pending.push(()=>resolve(new Response('[{"subscriber_id":"Late agent"}]'))));
                    if(mode==='network')throw Error('Offline');
                    if(mode==='http')return new Response('{}',{status:503});
                    if(mode==='malformed')return new Response('{}');
                    if(mode==='present')return new Response('[{"subscriber_id":"Orchestrator"},{"subscriber_id":"Orchestrator"},{"name":"UI"}]');
                    return new Response('[{"subscriber_id":"UI"}]');
                }
                if(url.endsWith('/tasks'))return new Response(JSON.stringify({tasks:s.currentBoardTasks}));
                return new Response('{}');
            };
        })()`);
        for (const mode of ['missing','network','http','malformed','present']) {
            const result=await ev(`(async()=>{
                mode=${JSON.stringify(mode)};writes=[];t.showTaskDetailModal(1);await t.enableTaskEditMode(1);
                const selected=document.getElementById('task-edit-assignee').value;
                const options=[...document.getElementById('task-edit-assignee').options].map(x=>x.value);
                document.getElementById('task-edit-title').value='Title only';await t.saveTaskEdit(1);
                return {selected,options,writes};
            })()`);
            console.log(mode,JSON.stringify(result));
            assert.equal(result.selected,'Orchestrator');
            assert.equal(result.options.filter(x=>x==='Orchestrator').length,1);
            assert.deepEqual(result.writes,[{url:'/api/board/fixture/tasks/1',body:{title:'Title only'}}]);
        }
        for (const assignment of ['', 'UI']) {
            const writes=await ev(`(async()=>{mode='missing';writes=[];t.showTaskDetailModal(1);await t.enableTaskEditMode(1);document.getElementById('task-edit-assignee').value=${JSON.stringify(assignment)};await t.saveTaskEdit(1);return writes;})()`);
            assert.deepEqual(writes,[{url:'/api/board/fixture/tasks/1',body:{assigned_to:assignment,subscriber_id:'Operator'}}]);
        }
        await ev(`(async()=>{mode='deferred';t.showTaskDetailModal(1);window.oldEdit=t.enableTaskEditMode(1);mode='missing';t.showTaskDetailModal(2);await t.enableTaskEditMode(2);document.getElementById('task-edit-title').value='Keep my draft';pending.shift()();await oldEdit;})()`);
        assert.deepEqual(await ev(`({title:document.getElementById('task-edit-title').value,assigned:document.getElementById('task-edit-assignee').value})`),{title:'Keep my draft',assigned:'UI'});
        for (const action of ['cancel','close','board','detail']) {
            const result=await ev(`(async()=>{
                s.currentSession.board_project='fixture';mode='deferred';t.showTaskDetailModal(1);const edit=t.enableTaskEditMode(1);
                const action=${JSON.stringify(action)};
                if(action==='cancel')t.cancelTaskEdit();
                if(action==='close')t.hideTaskDetailModal();
                if(action==='board')s.currentSession.board_project='other';
                if(action==='detail')t.showTaskDetailModal(2);
                const before=document.getElementById('task-detail-content').innerHTML;
                pending.shift()();await edit;
                return before===document.getElementById('task-detail-content').innerHTML;
            })()`);
            assert.equal(result,true,action+' must invalidate delayed subscriber response');
        }
        const writes=await ev(`(async()=>{s.currentSession.board_project='fixture';mode='missing';writes=[];t.showTaskDetailModal(1);await t.enableTaskEditMode(1);document.getElementById('task-edit-title').value='Wrong board';s.currentSession.board_project='other';await t.saveTaskEdit(1);return writes;})()`);
        assert.deepEqual(writes,[]);
        console.log('PASS assignment preserved on absent/failed/malformed discovery, deduplicated options, explicit unassign/reassign, stale form/cancel/close/board responses and board-save isolation');
    } finally {await c.close();}
})().catch(error=>{console.error(error);process.exitCode=1;});
