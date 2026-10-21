const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const watchdog=setTimeout(()=>{console.error('Review timeout');process.exit(1)},45000);watchdog.unref();
(async () => {
 const client = await CDP({port:Number(process.env.CDP_PORT)});
 const {Page, Runtime, Emulation, Input} = client;
 const ev = async expression => {const r=await Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw new Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 const {waitFor}=require('./helpers/wait').bind(ev);
 const results=[];
 const shot=async name=>fs.writeFileSync(`/tmp/coral-1273-${name}.png`,Buffer.from((await Page.captureScreenshot()).data,'base64'));
 const geometry=()=>ev(`(() => {
  const d=document.querySelector('#team-working-mode-dialog');
  const visible=selector=>{const e=d.querySelector(selector),r=e.getBoundingClientRect();return {visible:!!r.width&&!!r.height&&r.top>=0&&r.bottom<=innerHeight,top:r.top,bottom:r.bottom};};
  return {viewport:[innerWidth,innerHeight],dialog:{height:d.clientHeight,scrollHeight:d.scrollHeight,scrollTop:d.scrollTop},header:visible('header'),tabs:visible('[role=tablist]'),save:visible('[type=submit]'),close:visible('header button'),promptDetails:d.querySelectorAll('.prompt-inspection-content details').length,horizontalOverflow:document.documentElement.scrollWidth>innerWidth||d.scrollWidth>d.clientWidth};
 })()`);
 const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
 try {
  await Page.enable();const ready=Page.domContentEventFired();await Page.navigate({url:BASE});await ready;console.log("DOM ready");
  await ev(`import((window.__coralStatic||'/static/')+'team_working_mode.js').then(m=>window.openSettings=m.showTeamWorkingMode)`);
  for(const [name,width,height] of [['desktop',1280,800],['mobile',390,844]]) {
   await Emulation.setDeviceMetricsOverride({width,height,deviceScaleFactor:1,mobile:false});
   console.log(name,'opening');await ev(`window.openSettings('navigation-review')`);
   // Polled with timers, not animation frames: after the pass's Escape key press the Mac's headless Chrome reports the page hidden and runs no animation frames, so the old frame loop never saw the button enabled (#3703, #3705).
   await waitFor(`!!document.querySelector('[type=submit]') && !document.querySelector('[type=submit]').disabled`,'the settings to load',{timeout:10000,diag:`({status:(document.querySelector('.working-mode-status')||{}).textContent})`});
   results.push({name,stage:'workflow-initial',...await geometry()});await shot(`${name}-workflow`);
   await ev(`(() => {const f=document.querySelector('form.working-mode-form');f.elements.preset_instructions.value='Unsaved preset draft';f.elements.custom_instructions.value='Unsaved extra';document.querySelector('[data-settings-tab=workflow]').focus()})()`);
   await Input.dispatchKeyEvent({type:'keyDown',key:'ArrowRight',code:'ArrowRight'});await Input.dispatchKeyEvent({type:'keyUp',key:'ArrowRight',code:'ArrowRight'});
   assert.equal(await ev(`document.activeElement.dataset.settingsTab`),'prompts');
   results.push({name,stage:'prompts-top',...await geometry()});await shot(`${name}-prompts-top`);
   // One read-only page (UI audit #5589): no collapsibles, no inner scroll boxes, one role at a time chosen by a select.
   assert.equal(await ev(`[...document.querySelectorAll('.prompt-inspection-content details')].filter(d => d.open || !d.classList.contains('prompt-inspection-folded')).length`),0,'only the folded blocks are details, closed to begin with');
   assert.deepEqual(await ev(`[...document.querySelectorAll('.prompt-inspection-content pre')].filter(p => getComputedStyle(p).overflowY !== 'visible').length`),0,'no scroll box inside the page');
   assert.deepEqual(await ev(`[...document.querySelectorAll('.prompt-inspection-card')].map(c => !c.hidden)`),[true,false,false]);
   await ev(`document.querySelector('.prompt-inspection-role').focus()`);
   await ev(`(() => { const s = document.querySelector('.prompt-inspection-role'); s.value = 'worker'; s.dispatchEvent(new Event('change', { bubbles: true })); })()`);
   assert.deepEqual(await ev(`[...document.querySelectorAll('.prompt-inspection-card')].map(c => !c.hidden)`),[false,true,false],'the select shows the role chosen');
   await ev(`document.querySelector('.working-mode-form').scrollTop=100000`);
   results.push({name,stage:'prompts-bottom',...await geometry()});await shot(`${name}-prompts-bottom`);
   // The all-Coral health switch is in Settings now (#5128): the team panel has only the Workflow and Prompt inspection tabs (and Health check), no Global tab.
   assert.equal(await ev(`!!document.querySelector('[data-settings-tab=global]') || !!document.querySelector('.working-mode-form').elements.board_health_monitor`),false);
   if(name==='desktop') {
    // A failed working-mode save says so and keeps what was typed.
    await ev(`(() => {
     window.reviewOriginalFetch=window.fetch;
     window.reviewSaveDone=new Promise(resolve=>window.reviewResolve=resolve);
     window.fetch=async (url,opts)=>{
      const mode=opts?.method==='PUT' && String(url).includes('/working-mode');
      const response=mode?new Response(JSON.stringify({error:'Injected save failure'}),{status:500,headers:{'Content-Type':'application/json'}}):await window.reviewOriginalFetch(url,opts);
      if(mode)setTimeout(window.reviewResolve,0);
      return response;
     };
     const f=document.querySelector('.working-mode-form');
     return window.reviewOriginalFetch('/api/board/navigation-review/working-mode').then(r=>r.json()).then(m=>{
      f.elements.mode.value=m.mode==='worktrees'?'shared_checkout':'worktrees';
      document.querySelector('.team-settings-actions button').click();
     });
    })()`);
    await ev(`window.reviewSaveDone`);
    await waitFor(`/Save failed/.test(document.querySelector('.working-mode-status').textContent)`,'the failure shown');
    const status=await ev(`document.querySelector('.working-mode-status').textContent`);
    assert.match(status,/Save failed: .*Injected save failure/);
    assert.equal(await ev(`document.querySelector('.working-mode-form').elements.preset_instructions.value`),'Unsaved preset draft');
    assert.equal(await ev(`document.querySelector('.working-mode-form').elements.custom_instructions.value`),'Unsaved extra');
    results.push({name,stage:'partial-save',failure:'mode',status});
    await ev(`window.fetch=window.reviewOriginalFetch`);
   }
   await ev(`document.querySelector('[data-settings-tab=workflow]').click()`);
   assert.equal(await ev(`document.querySelector('form.working-mode-form').elements.preset_instructions.value`),'Unsaved preset draft');
   assert.equal(await ev(`document.querySelector('form.working-mode-form').elements.custom_instructions.value`),'Unsaved extra');
   await ev(`document.querySelector('[data-settings-tab=workflow]').focus()`);
   for (const [key,want] of [['End','messaging'],['Home','workflow']] /* the Messaging tab (#5510, #5516) is the last tab now */) {
    await Input.dispatchKeyEvent({type:'keyDown',key,code:key});await Input.dispatchKeyEvent({type:'keyUp',key,code:key});
    assert.equal(await ev(`document.activeElement.dataset.settingsTab`),want);
   }
   await Input.dispatchKeyEvent({type:'keyDown',key:'Escape',code:'Escape',windowsVirtualKeyCode:27,nativeVirtualKeyCode:27});await Input.dispatchKeyEvent({type:'keyUp',key:'Escape',code:'Escape',windowsVirtualKeyCode:27,nativeVirtualKeyCode:27});

   results.push({name,stage:'escape',closed:!await ev(`document.querySelector('#team-working-mode-dialog')?.open || false`)});
   await ev(`document.querySelector('#team-working-mode-dialog header .modal-close-btn')?.click()`);
  }
  fs.writeFileSync('/tmp/coral-1273-geometry.json',JSON.stringify(results,null,2));
  console.log(JSON.stringify(results,null,2));
  for(const row of results.filter(x=>x.header)) {
   for(const control of ['header','tabs','save','close']) assert.equal(row[control].visible,true,`${row.name}/${row.stage}/${control}`);
   assert.equal(row.horizontalOverflow,false);
  }
  console.log('PASS: controls reachable, prompt disclosure, keyboard tabs, no Global tab (#5128), failed-save feedback and edit preservation. Escape status recorded separately.');
 } finally {fs.writeFileSync('/tmp/coral-1273-geometry.json',JSON.stringify(results,null,2));await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message))); await client.close();}
})().catch(e=>{console.error(e);process.exit(1)});
