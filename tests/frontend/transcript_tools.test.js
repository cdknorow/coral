// Transcript sidebar tab: user messages and the agent's final reply are shown;
// everything in between folds into one collapsed "N steps" group. Inside it,
// tool calls are one-line rows (icon, name, description) whose command, diff
// and output are revealed on click. A reply stays visible only until more work
// arrives in the same turn. Same isolation pattern as the other suites: fetch
// and the coral WebSocket are stubbed; /chat answers slowly so the initial
// loads started by session select and tab switch overlap.
const CDP = require('chrome-remote-interface');
const fs = require('fs');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);
if (/:8420(\/|$)/.test(BASE) && !process.env.CORAL_TEST_ALLOW_PROD) { console.error('Refusing to run against the production port; use tests/frontend/run.sh.'); process.exit(2); }
const results = []; const check = (l, c, d) => { const v = c ? 'PASS' : 'FAIL'; results.push(v); console.log(`[${v}] ${l}${d !== undefined ? ' — ' + d : ''}`); };
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const SID = require('crypto').randomUUID();

const SESSIONS = [{ name: 'coral-go', display_name: 'Lead Dev', agent_type: 'claude', session_id: SID, summary: 'Working', working_directory: '/repo/coral-go' }];
const LONG = Array.from({ length: 40 }, (_, i) => `line ${i}`).join('\n');
const MESSAGES = [
  { type: 'tool_result', tool_use_id: 'orphan', tool_name: 'Read', content: 'from an older page' },
  { type: 'user', content: 'Please tidy the modal spacing.' },
  { type: 'assistant', text: 'Looking at the **CSS** first.', tool_uses: [
    { name: 'Bash', tool_use_id: 't1', description: 'Check modal section spacing', command: "cd /repo && grep -n '^\\.task-detail' agentic.css\ngit diff --stat" },
    { name: 'Edit', tool_use_id: 't2', input_summary: 'static/tasks.js', old_string: 'a', new_string: 'b' },
  ] },
  { type: 'tool_result', tool_use_id: 't1', content: LONG },
  { type: 'tool_result', tool_use_id: 't2', content: 'boom <script>window.__xss=1</script>', is_error: true },
  { type: 'assistant', text: '', tool_uses: [{ name: 'AskUserQuestion', tool_use_id: 't3', questions: [{ question: 'Which spacing?', options: [{ label: '16px', description: 'Roomy' }] }] }] },
  { type: 'assistant', text: 'Done — spacing fixed.' },
];

