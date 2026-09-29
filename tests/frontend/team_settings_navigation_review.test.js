const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL;
if (!BASE || /:8420(\/|$)/.test(BASE)) throw new Error('Isolated server required');
const watchdog=setTimeout(()=>{console.error('Review timeout');process.exit(1)},45000);watchdog.unref();
(async () => {
 const client = await CDP({port:Number(process.env.CDP_PORT)});
 const {Page, Runtime, Emulation, Input} = client;
 const ev = async expression => {const r=await Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw new Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 const results=[];
 const shot=async name=>fs.writeFileSync(`/tmp/coral-1273-${name}.png`,Buffer.from((await Page.captureScreenshot()).data,'base64'));
 const geometry=()=>ev(`(() => {
  const d=document.querySelector('#team-working-mode-dialog');
  const visible=selector=>{const e=d.querySelector(selector),r=e.getBoundingClientRect();return {visible:!!r.width&&!!r.height&&r.top>=0&&r.bottom<=innerHeight,top:r.top,bottom:r.bottom};};
  return {viewport:[innerWidth,innerHeight],dialog:{height:d.clientHeight,scrollHeight:d.scrollHeight,scrollTop:d.scrollTop},header:visible('header'),tabs:visible('[role=tablist]'),save:visible('[type=submit]'),close:visible('header button'),promptDetails:d.querySelectorAll('.prompt-inspection-content details').length,horizontalOverflow:document.documentElement.scrollWidth>innerWidth||d.scrollWidth>d.clientWidth};
 })()`);
 try {
  await Page.enable();const ready=Page.domContentEventFired();await Page.navigate({url:BASE});await ready;console.log("DOM ready");
  await ev(`import('/static/team_working_mode.js').then(m=>window.openSettings=m.showTeamWorkingMode)`);
  for(const [name,width,height] of [['desktop',1280,800],['mobile',390,844]]) {
   await Emulation.setDeviceMetricsOverride({width,height,deviceScaleFactor:1,mobile:false});
   console.log(name,'opening');await ev(`window.openSettings('navigation-review')`);
   await ev(`new Promise((resolve,reject)=>{setTimeout(()=>reject(new Error("settings load timeout")),10000);const check=()=>{if(!document.querySelector('[type=submit]').disabled)resolve();else requestAnimationFrame(check)};check()})`);
   results.push({name,stage:'workflow-initial',...await geometry()});await shot(`${name}-workflow`);
   await ev(`(() => {const f=document.querySelector('form.working-mode-form');f.elements.preset_instructions.value='Unsaved preset draft';f.elements.custom_instructions.value='Unsaved extra';document.querySelector('[data-settings-tab=workflow]').focus()})()`);
   await Input.dispatchKeyEvent({type:'keyDown',key:'ArrowRight',code:'ArrowRight'});await Input.dispatchKeyEvent({type:'keyUp',key:'ArrowRight',code:'ArrowRight'});
   assert.equal(await ev(`document.activeElement.dataset.settingsTab`),'prompts');
   results.push({name,stage:'prompts-top',...await geometry()});await shot(`${name}-prompts-top`);
   assert.equal(await ev(`document.querySelectorAll('.prompt-inspection-content details[open]').length`),0);
   await ev(`document.querySelector('.prompt-inspection-content summary').focus()`);
   await Input.dispatchKeyEvent({type:'keyDown',key:'Enter',code:'Enter',windowsVirtualKeyCode:13,text:'\r'});
   await Input.dispatchKeyEvent({type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13,text:'\r'});
   assert.equal(await ev(`document.querySelector('.prompt-inspection-content details').open`),true);
   await ev(`document.querySelectorAll('.prompt-inspection-content details').forEach(d=>d.open=true);document.querySelector('.working-mode-form').scrollTop=100000`);
   results.push({name,stage:'prompts-bottom',...await geometry()});await shot(`${name}-prompts-bottom`);
   await ev(`document.querySelector('[data-settings-tab=global]').click()`);
   results.push({name,stage:'global',...await geometry()});await shot(`${name}-global`);
   await ev(`(() => {
    window.reviewOriginalFetch=window.fetch;
    window.reviewSaveDone=new Promise(resolve=>window.reviewResolve=resolve);
    window.fetch=async (...args)=>{const response=await window.reviewOriginalFetch(...args);if(args[0]==='/api/settings'&&args[1]?.method==='PUT')setTimeout(window.reviewResolve,0);return response};
    document.querySelector('.working-mode-form').elements.board_health_monitor.checked=true;
    document.querySelector('.team-settings-actions button').click();
   })()`);
   await ev(`window.reviewSaveDone`);console.log('save response received');
   assert.equal(await ev(`fetch('/api/settings').then(r=>r.json()).then(x=>x.settings.board_health_monitor)`),'true');
   await ev(`window.fetch=window.reviewOriginalFetch`);
   if(name==='desktop') {
    for(const fail of ['mode','health','both']) {
     await ev(`(() => {
      window.reviewSaveDone=new Promise(resolve=>window.reviewResolve=resolve);
      window.fetch=async (url,opts)=>{
       const failure=opts?.method==='PUT' && (('${fail}'==='mode'||'${fail}'==='both')&&url.includes('/working-mode')||('${fail}'==='health'||'${fail}'==='both')&&url==='/api/settings');
       const response=failure?new Response(JSON.stringify({error:'Injected save failure'}),{status:500,headers:{'Content-Type':'application/json'}}):await window.reviewOriginalFetch(url,opts);
       if(url==='/api/settings'&&opts?.method==='PUT')setTimeout(window.reviewResolve,0);
       return response;
      };
      document.querySelector('.team-settings-actions button').click();
     })()`);
     await ev(`window.reviewSaveDone`);console.log('save response received');
     const status=await ev(`document.querySelector('.working-mode-status').textContent`);
     assert.match(status,fail==='mode'?/Working mode save failed/:fail==='health'?/Working mode saved.*Could not save global/:/Save failed/);
     assert.equal(await ev(`document.querySelector('.working-mode-form').elements.preset_instructions.value`),'Unsaved preset draft');
     assert.equal(await ev(`document.querySelector('.working-mode-form').elements.custom_instructions.value`),'Unsaved extra');
     results.push({name,stage:'partial-save',failure:fail,status});
     await ev(`window.fetch=window.reviewOriginalFetch`);
    }
   }
   await ev(`document.querySelector('[data-settings-tab=workflow]').click()`);
   assert.equal(await ev(`document.querySelector('form.working-mode-form').elements.preset_instructions.value`),'Unsaved preset draft');
   assert.equal(await ev(`document.querySelector('form.working-mode-form').elements.custom_instructions.value`),'Unsaved extra');
   await ev(`document.querySelector('[data-settings-tab=workflow]').focus()`);
   for (const [key,want] of [['End','global'],['Home','workflow']]) {
    await Input.dispatchKeyEvent({type:'keyDown',key,code:key});await Input.dispatchKeyEvent({type:'keyUp',key,code:key});
    assert.equal(await ev(`document.activeElement.dataset.settingsTab`),want);
   }
   await Input.dispatchKeyEvent({type:'keyDown',key:'Escape',code:'Escape',windowsVirtualKeyCode:27,nativeVirtualKeyCode:27});await Input.dispatchKeyEvent({type:'keyUp',key:'Escape',code:'Escape',windowsVirtualKeyCode:27,nativeVirtualKeyCode:27});

   results.push({name,stage:'escape',closed:!await ev(`document.querySelector('#team-working-mode-dialog')?.open || false`)});
   await ev(`document.querySelector('#team-working-mode-dialog header button')?.click()`);
  }
  fs.writeFileSync('/tmp/coral-1273-geometry.json',JSON.stringify(results,null,2));
  console.log(JSON.stringify(results,null,2));
  for(const row of results.filter(x=>x.header)) {
   for(const control of ['header','tabs','save','close']) assert.equal(row[control].visible,true,`${row.name}/${row.stage}/${control}`);
   assert.equal(row.horizontalOverflow,false);
  }
  console.log('PASS: controls reachable, prompt disclosure, keyboard tabs, Global persistence, partial-save feedback and edit preservation. Escape status recorded separately.');
 } finally {fs.writeFileSync('/tmp/coral-1273-geometry.json',JSON.stringify(results,null,2));await client.close();}
})().catch(e=>{console.error(e);process.exit(1)});
