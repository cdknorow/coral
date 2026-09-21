// Acceptance harness for the unified Agent state indicators (tasks #165-#167).
//
// One vocabulary everywhere: Working, Idle, Needs input, Check terminal, Stuck,
// Your turn, Sleeping, Ended. Priority:
//   Ended (killed rows) > Sleeping > Stuck > Needs input > Check terminal >
//   Your turn > Working > Idle.   Overlays: context >= 80%, unread, selection.
//
// Fixture-driven through the pre-navigation fetch + /ws/coral stub (same
// isolation pattern as agent_bar_tweaks / agent_list_compact). State flips go
// through window._coralHandleWsMessage so the real merge path is exercised.
//
// Expects CORAL_URL + CDP_PORT from tests/frontend/run.sh. Exit 0 / 1 / 2.

const CDP = require('chrome-remote-interface');
const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);
if (/:8420(\/|$)/.test(BASE) && !process.env.CORAL_TEST_ALLOW_PROD) {
    console.error(`Refusing to run against ${BASE}: that is the production Coral port. Use tests/frontend/run.sh.`);
    process.exit(2);
}

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const U = () => require('crypto').randomUUID();

const ID = { working: U(), idle: U(), needs: U(), check: U(), stuck: U(), turnSolo: U(), sleeping: U(), turnMember: U(), turnOrch: U(), kill: U() };
const base = (o) => Object.assign({ name: 'coral-go', display_name: '', agent_type: 'claude', summary: 'Goal text', context_pct: 10, working: false, waiting_for_input: false, not_started: false, stuck: false, sleeping: false, awaiting_user: false, done: false, board_unread: 0, working_directory: '/repo/coral-go' }, o);
const SESSIONS = [
    base({ session_id: ID.working, display_name: 'Worker', working: true }),
    base({ session_id: ID.idle, display_name: 'Idler' }),
    base({ session_id: ID.needs, display_name: 'Asker', waiting_for_input: true, context_pct: 87, context_window: 1000000 }),
    base({ session_id: ID.check, display_name: 'Fresh', not_started: true }),
    base({ session_id: ID.stuck, display_name: 'Jammed', stuck: true }),
    base({ session_id: ID.turnSolo, display_name: 'Solo Turn', awaiting_user: true, done: true }),
    base({ session_id: ID.sleeping, display_name: 'Sleeper', sleeping: true, status: 'Sleeping' }),
    base({ session_id: ID.kill, display_name: 'Victim' }),
    // team: a member whose turn ended (never counted) and the orchestrator (counted)
    base({ session_id: ID.turnMember, name: 'team-dir', working_directory: '/repo/team', display_name: 'Member Turn', awaiting_user: true, done: true, board_project: 'qa-team', board_is_orchestrator: false, board_unread: 3 }),
    base({ session_id: ID.turnOrch, name: 'team-dir', working_directory: '/repo/team', display_name: 'Orch Turn', awaiting_user: true, done: true, board_project: 'qa-team', board_is_orchestrator: true }),
];
const ORDER = SESSIONS.map(s => s.session_id).join(',');

const STUB = `
  window.__fixture = ${JSON.stringify(SESSIONS)}; window.__posts = [];
  (function(){ const Real = window.WebSocket; function Dead(u){ this.url=String(u); this.readyState=0; }
    Dead.prototype.send=function(){}; Dead.prototype.close=function(){}; Dead.prototype.addEventListener=function(){}; Dead.prototype.removeEventListener=function(){};
    function W(u,p){ if (/\\/ws\\/(coral|terminal)/.test(String(u))) return new Dead(u); return p===undefined? new Real(u): new Real(u,p); }
    W.prototype=Real.prototype; W.CONNECTING=0; W.OPEN=1; W.CLOSING=2; W.CLOSED=3; window.WebSocket=W; })();
  window.__origFetch = window.fetch.bind(window);
  window.fetch = (url, opts) => { const u=String(url); const path=u.replace(/^https?:\\/\\/[^/]+/,'').split('?')[0]; const m=((opts&&opts.method)||'GET').toUpperCase();
    const json=(b)=>Promise.resolve(new Response(JSON.stringify(b),{status:200,headers:{'Content-Type':'application/json'}}));
    if (path==='/api/sessions/live' && m==='GET') return json(window.__fixture);
    if (m!=='GET') { window.__posts.push(path); return json({ok:true}); }
    if (/\\/(tasks|notes|events)(\\?|$)/.test(path)) return json([]);
    if (/\\/files(\\?|$)/.test(path)) return json({ files: [] }); if (/\\/git(\\?|$)/.test(path)) return json({ snapshots: [] }); if (/\\/chat(\\?|$)/.test(path)) return json({ messages: [] });
    if (/\\/poll(\\?|$)/.test(path)) return json({ capture: { capture: null, error: 'stub' }, tasks: [], events: [] });
    if (/^\\/api\\/sessions\\/live\\/[^/]+/.test(path)) return json({ name: 'x', capture: null, error: 'stub', pane_capture: '' });
    return window.__origFetch(url, opts); };`;

