const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
if (/:8420(\/|$)/.test(BASE)) throw new Error('Use an isolated test server');
(async () => {
 const client = await CDP({port:Number(process.env.CDP_PORT || 9222)});
 const {Page,Runtime,Emulation}=client;
 const ev=async expression=>{const r=await Runtime.evaluate({expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw new Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 try {
  await Page.enable();await Page.navigate({url:BASE});await Page.loadEventFired();
  const response=await ev(`fetch('/api/board/empty-availability-test/status').then(async r=>({status:r.status,body:await r.json()}))`);
  assert.equal(response.status,200);assert.deepEqual(response.body.agents,[]);
  await ev(`(async()=>{
   const module=await import('/static/team_availability.js');window.openAvailability=module.showTeamAvailability;
   window.originalFetch=window.fetch;
   window.fetch=async()=>({ok:true,json:async()=>({summary:{available:1,busy:1},observed_at:new Date().toISOString(),agents:[{name:'<img src=x onerror="window.availabilityXSS=true">',agent_type:'codex',role:'Developer',availability:'available',available:true,reason:'Idle',tasks:[]},{name:'QA',availability:'busy',reason:'Has active work',tasks:[{scope:'board',id:12,status:'in_progress',title:'Test candidate'}]}],unassigned_tasks:[{scope:'board',id:13,status:'pending',title:'Release'}]})});
   openAvailability('Routing team');
  })()`);
  await ev(`new Promise(resolve=>setTimeout(resolve,100))`);
  assert.ok(await ev(`document.querySelector('#team-availability-dialog').textContent.includes('Test candidate')`));
  assert.equal(await ev(`Boolean(window.availabilityXSS)`),false);
  assert.ok(await ev(`document.querySelector('#team-availability-dialog').textContent.includes('Unassigned work')`));
  await Emulation.setDeviceMetricsOverride({width:390,height:844,deviceScaleFactor:1,mobile:true});
  assert.ok(await ev(`(()=>{const d=document.querySelector('#team-availability-dialog');return d.scrollWidth<=d.clientWidth+1 && d.getBoundingClientRect().width<=innerWidth})()`));
  await ev(`window.fetch=async()=>({ok:false,status:503});document.querySelector('.availability-toolbar button').click()`);
  await ev(`new Promise(resolve=>setTimeout(resolve,100))`);
  assert.ok(await ev(`document.querySelector('.availability-content').textContent.includes('503')`));
  await ev(`window.fetch=originalFetch;document.querySelector('.availability-toolbar button').click()`);
  await ev(`new Promise(resolve=>setTimeout(resolve,300))`);
  assert.ok(await ev(`document.querySelector('.availability-content').textContent.includes('No agents found')`));
  await ev(`document.querySelector('#team-availability-dialog header button').click()`);
  await ev(`new Promise(resolve=>setTimeout(resolve,50))`);
  assert.equal(await ev(`!!document.querySelector('#team-availability-dialog')`),false);
  console.log('PASS team availability API, rendering, mobile width, escaping, refresh/error, close');
 } finally {await Emulation.clearDeviceMetricsOverride();await client.close();}
})().catch(e=>{console.error(e);process.exit(1)});
