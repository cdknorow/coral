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
    if (m==='POST' && /\\/answer-prompt$/.test(path)) { window.__answers = (window.__answers||[]).concat([JSON.parse(opts.body)]);
      return window.__answerFail ? Promise.resolve(new Response(JSON.stringify({ error: 'The prompt changed. Answer it in the terminal.' }), { status: 409, headers: { 'Content-Type': 'application/json' } })) : json({ ok: true }); }
    if (m!=='GET') return json({ok:true});
    if (path==='/api/sessions/live') return json(window.__sessions);
    if (/^\\/api\\/sessions\\/live\\/[^/]+\\/chat$/.test(path) && qs.get('limit')) window.__chatLimits = (window.__chatLimits||[]).concat([qs.get('limit')]);
    if (/^\\/api\\/sessions\\/live\\/[^/]+\\/chat$/.test(path)) { const after=parseInt(qs.get('after')||'0',10);
      const body={messages: window.__msgs.slice(after), total: window.__msgs.length, has_more: false}; return new Promise(r => setTimeout(r, 400)).then(() => json(body)); }
    if (/\\/pending-tool$/.test(path)) return json({ pending: window.__pendingTool || null });
    if (/\\/prompt-options$/.test(path)) return json(window.__promptScreen || { question: '', options: [] });
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
  check('history loads in pages of 400 messages', (await ev(`window.__chatLimits || []`)).every(l => l === '400') && (await ev(`(window.__chatLimits || []).length`)) > 0, JSON.stringify(await ev(`window.__chatLimits`)));
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

  // Chat mode: the command bar keeps just Cancel (Esc) and Send
  const bar = () => ev(`Array.from(document.querySelectorAll('#command-toolbar button')).filter(b => b.offsetParent !== null && !b.closest('.send-btn-menu')).map(b => (b.innerText || b.getAttribute('aria-label') || '').trim())`);
  const chatBar = await bar();
  check('in chat mode the command bar shows only Cancel and Send', JSON.stringify(chatBar) === JSON.stringify(['Cancel', '', '']) || (chatBar.includes('Cancel') && !chatBar.includes('Esc') && chatBar.length <= 3 && !chatBar.some(t => /Bash|Undo|\u2191|\u2193/.test(t))), JSON.stringify(chatBar));
  await ev(`window.setLiveViewMode('terminal'); true`);
  const termBar = await bar();
  check('terminal mode keeps the full command bar with Esc', termBar.includes('Esc') && !termBar.includes('Cancel') && termBar.length > 6, JSON.stringify(termBar));
  await ev(`window.setLiveViewMode('chat'); true`);

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
  check('while the agent is Working the row shows just "Working" (the step is in the group summary)', w.shown && w.text === 'Working', JSON.stringify(w));
  const groupsBefore = await ev(`${C}.querySelectorAll(':scope > .work-group').length`);
  for (const [id, desc] of [['tw2', 'Build the app'], ['tw3', 'Lint the code']]) {
    await ev(`window.__msgs.push({ type: 'assistant', text: '', tool_uses: [{ name: 'Bash', tool_use_id: '${id}', description: '${desc}', command: 'make' }] }, { type: 'tool_result', tool_use_id: '${id}', content: 'ok' }); true`);
    await sleep(1300);
  }
  const grp = await ev(`(() => { const gs = ${C}.querySelectorAll(':scope > .work-group'); const g = gs[gs.length - 1]; return { groups: gs.length, steps: g.querySelector('.work-group-count').textContent, rows: g.querySelectorAll('.tool-call').length, rowText: (${C}.querySelector(':scope > .chat-working') || {}).textContent }; })()`);
  check('tool calls arriving while Working is shown join the same group (no new little groups)', grp.groups === groupsBefore && grp.rows === 3 && grp.steps === '4 steps', JSON.stringify({ before: groupsBefore, ...grp }));
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

  // Needs input: an open question (PreToolUse) shows as a card in place of Working
  const setRow = (o) => ev(`import('/static/state.js').then(st => { Object.assign(st.state.liveSessions.find(s => s.session_id === ${JSON.stringify(SID)}), ${JSON.stringify(o)}); return true; })`);
  const card = () => ev(`(() => { const c = ${C}.querySelector(':scope > .chat-needs-input'); if (!c) return null;
    const t = (sel) => (c.querySelector(sel) || {}).textContent || '';
    return { kicker: t('.cni-kicker'), question: t('.cni-question'), text: c.textContent.replace(/\\s+/g, ' ').trim(),
      options: Array.from(c.querySelectorAll('.cni-options .cni-answer')).map(b => ({ n: b.dataset.n, label: b.querySelector('.cni-answer-label').textContent, desc: (b.querySelector('.cni-answer-desc') || {}).textContent || '', dflt: b.classList.contains('is-default') })),
      layout: c.querySelector('.cni-options-cards') ? 'cards' : c.querySelector('.cni-options-chips') ? 'chips' : '',
      textForm: (c.querySelector('.cni-text-form input') || {}).placeholder || '', chat: t('.cni-foot .cni-answer[data-action="chat"]'),
      foot: Array.from(c.querySelectorAll('.cni-foot .btn.cni-answer')).map(b => b.textContent), review: Array.from(c.querySelectorAll('.cni-review-row')).map(r => [r.querySelector('dt').textContent, r.querySelector('dd').textContent]),
      working: !!${C}.querySelector(':scope > .chat-working'), last: ${C}.lastElementChild === c }; })()`);
  await setRow({ working: true, awaiting_user: false, waiting_for_input: false });
  await ev(`window.__pendingTool = { tool_use_id: 'q1', tool_name: 'AskUserQuestion', input: { questions: [{ question: 'Which layout?', options: [{ label: 'Compact', description: 'Denser rows' }, { label: 'Roomy' }] }] } }; true`);
  await sleep(2600);
  let cd = await card();
  check('an open question shows as a card with the question up front, replacing the Working row', cd && cd.kicker === 'Claude is asking' && cd.question === 'Which layout?' && !cd.working && cd.last, JSON.stringify(cd));

  // Answer from the chat: every option on screen, numbered like the terminal's keys
  await ev(`window.__promptScreen = { question: 'Which layout?', options: [{ n: 1, label: 'Compact', selected: true, action: 'select' }, { n: 2, label: 'Roomy', action: 'select' }, { n: 3, label: 'Type something.', action: 'text' }, { n: 4, label: 'Chat about this', action: 'chat' }] }; true`);
  await sleep(2600);
  cd = await card();
  check('options show as numbered cards (they have descriptions), the default outlined', cd.layout === 'cards' && JSON.stringify(cd.options) === JSON.stringify([{ n: '1', label: 'Compact', desc: 'Denser rows', dflt: true }, { n: '2', label: 'Roomy', desc: '', dflt: false }]), JSON.stringify(cd.options));
  check('Type something is an inline field and Chat about this a footer link', cd.textForm === 'Or type your own answer…' && cd.chat === 'Chat about this instead', JSON.stringify({ textForm: cd.textForm, chat: cd.chat }));
  // A typed answer is sent with the Type something option's number
  await ev(`(() => { window.__answers = []; const f = ${C}.querySelector('.cni-text-form[data-n="3"]'); f.querySelector('input').value = 'Something in between'; f.requestSubmit(); return true; })()`);
  await sleep(300);
  const typed = await ev(`window.__answers`);
  check('a typed answer is sent with the Type something option', JSON.stringify(typed) === JSON.stringify([{ session_id: SID, agent_type: 'claude', n: 3, label: 'Type something.', text: 'Something in between' }]), JSON.stringify(typed));
  // Chat about this: its number, then the command box is ready for the reply
  await ev(`window.__answers = []; ${C}.querySelectorAll('.cni-answer, .cni-text-form input, .cni-text-form button').forEach(b => b.disabled = false); ${C}.querySelector('.cni-answer[data-n="4"]').click(); true`);
  await sleep(300);
  const chat = await ev(`({ answers: window.__answers, focused: document.activeElement && document.activeElement.id })`);
  check('Chat about this sends its number and focuses the command box', chat.answers.length === 1 && chat.answers[0].n === 4 && chat.focused === 'command-input', JSON.stringify(chat));
  // Short options without descriptions are compact chips
  await ev(`window.__pendingTool = { tool_use_id: 'q2', tool_name: 'AskUserQuestion', input: { questions: [{ question: 'Next?', options: [{ label: 'Release' }, { label: 'Polish' }] }] } }; window.__promptScreen = { question: 'Next?', options: [{ n: 1, label: 'Release', selected: true, action: 'select' }, { n: 2, label: 'Polish', action: 'select' }] }; true`);
  await sleep(2600);
  cd = await card();
  check('short options without descriptions are chips', cd.layout === 'chips' && cd.options.length === 2, JSON.stringify(cd));
  // The review step: each question with its answer, Submit primary
  await ev(`window.__promptScreen = { question: 'Ready to submit your answers?', options: [{ n: 1, label: 'Submit answers', selected: true, action: 'select' }, { n: 2, label: 'Cancel', action: 'select' }], review: [{ question: 'Which layout?', answer: 'Roomy' }, { question: 'Which theme?', answer: 'Dark' }] }; true`);
  await sleep(2600);
  cd = await card();
  const primary = await ev(`!!${C}.querySelector('.cni-foot .btn-primary.cni-answer[data-n="1"]')`);
  check('the review step lists each question with its answer, with Submit as the primary action', cd.question === 'Ready to submit your answers?' && JSON.stringify(cd.review) === '[["Which layout?","Roomy"],["Which theme?","Dark"]]' && JSON.stringify(cd.foot) === '["Submit answers","Cancel"]' && primary && cd.options.length === 0, JSON.stringify(cd));
  await ev(`window.__promptScreen = { question: 'Which layout?', options: [{ n: 1, label: 'Compact', selected: true, action: 'select' }, { n: 2, label: 'Roomy', action: 'select' }] }; true`);
  await sleep(2600);
  await ev(`window.__answers = []; window.__answerFail = true; ${C}.querySelector('.cni-answer[data-n="2"]').click(); true`);
  await sleep(500);
  const refused = await ev(`({ answers: window.__answers, disabled: Array.from(${C}.querySelectorAll('.cni-answer')).some(b => b.disabled) })`);
  check('a refused answer re-enables the buttons', refused.answers.length === 1 && !refused.disabled, JSON.stringify(refused));
  await ev(`window.__answerFail = false; window.__answers = []; ${C}.querySelector('.cni-answer[data-n="2"]').click(); true`);
  await sleep(300);
  const sent = await ev(`({ answers: window.__answers, disabled: Array.from(${C}.querySelectorAll('.cni-answer')).every(b => b.disabled) })`);
  check('clicking an option sends its number and label, and locks the buttons', JSON.stringify(sent.answers) === JSON.stringify([{ session_id: SID, agent_type: 'claude', n: 2, label: 'Roomy', text: '' }]) && sent.disabled, JSON.stringify(sent));
  await ev(`window.__promptScreen = null; true`);

  // Open terminal switches this view without changing the saved Chat default
  const openTerm = await ev(`(() => { const before = localStorage.getItem('coral-live-view-mode'); ${C}.querySelector('.chat-needs-input .cni-foot button[onclick]').click(); const r = { chat: document.getElementById('capture-wrapper').classList.contains('chat-mode'), saved: localStorage.getItem('coral-live-view-mode'), before }; window.setLiveViewMode('chat'); return r; })()`);
  check('Open terminal shows the terminal without changing the saved view choice', !openTerm.chat && openTerm.saved === openTerm.before, JSON.stringify(openTerm));

  // A permission prompt: the Notification sets waiting_for_input; PreToolUse says what for
  await setRow({ working: false, waiting_for_input: true, waiting_summary: 'Notification: Claude needs your permission to use Bash' });
  await ev(`window.__pendingTool = { tool_use_id: 'b1', tool_name: 'Bash', input: { command: 'rm -rf build', description: 'Clean the build folder' } }; true`);
  await sleep(2600);
  cd = await card();
  check('a permission prompt names the tool and shows the command', cd && cd.kicker === 'Permission needed · Bash' && /rm -rf build/.test(cd.text) && /Clean the build folder/.test(cd.text), JSON.stringify(cd));

  // Agents launched before the hook existed: the notification text alone
  await ev(`window.__pendingTool = null; true`);
  await sleep(2600);
  cd = await card();
  check('without hook details the card shows the notification text', cd && cd.question === 'Claude needs your permission to use Bash' && /Answer in the terminal/.test(cd.text), JSON.stringify(cd));
  // ...and the question the terminal shows, when it shows one (e.g. an AskUserQuestion from an agent without the hook)
  await ev(`window.__promptScreen = { question: 'Which color do you prefer?', options: [{ n: 1, label: 'Red', selected: true, action: 'select' }, { n: 2, label: 'Green', action: 'select' }] }; true`);
  await sleep(2600);
  cd = await card();
  check('without hook details the card still shows the on-screen question and options', cd && cd.question === 'Which color do you prefer?' && JSON.stringify(cd.options.map(o => o.label)) === '["Red","Green"]', JSON.stringify(cd));
  await ev(`window.__promptScreen = null; true`);

  await setRow({ waiting_for_input: false, waiting_summary: null, awaiting_user: true });
  await sleep(1500);
  check('the card goes away once the prompt is answered', (await card()) === null);

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
