const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server');
(async () => {
 const client = await CDP({port:Number(process.env.CDP_PORT || 9222)});
 const {Page,Runtime}=client;
 const ev=async expression=>{const r=await Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw new Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 try {
  await Page.enable();await Page.navigate({url:BASE});await Page.loadEventFired();
  await ev(`showTeamWorkingMode('working-mode-browser')`);
  assert.equal(await ev(`document.querySelector('.working-mode-form').elements.mode.value`),'none');
  await ev(`(async()=>{const form=document.querySelector('.working-mode-form');form.elements.mode.value='worktrees';form.elements.dependency_guidance.checked=true;form.elements.custom_instructions.value='Verify exact commit.';await form.onsubmit({preventDefault(){}})})()`);
  assert.ok(await ev(`document.querySelector('.working-mode-status').textContent.includes('Saved')`));
  const claimed=await ev(`(async()=>{
   const post=async(path,body)=>{const r=await fetch('/api/board/working-mode-browser'+path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});if(!r.ok)throw new Error(await r.text());return r.json()};
   const task=await post('/tasks',{title:'Test mode',created_by:'Operator'});
   window.modeTaskID=task.id;
   return post('/tasks/claim',{subscriber_id:'dev',task_id:task.id});
  })()`);
  assert.equal(claimed.workflow.team_mode.mode,'worktrees');
  assert.ok(claimed.workflow.instructions.includes('Verify exact commit.'));
  await ev(`(async()=>{const form=document.querySelector('.working-mode-form');form.elements.mode.value='none';form.elements.dependency_guidance.checked=false;form.elements.custom_instructions.value='';await form.onsubmit({preventDefault(){}})})()`);
  const retained=await ev(`fetch('/api/board/working-mode-browser/tasks/'+modeTaskID).then(r=>r.json())`);
  assert.equal(retained.workflow.instructions,claimed.workflow.instructions);
  await ev(`document.querySelector('#team-working-mode-dialog header button').click()`);
  console.log('PASS team working mode editor, API claim instructions and immutable snapshot');
 }finally{await client.close()}
})().catch(e=>{console.error(e);process.exit(1)});
