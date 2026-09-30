const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL;
if (!BASE || /:8420(\/|$)/.test(BASE)) throw new Error('isolated server required');
(async () => {
  const client = await CDP({ port: Number(process.env.CDP_PORT) });
  const { Page, Runtime, Emulation } = client;
  const ev = async expression => {
    const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  };
  try {
    await Page.enable(); await Page.navigate({ url: BASE }); await Page.loadEventFired();
    await ev(`(async()=>{ const {state}=await import('/static/state.js'); const tasks=await import('/static/tasks.js'); window.testState=state; window.testTasks=tasks; state.currentSession={name:'completion-fixture',board_project:'completion-fixture',type:'live'}; state.currentBoardTasks=[{id:1464,title:'Long completion fixture',body:'${'Long task description '.repeat(120)}',status:'in_progress',assigned_to:'Frontend Dev',workflow:{required_outputs:['build','test_report']},blocked_by:[]}]; window.__completionRequest=null; window.fetch=async (url,opts={})=>{ if(opts.method==='POST'){window.__completionRequest=JSON.parse(opts.body); return new Response('{}',{status:200});} return new Response(JSON.stringify({tasks:state.currentBoardTasks}),{status:200,headers:{'Content-Type':'application/json'}}); }; })()`);
    await ev(`testTasks.showTaskDetailModal(1464); testTasks.completeBoardTask(1464); true`);
    const initial = await ev(`(()=>{const m=document.getElementById('task-detail-modal'),b=m.querySelector('.modal-body'),f=document.getElementById('task-detail-modal-footer'),c=f.querySelector('.btn-success'); return {bodyScroll:b.scrollHeight,bodyClient:b.clientHeight,footerBottom:f.getBoundingClientRect().bottom,viewport:innerHeight,completeVisible:!!c&&c.getBoundingClientRect().bottom<=innerHeight,required:document.querySelector('.task-completion-required')?.textContent||'',detailsOpen:document.querySelector('.task-completion-evidence')?.open}})()`);
    assert.ok(initial.bodyScroll > initial.bodyClient, JSON.stringify(initial)); assert.equal(initial.completeVisible,true); assert.match(initial.required,/build/); assert.match(initial.required,/test_report/); assert.equal(initial.detailsOpen,true);
    await Emulation.setDeviceMetricsOverride({width:390,height:520,deviceScaleFactor:1,mobile:true});
    await ev(`document.getElementById('task-complete-message').value='verified on mobile'; true`);
    const mobile = await ev(`(()=>{const m=document.getElementById('task-detail-modal'),b=m.querySelector('.modal-body'),f=document.getElementById('task-detail-modal-footer'),c=f.querySelector('.btn-success'); return {bodyScroll:b.scrollHeight,bodyClient:b.clientHeight,footerBottom:f.getBoundingClientRect().bottom,viewport:innerHeight,completeVisible:!!c&&c.getBoundingClientRect().bottom<=innerHeight,nameWidth:document.getElementById('task-detail-modal-title').getBoundingClientRect().width}})()`);
    fs.writeFileSync('/tmp/coral-1464-completion-mobile.png', Buffer.from((await Page.captureScreenshot()).data, 'base64'));
    assert.ok(mobile.bodyScroll > mobile.bodyClient, JSON.stringify(mobile)); assert.equal(mobile.completeVisible,true);
    await ev(`testTasks._doCompleteTask(1464); new Promise(r=>setTimeout(r,100))`);
    assert.ok(await ev(`document.body.textContent.includes('Required outputs missing')`), 'required output validation should reject incomplete evidence');
    await ev(`(()=>{ const input=document.getElementById('task-artifacts-file'); const file=new File([JSON.stringify([{name:'build',uri:'coral://artifacts/build'},{name:'test_report',content:'passed'}])],'manifest.json',{type:'application/json'}); Object.defineProperty(input,'files',{value:[file]}); document.getElementById('task-complete-message').value='verified'; })()`);
    await ev(`testTasks._doCompleteTask(1464); new Promise(r=>setTimeout(r,150))`);
    const request = await ev(`window.__completionRequest`);
    assert.equal(request.outcome,'success'); assert.deepEqual(request.artifacts.map(a=>a.name),['build','test_report']);
    console.log(JSON.stringify({initial,mobile,submittedArtifacts:request.artifacts.map(a=>a.name)}));
  } finally { await Emulation.clearDeviceMetricsOverride(); await client.close(); }
})().catch(error => { console.error(error); process.exit(1); });