const EXPECT = {
    [ID.working]: { state: 'working', label: 'Working', dot: 'working', pill: null, attention: false },
    [ID.idle]: { state: 'idle', label: 'Idle', dot: 'stale', pill: null, attention: false },
    [ID.needs]: { state: 'needs_input', label: 'Needs input', dot: 'waiting', pill: 'Needs input', attention: true },
    [ID.check]: { state: 'check_terminal', label: 'Check terminal', dot: 'waiting', pill: 'Check terminal', attention: true },
    [ID.stuck]: { state: 'stuck', label: 'Stuck', dot: 'stuck', pill: 'Stuck', attention: true },
    [ID.turnSolo]: { state: 'your_turn', label: 'Your turn', dot: 'your-turn', pill: null, attention: false },
    [ID.sleeping]: { state: 'sleeping', label: 'Sleeping', dot: 'sleeping', pill: null, attention: false },
    [ID.turnMember]: { state: 'your_turn', label: 'Your turn', dot: 'your-turn', pill: null, attention: false },
    [ID.turnOrch]: { state: 'your_turn', label: 'Your turn', dot: 'your-turn', pill: null, attention: false },
};

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Emulation, Input } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
    const exceptions = [];
    Runtime.exceptionThrown(e => exceptions.push((((e.exceptionDetails || {}).exception || {}).description || 'exception').slice(0, 200)));
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const diff = (obj) => ev(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [${JSON.stringify(obj)}] }); true`);
    const boot = async (w, h, mobile, grouped) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        for (let i = 0; i < 60; i++) { if (await ev(`typeof window._coralSetLiveSessions === 'function'`)) break; await sleep(100); }
        await ev(`localStorage.setItem('coral-group-by-team','${grouped ? 'true' : 'false'}'); localStorage.removeItem('coral-count-your-turn'); window.switchNavTab('agents'); window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__fixture))); true`);
        await sleep(300);
    };
    const ROWS = (listId) => `(() => Object.fromEntries(Array.from(document.querySelectorAll('#${listId} .session-group-item')).map(li => {
        const q = s => li.querySelector(s); const dot = q('.session-dot'); const dcs = dot ? getComputedStyle(dot) : null; const name = q('.session-label');
        const range = document.createRange(); range.selectNodeContents(name); const rects = range.getClientRects(); const nx = rects.length ? rects[0].left : null;
        const pills = Array.from(li.querySelectorAll('.session-state-pill')).map(p => ({ text: p.textContent.trim(), cls: p.className, transform: getComputedStyle(p).textTransform }));
        const chip = q('.session-unread-chip'); const mchip = q('.session-status-chip');
        return [li.dataset.sessionId, { state: li.getAttribute('data-state'), aria: li.getAttribute('aria-label'), dotCls: dot ? dot.className : null, dotHidden: dcs ? dcs.visibility === 'hidden' : null, dotBg: dcs ? dcs.backgroundColor : null, dotAnim: dcs ? dcs.animationName : null, dotGlyph: dot ? getComputedStyle(dot, '::before').content : null,
            pills, ctxPill: q('.session-ctx-pill') ? q('.session-ctx-pill').textContent.trim() : null, attention: li.classList.contains('needs-attention'), isStuck: li.classList.contains('is-stuck'), done: li.classList.contains('session-done'),
            unread: chip ? { text: chip.textContent.trim(), title: chip.getAttribute('title'), shown: getComputedStyle(chip).display !== 'none' } : null, mobileChip: mchip ? mchip.textContent.trim() : null,
            nameX: nx, h: li.getBoundingClientRect().height, labelDecoration: getComputedStyle(name).textDecorationLine }]; })))()`;
    const BADGE = `(() => { const b = document.getElementById('nav-tab-agents-badge'); return b ? b.textContent.trim() : null; })()`;
    const TOASTS = `document.querySelectorAll('.notification-toast').length`;

    try {
        await boot(1440, 900, false, false);
        let rows = await ev(ROWS('live-sessions-list'));

        // ── E/D: one vocabulary per state on row attr, dot, pill, aria, tooltip ──
        for (const [sid, exp] of Object.entries(EXPECT)) {
            const r = rows[sid]; const nm = SESSIONS.find(s => s.session_id === sid).display_name;
            check(`state ${exp.state} (${nm}): data-state, dot class, pill, aria label`, !!r && r.state === exp.state && new RegExp('\\b' + exp.dot + '\\b').test(r.dotCls || '') && (exp.pill === null ? r.pills.length === 0 : (r.pills.length === 1 && r.pills[0].text === exp.pill && r.pills[0].transform !== 'uppercase')) && new RegExp('^' + nm + ', ' + exp.label + '(,|$)').test(r.aria || '') && r.attention === exp.attention, JSON.stringify(r && { state: r.state, dot: r.dotCls, pills: r.pills.map(p => p.text), aria: r.aria, attention: r.attention }));
        }
        const tipStates = await ev(`Object.fromEntries(window._coralGetLiveSessions().map(s => [s.session_id, String(window.buildSessionTooltip(s)).replace(/<[^>]+>/g, ' ').replace(/\\s+/g, ' ')]))`);
        check('tooltip State row uses the same words', Object.entries(EXPECT).every(([sid, exp]) => new RegExp('State\\s+' + exp.label).test(tipStates[sid] || '')), JSON.stringify(Object.fromEntries(Object.entries(EXPECT).map(([sid, e]) => [e.state, (tipStates[sid] || '').slice(0, 40)]))));
        check('Your turn rows render NO pill while attention rows still do', [ID.turnSolo, ID.turnMember, ID.turnOrch].every(id => rows[id].pills.length === 0) && /session-attention-pill/.test(rows[ID.needs].pills[0].cls) && /session-attention-pill/.test(rows[ID.check].pills[0].cls) && /stuck/.test(rows[ID.stuck].pills[0].cls) && rows[ID.stuck].isStuck, JSON.stringify({ turn: rows[ID.turnSolo].pills, needs: rows[ID.needs].pills[0].cls, stuck: rows[ID.stuck].pills[0].cls }));
        // K/J: green only means Working; your turn is a hollow neutral ring; idle hidden; glyph states
        check('K Your turn is never green and is hollow (transparent fill); working is the only filled green/blue dot', /rgba\(0, 0, 0, 0\)|transparent/.test(rows[ID.turnSolo].dotBg) && rows[ID.working].dotBg !== rows[ID.turnSolo].dotBg && !/rgba\(0, 0, 0, 0\)/.test(rows[ID.working].dotBg), JSON.stringify({ turn: rows[ID.turnSolo].dotBg, working: rows[ID.working].dotBg }));
        check('J idle dot hidden (slot kept), sleeping shows a glyph, no dot animation in rows', rows[ID.idle].dotHidden === true && rows[ID.sleeping].dotGlyph && rows[ID.sleeping].dotGlyph !== 'none' && Object.values(rows).every(r => r.dotAnim === 'none'), JSON.stringify({ idleHidden: rows[ID.idle].dotHidden, sleepingGlyph: rows[ID.sleeping].dotGlyph, anims: [...new Set(Object.values(rows).map(r => r.dotAnim))] }));
        const CONTRAST = `(() => { const parse = c => { let m = /rgba?\\(([^)]+)\\)/.exec(c); if (m) { const p = m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number); return { r: p[0], g: p[1], b: p[2], a: p[3] === undefined ? 1 : p[3] }; } m = /color\\(srgb ([^)]+)\\)/.exec(c); if (m) { const p = m[1].split(/[ \\/]+/).filter(Boolean).map(Number); return { r: p[0] * 255, g: p[1] * 255, b: p[2] * 255, a: p[3] === undefined ? 1 : p[3] }; } return { r: 0, g: 0, b: 0, a: 0 }; };
            const over = (f, b) => ({ r: f.r * f.a + b.r * (1 - f.a), g: f.g * f.a + b.g * (1 - f.a), b: f.b * f.a + b.b * (1 - f.a), a: 1 });
            const bgOf = el => { const chain = []; for (let n = el; n; n = n.parentElement) chain.push(parse(getComputedStyle(n).backgroundColor)); let acc = { r: 255, g: 255, b: 255, a: 1 }; for (const c of chain.reverse()) if (c.a > 0) acc = over(c, acc); return acc; };
            const lum = c => { const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b); };
            const ratio = el => { const bg = bgOf(el); const fg = over(parse(getComputedStyle(el).color), bg); const a = lum(fg), b = lum(bg); return Math.round(((Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)) * 100) / 100; };
            const out = {}; document.querySelectorAll('#live-sessions-list .session-state-pill, #live-sessions-list .session-unread-chip').forEach(el => { out[el.className.replace(/badge |waiting-badge |session-state-pill /g, '') + ':' + el.textContent.trim()] = ratio(el); }); return out; })()`;
        const contrast = await ev(CONTRAST);
        check('J state pills and unread chip text contrast >= 4.5:1', Object.keys(contrast).length >= 4 && !Object.keys(contrast).some(k => /turn/i.test(k)) && Object.values(contrast).every(v => v >= 4.5), JSON.stringify(contrast));
        check('no-sort: row order equals payload order', Object.keys(rows).join(',') === ORDER);

        // ── G: context collisions ──
        check('G high context + Needs input: attention pill only, ring kept, aria carries the context suffix', rows[ID.needs].ctxPill === null && /ctx-high/.test(rows[ID.needs].dotCls) && /, context 87%/.test(rows[ID.needs].aria), JSON.stringify({ ctx: rows[ID.needs].ctxPill, dot: rows[ID.needs].dotCls, aria: rows[ID.needs].aria }));
        await diff({ name: 'coral-go', agent_type: 'claude', session_id: ID.idle, context_pct: 91, context_window: 1000000 }); await sleep(150);
        rows = await ev(ROWS('live-sessions-list'));
        check('G idle + high context: ring survives on the otherwise hidden slot and the ctx pill shows', rows[ID.idle].dotHidden === false && /ctx-high/.test(rows[ID.idle].dotCls) && rows[ID.idle].ctxPill === 'ctx 91%', JSON.stringify({ hidden: rows[ID.idle].dotHidden, ctx: rows[ID.idle].ctxPill }));
        await diff({ name: 'coral-go', agent_type: 'claude', session_id: ID.idle, context_pct: null, context_window: null }); await sleep(150);
        rows = await ev(ROWS('live-sessions-list'));
        check('G explicit null context clears ring, pill and aria suffix', rows[ID.idle].ctxPill === null && !/ctx-high/.test(rows[ID.idle].dotCls) && !/context/.test(rows[ID.idle].aria), JSON.stringify({ dot: rows[ID.idle].dotCls, aria: rows[ID.idle].aria }));

        // ── H: unread chip + aggregation ──
        check('H unread chip on line 2: count, title, aria suffix; absent at 0', rows[ID.turnMember].unread && rows[ID.turnMember].unread.text === '3' && /3 unread board messages/.test(rows[ID.turnMember].unread.title || '') && /, 3 unread$/.test(rows[ID.turnMember].aria) && (!rows[ID.working].unread || !rows[ID.working].unread.shown), JSON.stringify({ chip: rows[ID.turnMember].unread, aria: rows[ID.turnMember].aria }));
        // Aggregation: needs + check + stuck (3) + Your turn standalone (1) + Your turn orchestrator (1) = 5.
        // The ordinary team member (Your turn, 3 unread) counts through neither rule.
        check('H nav badge = attention states + operator-facing Your turn; ordinary member with unread is not counted (5)', (await ev(BADGE)) === '5', String(await ev(BADGE)));
        const ORCH = { name: 'team-dir', agent_type: 'claude', session_id: ID.turnOrch };
        await diff(Object.assign({}, ORCH, { awaiting_user: false, done: false, working: true, board_unread: 2 })); await sleep(150);
        const orchUnread = await ev(BADGE);
        await diff(Object.assign({}, ORCH, { board_unread: 0 })); await sleep(150);
        const orchNone = await ev(BADGE);
        await diff(Object.assign({}, ORCH, { awaiting_user: true, done: true, working: false })); await sleep(150);
        check('H unread counts only for an operator-facing row: working orchestrator with unread = 5, without = 4, back to Your turn = 5', orchUnread === '5' && orchNone === '4' && (await ev(BADGE)) === '5', JSON.stringify({ orchUnread, orchNone, restored: await ev(BADGE) }));
        await diff({ name: 'team-dir', agent_type: 'claude', session_id: ID.turnMember, board_unread: 9 }); await sleep(150);
        check('H more unread on an ordinary member changes the chip but not the badge', (await ev(BADGE)) === '5' && (await ev(ROWS('live-sessions-list')))[ID.turnMember].unread.text === '9', String(await ev(BADGE)));
        await ev(`localStorage.setItem('coral-count-your-turn', 'false'); window._coralSetLiveSessions(window._coralGetLiveSessions()); true`); await sleep(150);
        check('H setting coral-count-your-turn=false drops Your turn from the badge (3)', (await ev(BADGE)) === '3', String(await ev(BADGE)));
        await ev(`localStorage.removeItem('coral-count-your-turn'); localStorage.setItem('coral-group-by-team','true'); window._coralSetLiveSessions(window._coralGetLiveSessions()); true`); await sleep(250);
        const grp = await ev(`(() => { const card = Array.from(document.querySelectorAll('#live-sessions-list .session-board-card')).find(c => /qa-team/.test(c.textContent)); if (!card) return null; const c = card.querySelector('.group-attention-count'); const before = c ? { text: c.textContent.trim(), aria: c.getAttribute('aria-label'), stuck: c.classList.contains('stuck') } : null; const hdr = card.querySelector('.board-card-header'); const chev = hdr.querySelector('[class*="chevron"], .group-chevron, button') || hdr; chev.click(); const c2 = card.querySelector('.group-attention-count'); const out = { before, collapsedStillShown: !!c2 && getComputedStyle(c2).display !== 'none', rowsVisible: Array.from(card.querySelectorAll('.session-group-item')).filter(li => li.getBoundingClientRect().height > 0).length }; chev.click(); return out; })()`);
        check('H team header attention count = 1 (orchestrator Your turn; member not counted), still visible when collapsed with rows hidden', grp && grp.before && grp.before.text === '1' && grp.rowsVisible === 0 && /attention/.test(grp.before.aria || '') && grp.collapsedStillShown, JSON.stringify(grp));
        await ev(`localStorage.setItem('coral-group-by-team','false'); window._coralSetLiveSessions(window._coralGetLiveSessions()); true`); await sleep(200);

        // ── D: priority pairs on one row (Idler) ──
        const P = { name: 'coral-go', agent_type: 'claude', session_id: ID.idle };
        const stateOf = async () => (await ev(ROWS('live-sessions-list')))[ID.idle];
        const steps = [
            [{ working: true }, 'working'], [{ awaiting_user: true, done: true }, 'your_turn'], [{ not_started: true }, 'check_terminal'],
            [{ waiting_for_input: true }, 'needs_input'], [{ stuck: true }, 'stuck'], [{ sleeping: true }, 'sleeping'],
        ];
        const seen = [];
        for (const [patch, want] of steps) { await diff(Object.assign({}, P, patch)); await sleep(120); seen.push([want, (await stateOf()).state]); }
        check('D priority: each higher state wins while lower flags stay set (working < your_turn < check_terminal < needs_input < stuck < sleeping)', seen.every(([w, g]) => w === g), JSON.stringify(seen));
        await diff(Object.assign({}, P, { sleeping: false, stuck: false, waiting_for_input: false, not_started: false, awaiting_user: false, done: false, working: false })); await sleep(150);
        check('C explicit false on every flag returns the row to Idle', (await stateOf()).state === 'idle', (await stateOf()).state);

        // ── F: tick sequence with layout, order and focus stability ──
        await ev(`document.querySelector('#live-sessions-list [data-session-id="${ID.idle}"]').focus(); true`);
        const x0 = (await stateOf()).nameX; const seq = [];
        for (const [patch, want] of [[{ working: true }, 'working'], [{ working: false, awaiting_user: true, done: true }, 'your_turn'], [{ waiting_for_input: true }, 'needs_input'], [{ waiting_for_input: false, awaiting_user: false, done: false, working: true }, 'working']]) {
            await diff(Object.assign({}, P, patch)); await sleep(150); const r = await stateOf();
            seq.push({ want, got: r.state, dx: Math.abs(r.nameX - x0), h: r.h, focus: await ev(`(document.activeElement.dataset || {}).sessionId === '${ID.idle}'`), order: (await ev(`Array.from(document.querySelectorAll('#live-sessions-list .session-group-item')).map(l => l.dataset.sessionId).join(',')`)) === ORDER });
        }
        check('F Idle -> Working -> Your turn -> Needs input -> Working: state, name x ±0.5px, 40px, focus kept, order kept', seq.every(s => s.want === s.got && s.dx <= 0.5 && Math.abs(s.h - 40) <= 1 && s.focus && s.order), JSON.stringify(seq));

        // ── I: notifications ──
        // Fresh page: the first full update already contains Needs input rows.
        await boot(1440, 900, false, false);
        await ev(`document.querySelectorAll('.notification-toast').forEach(t => t.remove()); true`);
        await ev(`window._coralHandleWsMessage({ type: 'coral_update', sessions: JSON.parse(JSON.stringify(window.__fixture)) }); true`); await sleep(300);
        const initialToasts = await ev(TOASTS);
        check('I no toasts for rows that already need input in the first full update (rising edge only)', initialToasts === 0, String(initialToasts));
        await ev(`document.querySelectorAll('.notification-toast').forEach(t => t.remove()); true`);
        await diff({ name: 'coral-go', agent_type: 'claude', session_id: ID.working, working: false, waiting_for_input: true, waiting_summary: 'permission?' }); await sleep(300);
        check('I rising edge to Needs input raises exactly one toast', (await ev(TOASTS)) === 1, String(await ev(TOASTS)));
        await diff({ name: 'coral-go', agent_type: 'claude', session_id: ID.working, waiting_for_input: true }); await sleep(200);
        check('I a repeated tick in the same state raises no further toast', (await ev(TOASTS)) === 1, String(await ev(TOASTS)));

        // ── selected attention visibility + workspace header vocabulary ──
        await ev(`window.selectLiveSession('coral-go', 'claude', '${ID.needs}'); true`); await sleep(400);
        rows = await ev(ROWS('live-sessions-list'));
        const hdr = await ev(`(() => { const d = document.getElementById('terminal-status-dot'); return { cls: d ? d.className : null }; })()`);
        check('selected attention row keeps pill + dot; workspace header dot uses the same state class', rows[ID.needs].pills.length === 1 && rows[ID.needs].pills[0].text === 'Needs input' && rows[ID.needs].dotHidden === false && /waiting/.test(hdr.cls || ''), JSON.stringify({ pills: rows[ID.needs].pills.map(p => p.text), hdr: hdr.cls }));

        // toast suppression while the operator is typing into the selected session
        await ev(`window.selectLiveSession('coral-go', 'claude', '${ID.idle}'); true`); await sleep(400);
        await ev(`document.querySelectorAll('.notification-toast').forEach(t => t.remove()); const i = document.getElementById('command-input'); if (i) i.focus(); true`);
        const typingFocus = await ev(`(document.activeElement || {}).id === 'command-input'`);
        await diff({ name: 'coral-go', agent_type: 'claude', session_id: ID.idle, waiting_for_input: true }); await sleep(300);
        const typedToasts = await ev(TOASTS);
        await diff({ name: 'coral-go', agent_type: 'claude', session_id: ID.turnSolo, waiting_for_input: true }); await sleep(300);
        check('I no toast for the selected session while typing in its command input; another session still toasts', typingFocus && typedToasts === 0 && (await ev(TOASTS)) === 1, JSON.stringify({ typingFocus, typedToasts, after: await ev(TOASTS) }));
        await diff({ name: 'coral-go', agent_type: 'claude', session_id: ID.turnSolo, waiting_for_input: false }); await sleep(150);

        // ── K: Ended only for killed rows ──
        await ev(`window.killSessionDirect('coral-go', 'claude', '${ID.kill}'); true`); await sleep(200);
        await ev(`document.getElementById('confirm-modal-yes').click(); true`); await sleep(500);
        rows = await ev(ROWS('live-sessions-list'));
        const k = rows[ID.kill];
        check('K killed row is Ended: data-state ended, struck-through name, glyph dot, 36px, no pill, not counted', !!k && k.state === 'ended' && /line-through/.test(k.labelDecoration) && /ended/.test(k.dotCls) && Math.abs(k.h - 36) <= 1 && k.pills.length === 0 && /, Ended$/.test(k.aria), JSON.stringify(k && { state: k.state, deco: k.labelDecoration, dot: k.dotCls, h: k.h, aria: k.aria }));
        check('K live awaiting_user rows are never styled Ended/Done', !rows[ID.turnSolo].done && rows[ID.turnSolo].state === 'your_turn' && !/line-through/.test(rows[ID.turnSolo].labelDecoration), JSON.stringify({ state: rows[ID.turnSolo].state, deco: rows[ID.turnSolo].labelDecoration }));
        check('no page exceptions (desktop)', exceptions.length === 0, JSON.stringify(exceptions.slice(0, 2)));

        // ── mobile clone ──
        await boot(390, 844, true, false);
        const m = await ev(ROWS('mobile-session-list'));
        const words = { [ID.working]: 'Working', [ID.idle]: 'Idle', [ID.needs]: 'Needs input', [ID.check]: 'Check terminal', [ID.stuck]: 'Stuck', [ID.sleeping]: 'Sleeping' };
        check('mobile status chip uses the same sentence-case words', Object.entries(words).every(([sid, w]) => m[sid] && m[sid].mobileChip === w), JSON.stringify(Object.fromEntries(Object.entries(words).map(([sid, w]) => [w, m[sid] && m[sid].mobileChip]))));
        check('mobile Your turn rows render no status chip; the dot and aria-label still say Your turn', [ID.turnSolo, ID.turnMember, ID.turnOrch].every(id => m[id] && m[id].mobileChip === null && m[id].pills.length === 0 && /your-turn/.test(m[id].dotCls || '') && m[id].dotHidden === false && /, Your turn(,|$)/.test(m[id].aria || '')), JSON.stringify([ID.turnSolo, ID.turnMember, ID.turnOrch].map(id => m[id] && { chip: m[id].mobileChip, dot: m[id].dotCls, dotHidden: m[id].dotHidden, aria: m[id].aria })));
        check('mobile clone: same data-state per row, idle dot hidden, unread chip hidden in favour of the meta pill', Object.entries(EXPECT).every(([sid, e]) => m[sid] && m[sid].state === e.state) && m[ID.idle].dotHidden === true && (!m[ID.turnMember].unread || !m[ID.turnMember].unread.shown), JSON.stringify({ idle: m[ID.idle] && m[ID.idle].dotHidden, unread: m[ID.turnMember] && m[ID.turnMember].unread }));
    } finally {
        await client.close();
    }
    const failed = results.filter(v => v === 'FAIL').length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
}
run().catch(err => { console.error('HARNESS ERROR:', err); process.exit(2); });
