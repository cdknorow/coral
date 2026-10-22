// #6115: the phone Hub nav reaches actual linked-computer History and Board.
// Two computers deliberately share chat/board IDs: source identity must survive
// selection, search, Back, and reload. Cloud CSP and tiers stay in force.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { safePaused } = require('./helpers/paused');
const { BASE } = require('./test_base');
const { framesJs } = require('./helpers/wait');
// Use the app's theme functions without the contrast helper's injected style
// element: the Hub's CSP correctly rejects such an inline stylesheet.
const setTheme = (ev, theme) => ev(`(async()=>{const m=await import((window.__coralStatic||'/static/')+'settings_core.js');m.clearCustomThemeVars();m.applyTheme(${JSON.stringify(theme)});await ${framesJs(2)};return true;})()`);
const HOST = new URL(BASE).host;
const CSP = ["default-src 'self'", "script-src 'self'", "style-src 'self'", "style-src-attr 'unsafe-inline'", "font-src 'self'", "img-src 'self' data: blob:", `connect-src 'self' ws://${HOST}`, "object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'"].join('; ');
(async () => {
 const { client, Page, Emulation, ev, waitFor } = await connect();
 const { Fetch, Network } = client;
 const out = process.env.CORAL_SCREENSHOT_DIR;
 const shot = async name => { if(out) { fs.mkdirSync(out,{recursive:true}); fs.writeFileSync(`${out}/${name}.png`,Buffer.from((await Page.captureScreenshot({format:'png'})).data,'base64')); } };
 const errors=[];
 client.Runtime.exceptionThrown(e=>errors.push(e.exceptionDetails.text));
 try {
  await Promise.all([Page.enable(),Network.enable()]);
  await Network.setBypassServiceWorker({bypass:true});
  await Emulation.setDeviceMetricsOverride({width:390,height:844,deviceScaleFactor:1,mobile:true});
  await Emulation.setTouchEmulationEnabled({enabled:true});
  await Emulation.setEmulatedMedia({features:[{name:'prefers-reduced-motion',value:'reduce'}]});
  await Fetch.enable({patterns:[{urlPattern:`${BASE}/*`,resourceType:'Document',requestStage:'Response'}]});
  safePaused(client,async e=>{
   if(e.responseStatusCode!==200)return Fetch.continueRequest({requestId:e.requestId});
   const b=await Fetch.getResponseBody({requestId:e.requestId});
   const html=(b.base64Encoded?Buffer.from(b.body,'base64').toString('utf8'):b.body).replace('"mode":"local"','"mode":"cloud"');
   await Fetch.fulfillRequest({requestId:e.requestId,responseCode:200,responseHeaders:(e.responseHeaders||[]).filter(h=>!/^content-(security-policy|length)$/i.test(h.name)).concat([{name:'Content-Security-Policy',value:CSP}]),body:Buffer.from(html).toString('base64')});
  });
  await Page.addScriptToEvaluateOnNewDocument({source:`(() => {
   window.__cspViolations=[];document.addEventListener('securitypolicyviolation',e=>window.__cspViolations.push(e.violatedDirective));
   const original=window.fetch; window.__remoteRequests=[];
   const computers=[{id:'m1',label:'Laptop',status:'online',online:true,access_mode:'view',local_mode:'launch',effective_mode:'view'},{id:'m2',label:'Desktop',status:'online',online:true,access_mode:'interact',local_mode:'launch',effective_mode:'interact'}];
   const json=x=>Promise.resolve(new Response(JSON.stringify(x),{headers:{'content-type':'application/json'}}));
   window.fetch=(u,o={})=>{
    const p=new URL(String(u),location.href); const m=(o.method||'GET').toUpperCase();
    if(p.pathname==='/auth/session')return json({authenticated:true,email:'tester@example.com',csrf:'test'});
    if(p.pathname==='/api/sessions/live')return json({sessions:[],servers:window.__noComputers?[]:computers});
    if(p.pathname==='/api/servers')return json({servers:computers});
    if(p.pathname==='/api/settings'&&m==='GET')return json({settings:{}});
    const match=p.pathname.match(/^\\/api\\/remote\\/([^/]+)(\\/api\\/.*)$/);
    if(!match)return original(u,o);
    const [_,server,path]=match;window.__remoteRequests.push({server,path,method:m,query:p.search});
    if(path==='/api/sessions/history' && server==='m1' && window.__holdHistory) return new Promise(resolve=>{window.__releaseHistory=()=>resolve(new Response(JSON.stringify({sessions:[{session_id:'same-chat',summary_title:'OLD LAPTOP RESULT'}],total:1}),{headers:{'content-type':'application/json'}}));});
    if(path==='/api/sessions/history')return json({sessions:[{session_id:'same-chat',summary_title:server==='m1'?'Laptop conversation':'Desktop conversation',source_type:'claude'}],total:1,page:1,page_size:50});
    if(path==='/api/sessions/history/search')return json({status:'complete',results:[{session_id:'same-chat',type:'agent',hits:[{role:'user',excerpt:'needle from '+server,locator:{session_id:'same-chat',message_index:0}}]}]});
    if(path==='/api/sessions/history/same-chat')return json({agent_type:'claude',messages:[{type:'user',content:'needle from '+server},{type:'assistant',text:'Scoped to this computer.'}],total:2,start:0});
    if(path.endsWith('/notes'))return json({notes_md:'Notes from '+server});
    if(path==='/api/token-usage')return json({totals:{}});
    if(path.includes('/changes'))return Promise.resolve(new Response('Changes from '+server));
    if(path==='/api/tags'||/\\/(tags|events|tasks|agent-notes|files)$/.test(path))return json([]);
    if(path.endsWith('/git'))return json({commits:[]});
    if(path==='/api/board/projects')return json([{project:'same-team',board_label:'Release team',message_count:1,subscriber_count:1}]);
    if(path.endsWith('/messages/all'))return json({messages:[{id:1,subscriber_id:'Operator',job_title:'Operator',content:'Board message from '+server,created_at:'2026-10-22T12:00:00Z'}],total:1,before_count:0});
    if(path.endsWith('/subscribers'))return json([{subscriber_id:'Operator',job_title:'Owner'}]);
    if(path.endsWith('/paused'))return json({paused:false});
    if(path.endsWith('/sleep-status'))return json({sleeping:false});
    return json({});
   };
  })();`});
  const boot=async path=>{await Page.navigate({url:BASE+path});await Page.loadEventFired();await waitFor(`typeof window.switchNavTab==='function' && !!window.__coralReady`,'dashboard');await ev(`window.__coralReady.startup('liveList','settings')`);};
  const menu=async()=>{await ev(`document.getElementById('nav-select').click();true`);await waitFor(`!document.getElementById('nav-more-menu').hidden`,'phone nav');};
  await boot('/');
  for(const theme of ['light','dark']){
   await setTheme(ev,theme);await menu();
   const names=await ev(`[...document.querySelectorAll('#nav-more-menu .nav-more-item')].map(x=>x.dataset.arg)`);
   assert.ok(names.includes('history')&&names.includes('board'),'Chats and Board in the phone nav');
   assert.ok(!names.includes('jobs')&&!names.includes('workflows'),'local-only automation remains hidden');
   await shot(theme+'-390-nav');
   await ev(`document.querySelector('#nav-more-menu [data-arg="history"]').click();true`);
   await waitFor(`document.getElementById('history-computer').checkVisibility() && document.getElementById('history-sessions-list').textContent.includes('Laptop conversation')`,'linked Laptop history');
   assert.ok(await ev(`location.pathname==='/chats/@m1'`),'source in list URL');
   assert.ok(await ev(`document.getElementById('history-computer').getBoundingClientRect().height>=44`),'computer picker touch target');
   await shot(theme+'-390-chats');
   await ev(`document.querySelector('#history-sessions-list [data-on-click="history-open"]').click();true`);
   await waitFor(`document.getElementById('history-messages').textContent.includes('needle from m1')`,'Laptop transcript');
   assert.ok(await ev(`location.pathname==='/chats/@m1/same-chat'`),'source in transcript URL');
   assert.equal(await ev(`[...document.querySelectorAll('[data-history-local-only]')].some(e=>e.checkVisibility())`),false,'Never edits are not offered');
   await shot(theme+'-390-transcript');
   await ev(`document.querySelector('.mobile-history-back').click();true`);
   await waitFor(`document.getElementById('history-computer').checkVisibility()`,'return to scoped list');
   await menu();await ev(`document.querySelector('#nav-more-menu [data-arg="board"]').click();true`);
   await waitFor(`document.querySelectorAll('#mb-projects-ul [data-project]').length===2`,'linked boards chooser');
   assert.ok(await ev(`document.getElementById('mb-projects-ul').textContent.includes('Laptop') && document.getElementById('mb-projects-ul').textContent.includes('Desktop')`),'board sources named');
   await shot(theme+'-390-boards');
   await ev(`document.querySelector('#mb-projects-ul [data-project="@m1/same-team"]').click();true`);
   await waitFor(`document.getElementById('mb-messages').textContent.includes('Board message from m1')`,'correct remote board');
   assert.ok(await ev(`document.getElementById('messageboard-title').textContent.includes('Laptop')`),'board computer stays named');
   await waitFor(`document.querySelector('[data-on-click=mb-post]').getAttribute('aria-disabled')==='true'`,'View tier marks Board send unavailable');
   await shot(theme+'-390-board');
   assert.deepEqual(await ev(`window.__cspViolations`),[],'hub CSP violations');
   assert.ok(await ev(`document.documentElement.scrollWidth<=innerWidth`),'no sideways scroll');
   await menu();await ev(`document.querySelector('#nav-more-menu [data-arg="history"]').click();true`);
  }
  await ev(`(()=>{const s=document.getElementById('history-computer');s.value='m2';s.dispatchEvent(new Event('change',{bubbles:true}));true;})()`);
  await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Desktop conversation') && location.pathname==='/chats/@m2'`,'switch source');
  await ev(`document.querySelector('#history-sessions-list [data-on-click="history-open"]').click();true`);
  await waitFor(`document.getElementById('history-messages').textContent.includes('needle from m2')`,'same id on Desktop');
  await boot('/chats/@m2/same-chat');
  await waitFor(`document.getElementById('history-messages').textContent.includes('needle from m2')`,'source survives direct-link reload');
  await ev(`document.querySelector('.mobile-history-back').click();true`);
  await waitFor(`document.getElementById('history-search').checkVisibility()`,'search list');
  await ev(`(()=>{const q=document.getElementById('history-search');q.value='needle';q.dispatchEvent(new Event('input',{bubbles:true}));true;})()`);
  await waitFor(`document.querySelector('.chat-search-hit')`,'scoped search');
  await ev(`document.querySelector('.chat-search-hit').click();true`);
  await waitFor(`document.getElementById('history-messages').textContent.includes('needle from m2')`,'scoped search hit');
  const calls=await ev(`window.__remoteRequests`);
  assert.ok(calls.some(x=>x.path.endsWith('/history/search')&&x.server==='m2'),'search goes to Desktop');
  assert.ok(calls.filter(x=>x.path.includes('/sessions/history')).every(x=>x.method==='GET'),'no Never history mutations');
  assert.ok(!calls.some(x=>x.server==='m1'&&x.method==='POST'&&x.path.endsWith('/subscribe')),'view-only Board does not subscribe');
  const before=await ev(`window.__remoteRequests.length`);
  assert.equal(await ev(`(async()=>{try{await (await import('/static/history_source.js')).historyFetch('/api/sessions/history/same-chat/notes',{method:'PUT',body:'{}'});return false;}catch{return true;}})()`),true,'programmatic Never edits refused before fetch');
  assert.equal(await ev(`window.__remoteRequests.length`),before,'Never request was not sent');
  assert.equal(await ev(`window.selectHistorySession('../../api/settings')`),false,'hostile remote history id is refused before requests');
  assert.equal(await ev(`window.__remoteRequests.length`),before,'hostile id sent no request');
  await ev(`document.querySelector('.mobile-history-back').click();true`);
  await waitFor(`document.getElementById('history-computer').checkVisibility()`,'source picker');
  await ev(`(()=>{document.getElementById('history-search').value='';document.getElementById('history-search').dispatchEvent(new Event('input',{bubbles:true}));window.__holdHistory=true;const s=document.getElementById('history-computer');s.value='m1';s.dispatchEvent(new Event('change',{bubbles:true}));true;})()`);
  await waitFor(`typeof window.__releaseHistory==='function'`,'held old computer request');
  await ev(`(()=>{const s=document.getElementById('history-computer');s.value='m2';s.dispatchEvent(new Event('change',{bubbles:true}));true;})()`);
  await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Desktop conversation')`,'new computer wins');
  await ev(`window.__releaseHistory();true`);await ev(framesJs(2));
  assert.ok(await ev(`document.getElementById('history-sessions-list').textContent.includes('Desktop conversation') && !document.getElementById('history-sessions-list').textContent.includes('OLD LAPTOP')`),'late old response does not overwrite the new computer');
  assert.deepEqual(errors,[],'no page exceptions');
  await Page.addScriptToEvaluateOnNewDocument({source:'window.__noComputers=true;'});
  await boot('/chats');
  await waitFor(`document.getElementById('history-computer').disabled && document.getElementById('history-sessions-list').textContent.includes('No computers online')`,'empty linked-computer state');
  assert.ok(!(await ev(`window.__remoteRequests`)).some(x=>x.path.includes('/sessions/history')),'no history request without an online computer');
  await ev(framesJs(2));
  console.log('PASS 390px Hub nav, source-scoped History/Board, shared IDs, reload, search, Never controls, light/dark, and tiered subscribe');
 }finally{await client.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
