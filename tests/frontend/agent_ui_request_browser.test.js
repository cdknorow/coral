// No real agent notifications: API fixtures exercise the shipped form/chat modules.
const assert=require('node:assert/strict');const fs=require('node:fs');const CDP=require('chrome-remote-interface');
const base=process.env.CORAL_URL;if(!base||/:8420(\/|$)/.test(base))throw Error('Use isolated server');
(async()=>{
 const c=await CDP({port:Number(process.env.CDP_PORT||9222)});
 const ev=async expression=>{const r=await c.Runtime.evaluate({expression:expression.includes("await ") && !expression.startsWith("(async") ? `(async()=>{${expression}})()` : expression,returnByValue:true,awaitPromise:true});if(r.exceptionDetails)throw Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 const settle=()=>ev('new Promise(r=>setTimeout(r,80))');
 const type=async text=>ev(`(()=>{const t=document.querySelector('.agent-ui-prompt');t.value=${JSON.stringify(text)};t.dispatchEvent(new Event('input',{bubbles:true}))})()`);
 const send=async()=>{await ev(`document.querySelector('.agent-ui-request').requestSubmit()`);await settle();};
 const status=()=>ev(`document.querySelector('.agent-ui-request-status').textContent`);
 const shot=async name=>{if(!process.env.CORAL_SCREENSHOT_DIR)return;fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR,{recursive:true});const {data}=await c.Page.captureScreenshot({format:'png'});fs.writeFileSync(process.env.CORAL_SCREENSHOT_DIR+'/'+name+'.png',Buffer.from(data,'base64'));};
 try{
 await c.Page.enable();await c.Emulation.setDeviceMetricsOverride({width:1100,height:850,deviceScaleFactor:1,mobile:false});
 await c.Page.addScriptToEvaluateOnNewDocument({source:"localStorage.removeItem('coral-pending-messages');sessionStorage.clear();"});await c.Page.navigate({url:base});await c.Page.loadEventFired();
 await ev(`(async()=>{
 sessionStorage.clear();localStorage.removeItem('coral-pending-messages');
 const {state}=await import('/static/state.js');window.s=state;window.ui=await import('/static/agent_ui.js');window.chat=await import('/static/live_chat.js');chat.stopLiveHistoryPoll();chat.resetLiveHistory();
 s.currentSession={type:'live',session_id:'a',name:'Agent A',agent_type:'claude',state:'idle'};s.liveSessions=[s.currentSession];
 window.posts=[];window.panels=[];window.messages=[];window.mode='success';window.pending=[];
 window.fetch=async(url,options={})=>{
 const u=new URL(url,location.origin);
 if(u.pathname==='/api/agent/ui-request'){
 const body=JSON.parse(options.body),sid=u.searchParams.get('session_id');posts.push({sid,...body});
 const notification='[Coral UI panel request '+body.request_id+'] '+body.request.split('\\n')[0]+'\\n<<<USER_REQUEST '+body.request_id+'>>>\\n'+body.request+'\\n<<<END_USER_REQUEST '+body.request_id+'>>>\\nBuilt-in generation instructions';
 const data={ok:true,delivered:true,duplicate:window.mode==='duplicate',request_id:body.request_id,session_id:sid,panel_id:'request-'+body.request_id,notification,...(window.mode==='warning'?{recorded:false,warning:'Sent, but delivery could not be recorded.'}:{})};window.sent=data;
 if(window.mode==='unknown')return new Response(JSON.stringify({delivered:false,delivery_unknown:true,retryable:false,error:'Delivery is unknown.'}),{status:502});
 if(window.mode==='in-progress')return new Response(JSON.stringify({delivered:false,in_progress:true,retryable:false,error:'Request is already being delivered.'}),{status:409});
 if(window.mode==='error')return new Response(JSON.stringify({delivered:false,retryable:true,error:'Terminal delivery failed. Retry the request.'}),{status:502});
 if(window.mode==='defer')return new Promise(r=>pending.push(()=>r(new Response(JSON.stringify(data)))));
 return new Response(JSON.stringify(data));
 }
 if(u.pathname==='/api/agent/ui')return new Response(JSON.stringify(panels));
 if(u.pathname.endsWith('/chat')){const m=messages.splice(0);return new Response(JSON.stringify({messages:m,total:m.length,has_more:false}));}
 return new Response(JSON.stringify({files:[]}));
 };
 const p=document.getElementById('agentic-panel-agent-ui');document.body.append(p);p.classList.add('active');p.style.cssText='display:flex;position:fixed;top:20px;left:20px;width:600px;height:760px;z-index:99999;background:var(--bg-primary);padding:16px';
 await ui.refreshAgentUI();
 })()`);
 assert.equal(await ev('posts.length'),0);assert.equal(await ev(`document.querySelector('.agent-ui-prompt').readOnly`),false);
 assert.equal(await ev(`document.querySelector('.agent-ui-send').disabled`),true);
 await type('Build an architecture diagram\nInclude service dependencies.');
 await ev(`window.input=document.querySelector('.agent-ui-prompt');input.focus();input.setSelectionRange(7,12);await ui.refreshAgentUI()`);
 assert.equal(await ev(`input===document.querySelector('.agent-ui-prompt')&&document.activeElement===input&&input.selectionStart===7`),true,'poll preserves focused textarea/caret');
 await ev(`s.currentSession={...s.currentSession,session_id:'b',name:'Agent B'};await ui.refreshAgentUI()`);
 assert.equal(await ev(`document.querySelector('.agent-ui-prompt').value`),'');await type('Agent B draft');
 await ev(`s.currentSession={...s.currentSession,session_id:'a',name:'Agent A'};await ui.refreshAgentUI()`);
 assert.match(await ev(`document.querySelector('.agent-ui-prompt').value`),/architecture/);assert.equal(await ev('posts.length'),0);
 await type('😀'.repeat(2100));assert.equal(await ev(`document.querySelector('.agent-ui-send').disabled`),true);await send();assert.equal(await ev('posts.length'),0);
 await type('Build an architecture diagram\nInclude service dependencies.');await shot('request-desktop');
 await ev(`Object.defineProperty(crypto,'randomUUID',{value:undefined,configurable:true});window.mode='defer';document.querySelector('.agent-ui-prompt').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',ctrlKey:true,bubbles:true}))`);await settle();
 await send();assert.equal(await ev('posts.length'),1,'duplicate submit blocked');assert.match(await ev('posts[0].request_id'),/^[a-f0-9]{32}$/,'HTTP fallback request ID');assert.equal(await ev(`document.querySelector('.agent-ui-prompt').disabled`),true);
 await ev(`await ui.refreshAgentUI()`);assert.equal(await ev(`document.querySelector('.agent-ui-send').disabled`),true);
 // Switching agents during delivery must not change another draft/status or chat.
 await ev(`s.currentSession={...s.currentSession,session_id:'b',name:'Agent B'};await ui.refreshAgentUI();pending.shift()()`);await settle();
 assert.equal(await ev(`document.querySelector('.agent-ui-prompt').value`),'Agent B draft');assert.equal(await status(),'');assert.equal(await ev(`document.querySelectorAll('#live-history-messages .pending').length`),0);
 await ev(`s.currentSession={...s.currentSession,session_id:'a',name:'Agent A'};await ui.refreshAgentUI();await chat.refreshLiveHistory()`);
 assert.match(await status(),/Request sent/);assert.equal(await ev(`document.querySelector('.agent-ui-prompt').value`),'');
 assert.equal(await ev(`document.querySelectorAll('#live-history-messages .human.pending').length`),0);
 assert.equal(await ev(`document.querySelector('#live-history-messages .pending .chat-system-label').textContent`),'Coral');
 assert.equal(await ev(`document.querySelector('#live-history-messages .pending .chat-system-text').textContent`),await ev(`sent.notification.split('\\n')[0]`));
 // A real transcript poll consumes exact pending notification, leaving one Coral notice.
 await ev(`messages=[{type:'user',content:sent.notification,timestamp:new Date().toISOString()}];await chat.refreshLiveHistory()`);
 assert.equal(await ev(`document.querySelectorAll('#live-history-messages .pending').length`),0);
 assert.equal(await ev(`document.querySelectorAll('#live-history-messages .chat-system').length`),1);
 assert.equal(await ev(`document.querySelector('#live-history-messages .chat-system-text').textContent`),await ev(`sent.notification.split('\\n')[0]`));
 assert.equal(await ev(`document.querySelector('#live-history-messages').textContent.includes('Built-in generation')`),false);
 await ev(`chat.addPendingMessage('a',sent.notification,Date.now()+1000)`);assert.equal(await ev(`document.querySelectorAll('#live-history-messages .pending').length`),0,'retry receipt must not duplicate an older transcript notice');
 // Failure preserves text and reuses request_id. Editing uses a fresh identity.
 await type('Build a <script>alert(1)</script> timeline');await ev(`window.mode='error'`);await send();
 assert.match(await status(),/Terminal delivery failed/);assert.match(await ev(`document.querySelector('.agent-ui-prompt').value`),/timeline/);
 await ev(`window.mode='duplicate'`);await send();assert.equal(await ev(`posts[1].request_id===posts[2].request_id`),true);assert.match(await status(),/Request sent/);
 assert.equal(await ev(`document.querySelectorAll('#live-history-messages script').length`),0);
 await ev(`messages=[{type:'user',content:sent.notification,timestamp:new Date().toISOString()}];await chat.refreshLiveHistory()`);
 await type('Another panel');await ev(`window.mode='error'`);await send();await type('Edited panel');await send();assert.equal(await ev(`posts[3].request_id!==posts[4].request_id`),true);
 // Transcript can arrive before the HTTP response; reconciliation must not duplicate it.
 await type('Fast panel');await ev(`window.mode='defer'`);await send();
 await ev(`messages=[{type:'user',content:sent.notification,timestamp:new Date().toISOString()}];await chat.refreshLiveHistory();pending.shift()()`);await settle();
 assert.equal(await ev(`document.querySelectorAll('#live-history-messages .pending').length`),0);
 await type('Warning panel');await ev(`window.mode='warning'`);await send();assert.match(await status(),/Request sent.*could not be recorded/);assert.equal(await ev(`document.querySelector('.agent-ui-send').textContent`),'Send request');assert.equal(await ev(`document.querySelector('.agent-ui-prompt').value`),'');
 await type('Uncertain panel');await ev(`window.mode='unknown'`);await send();assert.match(await status(),/could not be confirmed.*Check with your agent/);assert.equal(await ev(`document.querySelector('.agent-ui-send').disabled`),true);const blockedCount=await ev('posts.length');await send();assert.equal(await ev('posts.length'),blockedCount);assert.equal(await ev(`document.querySelector('.agent-ui-prompt').value`),'Uncertain panel');await type('Uncertain panel ');await send();assert.equal(await ev('posts.length'),blockedCount,'unchanged trimmed request cannot be resent');
 await type('Pending panel');await ev(`window.mode='in-progress'`);await send();assert.equal(await ev(`document.querySelector('.agent-ui-send').disabled`),true);assert.match(await status(),/Check with your agent/);const pendingCount=await ev('posts.length');await send();assert.equal(await ev('posts.length'),pendingCount);
 // Existing panel publication and Home navigation keep request form and draft.
 await type('Persistent draft');await ev(`panels=[{id:'demo',title:'Published panel',revision:1,event_count:0}];await ui.refreshAgentUI();window.frame=document.querySelector('.agent-ui-card iframe');await ui.refreshAgentUI()`);
 assert.equal(await ev(`frame===document.querySelector('.agent-ui-card iframe')`),true);
 assert.equal(await ev(`!!document.querySelector('.agent-ui-request')&&!!document.querySelector('.agent-ui-home-row')`),true);
 await ev(`document.querySelector('.agent-ui-home-row').click()`);await settle();await ev(`document.querySelector('.agent-ui-dismiss').click()`);await settle();assert.equal(await ev(`document.querySelector('.agent-ui-prompt').value`),'Persistent draft');
 await c.Emulation.setDeviceMetricsOverride({width:390,height:844,deviceScaleFactor:1,mobile:true});await ev(`document.getElementById('agentic-panel-agent-ui').style.width='350px'`);
 assert.equal(await ev(`(()=>{const r=document.querySelector('.agent-ui-request').getBoundingClientRect();return r.left>=0&&r.right<=390})()`),true);await shot('request-mobile');
 console.log('PASS panel request: no automatic sends, scoped stable drafts/caret, byte limit, keyboard/send/pending, switched delivery, retry identity, Coral chat/pending reconciliation including fast transcript, panel preservation, mobile');
 }finally{await c.close();}
})().catch(e=>{console.error(e);process.exitCode=1});
