const assert = require('node:assert/strict');
const {execFileSync} = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const sleep = ms => new Promise(r => setTimeout(r, ms));
(async () => {
 const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-ui-test-'));
 const bin = path.join(dir, 'coral-agent');
 execFileSync('go', ['build', '-o', bin, './cmd/coral-agent'], {cwd:path.resolve(__dirname, '../../coral-go')});
 const api = async (route, method='GET', body) => {
  const r = await fetch(BASE + route, {method,headers:{'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});
  if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);return r.json();
 };
 const launch = await api('/api/sessions/launch', 'POST', {working_dir:dir,agent_type:'terminal',display_name:'Agent UI POC test'});
 const sid = launch.session_id;
 const env = {...process.env, CORAL_URL:BASE, CORAL_SESSION_NAME:'terminal-'+sid};delete env.TMUX;
 const cli = args => JSON.parse(execFileSync(bin,['ui',...args],{env,encoding:'utf8'}));
 const client = await CDP({port:Number(process.env.CDP_PORT || 9222)});
 const {Page, Runtime, Emulation} = client;
 const ev = async (expression, contextId) => {
  const r = await Runtime.evaluate({expression,contextId,returnByValue:true,awaitPromise:true});
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));return r.result.value;
 };
 // Condition waits (#3118). untilEvent polls the panel's events through the CLI (the panel reports asynchronously).
 const { waitFor, nextFrame, coralReady } = require('./helpers/wait').bind(ev);
 const untilEvent = async (pred, what, ms = 5000) => { const end = Date.now() + ms; let e = [];
  while (Date.now() < end) { e = cli(['events','--id','demo']); if (e.some(pred)) return e; await sleep(50); }
  return e; };
 try {
  const file = path.join(dir,'panel.html');
  fs.writeFileSync(file,`<style>body{margin:0;color:white;background:#222}button{padding:16px}</style><button id="choose" onclick="coralUI.emit('choose',{option:'A'})">Choose A</button><script>
  (async()=>{let parentBlocked=false,networkBlocked=false;try{parent.document.body.dataset.uiEscaped='yes'}catch(e){parentBlocked=true}try{await fetch('/api/sessions/live')}catch(e){networkBlocked=true}await coralUI.emit('isolation',{parentBlocked,networkBlocked});})();</script>`);
  const published=cli(['publish','--id','demo','--title','<b>Demo</b>','--file',file]);
  assert.equal(published.revision,1);assert.equal(cli(['list']).length,1);
  await Page.enable();await Runtime.enable();await Page.navigate({url:BASE});await Page.loadEventFired();await coralReady();
  await ev(`(async()=>{const {state}=await import((window.__coralStatic||'/static/')+'state.js');state.currentSession={type:'live',session_id:${JSON.stringify(sid)}};document.getElementById('live-session-view').style.display='';const ui=await import((window.__coralStatic||'/static/')+'agent_ui.js');await ui.refreshAgentUI();window.switchAgenticTab('agent-ui','top');})()`);
  await waitFor(`!!document.querySelector('.agent-ui-home-row')`, 'the panel list');
  await ev(`document.querySelector('.agent-ui-home-row').click()`);
  let events=await untilEvent(e=>e.action==='isolation', 'the isolation report from the panel');
  assert.ok(events.some(e=>e.action==='isolation'&&e.payload.parentBlocked&&e.payload.networkBlocked),JSON.stringify(events));
  assert.equal(await ev(`document.body.dataset.uiEscaped`),undefined);
  assert.ok(await ev(`document.querySelector('.agent-ui-card strong').textContent.startsWith('<b>Demo</b> · v1')`));
  assert.equal(await ev(`document.querySelector('.agent-ui-card strong b')!==null`),false);
  assert.equal(await ev(`document.querySelector('.agent-ui-card iframe').getAttribute('sandbox')`),'allow-scripts');
  assert.equal(await ev(`getComputedStyle(document.querySelector('.agent-ui-card')).flexGrow`),'1');
  assert.equal(await ev(`getComputedStyle(document.querySelector('.agent-ui-card button')).borderRadius`),'6px');
  // The panel frame on screen: right after opening, an earlier frame's target can still be listed, so match the
  // card's current iframe URL exactly.
  const frameSrc=await ev(`document.querySelector('.agent-ui-card iframe').src`);
  let child;
  for (const end=Date.now()+5000; !child && Date.now()<end; await sleep(50)) child=(await client.Target.getTargets()).targetInfos.find(t=>t.type==='iframe'&&t.url===frameSrc);
  assert.ok(child,'isolated panel target exists');
  const attached=await client.Target.attachToTarget({targetId:child.targetId,flatten:true});
  // A panel may send one response per 500 ms (panel_sandbox createEventThrottle, minGapMs); its isolation report
  // was the last one, so a click inside that window is refused ('Too many responses; slow down.').
  await sleep(600); // sleep-ok: the panel's documented minimum gap between responses
  await client.send('Runtime.evaluate',{expression:"document.getElementById('choose').click()"},attached.sessionId);
  events=await untilEvent(e=>e.action==='choose', 'the choose event');
  await waitFor(`document.querySelector('.agent-ui-status')?.textContent.includes('Response saved')`, 'the response status', { soft: true });assert.ok(events.some(e=>e.action==='choose'&&e.payload.option==='A'));
  assert.ok(await ev(`document.querySelector('.agent-ui-status').textContent.includes('Response saved; agent not notified: session is not an agent')`));
  assert.equal(cli(['events','--id','demo','--after',String(events.at(-1).id)]).length,0);
  await ev(`window.testUIFrame=document.querySelector('.agent-ui-card iframe');import((window.__coralStatic||'/static/')+'agent_ui.js').then(m=>m.refreshAgentUI())`);
  assert.equal(await ev(`window.testUIFrame===document.querySelector('.agent-ui-card iframe')`),true);
  await Emulation.setDeviceMetricsOverride({width:390,height:844,deviceScaleFactor:1,mobile:true});
  await ev(`document.querySelector('.agent-ui-card button').click()`);
  await waitFor(`!!document.getElementById('agent-ui-expanded')?.open`, 'the expanded panel', { soft: true });
  assert.ok(await ev(`(()=>{const d=document.getElementById('agent-ui-expanded');return d.open&&d.getBoundingClientRect().width<=innerWidth})()`));
  await ev(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape'}))`);
  assert.equal(cli(['publish','--id','demo','--file',file]).revision,2);
  await ev(`import((window.__coralStatic||'/static/')+'agent_ui.js').then(m=>m.refreshAgentUI())`);
  assert.equal(await ev(`window.testUIFrame===document.querySelector('.agent-ui-card iframe')`),false);
  // Close is a browser-local dismissal, never a destructive API delete.
  await ev(`document.querySelector('.agent-ui-card .agent-ui-dismiss').click()`);
  await waitFor(`document.querySelector('.agent-ui-card')?.hidden === true`, 'the dismissed card to hide', { soft: true });
  assert.equal(await ev(`document.querySelector('.agent-ui-card')?.hidden`),true);
  assert.equal(cli(['list']).length,1);
  assert.ok(cli(['events','--id','demo']).length>0);
  assert.ok(await ev(`Boolean(document.querySelector('.agent-ui-home'))`));
  await ev(`import((window.__coralStatic||'/static/')+'agent_ui.js').then(m=>m.refreshAgentUI())`); await nextFrame(); // the refresh is awaited
  assert.equal(await ev(`document.querySelector('.agent-ui-card')?.hidden`),true,'poll must not restore a dismissed panel');
  await Page.reload();await Page.loadEventFired();await coralReady();
  await ev(`(async()=>{const {state}=await import((window.__coralStatic||'/static/')+'state.js');state.currentSession={type:'live',session_id:${JSON.stringify(sid)}};await (await import((window.__coralStatic||'/static/')+'agent_ui.js')).refreshAgentUI();})()`);
  assert.equal(await ev(`document.querySelector('.agent-ui-card')?.hidden`),true,'dismissal survives reload');
  await ev(`document.querySelector('.agent-ui-home-row').click()`);
  await waitFor(`document.querySelectorAll('.agent-ui-card').length >= 1`, 'the reopened card', { soft: true });
  assert.equal(await ev(`document.querySelectorAll('.agent-ui-card').length`),1);
  await ev(`document.querySelector('.agent-ui-card button').click()`);
  await waitFor(`!!document.getElementById('agent-ui-expanded')?.open`, 'the expanded panel', { soft: true });
  await ev(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape'}))`);
  await waitFor(`!document.getElementById('agent-ui-expanded')`, 'the expanded panel to close', { soft: true });
  assert.equal(await ev(`Boolean(document.querySelector('.agent-ui-card') && !document.querySelector('.agent-ui-card').hidden && !document.getElementById('agent-ui-expanded'))`),true,'close expanded panel returns to the card');
  assert.equal(cli(['publish','--id','demo','--file',file]).revision,3);
  await ev(`import((window.__coralStatic||'/static/')+'agent_ui.js').then(m=>m.refreshAgentUI())`);
  assert.equal(await ev(`document.querySelectorAll('.agent-ui-card').length`),1,'a new revision resurfaces the panel');

  await ev(`(async()=>{const {state}=await import((window.__coralStatic||'/static/')+'state.js');state.currentSession=null;await (await import((window.__coralStatic||'/static/')+'agent_ui.js')).refreshAgentUI()})()`);
  assert.equal(await ev(`document.querySelectorAll('#agentic-panel-agent-ui iframe').length`),0);
  cli(['remove','--id','demo']);assert.deepEqual(cli(['list']),[]);
  console.log('PASS agent UI: CLI/API publish, events, cursor, revision replacement, escaping, iframe isolation, network denial, unchanged-frame preservation, mobile expansion, persistent close/restore, revision resurfacing, session switch, removal');
 } finally {
  await Emulation.clearDeviceMetricsOverride();await client.close();
  await api(`/api/sessions/live/${encodeURIComponent(launch.session_name)}/kill`,'POST',{session_id:sid,agent_type:'terminal'}).catch(()=>{});
  fs.rmSync(dir,{recursive:true,force:true});
 }
})().catch(e=>{console.error(e);process.exit(1)});
