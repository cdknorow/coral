// Draft harness for AGENT_LIST_COMPACT.md (items 1-19). Written against the
// spec's selectors before #150 lands; moved into tests/frontend and registered
// in run.sh once the implementation is in the tree. Uses the same isolation
// pattern as agent_bar_tweaks.test.js (pre-navigation fetch + WebSocket stub).
const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);
if (/:8420(\/|$)/.test(BASE) && !process.env.CORAL_TEST_ALLOW_PROD) { console.error('Refusing to run against the production port; use tests/frontend/run.sh.'); process.exit(2); }
const results = []; const check = (l, c, d) => { const v = c ? 'PASS' : 'FAIL'; results.push(v); console.log(`[${v}] ${l}${d !== undefined ? ' — ' + d : ''}`); };
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const U = () => require('crypto').randomUUID();
const A = U(), B = U(), C = U(), D = U(), E = U(), F = U();
const SESSIONS = [
  { name: 'coral-go', display_name: 'Lead Dev', agent_type: 'claude', session_id: A, summary: 'Refactoring the store layer', context_pct: 87, context_window: 1000000, working: true, working_directory: '/repo/coral-go', branch: 'feat/x', board_project: 'ui-team', board_job_title: 'Lead', token_input: 1200, token_output: 3400 },
  { name: 'coral-go', display_name: '', auto_name: 'Auto Bot', agent_type: 'claude', session_id: B, summary: '', first_prompt: 'Please fix the flaky websocket reconnect test', context_pct: 42, waiting_for_input: true, working_directory: '/repo/coral-go', board_project: 'ui-team' },
  { name: 'coral-go', display_name: '', agent_type: 'claude', session_id: C, summary: '', first_prompt: '', context_pct: null, context_window: null, not_started: true, working_directory: '/repo/coral-go', board_project: 'ui-team' },
  { name: 'coral-go', display_name: 'Sleeper', agent_type: 'claude', session_id: D, summary: 'Waiting for the next batch', sleeping: true, status: 'Sleeping', working_directory: '/repo/coral-go', board_project: 'ui-team' },
  { name: 'coral-go', display_name: '', agent_type: 'terminal', session_id: E, summary: '', first_prompt: '', context_pct: 0, working_directory: '/repo/coral-go' },
  { name: 'other-proj', display_name: 'Zed', agent_type: 'claude', session_id: F, summary: 'Writing docs', context_pct: 12, working_directory: '/repo/other-proj' },
];
const ORDER = SESSIONS.map(s => s.session_id).join(',');
const STUB = `
  window.__fixture = ${JSON.stringify(SESSIONS)}; window.__sendCalls = [];
  (function(){ const Real = window.WebSocket; function Dead(u){ this.url=String(u); this.readyState=0; } Dead.prototype.send=function(){}; Dead.prototype.close=function(){}; Dead.prototype.addEventListener=function(){}; Dead.prototype.removeEventListener=function(){};
    function W(u,p){ if (/\\/ws\\/coral(\\?|$)/.test(String(u))) return new Dead(u); return p===undefined? new Real(u): new Real(u,p); } W.prototype=Real.prototype; W.CONNECTING=0; W.OPEN=1; W.CLOSING=2; W.CLOSED=3; window.WebSocket=W; })();
  window.__origFetch = window.fetch.bind(window);
  window.fetch = (url, opts) => { const u=String(url); const path=u.replace(/^https?:\\/\\/[^/]+/,'').split('?')[0]; const m=((opts&&opts.method)||'GET').toUpperCase();
    const json=(b)=>Promise.resolve(new Response(JSON.stringify(b),{status:200,headers:{'Content-Type':'application/json'}}));
    if (path==='/api/sessions/live' && m==='GET') return json(window.__fixture);
    if (m!=='GET') { window.__sendCalls.push({path, body: opts && opts.body}); return json({ok:true}); }
    return window.__origFetch(url, opts); };`;