const STUB = `
  window.__sessions = ${JSON.stringify(SESSIONS)}; window.__msgs = ${JSON.stringify(MESSAGES)};
  (function(){ const Real = window.WebSocket; function Dead(u){ this.url=String(u); this.readyState=0; } Dead.prototype.send=function(){}; Dead.prototype.close=function(){}; Dead.prototype.addEventListener=function(){}; Dead.prototype.removeEventListener=function(){};
    function W(u,p){ if (/\\/ws\\//.test(String(u))) return new Dead(u); return p===undefined? new Real(u): new Real(u,p); } W.prototype=Real.prototype; W.CONNECTING=0; W.OPEN=1; W.CLOSING=2; W.CLOSED=3; window.WebSocket=W; })();
  window.__origFetch = window.fetch.bind(window);
  window.fetch = (url, opts) => { const u=String(url); const rel=u.replace(/^https?:\\/\\/[^/]+/,''); const path=rel.split('?')[0]; const qs=new URLSearchParams(rel.split('?')[1]||''); const m=((opts&&opts.method)||'GET').toUpperCase();
    const json=(b)=>Promise.resolve(new Response(JSON.stringify(b),{status:200,headers:{'Content-Type':'application/json'}}));
    if (m!=='GET') return json({ok:true});
    if (path==='/api/sessions/live') return json(window.__sessions);
    if (/^\\/api\\/sessions\\/live\\/[^/]+\\/chat$/.test(path)) { const after=parseInt(qs.get('after')||'0',10);
      const body={messages: window.__msgs.slice(after), total: window.__msgs.length, has_more: false}; return new Promise(r => setTimeout(r, 400)).then(() => json(body)); }
    if (/\\/resolve-path$/.test(path)) { window.__resolves = (window.__resolves||[]).concat([qs.get('filepath')]); return json({ filepath: 'resolved/' + qs.get('filepath').replace(/:.*$/, ''), line: 0 }); }
    if (/^\\/api\\/board\\//.test(path)) return json({});
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
  await ev(`Promise.resolve(window.selectLiveSession('coral-go', 'claude', ${JSON.stringify(SID)})).then(() => { window.setLiveViewMode('chat'); return true; })`);
  for (let i = 0; i < 30; i++) { if (await ev(`document.querySelectorAll('#live-history-messages .tool-call').length > 0`)) break; await sleep(100); }

  const C = `document.getElementById('live-history-messages')`;
  const top = () => ev(`Array.from(${C}.children).map(el => el.classList.contains('work-group')
      ? { kind: 'group', open: el.open, steps: el.querySelector('.work-group-count').textContent, latest: el.querySelector('.work-group-latest').textContent }
      : { kind: el.classList.contains('human') ? 'user' : 'reply', final: el.classList.contains('turn-final'), text: el.innerText.trim() })`);
  await sleep(1500);
  let t = await top();
  check('overlapping initial loads render the history once', t.filter(x => x.kind === 'user').length === 1, JSON.stringify(t.map(x => x.kind)));
  check('top level: group, user, group, final reply', JSON.stringify(t.map(x => x.kind)) === '["group","user","group","reply"]', JSON.stringify(t.map(x => x.kind)));
  check('groups are collapsed by default', t.filter(x => x.kind === 'group').every(g => !g.open));
  check('group summary counts steps and names the latest tool', t[2].steps === '4 steps' && t[2].latest === 'static/tasks.js', `${t[2].steps} / ${t[2].latest}`);
  check('orphan result from an older page is grouped', t[0].steps === '1 step' && t[0].latest === 'output', `${t[0].steps} / ${t[0].latest}`);
  check('final reply is visible and marked final', t[3].final && t[3].text === 'Done — spacing fixed.');
  check('intermediate text, commands and output hidden while collapsed', await ev(`(() => { const x = ${C}.innerText; return x.includes('Please tidy the modal spacing.') && !x.includes('Looking at the') && !x.includes('git diff --stat') && !x.includes('line 39') && !x.includes('Which spacing?'); })()`));
  check('result HTML is escaped', await ev(`!window.__xss && !${C}.querySelector('script')`));

  await ev(`${C}.querySelectorAll('.work-group > summary')[1].click(); true`);
  await sleep(100);
  const calls = await ev(`Array.from(${C}.querySelectorAll('.work-group')[1].querySelectorAll('.tool-call')).map(d => ({ id: d.dataset.toolUseId, open: d.open, err: d.classList.contains('tool-call-error'),
      label: d.querySelector('.tool-call-label').textContent, bodyVisible: d.querySelector('.tool-call-body').checkVisibility(), h: d.getBoundingClientRect().height }))`);
  const byId = (id) => calls.find(c => c.id === id);
  check('opening the group shows intermediate text and the question', await ev(`(() => { const x = ${C}.innerText; return x.includes('Looking at the CSS first.') && x.includes('Which spacing?'); })()`));
  check('tool rows inside stay collapsed to one line', calls.length === 2 && calls.every(c => !c.open && !c.bodyVisible && c.h < 30), JSON.stringify(calls));
  check('Bash row labelled by its description', byId('t1') && byId('t1').label === 'Check modal section spacing');
  check('errored tool row is marked', byId('t2').err && !byId('t1').err);

  await ev(`${C}.querySelector('.tool-call[data-tool-use-id="t1"] > summary').click(); true`);
  await sleep(100);
  const opened = await ev(`(() => { const d = ${C}.querySelector('.tool-call[data-tool-use-id="t1"]'); return { open: d.open, text: d.querySelector('.tool-call-body').innerText }; })()`);
  check('clicking a tool row reveals its command and full output', opened.open && opened.text.includes('git diff --stat') && opened.text.includes('line 39'));

  // Live polling: a reply shown as final folds into the group once more work arrives.
  await ev(`window.__msgs.push({ type: 'user', content: 'Now the header.' }, { type: 'assistant', text: 'Interim note.' }); true`);
  await sleep(1500);
  t = await top();
  check('new reply appears as final', t[t.length - 1].final && t[t.length - 1].text === 'Interim note.', JSON.stringify(t[t.length - 1]));
  await ev(`window.__msgs.push({ type: 'assistant', text: '', tool_uses: [{ name: 'Read', tool_use_id: 't9', input_summary: 'header.css' }] }, { type: 'assistant', text: 'Header done.' }); true`);
  await sleep(1500);
  t = await top();
  check('earlier reply folds into a new group once work follows', JSON.stringify(t.slice(-3).map(x => x.kind)) === '["user","group","reply"]' && t[t.length - 2].steps === '2 steps' && t[t.length - 1].text === 'Header done.', JSON.stringify(t.slice(-3)));
  check('earlier groups keep their open state across polls', t[2].open === true);

  const shot = await Page.captureScreenshot({ format: 'png', clip: await ev(`(() => { const r = ${C}.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: Math.min(r.height, 900), scale: 1 }; })()`) });
  if (process.env.SHOT) fs.writeFileSync(process.env.SHOT, Buffer.from(shot.data, 'base64'));

  // Links and file references
  const REF_TEXT = 'See `coral-go/static/render.js:1558` and `CLAUDE.md`, not `window.fetch` or `v1.1.5`. Docs: [site](https://example.com/x) and [local](static/app.js).';
  await ev(`window.__opened = []; window.openFilePreview = (p) => window.__opened.push(p); window.__msgs.push({ type: 'assistant', text: ${JSON.stringify(REF_TEXT)} }); true`);
  for (let i = 0; i < 30; i++) { if (await ev(`!!${C}.querySelector('.chat-file-ref')`)) break; await sleep(100); }
  const refs = await ev(`(() => { const last = Array.from(${C}.querySelectorAll('.chat-bubble.assistant')).pop();
    return { refs: Array.from(last.querySelectorAll('[data-file-ref]')).map(e => e.dataset.fileRef),
      ext: (a => a && { target: a.target, rel: a.rel })(last.querySelector('a[href^="https://"]')) }; })()`);
  check('path-looking inline code and relative links become file refs; identifiers and versions do not', JSON.stringify(refs.refs) === JSON.stringify(['coral-go/static/render.js:1558', 'CLAUDE.md', 'static/app.js']), JSON.stringify(refs.refs));
  check('web links open in a new tab without opener access', refs.ext && refs.ext.target === '_blank' && /noopener/.test(refs.ext.rel), JSON.stringify(refs.ext));
  await ev(`Array.from(${C}.querySelectorAll('[data-file-ref]')).find(e => e.dataset.fileRef.startsWith('coral-go')).click(); true`);
  await sleep(300);
  const refClick = await ev(`({ resolves: window.__resolves, opened: window.__opened, url: location.pathname })`);
  check('clicking a file ref resolves it and opens the Files preview', JSON.stringify(refClick.resolves) === '["coral-go/static/render.js:1558"]' && JSON.stringify(refClick.opened) === '["resolved/coral-go/static/render.js"]' && refClick.url === '/', JSON.stringify(refClick));

  // Chat is the default center view
  const dflt = await ev(`(() => { localStorage.removeItem('coral-live-view-mode'); return import('/static/live_chat.js').then(m => { m.applyLiveViewMode(); return { mode: m.getLiveViewMode(), chat: document.getElementById('capture-wrapper').classList.contains('chat-mode'), toggle: !document.querySelector('.live-view-toggle').hidden }; }); })()`);
  check('Chat is the default center view', dflt.mode === 'chat' && dflt.chat && dflt.toggle, JSON.stringify(dflt));

  // A sent message shows as Queued until the transcript records it
  await ev(`document.getElementById('command-input').value = 'Please also fix the header'; window.sendCommand(); true`);
  await sleep(300);
  const pend = await ev(`(() => { const p = ${C}.querySelector('.chat-bubble.human.pending'); const kids = Array.from(${C}.children); return { shown: !!p, label: p && p.querySelector('.pending-label').textContent, tail: kids.slice(-2).map(e => e.className), text: p && p.querySelector('.message-text').textContent.trim() }; })()`);
  check('a message sent to an idle agent appears at once as Sent, with Working below it', pend.shown && pend.label === 'Sent' && pend.text === 'Please also fix the header' && JSON.stringify(pend.tail) === '["pending-messages pending-sent","chat-working"]', JSON.stringify(pend));
  await ev(`window.__msgs.push({ type: 'assistant', text: 'Still working on the modal.' }); true`);
  await sleep(1500);
  const still = await ev(`({ pending: !!${C}.querySelector('.chat-bubble.human.pending'), last: ${C}.lastElementChild.classList.contains('pending-messages') })`);
  check('queued message stays at the bottom while the agent keeps replying', still.pending && still.last, JSON.stringify(still));
  await ev(`window.__msgs.push({ type: 'user', content: 'Please also fix the header', timestamp: new Date().toISOString() }); true`);
  await sleep(1500);
  const settled = await ev(`({ pending: ${C}.querySelectorAll('.chat-bubble.human.pending').length, real: Array.from(${C}.querySelectorAll('.chat-bubble.human:not(.pending)')).filter(b => b.textContent.includes('Please also fix the header')).length })`);
  check('once the transcript records it, the queued bubble is replaced by the real one', settled.pending === 0 && settled.real === 1, JSON.stringify(settled));

  // Messages queued while the agent is busy can land merged into one entry (interrupt)
  for (let i = 0; i < 3; i++) { await ev(`document.getElementById('command-input').value = 'test'; window.sendCommand(); true`); await sleep(100); }
  await sleep(200);
  check('three sends show three queued bubbles', (await ev(`${C}.querySelectorAll('.chat-bubble.human.pending').length`)) === 3);
  await ev(`window.__msgs.push({ type: 'user', content: 'test\\ntest', timestamp: new Date().toISOString() }); true`);
  await sleep(1500);
  check('a merged entry settles every queued message it contains, and only those', (await ev(`${C}.querySelectorAll('.chat-bubble.human.pending').length`)) === 1);
  await ev(`window.__msgs.push({ type: 'user', content: 'test', timestamp: new Date().toISOString() }); true`);
  await sleep(1500);
  check('the remaining queued message settles when it arrives', (await ev(`${C}.querySelectorAll('.chat-bubble.human.pending').length`)) === 0);

  // Working indicator: right after a send, and while the agent's state is Working
  const W = () => ev(`(() => { const w = ${C}.querySelector(':scope > .chat-working'); return w ? { shown: true, text: w.textContent.trim(), abovePending: !w.nextElementSibling || w.nextElementSibling.classList.contains('pending-messages') } : { shown: false }; })()`);
  let w = await W();
  check('after a send, a Working row shows before the agent has output anything', w.shown && /^Working/.test(w.text) && w.abovePending, JSON.stringify(w));
  await ev(`window.__msgs.push({ type: 'assistant', text: 'All three received.', timestamp: new Date().toISOString() }); true`);
  await sleep(1500);
  w = await W();
  check('the row goes away once the agent replies and is no longer working', !w.shown, JSON.stringify(w));
  await ev(`import('/static/state.js').then(st => { st.state.liveSessions.find(s => s.session_id === ${JSON.stringify(SID)}).working = true; window.__msgs.push({ type: 'assistant', text: '', tool_uses: [{ name: 'Bash', tool_use_id: 'tw', description: 'Run the test suite', command: 'npm test' }] }); return true; })`);
  await sleep(1500);
  w = await W();
  check('while the agent is Working the row shows and names the latest step', w.shown && /Run the test suite/.test(w.text), JSON.stringify(w));
  const groupsBefore = await ev(`${C}.querySelectorAll(':scope > .work-group').length`);
  for (const [id, desc] of [['tw2', 'Build the app'], ['tw3', 'Lint the code']]) {
    await ev(`window.__msgs.push({ type: 'assistant', text: '', tool_uses: [{ name: 'Bash', tool_use_id: '${id}', description: '${desc}', command: 'make' }] }, { type: 'tool_result', tool_use_id: '${id}', content: 'ok' }); true`);
    await sleep(1300);
  }
  const grp = await ev(`(() => { const gs = ${C}.querySelectorAll(':scope > .work-group'); const g = gs[gs.length - 1]; return { groups: gs.length, steps: g.querySelector('.work-group-count').textContent, rows: g.querySelectorAll('.tool-call').length, rowText: (${C}.querySelector(':scope > .chat-working') || {}).textContent }; })()`);
  check('tool calls arriving while Working is shown join the same group (no new little groups)', grp.groups === groupsBefore && grp.rows === 3 && /Lint the code/.test(grp.rowText || ''), JSON.stringify({ before: groupsBefore, ...grp }));
  await ev(`import('/static/state.js').then(st => { const r = st.state.liveSessions.find(s => s.session_id === ${JSON.stringify(SID)}); r.working = false; r.awaiting_user = true; return true; })`);
  await sleep(1500);
  w = await W();
  check('the row hides when the turn ends', !w.shown, JSON.stringify(w));

  // Sent while busy: Queued, below the Working row
  await ev(`import('/static/state.js').then(st => { const r = st.state.liveSessions.find(s => s.session_id === ${JSON.stringify(SID)}); r.working = true; r.awaiting_user = false; return true; })`);
  await ev(`document.getElementById('command-input').value = 'Then update the docs'; window.sendCommand(); true`);
  await sleep(300);
  const busy = await ev(`(() => { const q = ${C}.querySelector('.pending-queued .chat-bubble.pending'); const w = ${C}.querySelector(':scope > .chat-working'); return { label: q && q.querySelector('.pending-label').textContent, rowAbove: !!w && w.nextElementSibling === q.parentElement }; })()`);
  check('a message sent while the agent is working is Queued, below the Working row', busy.label === 'Queued' && busy.rowAbove, JSON.stringify(busy));

  // Esc: the interrupt shows as a note, and the queued message is now being worked on
  await ev(`import('/static/state.js').then(st => { const r = st.state.liveSessions.find(s => s.session_id === ${JSON.stringify(SID)}); r.working = false; r.awaiting_user = true; window.__msgs.push({ type: 'user', content: '[Request interrupted by user]', timestamp: new Date().toISOString() }); return true; })`);
  await sleep(1500);
  const intr = await ev(`(() => { const notes = Array.from(${C}.querySelectorAll('.chat-note')).map(n => n.textContent); const asUser = Array.from(${C}.querySelectorAll('.chat-bubble.human')).some(b => b.textContent.includes('Request interrupted'));
    const p = ${C}.querySelector('.chat-bubble.pending'); const w = ${C}.querySelector(':scope > .chat-working');
    return { notes, asUser, label: p && p.querySelector('.pending-label').textContent, rowBelow: !!w && !!p && w.previousElementSibling === p.parentElement }; })()`);
  check('an interrupt shows as an "Interrupted" note, not a user message', intr.notes.includes('Interrupted') && !intr.asUser, JSON.stringify(intr));
  check('after an interrupt the queued message flips to Sent and Working moves below it', intr.label === 'Sent' && intr.rowBelow, JSON.stringify(intr));
  await ev(`window.__msgs.push({ type: 'user', content: 'Then update the docs', timestamp: new Date().toISOString() }, { type: 'assistant', text: 'Docs updated.', timestamp: new Date(Date.now() + 1000).toISOString() }); true`);
  await sleep(1500);
  const done = await ev(`({ pending: ${C}.querySelectorAll('.chat-bubble.pending').length, working: !!${C}.querySelector(':scope > .chat-working') })`);
  check('once it lands and the agent replies, nothing is pending and Working is gone', done.pending === 0 && !done.working, JSON.stringify(done));

  // A plain terminal has no transcript: always Terminal, toggle hidden
  const term = await ev(`Promise.all([import('/static/state.js'), import('/static/live_chat.js')]).then(([st, m]) => {
    const prev = st.state.currentSession.agent_type; st.state.currentSession.agent_type = 'terminal'; m.applyLiveViewMode();
    const r = { chat: document.getElementById('capture-wrapper').classList.contains('chat-mode'), toggleHidden: document.querySelector('.live-view-toggle').hidden };
    st.state.currentSession.agent_type = prev; m.applyLiveViewMode(); return r; })`);
  check('plain terminals always show the terminal, with no toggle', !term.chat && term.toggleHidden, JSON.stringify(term));

  // History Chat tab renders the same transcript through the same renderer:
  // tool output folds into step groups instead of being shown as reply prose.
  const hist = await ev(`import('/static/render.js').then(r => { r.renderHistoryChat(window.__msgs); const H = document.getElementById('history-messages');
    const top = Array.from(H.children); return {
      kinds: top.map(e => e.classList.contains('work-group') ? 'group' : e.classList.contains('human') ? 'user' : e.classList.contains('assistant') ? 'reply' : e.tagName),
      outputInProse: Array.from(H.querySelectorAll(':scope > .chat-bubble.assistant')).some(b => /line 39|boom/.test(b.textContent)),
      toolRows: H.querySelectorAll('.work-group .tool-call').length,
      editBtns: H.querySelectorAll('.chat-bubble.human .edit-btn').length }; })`);
  check('history: tool output is never rendered as reply prose', !hist.outputInProse && hist.toolRows >= 2, JSON.stringify(hist));
  check('history: user turns keep Edit & Resubmit', hist.editBtns === hist.kinds.filter(k => k === 'user').length && hist.editBtns > 0, JSON.stringify(hist.kinds));

  await client.close();
  const failed = results.filter(r => r === 'FAIL').length;
  console.log(`\n${results.length - failed}/${results.length} passed`);
  process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error(e); process.exit(2); });
