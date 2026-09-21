// Transcript sidebar tab for a Codex agent, end to end: a real Codex rollout
// file (coral-go/internal/jsonl/testdata/codex_rollout.jsonl) is placed in the
// test server's isolated CODEX_HOME and read through the real /chat endpoint.
// Checks that Codex turns fold the same way Claude turns do: user message,
// one collapsed "N steps" group (commentary, exec_command, shell, apply_patch,
// view_image), then the final answer. Also appends to the rollout while the
// tab is open to check live polling folds a superseded reply into the group.
const CDP = require('chrome-remote-interface');
const fs = require('fs');
const os = require('os');
const path = require('path');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);
if (/:8420(\/|$)/.test(BASE) && !process.env.CORAL_TEST_ALLOW_PROD) { console.error('Refusing to run against the production port; use tests/frontend/run.sh.'); process.exit(2); }
const CODEX_HOME = process.env.CODEX_HOME;
if (!CODEX_HOME || path.resolve(CODEX_HOME) === path.join(os.homedir(), '.codex')) {
  console.error('CODEX_HOME must point at an isolated test directory; use tests/frontend/run.sh.'); process.exit(2);
}
const results = []; const check = (l, c, d) => { const v = c ? 'PASS' : 'FAIL'; results.push(v); console.log(`[${v}] ${l}${d !== undefined ? ' — ' + d : ''}`); };
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

const SID = require('crypto').randomUUID();
const FIXTURE = path.join(__dirname, '..', '..', 'coral-go', 'internal', 'jsonl', 'testdata', 'codex_rollout.jsonl');
const dayDir = path.join(CODEX_HOME, 'sessions', '2026', '09', '20');
const ROLLOUT = path.join(dayDir, `rollout-2026-09-20T10-00-00-${SID}.jsonl`);
fs.mkdirSync(dayDir, { recursive: true });
fs.copyFileSync(FIXTURE, ROLLOUT);
const append = (...rows) => fs.appendFileSync(ROLLOUT, rows.map(r => JSON.stringify(r) + '\n').join(''));

const SESSIONS = [{ name: 'game', display_name: 'Codex Dev', agent_type: 'codex', session_id: SID, summary: 'Working', working_directory: '/repo/game' }];

// Only the session list is stubbed; /chat goes to the real test server.
const STUB = `
  window.__sessions = ${JSON.stringify(SESSIONS)}; window.__chatFetches = 0;
  (function(){ const Real = window.WebSocket; function Dead(u){ this.url=String(u); this.readyState=0; } Dead.prototype.send=function(){}; Dead.prototype.close=function(){}; Dead.prototype.addEventListener=function(){}; Dead.prototype.removeEventListener=function(){};
    function W(u,p){ if (/\\/ws\\//.test(String(u))) return new Dead(u); return p===undefined? new Real(u): new Real(u,p); } W.prototype=Real.prototype; W.CONNECTING=0; W.OPEN=1; W.CLOSING=2; W.CLOSED=3; window.WebSocket=W; })();
  window.__origFetch = window.fetch.bind(window);
  window.fetch = (url, opts) => { const u=String(url); const rel=u.replace(/^https?:\\/\\/[^/]+/,''); const p=rel.split('?')[0]; const m=((opts&&opts.method)||'GET').toUpperCase();
    const json=(b)=>Promise.resolve(new Response(JSON.stringify(b),{status:200,headers:{'Content-Type':'application/json'}}));
    if (p==='/api/sessions/live' && m==='GET') return json(window.__sessions);
    if (/\\/chat$/.test(p)) window.__chatFetches++;
    return window.__origFetch(url, opts); };`;