async function run() {
  const client = await CDP({ port: CDP_PORT }); const { Page, Runtime, Emulation } = client;
  await Promise.all([Page.enable(), Runtime.enable()]);
  await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
  const exceptions = []; Runtime.exceptionThrown(e => exceptions.push(((e.exceptionDetails || {}).exception || {}).description || 'exception'));
  const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
  const boot = async (w, h, mobile) => { await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); for (let i = 0; i < 50; i++) { if (await ev(`typeof window._coralSetLiveSessions === 'function'`)) break; await sleep(100); } await ev(`localStorage.setItem('coral-group-by-team','false'); window.switchNavTab('agents'); window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__fixture))); true`); await sleep(250); };
  const rows = () => ev(`Array.from(document.querySelectorAll('#live-sessions-list .session-group-item')).map(li => { const q = s => li.querySelector(s); const cs = el => el ? getComputedStyle(el) : null; const r = li.getBoundingClientRect();
    return { sid: li.dataset.sessionId, h: r.height, tabindex: li.getAttribute('tabindex'), role: li.getAttribute('role'), aria: li.getAttribute('aria-label'), current: li.getAttribute('aria-current'),
      avatar: !!q('.agent-avatar'), ctxBar: !!q('.session-context-bar'), dirChip: !!q('.agent-dir-chip'), inlineStatus: !!q('.session-inline-status'), activity: !!q('.session-activity-text'),
      dot: !!q('.session-dot'), identity: (q('.session-label')||{}).textContent, identityTitle: q('.session-label') ? q('.session-label').getAttribute('title') : null,
      pills: Array.from(li.querySelectorAll('.session-attention-pill, .session-ctx-pill')).map(p => p.textContent.trim()), goal: q('.session-goal') ? q('.session-goal').textContent.replace(/\\s+/g,' ').trim() : null, goalTitle: q('.session-goal') ? q('.session-goal').getAttribute('title') : null,
      kebabFirst: (() => { const m = q('.sidebar-kebab-menu'); const f = m ? Array.from(m.children).find(c => c.matches('.overflow-menu-item')) : null; return f ? f.textContent.trim() : null; })(),
      padding: cs(li).padding, hasUuidText: li.textContent.includes(li.dataset.sessionId) }; })`);
  try {
    await boot(1440, 900, false);
    let R = await rows(); const by = Object.fromEntries(R.map(r => [r.sid, r]));
    check('1 rows with goal are 40px ±1; terminal/ended rows 36px ±1', Math.abs(by[A].h - 40) <= 1 && Math.abs(by[E].h - 36) <= 1, JSON.stringify({ A: by[A].h, E: by[E].h }));
    check('1 no avatar / context bar / dir chip / inline status / activity text in rows', R.every(r => !r.avatar && !r.dirChip && !r.inlineStatus && !r.activity) && R.filter(r => r.ctxBar).length === 0, JSON.stringify(R.map(r => [r.avatar, r.ctxBar, r.dirChip, r.inlineStatus, r.activity])));
    check('2 line 1 = dot + D1 identity (+pill) + kebab; identity/goal carry title without uuid', R.every(r => r.dot) && by[B].identity.trim() === 'Auto Bot' && by[A].identityTitle === 'Lead Dev' && by[B].goalTitle && !/[0-9a-f]{8}-/.test(by[B].goalTitle) && R.every(r => !r.hasUuidText), JSON.stringify({ b: by[B].identity, at: by[A].identityTitle }));
    check('2 attention rows show exactly one text pill (Needs input / Check terminal)', by[B].pills.filter(p => /Needs input|Check terminal|Stuck/.test(p)).length === 1 && by[C].pills.filter(p => /Check terminal/.test(p)).length === 1 && by[A].pills.filter(p => /Needs input|Check terminal|Stuck/.test(p)).length === 0, JSON.stringify({ B: by[B].pills, C: by[C].pills, A: by[A].pills }));
    check('12/D-C ctx pill only at >=80% (A: ctx 87%), none at 42% or unknown', by[A].pills.some(p => /^ctx 87%$/.test(p)) && !by[B].pills.some(p => /^ctx/.test(p)) && !by[C].pills.some(p => /^ctx/.test(p)), JSON.stringify({ A: by[A].pills, B: by[B].pills, C: by[C].pills }));
    check('19/D-E sleeping row keeps goal line (40px)', by[D].goal === 'Waiting for the next batch' && Math.abs(by[D].h - 40) <= 1, JSON.stringify({ goal: by[D].goal, h: by[D].h }));
    check('D-F rows are focusable list items, not buttons, with aria-label identity + state', R.every(r => r.tabindex === '0' && r.role !== 'button') && /^Auto Bot, Needs input$/.test(by[B].aria || '') && /^Sleeper, Sleeping$/.test(by[D].aria || ''), JSON.stringify({ B: by[B].aria, D: by[D].aria, roles: R.map(r => r.role) }));
    check('14 Open Agent Tab is still the first kebab item', by[A].kebabFirst === 'Open Agent Tab', by[A].kebabFirst);
    check('11 order preserved (no-sort)', R.map(r => r.sid).join(',') === ORDER);
    // headers
    const hdr = await ev(`(() => { const t = document.querySelector('#live-sessions-list .session-board-card .session-board-header, #live-sessions-list .session-board-card > [class*="header"]'); const f = document.querySelector('#live-sessions-list .session-group-header'); const rect = el => el ? el.getBoundingClientRect().height : null; return { teamH: rect(t), teamText: t ? t.textContent.replace(/\\s+/g,' ').trim() : null, teamCount: !!(t && t.querySelector('.session-group-count')), teamDir: !!document.querySelector('#live-sessions-list .board-card-dir, #live-sessions-list .board-card-branch-line, #live-sessions-list .team-token-usage'), folderH: rect(f), folderCount: !!(f && f.querySelector('.session-group-count')), folderDir: !!(f && (f.querySelector('.group-dir-line') || /\\//.test(f.textContent))) }; })()`);
    check('3 team header = name + count, 28px, no dir/branch/token text', hdr.teamCount && !hdr.teamDir && hdr.teamH !== null && Math.abs(hdr.teamH - 28) <= 2, JSON.stringify(hdr));
    check('3 folder header = name + count, 24px', hdr.folderCount && hdr.folderH !== null && Math.abs(hdr.folderH - 24) <= 2 && !hdr.folderDir, JSON.stringify(hdr));
    // tooltip
    const tip = await ev(`(() => { const t = typeof window.buildSessionTooltip === 'function' ? window.buildSessionTooltip(window.__fixture[2]) : (document.querySelector('#live-sessions-list [data-session-id="${C}"]').getAttribute('title') || ''); return String(t); })()`);
    const tipText = tip.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ');
    check('12 tooltip Context row prints unknown for null context', /Context:?\s*unknown/i.test(tipText), tipText.slice(0, 120));
    const tipA = await ev(`(() => { const t = typeof window.buildSessionTooltip === 'function' ? window.buildSessionTooltip(window.__fixture[0]) : ''; return String(t); })()`);
    const tipAText = tipA.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ');
    check('4 tooltip has Context/Tokens/Branch/Last action rows for a known session', /Context:?\s*87%/.test(tipAText) && /Tokens/.test(tipAText) && /Branch/.test(tipAText) && /Last action/.test(tipAText), tipAText.slice(0, 160));
    // keyboard: Enter selects, Space no scroll, actions excluded
    await ev(`document.querySelector('#live-sessions-list [data-session-id="${B}"]').focus(); true`);
    await client.Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 }); await client.Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 }); await sleep(300);
    check('7 Enter on a focused row selects it (aria-current + header identity)', await ev(`document.querySelector('#live-sessions-list [data-session-id="${B}"]').getAttribute('aria-current') === 'true' && /Auto Bot/.test(document.getElementById('session-name').textContent)`));
    check('13 focus survives the re-render triggered by selection', await ev(`document.activeElement && document.activeElement.dataset && document.activeElement.dataset.sessionId === '${B}'`));
    const scrollBefore = await ev(`document.getElementById('live-sessions-list').scrollTop`);
    await client.Input.dispatchKeyEvent({ type: 'keyDown', key: ' ', code: 'Space', windowsVirtualKeyCode: 32 }); await client.Input.dispatchKeyEvent({ type: 'keyUp', key: ' ', code: 'Space', windowsVirtualKeyCode: 32 }); await sleep(200);
    check('16 Space on a row does not scroll the list', (await ev(`document.getElementById('live-sessions-list').scrollTop`)) === scrollBefore);
    await ev(`window.__sendCalls = []; const sp = document.querySelector('#live-sessions-list [data-session-id="${C}"] .sidebar-goal-btn'); sp.focus(); true`);
    await client.Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 }); await client.Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 }); await sleep(300);
    check('14 Enter on the sparkle triggers the goal action, not row selection', await ev(`document.querySelector('#live-sessions-list [data-session-id="${B}"]').getAttribute('aria-current') === 'true'`));
    // rename/state tick updates aria-label in place
    await ev(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [${JSON.stringify({ ...SESSIONS[1], display_name: 'Renamed Bot', waiting_for_input: false, working: true })}] }); true`); await sleep(200);
    R = await rows(); const b2 = R.find(r => r.sid === B);
    check('13 aria-label tracks rename + state tick', /^Renamed Bot, Working$/.test(b2.aria || '') && b2.pills.length === 0, JSON.stringify({ aria: b2.aria, pills: b2.pills }));
    check('11 order preserved after state flips', R.map(r => r.sid).join(',') === ORDER);
    // team details disclosure
    const td = await ev(`(async () => { const item = Array.from(document.querySelectorAll('#live-sessions-list .session-board-card .overflow-menu-item')).find(b => /Team details/i.test(b.textContent)); if (!item) return { item: false }; item.click(); await new Promise(r => setTimeout(r, 200)); const pop = document.querySelector('.team-details-popover, [role="dialog"].team-details'); const out = { item: true, open: !!pop && getComputedStyle(pop).display !== 'none', role: pop ? pop.getAttribute('role') : null, labelled: !!(pop && pop.getAttribute('aria-labelledby')), focusInside: !!(pop && pop.contains(document.activeElement)), text: pop ? pop.textContent.replace(/\\s+/g,' ').trim().slice(0, 160) : '' }; return out; })()`);
    check('18 Team details opens from the team kebab with dialog semantics and focus inside', td.item && td.open && td.role === 'dialog' && td.labelled && td.focusInside, JSON.stringify(td));
    await client.Input.dispatchKeyEvent({ type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 }); await client.Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 }); await sleep(200);
    check('18 Escape closes Team details and returns focus to the trigger', await ev(`(() => { const pop = document.querySelector('.team-details-popover, [role="dialog"].team-details'); const closed = !pop || getComputedStyle(pop).display === 'none'; const ae = document.activeElement; return closed && !!ae && (ae.matches('.sidebar-kebab-btn, .board-kebab-btn, [class*="kebab"]') || !!ae.closest('.session-board-card')); })()`));
    check('18 Team details text has no uuid/tmux/path leakage beyond the directory field', !/[0-9a-f]{8}-[0-9a-f]{4}-|claude-|terminal-/.test(td.text), td.text);
    check('exceptions none (desktop)', exceptions.length === 0, JSON.stringify(exceptions.slice(0, 2)));
    // phone
    await boot(390, 844, true);
    const m = await ev(`(() => { const li = document.querySelector('#mobile-session-list [data-session-id="${B}"]'); if (!li) return null; const q = s => li.querySelector(s); const k = q('.sidebar-kebab-btn, [class*="kebab"]'); const kr = k ? k.getBoundingClientRect() : null; const goal = q('.session-goal'); return { h: li.getBoundingClientRect().height, avatar: !!q('.agent-avatar'), activity: !!q('.session-activity-text'), chip: !!q('.session-status-chip'), unread: !!q('.session-mobile-meta-pill'), kebab: kr ? [kr.width, kr.height] : null, clamp: goal ? getComputedStyle(goal).webkitLineClamp : null, banner: q('.session-mobile-banner-row') ? getComputedStyle(q('.session-mobile-banner-row')).display : null }; })()`);
    check('8 phone card: no avatar, no elapsed text, status chip kept, 56px min, 44px kebab, 2-line goal clamp, banner visible', m && !m.avatar && !m.activity && m.chip && m.h >= 56 && m.kebab && m.kebab[0] >= 44 && m.kebab[1] >= 44 && String(m.clamp) === '2' && m.banner !== 'none', JSON.stringify(m));
    await ev(`document.querySelector('#mobile-session-list [data-session-id="${B}"]').focus(); true`);
    await client.Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 }); await client.Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 }); await sleep(300);
    check('14 cloned phone row: Enter selects (delegated handler survives cloneNode)', await ev(`/Auto Bot|Renamed Bot/.test(document.getElementById('session-name').textContent)`));
    check('14 cloned phone row: kebab first item is Open Agent Tab', await ev(`(() => { const m = document.querySelector('#mobile-session-list [data-session-id="${B}"] .sidebar-kebab-menu'); const f = m ? Array.from(m.children).find(c => c.matches('.overflow-menu-item')) : null; return f ? f.textContent.trim() : null; })()`) === 'Open Agent Tab');
  } finally { await client.close(); }
  const f = results.filter(v => v === 'FAIL').length; console.log(`\n${results.length - f}/${results.length} checks passed`); process.exit(f ? 1 : 0);
}
run().catch(e => { console.error('HARNESS ERROR:', e); process.exit(2); });
