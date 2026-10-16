// #3830: a Claude agent that waits for a sign-in. Its chat shows a "needs you to sign in" card
// with the CLI's own message and an Open terminal button that shows the terminal (where the login
// happens), never the usage-limit notice; its row says Needs input \u00b7 Sign in. When the login is
// done the card goes, and a real usage-limit error shows the CLI's message as the notice. Same
// isolation pattern as transcript_tools: fetch and the coral WebSocket are stubbed, and the chat
// reply carries needs_login / login_reason / limit_notice as the server sends them.
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);
const results = []; const check = (l, c, d) => { const v = c ? 'PASS' : 'FAIL'; results.push(v); console.log(`[${v}] ${l}${d !== undefined ? ' — ' + d : ''}`); };
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const SID = require('crypto').randomUUID();

const SESSIONS = [{ name: 'qa-login', display_name: 'Sign-in Agent', agent_type: 'claude', session_id: SID, working_directory: '/repo/qa',
  waiting_for_input: true, needs_login: true, waiting_kind: 'login', waiting_reason: 'login', waiting_summary: 'Invalid API key \u00b7 Please run /login' }];

const STUB = `
  window.__sessions = ${JSON.stringify(SESSIONS)};
  window.__chat = { needs_login: true, login_reason: 'Invalid API key \u00b7 Please run /login', limit_notice: '' };
  (function(){ const Real = window.WebSocket; function Dead(u){ this.url=String(u); this.readyState=0; } Dead.prototype.send=function(){}; Dead.prototype.close=function(){}; Dead.prototype.addEventListener=function(){}; Dead.prototype.removeEventListener=function(){};
    function W(u,p){ if (/\\/ws\\//.test(String(u))) return new Dead(u); return p===undefined? new Real(u): new Real(u,p); } W.prototype=Real.prototype; W.CONNECTING=0; W.OPEN=1; W.CLOSING=2; W.CLOSED=3; window.WebSocket=W; })();
  window.__origFetch = window.fetch.bind(window);
  window.fetch = (url, opts) => { const u=String(url); const rel=u.replace(/^https?:\\/\\/[^/]+/,''); const path=rel.split('?')[0]; const m=((opts&&opts.method)||'GET').toUpperCase();
    const json=(b)=>Promise.resolve(new Response(JSON.stringify(b),{status:200,headers:{'Content-Type':'application/json'}}));
    if (m!=='GET') return json({ok:true});
    if (path==='/api/sessions/live') return json(window.__sessions);
    if (/^\\/api\\/sessions\\/live\\/[^/]+\\/chat$/.test(path)) { window.__chatReplies = (window.__chatReplies || 0) + 1;
      return json(Object.assign({ messages: [], total: 0, has_more: false, pending: null }, window.__chat)); }
    if (/^\\/api\\/board\\//.test(path)) return json({});
    return window.__origFetch(url, opts); };`;