async function run() {
  const client = await CDP({ port: CDP_PORT }); const { Page, Runtime, Emulation } = client;
  await Promise.all([Page.enable(), Runtime.enable()]);
  await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
  const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
  await Emulation.setDeviceMetricsOverride({ width: 1500, height: 950, deviceScaleFactor: 1, mobile: false });
  await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
  for (let i = 0; i < 50; i++) { if (await ev(`typeof window._coralSetLiveSessions === 'function' && typeof window.selectLiveSession === 'function'`)) break; await sleep(100); }
  await ev(`window.switchNavTab('agents'); window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); true`);
  await ev(`Promise.resolve(window.selectLiveSession('game', 'codex', ${JSON.stringify(SID)})).then(() => { window.setLiveViewMode('chat'); return true; })`);
  for (let i = 0; i < 40; i++) { if (await ev(`!!document.querySelector('#live-history-messages .work-group')`)) break; await sleep(100); }

  const C = `document.getElementById('live-history-messages')`;
  const top = () => ev(`Array.from(${C}.children).filter(el => !el.classList.contains('load-more-btn')).map(el => el.classList.contains('work-group')
      ? { kind: 'group', open: el.open, steps: el.querySelector('.work-group-count').textContent, latest: el.querySelector('.work-group-latest').textContent }
      : { kind: el.classList.contains('human') ? 'user' : 'reply', final: el.classList.contains('turn-final'), text: el.querySelector('.message-text').innerText.trim() })`);

  check('chat history came from the real /chat endpoint', (await ev(`window.__chatFetches`)) > 0);
  let t = await top();
  check('turn 1 folds into user, group, final; turn 2 has no group', JSON.stringify(t.map(x => x.kind)) === '["user","group","reply","user","reply"]', JSON.stringify(t.map(x => x.kind)));
  check('no duplicated messages from response_item copies', t.filter(x => x.kind === 'user').length === 2 && t.filter(x => x.kind === 'reply').length === 2);
  check('group is collapsed and counts six steps', t[1] && !t[1].open && t[1].steps === '6 steps', t[1] && t[1].steps);
  check('group summary names the latest tool (view_image path)', t[1] && t[1].latest === '/repo/game/shot.png', t[1] && t[1].latest);
  check('final answers are visible', t[2] && t[2].final && t[2].text.startsWith('Added index.html and styles.css.') && t[4].text === 'The page title is Snowy Mountains.');
  check('commentary and commands are hidden while collapsed', await ev(`(() => { const x = ${C}.innerText; return !x.includes("I'll inspect the project structure first.") && !x.includes('rg --files') && !x.includes('Snowy Mountains</title>'); })()`));

  await ev(`${C}.querySelector('.work-group > summary').click(); true`);
  await sleep(100);
  const calls = await ev(`Array.from(${C}.querySelector('.work-group').querySelectorAll('.tool-call')).map(d => ({ id: d.dataset.toolUseId || null, name: d.querySelector('.tool-call-name').textContent,
      label: d.querySelector('.tool-call-label').textContent, open: d.open, h: d.getBoundingClientRect().height,
      command: (d.querySelector('.tool-card-command')||{}).textContent || null, output: (d.querySelector('.tool-card-output')||{}).textContent || null,
      adds: d.querySelectorAll('.diff-line.diff-add').length, dels: d.querySelectorAll('.diff-line.diff-del').length }))`);
  const by = (n) => calls.find(c => c.name === n) || {};
  check('four Codex tool rows, collapsed to one line', calls.length === 4 && calls.every(c => !c.open && c.h < 30), JSON.stringify(calls.map(c => [c.name, c.h])));
  check('every tool output attached to its call (no orphan rows)', calls.every(c => c.id && c.output), JSON.stringify(calls.map(c => [c.name, !!c.output])));
  check('exec_command row labelled by its cmd', by('exec_command').label === 'pwd && rg --files | head -200' && by('exec_command').command === 'pwd && rg --files | head -200', by('exec_command').label);
  check('legacy shell argv shown as a command', by('shell').command === 'bash -lc ls -la' && /total 8/.test(by('shell').output), by('shell').command);
  check('apply_patch row lists files and renders the patch as a diff', by('apply_patch').label === 'index.html, styles.css' && by('apply_patch').adds >= 3 && by('apply_patch').dels === 1, JSON.stringify(by('apply_patch')));
  check('commentary visible once the group is opened', await ev(`${C}.innerText.includes("I'll inspect the project structure first.")`));

  // Live: a new turn streams in; its first reply looks final until work follows.
  const ts = (s) => `2026-09-20T10:${s}Z`;
  append({ timestamp: ts('02:00.000'), type: 'event_msg', payload: { type: 'user_message', message: 'Add falling snow.', images: [] } },
         { timestamp: ts('02:01.000'), type: 'event_msg', payload: { type: 'agent_message', message: "I'll add a particle system.", phase: 'commentary' } });
  let last;
  for (let i = 0; i < 30; i++) { t = await top(); last = t[t.length - 1]; if (last.text === "I'll add a particle system.") break; await sleep(200); }
  check('streamed reply shows as final while the turn is in progress', last.final && last.text === "I'll add a particle system.", JSON.stringify(last));

  append({ timestamp: ts('02:02.000'), type: 'response_item', payload: { type: 'function_call', name: 'exec_command', arguments: JSON.stringify({ cmd: 'npm test' }), call_id: 'call_live1' } },
         { timestamp: ts('02:03.000'), type: 'response_item', payload: { type: 'function_call_output', call_id: 'call_live1', output: 'ok' } },
         { timestamp: ts('02:04.000'), type: 'event_msg', payload: { type: 'agent_message', message: 'Snow is falling.', phase: 'final_answer' } });
  for (let i = 0; i < 30; i++) { t = await top(); if (t[t.length - 1].text === 'Snow is falling.') break; await sleep(200); }
  const tail = t.slice(-3);
  check('superseded reply folds into a new group once work follows', JSON.stringify(tail.map(x => x.kind)) === '["user","group","reply"]' && tail[1].steps === '2 steps' && tail[1].latest === 'npm test' && tail[2].text === 'Snow is falling.', JSON.stringify(tail));
  check('opened group stays open across polls', t[1].open === true);

  await client.close();
  const failed = results.filter(r => r === 'FAIL').length;
  console.log(`\n${results.length - failed}/${results.length} passed`);
  process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error(e); process.exit(2); });
