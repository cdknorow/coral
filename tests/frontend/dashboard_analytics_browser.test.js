// Shipped dashboard initialization, isolated network fixtures; no telemetry egress.
const assert=require('node:assert/strict');const CDP=require('chrome-remote-interface');
const base=process.env.CORAL_URL;if(!base||/:8420(\/|$)/.test(base))throw Error('Use isolated server');
(async()=>{
 const c=await CDP({port:Number(process.env.CDP_PORT||9222)});let script;
 const ev=async expression=>{const r=await c.Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 const wait=ms=>new Promise(r=>setTimeout(r,ms));
 const events=()=>ev('window.__events');
 async function page(mode='normal',popout=false){
  if(script)await c.Page.removeScriptToEvaluateOnNewDocument({identifier:script});
  const source=`window.__mode=${JSON.stringify(mode)};window.__events=[];window.__startup=false;window.__release=[];window.__order=[];window.__socketOpen=false;
  window.WebSocket=class {static OPEN=1;static CLOSED=3;constructor(){this.readyState=window.__socketOpen?1:0}send(){window.__order.push('ws')}close(){}addEventListener(){}removeEventListener(){}};
  const realFetch=window.fetch;
  window.fetch=async(url,options={})=>{const path=new URL(url,location.origin).pathname;const json=v=>new Response(JSON.stringify(v),{headers:{'Content-Type':'application/json'}});
   if(path==='/api/tracking/event'){__events.push(JSON.parse(options.body));__order.push('event:'+JSON.parse(options.body).event);if(window.__rejectTracking)throw Error('fixture tracking rejected');return json({ok:true});}
   if(path==='/api/sessions/live'){
    if(__mode==='sessions_http')return new Response('SECRET_RAW_ERROR',{status:503});
    if(__mode==='sessions_network')throw Error('SECRET_NETWORK_ERROR');
    if(__mode==='sessions_invalid')return json({wrong:'SECRET_PATH'});
    if(__mode==='deferred')return new Promise(r=>__release.push(()=>r(json([]))));
    return json([]);
   }
   if(path==='/api/system/status'){
    if(__mode==='status_http')return new Response('SECRET_RAW_ERROR',{status:503});
    if(__mode==='status_network')throw Error('SECRET_NETWORK_ERROR');
    if(__mode==='status_invalid')return json(null);
    return json({startup_complete:__startup,tmux_available:true});
   }
   if(path.endsWith('/send')){__order.push('http');return __mode==='send_failure'?new Response('SECRET_FAILURE',{status:503}):json({ok:true});}
   if(path==='/api/settings')return json({});
   if(path.startsWith('/api/'))return json([]);
   return realFetch(url,options);
  };
  ${popout?"document.addEventListener('DOMContentLoaded',()=>{document.body.dataset.entryMode='agent'},{once:true});":''}`;
  ({identifier:script}=await c.Page.addScriptToEvaluateOnNewDocument({source}));
  await c.Page.navigate({url:base});await c.Page.loadEventFired();await wait(250);
 }
 try{
  await c.Page.enable();await page('deferred');
  assert.deepEqual(await events(),[],'no readiness until list rendered and startup complete');
  await ev(`window.__startup=true`);await wait(2100);assert.deepEqual(await events(),[],'startup alone is insufficient');
  await ev(`window.__mode='normal';__release.splice(0).forEach(r=>r())`);await wait(150);
  const readyEvent=(await events())[0];assert.equal(readyEvent.event,'dashboard_ready');assert.match(readyEvent.props.page_id,/^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$/);assert.deepEqual(Object.keys(readyEvent.props),['page_id']);assert.deepEqual((await events())[1],{event:'dashboard_active_day',props:{}});
  await ev(`(async()=>{const a=await import('/static/dashboard_analytics.js');a.dashboardInitialized();a.dashboardSessionsLoaded(true);a.dashboardStartupComplete();document.dispatchEvent(new Event('visibilitychange'));const chat=await import('/static/live_chat.js');chat.renderTranscript([{type:'user',content:'SECRET_PROMPT'},{type:'assistant',text:'SECRET_RESPONSE'}],document.createElement('div'));})()`);
  assert.equal((await events()).filter(x=>x.event==='dashboard_ready').length,1,'repeated initialization adds no ready event');assert.ok((await events()).every(x=>['dashboard_ready','dashboard_active_day'].includes(x.event)),'historical transcripts add no prompt/response events');
  // UTC midnight while the same dashboard stays open (no date property leaves the page).
  await ev(`(()=>{const RealDate=Date;window.Date=class extends RealDate {constructor(...a){super(...(a.length?a:[RealDate.now()+86400000]))}static now(){return RealDate.now()+86400000}};document.dispatchEvent(new Event('visibilitychange'));document.dispatchEvent(new Event('visibilitychange'));})()`);
  assert.equal((await events()).filter(x=>x.event==='dashboard_active_day').length,4);
  await ev(`window.__rejectTracking=true;document.dispatchEvent(new Event('visibilitychange'))`);const rejectedDayCount=(await events()).length;await ev(`window.__rejectTracking=false`);await wait(50);assert.equal((await events()).length,rejectedDayCount,'no automatic replay on consent/delivery change');await ev(`document.dispatchEvent(new Event('visibilitychange'))`);assert.equal((await events()).length,rejectedDayCount+1,'new visibility retries current active day after rejected tracking');
  for(const [mode,code] of [['sessions_http','sessions_fetch_http'],['sessions_network','sessions_fetch_network'],['sessions_invalid','sessions_fetch_invalid'],['status_http','status_fetch_http'],['status_network','status_fetch_network'],['status_invalid','status_fetch_invalid']]){
   await page(mode);assert.deepEqual(await events(),[{event:'dashboard_failed',props:{code}}],mode);
   await ev(`(async()=>{const a=await import('/static/dashboard_analytics.js');a.dashboardFailed(${JSON.stringify(code)});a.dashboardFailed('SECRET_RAW_ERROR');})()`);
   assert.equal((await events()).length,1,'bounded fixed-code failures');
  }
  // Recovery is observable: failures do not permanently suppress readiness.
  await ev(`window.__mode='normal';window.__startup=true`);await wait(2200);
  assert.deepEqual((await events()).map(x=>x.event),['dashboard_failed','dashboard_ready','dashboard_active_day']);
  assert.notEqual((await events()).find(x=>x.event==='dashboard_ready').props.page_id,readyEvent.props.page_id,'reload creates a new ephemeral page ID');
  assert.ok(!(JSON.stringify(await events())).includes('SECRET'));
  await page('normal',true);await ev(`window.__startup=true`);await wait(2100);assert.deepEqual(await events(),[],'agent popout is not a dashboard load');
  for(const transport of ['ws','http','send_failure']){
   await page(transport);
   await ev(`(async()=>{const {state}=await import('/static/state.js');state.currentSession={type:'live',name:'test-agent',agent_type:'claude',session_id:'analytics-fixture'};window.controls=await import('/static/controls.js');document.getElementById('command-input').value='';await controls.sendCommand();})()`);
   assert.deepEqual(await events(),[],'empty composer has no intent');
   await ev(`(async()=>{if(${JSON.stringify(transport)}==='ws'){window.__socketOpen=true;(await import('/static/xterm_renderer.js')).connectTerminalWs('test-agent','claude','analytics-fixture')}window.__rejectTracking=true;document.getElementById('command-input').value='SECRET_PROMPT';await controls.sendCommand();})()`);
   assert.deepEqual(await events(),[{event:'prompt_submit_requested',props:{source:'dashboard_composer'}}]);
   const order=await ev('window.__order');assert.ok(order.indexOf('event:prompt_submit_requested')<order.indexOf(transport==='ws'?'ws':'http'),'intent precedes transport');
   await ev(`window.__rejectTracking=false`);await wait(50);assert.equal((await events()).length,1,'no automatic intent replay after opt-in');
   await ev(`document.getElementById('command-input').value='SECOND_SECRET';window.controls.sendCommand()`);await wait(50);assert.equal((await events()).length,2,'each explicit intent reaches server-owned once/install dedupe');
  }
  // Observability failure cannot throw into application initialization.
  await ev(`(async()=>{const a=await import('/static/dashboard_analytics.js?failure-test');document.body.dataset.entryMode='dashboard';a.beginDashboardAnalytics();window.fetch=()=>{throw Error('tracking unavailable')};a.dashboardFailed('init_failed');a.dashboardInitialized();a.dashboardSessionsLoaded(true);a.dashboardStartupComplete()})()`);
  console.log('PASS dashboard analytics: real initialization gates, once/load, visible UTC-day rollover, HTTP/network/invalid failure codes, recovery, popout exclusion, historical transcript exclusion, real composer WS/HTTP/failed-send intent, no raw payload, nonblocking delivery');
 }finally{await c.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