async function run() {
  const client = await CDP({ port: CDP_PORT }); const { Page, Runtime, Emulation } = client;
  await Promise.all([Page.enable(), Runtime.enable()]);
  await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
  const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
  const { waitFor, nextFrame, coralReady } = require('./helpers/wait').bind(ev);
  const until = (expression, what, timeout = 5000) => waitFor(expression, what, { soft: true, timeout });
  const refreshOnce = () => ev(`import((window.__coralStatic||'/static/')+'live_chat.js').then(m => m.refreshLiveHistory()).then(() => true)`);
  const cycles = async (n) => { const start = await ev(`window.__chatReplies || 0`); for (let i = 0; i < n * 10 && (await ev(`window.__chatReplies || 0`)) < start + n; i++) { await refreshOnce(); await sleep(30); } await nextFrame(); };
  await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
  await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
  await coralReady('startup', 'liveList', 'liveListLoaded', 'history', 'settings');
  await ev(`window.switchNavTab('agents'); window._coralSetLiveSessions(JSON.parse(JSON.stringify(window.__sessions))); true`);
  await ev(`Promise.resolve(window.selectLiveSession('qa-login', 'claude', ${JSON.stringify(SID)})).then(() => { window.setLiveViewMode('chat'); return true; })`);
  await coralReady('agentSelected', 'qa-login');
  await cycles(2);

  const C = `document.getElementById('live-history-messages')`;
  await until(`!!${C}.querySelector(':scope > .chat-needs-input .cni-login-open')`, 'the sign-in card');
  const card = () => ev(`(() => { const c = ${C}.querySelector(':scope > .chat-needs-input'); if (!c) return null;
    const t = s => (c.querySelector(s) || {}).textContent || '';
    return { kicker: t('.cni-kicker'), question: t('.cni-question'), reason: t('.cni-login-reason'), button: !!c.querySelector('button.btn-primary.cni-login-open[data-on-click="terminal-view-open"]'),
      answers: c.querySelectorAll('.cni-answer').length }; })()`);
  let cd = await card();
  check('the chat shows the sign-in card', cd && cd.question === 'Claude needs you to sign in' && cd.kicker === 'Sign-in needed', JSON.stringify(cd));
  check("with the CLI's message", cd && cd.reason === 'Invalid API key \u00b7 Please run /login', JSON.stringify(cd));
  check('and Open terminal as its one action', cd && cd.button && cd.answers === 0, JSON.stringify(cd));
  check('no usage-limit notice while a login is pending', !(await ev(`!!${C}.querySelector('.chat-usage-limit-notice')`)));
  const row = await ev(`import((window.__coralStatic||'/static/')+'render.js').then(r => r.sessionStateInfo(window.__sessions[0]))`);
  check('the agent list marks it like other requests: Needs input \u00b7 Sign in', row.key === 'needs_input' && row.label === 'Needs input \u00b7 Sign in' && row.attention, JSON.stringify(row));

  // The button opens the terminal view (the saved Chat/Terminal choice stays).
  const opened = await ev(`(() => { const before = localStorage.getItem('coral-live-view-mode');
    ${C}.querySelector('.chat-needs-input .cni-login-open').click();
    const r = { chat: document.getElementById('capture-wrapper').classList.contains('chat-mode'), terminalActive: document.getElementById('live-view-btn-terminal')?.classList.contains('active'), saved: localStorage.getItem('coral-live-view-mode'), before };
    window.setLiveViewMode('chat'); return r; })()`);
  check('Open terminal shows the terminal', !opened.chat && opened.terminalActive !== false && opened.saved === opened.before, JSON.stringify(opened));

  // Signed in; then the CLI reports its usage limit: the card goes, the notice is the CLI's own text.
  await ev(`window.__chat = { needs_login: false, login_reason: null, limit_notice: "You've hit your limit \u00b7 resets 3pm (UTC)" };
    import((window.__coralStatic||'/static/')+'state.js').then(st => { Object.assign(st.state.liveSessions[0], { waiting_for_input: false, needs_login: false, waiting_kind: null, waiting_summary: null }); return true; })`);
  await cycles(2);
  await until(`!${C}.querySelector(':scope > .chat-needs-input')`, 'the card to go');
  cd = await card();
  check('after the sign-in the card is gone', cd === null, JSON.stringify(cd));
  const notice = await ev(`(${C}.querySelector('.chat-usage-limit-notice .message-text') || {}).textContent || ''`);
  check("a usage limit shows the CLI's message", notice === "You've hit your limit \u00b7 resets 3pm (UTC)", notice);

  // The chat reply is fresher than the row: a reply that says login wins over a row that doesn't yet.
  await ev(`window.__chat = { needs_login: true, login_reason: null, limit_notice: '' }; true`);
  await cycles(2);
  await until(`!!${C}.querySelector(':scope > .chat-needs-input .cni-login-open')`, 'the card from the reply');
  cd = await card();
  check('the card follows the chat reply, with no message when the CLI gave none', cd && cd.button && cd.reason === '', JSON.stringify(cd));

  await client.close();
  const failed = results.filter(r => r === 'FAIL').length;
  console.log(`\n${results.length - failed}/${results.length} passed`);
  process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error(e); process.exit(2); });
