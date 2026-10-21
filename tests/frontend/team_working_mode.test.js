const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const { removeBoards } = require('./helpers/board_cleanup');
const BOARD = 'working-mode-browser-' + Date.now().toString(36); // a fresh board per run: reruns on one server do not collide
(async () => {
 const client = await CDP({port:Number(process.env.CDP_PORT || 9222)});
 const {Page,Runtime}=client;
 const ev=async expression=>{const r=await Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw new Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 // The dialog loads its settings after opening and enables Save when done; a
 // load that finishes after a save resets the status line, so submit only then.
 const loaded=()=>ev(`new Promise((resolve,reject)=>{const end=Date.now()+15000;(function check(){const b=document.querySelector('.working-mode-form [type=submit]');if(b&&!b.disabled)return resolve(true);if(Date.now()>end)return reject(new Error('team settings never finished loading: '+document.querySelector('.working-mode-status')?.textContent));setTimeout(check,25)})()})`);
 try {
  await Page.enable();await Page.navigate({url:BASE});await Page.loadEventFired();
  await ev(`showTeamWorkingMode('${BOARD}')`);
  await loaded();
  assert.equal(await ev(`document.querySelector('.working-mode-form').elements.mode.value`),'none');
  await ev(`(async()=>{const form=document.querySelector('.working-mode-form');form.elements.mode.value='worktrees';form.elements.dependency_guidance.checked=true;form.elements.custom_instructions.value='Verify exact commit.';await form.onsubmit({preventDefault(){}})})()`);
  assert.ok(await ev(`document.querySelector('.working-mode-status').textContent.includes('Saved')`));
  const claimed=await ev(`(async()=>{
   const post=async(path,body)=>{const r=await fetch('/api/board/${BOARD}'+path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(!r.ok)throw new Error(await r.text());return r.json()};
   const task=await post('/tasks',{title:'Test mode',created_by:'Operator'});
   window.modeTaskID=task.id;
   return post('/tasks/claim',{subscriber_id:'dev',task_id:task.id});
  })()`);
  assert.equal(claimed.workflow.team_mode.mode,'worktrees');
  assert.ok(claimed.workflow.instructions.includes('Verify exact commit.'));
  await loaded();
  await ev(`(async()=>{const form=document.querySelector('.working-mode-form');form.elements.mode.value='none';form.elements.dependency_guidance.checked=false;form.elements.custom_instructions.value='';await form.onsubmit({preventDefault(){}})})()`);
  const retained=await ev(`fetch('/api/board/${BOARD}/tasks/'+modeTaskID).then(r=>r.json())`);
  assert.equal(retained.workflow.instructions,claimed.workflow.instructions);
  await ev(`document.querySelector('#team-working-mode-dialog header .modal-close-btn').click()`);
  console.log('PASS team working mode editor, API claim instructions and immutable snapshot');
 }finally{await removeBoards(ev, [BOARD]); await client.close()}
})().catch(e=>{console.error(e);process.exit(1)});
