// Acceptance harness for the single-agent popout (specs/SINGLE_AGENT_POPOUT.md).
//
// Numbers in check labels refer to the checklist in the spec (1-37).
// Selector/hook contract: task #129 (Frontend Dev) + #128 (Lead Developer), as
// decided by the Orchestrator (board message 2026-09-17 18:22).
//
// Sections
//   A. HTTP/backend contract (node fetch): route, resolver, mismatch/unknown
//      rejections, spoofed Host, WS tuple mismatch.
//   B. Real isolated terminal session: shell, identity, no-id leakage, refresh,
//      command targeting, owner/viewer (same browser context), cross-context
//      viewer (no BroadcastChannel), ended-on-kill, cleanup.
//   C. Fixture-driven states via pre-navigation fetch/WS stubs on the real
//      /agent/<uuid> route: not-found, history-only, identity chain/pill/title,
//      rename, sleeping, own-session toasts, restart not followed, mobile,
//      aria, no destructive controls, storage isolation.
//   D. Main-app regression: kebab/header anchors, #chat/<sid> restore, null
//      DOM targets.
//
// Expects CORAL_URL + CDP_PORT from tests/frontend/run.sh (isolated server,
// temp home, production-port guard). Exits 0 on success, 1 on any FAIL,
// 2 on harness error.

const CDP = require('chrome-remote-interface');
const http = require('http');

const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

if (/:8420(\/|$)/.test(BASE) && !process.env.CORAL_TEST_ALLOW_PROD) {
    console.error(`Refusing to run against ${BASE}: that is the production Coral port. Use tests/frontend/run.sh.`);
    process.exit(2);
}

const results = [];
function check(label, cond, detail) {
    const verdict = cond ? 'PASS' : 'FAIL';
    results.push({ verdict, label });
    console.log(`[${verdict}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const randomUuid = () => require('crypto').randomUUID();

// ── Pre-navigation stubs (fixture sections) ─────────────────────────────
// Installed with Page.addScriptToEvaluateOnNewDocument. Stubs the resolver,
// the live list, the history detail and every non-GET call; wraps WebSocket
// so /ws/coral and /ws/terminal never open real sockets (attempts counted).
function stubSource(fx) {
    return `
    window.__fx = ${JSON.stringify(fx)};
    window.__calls = []; window.__wsAttempts = []; window.__opened = []; window.__resolveCalls = 0;
    (function () {
        const Real = window.WebSocket;
        function Dead(u) { this.url = String(u); this.readyState = 0; }
        Dead.prototype.send = function () {}; Dead.prototype.close = function () {};
        Dead.prototype.addEventListener = function () {}; Dead.prototype.removeEventListener = function () {};
        function W(u, p) { const s = String(u); if (/\\/ws\\/(coral|terminal)/.test(s)) { window.__wsAttempts.push(s); return new Dead(s); } return p === undefined ? new Real(u) : new Real(u, p); }
        W.prototype = Real.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3; window.WebSocket = W;
        const realOpen = window.open;
        window.open = function (u, t, f) { window.__opened.push({ url: String(u), target: t, features: f }); return null; };
    })();
    window.__origFetch = window.fetch.bind(window);
    window.fetch = (url, opts) => {
        const u = String(url);
        const path = u.replace(/^https?:\\/\\/[^/]+/, '').split('?')[0];
        const method = ((opts && opts.method) || 'GET').toUpperCase();
        const json = (body, status) => Promise.resolve(new Response(JSON.stringify(body), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
        const fx = window.__fx;
        let m;
        window.__calls.push({ path, method, body: opts && opts.body, query: u.includes('?') ? u.split('?')[1] : '' });
        if (method !== 'GET') return json({ ok: true });
        if (path === '/api/sessions/live') return json(fx.live || []);
        if ((m = path.match(/^\\/api\\/sessions\\/([^/]+)\\/resolve$/))) {
            window.__resolveCalls++;
            const forced = (fx.resolveFail || {})[m[1]];
            if (forced === 'network') return Promise.reject(new TypeError('Failed to fetch'));
            if (typeof forced === 'number') return json({ error: 'resolver unavailable' }, forced);
            const r = (fx.resolve || {})[m[1]];
            return json(r || { session_id: m[1], state: 'not_found', active: false, sleeping: false, agent_type: null, name: null, tmux_session: null });
        }
        if ((m = path.match(/^\\/api\\/sessions\\/history\\/([^/]+)$/))) {
            const h = (fx.history || {})[m[1]];
            return h ? json(h) : json({ error: 'not found' }, 404);
        }
        if ((m = path.match(/^\\/api\\/sessions\\/([^/]+)\\/status$/))) {
            const r = (fx.resolve || {})[m[1]];
            return json({ session_id: m[1], active: !!(r && r.active), sleeping: !!(r && r.sleeping) });
        }
        if (/\\/api\\/sessions\\/live\\/[^/]+\\/(tasks|notes)(\\?|$)/.test(path)) return json([]);
        if (/\\/api\\/sessions\\/live\\/[^/]+\\/files(\\?|$)/.test(path)) return json({ agent_name: 'x', files: [], diff_mode: 'working' });
        if (/\\/api\\/sessions\\/live\\/[^/]+\\/git(\\?|$)/.test(path)) return json({ agent_name: 'x', snapshots: [] });
        if (/\\/api\\/sessions\\/live\\/[^/]+\\/chat(\\?|$)/.test(path)) return json({ messages: [], total: 0 });
        if (/\\/api\\/sessions\\/live\\/[^/]+\\/poll(\\?|$)/.test(path)) return json({ capture: { name: 'x', capture: null, error: 'stub' }, tasks: [], events: [], notes: [] });
        if (/\\/api\\/sessions\\/live\\/[^/]+\\/(capture|info)(\\?|$)/.test(path) || /^\\/api\\/sessions\\/live\\/[^/]+$/.test(path)) return json({ name: 'x', capture: null, error: 'stub', pane_capture: '' });
        if (/^\\/api\\/(board|agent-events|events)/.test(path)) return json([]);
        return window.__origFetch(url, opts);
    };`;
}

// ── HTTP helpers (node side) ──────────────────────────────────────────────
async function httpJson(method, path, body, headers) {
    const r = await fetch(BASE + path, { method, headers: Object.assign({ 'Content-Type': 'application/json' }, headers || {}), body: body ? JSON.stringify(body) : undefined, redirect: 'manual' });
    let data = null; const text = await r.text();
    try { data = JSON.parse(text); } catch { data = text; }
    return { status: r.status, data, text, headers: r.headers };
}
function rawGet(path, hostHeader) {
    return new Promise((resolve, reject) => {
        const u = new URL(BASE);
        const req = http.request({ host: u.hostname, port: u.port, path, method: 'GET', headers: { Host: hostHeader || u.host } }, (res) => {
            let b = ''; res.on('data', c => b += c); res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body: b }));
        });
        req.on('error', reject); req.end();
    });
}
function wsCloseCode(url, timeoutMs) {
    return new Promise((resolve) => {
        let done = false; const ws = new WebSocket(url);
        const finish = (v) => { if (!done) { done = true; resolve(v); try { ws.close(); } catch {} } };
        ws.onclose = (ev) => finish({ closed: true, code: ev.code, reason: ev.reason });
        ws.onerror = () => {};
        ws.onopen = () => setTimeout(() => finish({ closed: false, code: null, reason: 'still open' }), timeoutMs);
        setTimeout(() => finish({ closed: false, code: null, reason: 'timeout' }), timeoutMs + 500);
    });
}

// ── CDP helpers ───────────────────────────────────────────────────────────
async function browserClient() {
    const info = await (await fetch(`http://127.0.0.1:${CDP_PORT}/json/version`)).json();
    return CDP({ target: info.webSocketDebuggerUrl });
}
async function openTab(url, { contextId, stub, width = 1280, height = 800, mobile = false } = {}) {
    const browser = await browserClient();
    const { Target } = browser;
    const { targetId } = await Target.createTarget(Object.assign({ url: 'about:blank' }, contextId ? { browserContextId: contextId } : {}));
    await browser.close();
    const client = await CDP({ port: CDP_PORT, target: targetId });
    const { Page, Runtime, Network, Emulation } = client;
    await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
    const exceptions = []; Runtime.exceptionThrown(e => { const d = e.exceptionDetails || {}; const ex = d.exception || {}; exceptions.push(`${d.text || ''} ${ex.description || ex.value || ''} @${(d.url || '').split('/').pop()}:${d.lineNumber}`.trim().slice(0, 220)); });
    const wsCreated = []; Network.webSocketCreated(({ url: u }) => wsCreated.push(u));
    const requests = []; Network.requestWillBeSent(({ request }) => { if (/\/api\/sessions\//.test(request.url)) requests.push({ url: request.url, method: request.method, postData: request.postData || '' }); });
    if (stub) await Page.addScriptToEvaluateOnNewDocument({ source: stub });
    await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile });
    const ev = async (expr) => {
        const r = await Runtime.evaluate({ expression: expr, awaitPromise: true, returnByValue: true });
        if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300));
        return r.result.value;
    };
    const waitFor = async (expr, ms = 8000, step = 100) => { const t0 = Date.now(); while (Date.now() - t0 < ms) { try { if (await ev(expr)) return true; } catch {} await sleep(step); } return false; };
    const goto = async (u) => { await Page.navigate({ url: u }); await Page.loadEventFired(); };
    const close = async () => { try { await client.close(); } catch {} try { const b = await browserClient(); await b.Target.closeTarget({ targetId }); await b.close(); } catch {} };
    await goto(url);
    return { client, Page, Runtime, Network, Emulation, targetId, ev, waitFor, goto, close, exceptions, wsCreated, requests };
}
// Every /api/sessions/live/{name}/... call made by the popout must carry BOTH
// routing fields from the resolver and use the resolver's name (agent name).
function auditRouting(requests, expected) {
    const bad = [];
    for (const r of requests) {
        const m = r.url.match(/\/api\/sessions\/live\/([^/?]+)(?:\/([a-z-]+))?(\?.*)?$/);
        if (!m) continue;
        const name = decodeURIComponent(m[1]); const action = m[2] || 'detail';
        if (['team', 'launch', 'sleep-all', 'wake-all'].includes(name)) continue;
        if (!['detail', 'capture', 'poll', 'send', 'keys', 'resize'].includes(action)) continue;
        let agentType = null, sessionId = null;
        if (r.method === 'GET') { const q = new URL(r.url).searchParams; agentType = q.get('agent_type'); sessionId = q.get('session_id'); }
        else { try { const b = JSON.parse(r.postData || '{}'); agentType = b.agent_type || null; sessionId = b.session_id || null; } catch { /* non-JSON */ } }
        if (agentType !== expected.agent_type || sessionId !== expected.session_id || (name !== expected.name && name !== expected.tmux_session)) bad.push({ action, name, agentType, sessionId, method: r.method });
    }
    return bad;
}
async function newContext() { const b = await browserClient(); const { browserContextId } = await b.Target.createBrowserContext(); await b.close(); return browserContextId; }

const SHELL_PROBE = `(() => {
    const vis = (sel) => { const el = document.querySelector(sel); if (!el) return 'absent'; const cs = getComputedStyle(el); return (cs.display === 'none' || cs.visibility === 'hidden') ? 'hidden' : 'visible'; };
    const p = window._coralPopout;
    return {
        mode: document.body.getAttribute('data-entry-mode'), target: document.body.getAttribute('data-target-session-id'), popoutClass: document.body.classList.contains('popout-mode'),
        chrome: { topBar: vis('.top-bar'), sidebar: vis('.sidebar'), sidebarHandle: vis('#sidebar-resize-handle'), tabletToggle: vis('.tablet-sidebar-toggle'), mobileBar: vis('.mobile-tab-bar'), mobileList: vis('#mobile-agent-list'), welcome: vis('#welcome-screen') },
        layoutH: document.querySelector('.layout') ? document.querySelector('.layout').getBoundingClientRect().height : null, innerH: window.innerHeight,
        identity: (document.getElementById('popout-header-identity') || {}).textContent || '', identityLive: (document.getElementById('popout-header-identity') || { getAttribute() { return null; } }).getAttribute('aria-live'),
        badge: (document.getElementById('terminal-type-badge') || {}).textContent || '', pill: (document.getElementById('terminal-state-pill') || { getAttribute() { return null; } }).getAttribute('data-state'), pillText: (document.getElementById('terminal-state-pill') || {}).textContent || '',
        openMain: (() => { const a = document.getElementById('popout-open-main-btn'); return a ? { tag: a.tagName, href: a.getAttribute('href'), target: a.getAttribute('target'), rel: a.getAttribute('rel') } : null; })(),
        toggle: (() => { const b = document.getElementById('popout-panel-toggle-btn'); return b ? { pressed: b.getAttribute('aria-pressed'), name: b.getAttribute('aria-label') || b.title || b.textContent.trim() } : null; })(),
        title: document.title, hash: location.hash, path: location.pathname,
        hooks: { popout: !!p && typeof p.isPopout === 'function' && typeof p.targetSessionId === 'function' && typeof p.getState === 'function' && typeof p.isOwner === 'function' && typeof p.claimOwnership === 'function', set: typeof window._coralSetLiveSessions === 'function', ws: typeof window._coralHandleWsMessage === 'function', get: typeof window._coralGetLiveSessions === 'function', select: typeof window.selectLiveSession === 'function' },
        state: p && p.getState ? p.getState() : null, isOwner: p && p.isOwner ? p.isOwner() : null, targetId: p && p.targetSessionId ? p.targetSessionId() : null,
        workspace: { live: vis('#live-session-view'), cmd: vis('#command-input'), toolbar: vis('#command-toolbar'), panel: vis('#agentic-state'), splitHandle: vis('#task-bar-resize-handle'), cmdHandle: vis('#command-pane-resize-handle') },
        inputDisabled: !!(document.getElementById('command-input') && document.getElementById('command-input').disabled),
        overlays: { notFound: vis('#popout-not-found'), ended: vis('#popout-ended'), endedOverlay: vis('#session-ended-overlay'), sleeping: vis('#session-sleeping-overlay'), lost: vis('#session-lost-connection'), chip: vis('#popout-viewer-chip'), waiting: vis('#waiting-banner') },
        restartedHref: (() => { const a = document.getElementById('popout-restarted-link'); return a ? a.getAttribute('href') : null; })(),
        history: (() => { const a = document.getElementById('popout-open-history'); if (!a) return null; const cs = getComputedStyle(a); const r = a.getBoundingClientRect(); return { tag: a.tagName, href: a.getAttribute('href'), target: a.getAttribute('target'), rel: a.getAttribute('rel'), visible: cs.display !== 'none' && cs.visibility !== 'hidden' && r.width > 0 && r.height > 0 }; })(),
        destructive: Array.from(document.querySelectorAll('[onclick*="killSession"],[onclick*="restartSession"],[onclick*="renameAgent"],[onclick*="confirmRestart"],[onclick*="killSessionDirect"],[onclick*="restartDirect"]')).map(el => (el.id || el.textContent.trim().slice(0, 30)) + ':' + (el.getAttribute('onclick') || '').slice(0, 40)),
        toasts: document.querySelectorAll('.notification-toast').length,
    };
})()`;

// Where does the uuid appear? Allowed: body[data-target-session-id], action hrefs.
const LEAK_PROBE = (uuid) => `(() => {
    const id = ${JSON.stringify(uuid)}; const out = { text: [], attrs: [], title: document.title.includes(id) };
    const ALLOWED_HREF = (el) => el.tagName === 'A' && (['popout-open-main-btn', 'terminal-open-window-link', 'popout-restarted-link', 'popout-open-history'].includes(el.id) || el.classList.contains('overflow-menu-open-window'));
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    let n; while ((n = walker.nextNode())) { if (n.nodeValue.includes(id)) { const el = n.parentElement; const cs = el ? getComputedStyle(el) : null; if (!cs || (cs.display !== 'none' && cs.visibility !== 'hidden')) out.text.push((el && (el.id || el.className)) || 'text'); } }
    for (const el of document.querySelectorAll('*')) for (const a of el.attributes) {
        if (!a.value.includes(id)) continue;
        if (el === document.body && a.name === 'data-target-session-id') continue;
        if (a.name === 'href' && ALLOWED_HREF(el)) continue;
        out.attrs.push((el.tagName + (el.id ? '#' + el.id : '') + '[' + a.name + ']').toLowerCase());
    }
    return out;
})()`;

async function run() {
    const uuidUnknown = randomUuid();

    // ── A. HTTP/backend contract ──────────────────────────────────────────
    console.log('\n── A. backend contract');
    const routeOk = await httpJson('GET', '/agent/' + uuidUnknown);
    check('1 GET /agent/<valid uuid> renders the shell (200 HTML, agent boot attrs)', routeOk.status === 200 && /data-entry-mode="agent"/.test(routeOk.text) && routeOk.text.includes(`data-target-session-id="${uuidUnknown}"`), `status=${routeOk.status}`);
    const bad = await httpJson('GET', '/agent/not-a-uuid');
    check('2 GET /agent/<malformed> -> 400', bad.status === 400, `status=${bad.status}`);
    const upper = await httpJson('GET', '/agent/' + uuidUnknown.toUpperCase());
    check('2 GET /agent/<non-canonical uppercase uuid> -> 400', upper.status === 400, `status=${upper.status}`);
    const trav = await httpJson('GET', '/agent/..%2F..%2Fetc');
    check('2 GET /agent/<traversal> is not the shell', trav.status !== 200 || !/data-entry-mode="agent"/.test(trav.text), `status=${trav.status}`);
    const resolveUnknown = await httpJson('GET', `/api/sessions/${uuidUnknown}/resolve`);
    check('19 resolver for unknown id -> 200 state=not_found (no routing fields fabricated)', resolveUnknown.status === 200 && resolveUnknown.data && resolveUnknown.data.state === 'not_found' && resolveUnknown.data.session_id === uuidUnknown && !resolveUnknown.data.tmux_session && !resolveUnknown.data.name, `status=${resolveUnknown.status} data=${JSON.stringify(resolveUnknown.data).slice(0, 120)}`);
    const resolveBad = await httpJson('GET', '/api/sessions/not-a-uuid/resolve');
    check('3 resolver for malformed id -> 400', resolveBad.status === 400, `status=${resolveBad.status}`);
    const spoof = await rawGet('/agent/' + uuidUnknown, 'evil.example.com');
    check('7 spoofed non-localhost Host on a loopback client is refused (no shell served)', spoof.status !== 200 || !/data-entry-mode="agent"/.test(spoof.body), `status=${spoof.status}`);

    // ── B. Real isolated terminal session ─────────────────────────────────
    console.log('\n── B. real session');
    const launch = await httpJson('POST', '/api/sessions/launch', { working_dir: '/tmp', agent_type: 'terminal', display_name: 'Popout QA' });
    const sid = launch.data && launch.data.session_id; const sname = launch.data && launch.data.session_name;
    check('B launch isolated terminal session', !!sid && UUID_RE.test(sid), JSON.stringify({ sid, sname, status: launch.status }));
    if (!sid) throw new Error('cannot launch a terminal session on the isolated server');
    await sleep(2500);

    const resolveLive = await httpJson('GET', `/api/sessions/${sid}/resolve`);
    const rl = resolveLive.data || {};
    const aname = rl.name || sname; // resolver's agent name is the REST {name}
    const REQ_KEYS = ['session_id', 'state', 'agent_type', 'name', 'tmux_session', 'display_name', 'auto_name', 'board_job_title', 'board_project', 'status', 'waiting_for_input', 'awaiting_user', 'waiting_reason', 'waiting_summary', 'working', 'done'];
    const missingLive = REQ_KEYS.filter(k => !(k in rl));
    check('18 resolver for a live session carries the full identity/status key set', missingLive.length === 0, `missing=${JSON.stringify(missingLive)}`);
    check('18 resolver for live session returns exact routing tuple', resolveLive.status === 200 && rl.state === 'active' && rl.agent_type === 'terminal' && typeof rl.name === 'string' && rl.name.length > 0 && rl.tmux_session === `terminal-${sid}` && rl.display_name === 'Popout QA', JSON.stringify({ state: rl.state, agent_type: rl.agent_type, name: rl.name, tmux: rl.tmux_session, dn: rl.display_name }));

    // exact targeting: mismatched tuple / unknown must not act
    const wrongName = await httpJson('POST', `/api/sessions/live/definitely-not-${aname}/send`, { command: 'echo POPOUT-MISMATCH-MARKER', agent_type: 'terminal', session_id: sid });
    check('4 send with name that does not belong to session_id -> 400/404', wrongName.status === 400 || wrongName.status === 404, `status=${wrongName.status}`);
    const unknownSend = await httpJson('POST', `/api/sessions/live/${encodeURIComponent(aname)}/send`, { command: 'echo POPOUT-UNKNOWN-MARKER', agent_type: 'terminal', session_id: uuidUnknown });
    check('4 send with unknown session_id -> 404/400', unknownSend.status === 404 || unknownSend.status === 400, `status=${unknownSend.status}`);
    const wrongType = await httpJson('POST', `/api/sessions/live/${encodeURIComponent(aname)}/resize`, { columns: 100, agent_type: 'claude', session_id: sid });
    check('3 resize with mismatched agent_type -> 400/404', wrongType.status === 400 || wrongType.status === 404, `status=${wrongType.status}`);
    await sleep(800);
    const tmuxForm = await httpJson('POST', `/api/sessions/live/terminal-${sid}/send`, { command: 'echo POPOUT-TMUXNAME-MARKER', agent_type: 'terminal', session_id: sid });
    check('4 REST accepts the canonical tmux name (launch session_name) as {name} with a matching tuple (compat)', tmuxForm.status === 200, `status=${tmuxForm.status} ${JSON.stringify(tmuxForm.data).slice(0, 80)}`);
    const cap0 = await httpJson('GET', `/api/sessions/live/${encodeURIComponent(aname)}/capture?agent_type=terminal&session_id=${sid}`);
    const cap0txt = (cap0.data && cap0.data.capture) || '';
    check('4 rejected requests had no side effect on the pane', !/POPOUT-MISMATCH-MARKER|POPOUT-UNKNOWN-MARKER/.test(cap0txt));
    const wsBase = BASE.replace(/^http/, 'ws');
    const wsMismatch = await wsCloseCode(`${wsBase}/ws/terminal/terminal-${sid}?agent_type=terminal&session_id=${uuidUnknown}`, 2500);
    check('4 /ws/terminal with tuple mismatch is closed (policy violation)', wsMismatch.closed && (wsMismatch.code === 1008 || wsMismatch.code === 4004 || wsMismatch.code === 1002 || wsMismatch.code === 1011 || wsMismatch.code === 1006), JSON.stringify(wsMismatch));
    const wsNameOnly = await wsCloseCode(`${wsBase}/ws/terminal/${encodeURIComponent(aname)}`, 2500);
    check('4 /ws/terminal by agent (folder) name without session tuple does not stay attached', wsNameOnly.closed, JSON.stringify(wsNameOnly));

    // popout tab (real route, real sockets)
    const t1 = await openTab(`${BASE}/agent/${sid}`);
    await t1.waitFor(`window._coralPopout && window._coralPopout.getState && window._coralPopout.getState() !== 'loading'`, 10000);
    await sleep(800);
    let s1 = await t1.ev(SHELL_PROBE);
    check('1 popout boot attrs + popout-mode class', s1.mode === 'agent' && s1.target === sid && s1.popoutClass, JSON.stringify({ mode: s1.mode, target: s1.target, cls: s1.popoutClass }));
    check('8 chrome absent/hidden (top bar, sidebar, handle, tablet toggle, mobile bar/list, welcome)', Object.values(s1.chrome).every(v => v !== 'visible'), JSON.stringify(s1.chrome));
    check('8 .layout fills the viewport', s1.layoutH !== null && Math.abs(s1.layoutH - s1.innerH) <= 2, `${s1.layoutH} vs ${s1.innerH}`);
    check('8 workspace present: live view, command input, quick-action strip, tools pane, split + command handles', Object.values(s1.workspace).every(v => v === 'visible'), JSON.stringify(s1.workspace));
    check('9 slim header identity is the resolved display name', /Popout QA/.test(s1.identity) && !s1.identity.includes(sid) && !/terminal-[0-9a-f]{8}/.test(s1.identity), s1.identity.trim());
    check('9 header carries no agent-type badge (decluttered)', s1.badge === '' && await t1.ev(`!document.getElementById('terminal-type-badge')`), s1.badge);
    check('9 state pill has an agreed data-state and matching text', ['working', 'idle', 'waiting', 'sleeping', 'ended', 'reconnecting', 'loading', 'not-found'].includes(s1.pill) && (s1.pill !== 'idle' || /^Idle$/i.test(s1.pillText.trim())) && (s1.pill !== 'working' || /^Working$/i.test(s1.pillText.trim())), JSON.stringify({ pill: s1.pill, text: s1.pillText.trim() }));
    check('10 document.title is "<glyph> <identity> · Coral" without the id', /Popout QA · Coral$/.test(s1.title) && !s1.title.includes(sid), s1.title);
    check('11 Open in Coral is a noopener anchor to /#chat/<uuid>', s1.openMain && s1.openMain.tag === 'A' && s1.openMain.href === `/#chat/${sid}` && s1.openMain.target === '_blank' && /noopener/.test(s1.openMain.rel || '') && /noreferrer/.test(s1.openMain.rel || ''), JSON.stringify(s1.openMain));
    check('11 panel toggle has aria-pressed and an accessible name', s1.toggle && (s1.toggle.pressed === 'true' || s1.toggle.pressed === 'false') && !!s1.toggle.name, JSON.stringify(s1.toggle));
    check('18 hooks present in popout mode (_coralPopout + existing test hooks)', Object.values(s1.hooks).every(Boolean), JSON.stringify(s1.hooks));
    check('18 targetSessionId() is the URL uuid; location.hash untouched (no #chat push)', s1.targetId === sid && s1.hash === '', JSON.stringify({ target: s1.targetId, hash: s1.hash }));
    const leak = await t1.ev(LEAK_PROBE(sid));
    check('9 no session id in visible text / document.title / non-href attributes', leak.text.length === 0 && !leak.title && leak.attrs.length === 0, JSON.stringify(leak));
    check('37 no destructive controls in the popout', s1.destructive.length === 0, JSON.stringify(s1.destructive));
    check('32 identity region is aria-live=polite', s1.identityLive === 'polite', String(s1.identityLive));
    check('B no page exceptions during popout boot', t1.exceptions.length === 0, JSON.stringify(t1.exceptions.slice(0, 3)));
    const audit0 = auditRouting(t1.requests, { agent_type: 'terminal', session_id: sid, name: aname, tmux_session: `terminal-${sid}` });
    check('4 every popout REST call at boot carries both routing fields from the resolver', t1.requests.filter(r => /\/api\/sessions\/live\//.test(r.url)).length > 0 && audit0.length === 0, JSON.stringify(audit0.slice(0, 3)));
    const wsTerm = t1.wsCreated.filter(u => /\/ws\/terminal\//.test(u));
    check('4 terminal socket URL carries the canonical tmux name + exact tuple', wsTerm.length >= 1 && wsTerm.every(u => u.includes(`/ws/terminal/terminal-${sid}`) && u.includes(`session_id=${sid}`)), JSON.stringify(wsTerm));

    // 12: panel toggle + storage isolation
    await t1.ev(`localStorage.removeItem('coral-agentic-collapsed'); localStorage.setItem('coral-taskbar-width', '333'); true`);
    await t1.ev(`document.getElementById('popout-panel-toggle-btn').click(); true`); await sleep(300);
    const st = await t1.ev(`({ pressed: document.getElementById('popout-panel-toggle-btn').getAttribute('aria-pressed'), main: localStorage.getItem('coral-agentic-collapsed'), pop: localStorage.getItem('coral-agentic-collapsed:popout'), width: localStorage.getItem('coral-taskbar-width'), panel: getComputedStyle(document.getElementById('agentic-state')).display })`);
    check('12 panel toggle persists under :popout key and leaves main-app keys untouched', st.pop !== null && st.main === null && st.width === '333', JSON.stringify(st));
    await t1.ev(`document.getElementById('popout-panel-toggle-btn').click(); true`); await sleep(200);

    // 6: refresh keeps the session
    await t1.Page.reload(); await t1.Page.loadEventFired();
    await t1.waitFor(`window._coralPopout && window._coralPopout.getState && window._coralPopout.getState() !== 'loading'`, 10000); await sleep(600);
    s1 = await t1.ev(SHELL_PROBE);
    check('6 refresh re-renders the same session from server data', s1.target === sid && /Popout QA/.test(s1.identity) && s1.workspace.cmd === 'visible' && !s1.inputDisabled, JSON.stringify({ target: s1.target, identity: s1.identity.trim(), disabled: s1.inputDisabled }));

    // 26: command targeting through the popout's own send path
    await t1.ev(`document.getElementById('command-input').value = 'echo POPOUT-QA-MARKER-1'; true`);
    await t1.ev(`window.sendCommand(); true`); await sleep(1500);
    let cap1 = await httpJson('GET', `/api/sessions/live/${encodeURIComponent(aname)}/capture?agent_type=terminal&session_id=${sid}`);
    check('26 command typed in the popout reaches exactly this session\'s pane', /POPOUT-QA-MARKER-1/.test((cap1.data && cap1.data.capture) || ''));
    const sendReqs = t1.requests.filter(r => /\/send(\?|$)/.test(r.url) && r.method === 'POST');
    const audit1 = auditRouting(t1.requests, { agent_type: 'terminal', session_id: sid, name: aname, tmux_session: `terminal-${sid}` });
    const wsTuple = t1.wsCreated.filter(u => /\/ws\/terminal\//.test(u)).every(u => u.includes(`/ws/terminal/terminal-${sid}`) && u.includes(`session_id=${sid}`) && u.includes('agent_type=terminal'));
    check('26 input path uses the exact tuple (POST /send with both fields, or the tuple-bearing terminal socket)', (sendReqs.length === 0 ? wsTuple : true) && audit1.length === 0, JSON.stringify({ sends: sendReqs.length, wsTuple, bad: audit1.slice(0, 3) }));

    // 28/29: same-browser owner/viewer
    const t2 = await openTab(`${BASE}/agent/${sid}`, { width: 900, height: 700 });
    await t2.waitFor(`window._coralPopout && window._coralPopout.getState && window._coralPopout.getState() !== 'loading'`, 10000); await sleep(800);
    let o1 = await t1.ev(`window._coralPopout.isOwner()`); let o2 = await t2.ev(`window._coralPopout.isOwner()`);
    check('28 exactly one interactive owner across two same-profile windows', (o1 ? 1 : 0) + (o2 ? 1 : 0) === 1, JSON.stringify({ first: o1, second: o2 }));
    const chip2 = await t2.ev(`(() => { const c = document.getElementById('popout-viewer-chip'); return c ? getComputedStyle(c).display !== 'none' : false; })()`);
    const chip1 = await t1.ev(`(() => { const c = document.getElementById('popout-viewer-chip'); return c ? getComputedStyle(c).display !== 'none' : false; })()`);
    check('28 viewer chip shown only on the non-owner', (o2 ? chip1 : chip2) && !(o2 ? chip2 : chip1), JSON.stringify({ chip1, chip2 }));
    // resize contention: count /resize + WS resize frames from each tab over 4s at different widths
    for (const t of [t1, t2]) await t.ev(`window.__resizeCalls = 0; const of = window.fetch; window.fetch = (u, o) => { if (/\\/resize$/.test(String(u).split('?')[0]) && o && (o.method || 'GET').toUpperCase() === 'POST') window.__resizeCalls++; return of(u, o); }; true`);
    await t1.Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
    await t2.Emulation.setDeviceMetricsOverride({ width: 900, height: 700, deviceScaleFactor: 1, mobile: false });
    await sleep(4000);
    const r1 = await t1.ev(`window.__resizeCalls`); const r2 = await t2.ev(`window.__resizeCalls`);
    o1 = await t1.ev(`window._coralPopout.isOwner()`); o2 = await t2.ev(`window._coralPopout.isOwner()`);
    const viewerCalls = o1 ? r2 : r1; const ownerCalls = o1 ? r1 : r2;
    check('29 only the owner posts /resize; no oscillation (viewer 0, owner <= 2 after settle)', viewerCalls === 0 && ownerCalls <= 2, JSON.stringify({ owner: ownerCalls, viewer: viewerCalls, o1, o2 }));
    // ownership transfer
    const viewer = o1 ? t2 : t1; const owner = o1 ? t1 : t2;
    await viewer.ev(`window._coralPopout.claimOwnership(); true`); await sleep(500);
    const afterClaim = { v: await viewer.ev(`window._coralPopout.isOwner()`), o: await owner.ev(`window._coralPopout.isOwner()`) };
    check('28 claimOwnership() transfers ownership to the viewer and revokes the previous owner', afterClaim.v === true && afterClaim.o === false, JSON.stringify(afterClaim));
    await viewer.ev(`document.getElementById('command-input').value = 'echo POPOUT-QA-MARKER-2'; window.sendCommand(); true`); await sleep(1500);
    cap1 = await httpJson('GET', `/api/sessions/live/${encodeURIComponent(aname)}/capture?agent_type=terminal&session_id=${sid}`);
    check('28 new owner input reaches the pane', /POPOUT-QA-MARKER-2/.test((cap1.data && cap1.data.capture) || ''));
    await t2.close();
    await sleep(600);
    let backOwner = await t1.ev(`window._coralPopout.isOwner()`);
    if (!backOwner) { await t1.ev(`window._coralPopout.claimOwnership(); true`); await sleep(400); backOwner = await t1.ev(`window._coralPopout.isOwner()`); }
    check('28 after the owner closes (pagehide release) the remaining window can claim ownership', backOwner === true, String(backOwner));

    // 30: cross-context viewer (separate profile => no BroadcastChannel)
    const ctx = await newContext();
    const t3 = await openTab(`${BASE}/agent/${sid}`, { contextId: ctx, width: 1000, height: 700 });
    await t3.waitFor(`window._coralPopout && window._coralPopout.getState && window._coralPopout.getState() !== 'loading'`, 10000); await sleep(800);
    const s3 = await t3.ev(SHELL_PROBE);
    check('30 cross-profile viewer renders the same exact session with no exceptions', s3.target === sid && /Popout QA/.test(s3.identity) && t3.exceptions.length === 0, JSON.stringify({ target: s3.target, identity: s3.identity.trim(), exc: t3.exceptions.slice(0, 2) }));
    const ws3 = t3.wsCreated.filter(u => /\/ws\/terminal\//.test(u));
    check('30 cross-profile viewer attaches only to the exact tmux tuple (no cross-session targeting)', ws3.length >= 1 && ws3.every(u => u.includes(`/ws/terminal/terminal-${sid}`) && u.includes(`session_id=${sid}`)), JSON.stringify(ws3));
    await t3.ev(`document.getElementById('command-input').value = 'echo POPOUT-QA-MARKER-3'; window.sendCommand(); true`); await sleep(1500);
    cap1 = await httpJson('GET', `/api/sessions/live/${encodeURIComponent(aname)}/capture?agent_type=terminal&session_id=${sid}`);
    check('30 documented limitation: cross-profile client can still send (input reached this pane only)', /POPOUT-QA-MARKER-3/.test((cap1.data && cap1.data.capture) || ''));
    const audit3 = auditRouting(t3.requests, { agent_type: 'terminal', session_id: sid, name: aname, tmux_session: `terminal-${sid}` });
    check('30 cross-profile viewer also sends only the exact tuple', audit3.length === 0, JSON.stringify(audit3.slice(0, 3)));
    await t3.close();

    // 33: the resolver must derive state exactly like the list row. It used to
    // treat ANY trailing notification as "waiting for input", and the popout
    // merges the resolver record over the live row, so Claude Code's idle
    // reminder ("waiting for your input", sent ~1 min after every turn ends)
    // showed "Needs input" on an agent that was asking nothing.
    const postEvent = (event_type, summary) => httpJson('POST', `/api/sessions/live/${encodeURIComponent(aname)}/events`, { event_type, summary, session_id: sid });
    const stateOf = (o) => ({ waiting_for_input: o && o.waiting_for_input, awaiting_user: o && o.awaiting_user, waiting_reason: o && o.waiting_reason, waiting_summary: o && o.waiting_summary });
    const listRow = async () => { const l = await httpJson('GET', '/api/sessions/live'); return (Array.isArray(l.data) ? l.data : []).find(r => r.session_id === sid); };
    await postEvent('prompt_submit', 'User submitted prompt');
    await postEvent('notification', 'Notification: Claude needs your permission to use Bash');
    const pendingResolve = (await httpJson('GET', `/api/sessions/${sid}/resolve`)).data || {};
    const pendingRow = await listRow();
    check('33 real server: a pending permission request is Needs input in resolver and list alike', pendingResolve.waiting_for_input === true && pendingResolve.awaiting_user === false && pendingResolve.waiting_reason === 'notification' && JSON.stringify(stateOf(pendingResolve)) === JSON.stringify(stateOf(pendingRow)), JSON.stringify({ resolve: stateOf(pendingResolve), list: stateOf(pendingRow) }));
    // Denied: no tool event follows, the turn just ends, then the idle reminder.
    await postEvent('stop', 'Agent stopped: unknown');
    await postEvent('notification', 'Notification: Claude is waiting for your input');
    const realIdleResolve = (await httpJson('GET', `/api/sessions/${sid}/resolve`)).data || {};
    const idleRow = await listRow();
    const idleStatus = (await httpJson('GET', `/api/sessions/${sid}/status`)).data || {};
    check('33 real server: turn ended + idle reminder is Ready for input in the resolver, never Needs input', realIdleResolve.waiting_for_input === false && realIdleResolve.awaiting_user === true && realIdleResolve.waiting_reason === null && realIdleResolve.waiting_summary === null && realIdleResolve.done === false, JSON.stringify(stateOf(realIdleResolve)));
    check('33 real server: resolver state fields equal the list row', !!idleRow && JSON.stringify(stateOf(realIdleResolve)) === JSON.stringify(stateOf(idleRow)), JSON.stringify({ resolve: stateOf(realIdleResolve), list: stateOf(idleRow) }));
    check('33 real server: status endpoint is not waiting_for_input for an idle agent', idleStatus.waiting_for_input === false && idleStatus.awaiting_user === true && idleStatus.state !== 'waiting_for_input', JSON.stringify({ state: idleStatus.state, ...stateOf(idleStatus) }));

    // 21: ended on kill; 24: no reconnect loop
    const wsBefore = t1.wsCreated.filter(u => /\/ws\/terminal\//.test(u)).length;
    const kill = await httpJson('POST', `/api/sessions/live/${encodeURIComponent(aname)}/kill`, { agent_type: 'terminal', session_id: sid });
    check('B kill request accepted', kill.status === 200 && kill.data && kill.data.ok === true, `status=${kill.status}`);
    const ended = await t1.waitFor(`(() => { const e = document.getElementById('popout-ended'); const p = document.getElementById('terminal-state-pill'); return e && getComputedStyle(e).display !== 'none' && p && p.getAttribute('data-state') === 'ended'; })()`, 12000);
    s1 = await t1.ev(SHELL_PROBE);
    check('21 kill from outside -> Ended state within a tick (overlay + pill)', ended, JSON.stringify({ ended: s1.overlays.ended, pill: s1.pill }));
    check('21 input disabled and title marks ended', s1.inputDisabled && /^✕ /.test(s1.title) && !s1.title.includes(sid), JSON.stringify({ disabled: s1.inputDisabled, title: s1.title }));
    check('20 ended state offers #popout-open-history as a noopener anchor to /#session/<uuid>', !!s1.history && s1.history.tag === 'A' && s1.history.visible && s1.history.href === `/#session/${sid}` && s1.history.target === '_blank' && /noopener/.test(s1.history.rel || '') && /noreferrer/.test(s1.history.rel || ''), JSON.stringify(s1.history));
    const leakEnded = await t1.ev(LEAK_PROBE(sid));
    check('9 ended state: id only in boot attr and allowed hrefs', leakEnded.text.length === 0 && !leakEnded.title && leakEnded.attrs.length === 0, JSON.stringify(leakEnded));
    await sleep(3500);
    const wsAfter = t1.wsCreated.filter(u => /\/ws\/terminal\//.test(u)).length;
    check('24 no terminal reconnect attempts after terminal_closed', wsAfter === wsBefore, `${wsBefore} -> ${wsAfter}`);
    const dEnded = (await t1.ev(SHELL_PROBE)).destructive; check('37 still no destructive controls after ending', dEnded.length === 0, JSON.stringify(dEnded));
    await t1.close();
    // cleanup: wait until the session is gone from the live list
    let gone = false;
    for (let i = 0; i < 50 && !gone; i++) { const l = await httpJson('GET', '/api/sessions/live'); gone = Array.isArray(l.data) && !l.data.some(s => s.session_id === sid); if (!gone) await sleep(200); }
    check('B cleanup: test session removed from /api/sessions/live', gone);

    // ── C. Fixture-driven states ──────────────────────────────────────────
    console.log('\n── C. fixture states');
    const A = randomUuid(), B = randomUuid(), H = randomUuid(), N = randomUuid(), O = randomUuid();
    const liveA = { name: 'coral-go', display_name: '', auto_name: 'Auto Bot', board_job_title: 'Debugger', agent_type: 'claude', session_id: A, tmux_session: `claude-${A}`, summary: 'Working on tests', first_prompt: 'Fix the flaky test', context_pct: 12, waiting_for_input: true, working_directory: '/repo/coral-go', board_project: null };
    const resolveA = { session_id: A, state: 'active', active: true, sleeping: false, agent_type: 'claude', name: 'coral-go', tmux_session: `claude-${A}`, display_name: '', auto_name: 'Auto Bot', board_job_title: 'Debugger', board_project: null, icon: null, working_directory: '/repo/coral-go', status: 'Thinking', waiting_for_input: true, stuck: false, not_started: false };
    const liveO = { name: 'other-proj', display_name: 'Other Agent', agent_type: 'claude', session_id: O, tmux_session: `claude-${O}`, summary: 'x', context_pct: 5, waiting_for_input: false, working_directory: '/repo/other' };
    const fx = { live: [liveA, liveO], resolve: { [A]: resolveA, [H]: { session_id: H, state: 'finished', active: false, sleeping: false, agent_type: 'claude', name: 'coral-go', tmux_session: null, display_name: 'Finished One', auto_name: '', board_job_title: '', board_project: null } }, history: { [H]: { session_id: H, display_name: 'Finished One', messages: [], agent_type: 'claude', summary: 'done' } } };

    // not found
    const tn = await openTab(`${BASE}/agent/${N}`, { stub: stubSource(fx) });
    await tn.waitFor(`(() => { const e = document.getElementById('popout-not-found'); return e && getComputedStyle(e).display !== 'none'; })()`, 8000); await sleep(400);
    let sn = await tn.ev(SHELL_PROBE); const cn = await tn.ev(`({ calls: window.__calls, ws: window.__wsAttempts })`);
    check('19 valid unknown id -> not-found state, pill not-found', sn.overlays.notFound === 'visible' && sn.pill === 'not-found', JSON.stringify({ nf: sn.overlays.notFound, pill: sn.pill }));
    check('19 not-found makes zero send/resize/attach calls and opens no terminal socket', cn.calls.filter(c => c.method !== 'GET' && /\/(send|keys|resize|kill|wake)$/.test(c.path)).length === 0 && cn.ws.filter(u => /\/ws\/terminal/.test(u)).length === 0, JSON.stringify(cn));
    check('19 not-found never shows the history link', !sn.history || !sn.history.visible, JSON.stringify(sn.history));
    check('19 not-found offers Retry / Open Coral', await tn.ev(`!!document.getElementById('popout-retry-btn') || !!document.querySelector('#popout-not-found button, #popout-not-found a')`));
    check('19 not-found: no exceptions', tn.exceptions.length === 0, JSON.stringify(tn.exceptions.slice(0, 2)));
    await tn.close();

    // history-only
    const th = await openTab(`${BASE}/agent/${H}`, { stub: stubSource(fx) });
    await th.waitFor(`(() => { const e = document.getElementById('popout-ended'); return e && getComputedStyle(e).display !== 'none'; })()`, 8000); await sleep(400);
    const sh = await th.ev(SHELL_PROBE); const ch = await th.ev(`({ ws: window.__wsAttempts, calls: window.__calls })`);
    check('20 history-only id -> Ended, input disabled, identity from history, no attach', sh.overlays.ended === 'visible' && sh.pill === 'ended' && sh.inputDisabled && /Finished One/.test(sh.identity) && ch.ws.filter(u => /\/ws\/terminal/.test(u)).length === 0, JSON.stringify({ ended: sh.overlays.ended, pill: sh.pill, disabled: sh.inputDisabled, identity: sh.identity.trim(), ws: ch.ws }));
    check('20 ended title uses the ✕ glyph and no id', /^✕ .*Finished One · Coral$/.test(sh.title) && !sh.title.includes(H), sh.title);
    check('20 history-only state shows #popout-open-history -> /#session/<uuid>', !!sh.history && sh.history.visible && sh.history.href === `/#session/${H}` && /noopener/.test(sh.history.rel || ''), JSON.stringify(sh.history));
    const leakH = await th.ev(LEAK_PROBE(H));
    check('9 history-only: id only in boot attr and allowed hrefs', leakH.text.length === 0 && !leakH.title && leakH.attrs.length === 0, JSON.stringify(leakH));
    await th.close();

    // live fixture: identity chain, pill, title, rename, sleeping, toasts, restart
    const ta = await openTab(`${BASE}/agent/${A}`, { stub: stubSource(fx) });
    await ta.waitFor(`window._coralPopout && window._coralPopout.getState && window._coralPopout.getState() !== 'loading'`, 8000); await sleep(500);
    let sa = await ta.ev(SHELL_PROBE);
    check('9 identity chain: auto_name wins over board_job_title when display_name empty', /^Auto Bot/.test(sa.identity.trim()), sa.identity.trim());
    check('9 pill reflects waiting_for_input from the resolver before any WS tick (no state flash)', sa.pill === 'waiting', JSON.stringify({ pill: sa.pill, banner: sa.overlays.waiting }));
    check('10 title glyph ⏸ for needs input', /^⏸ Auto Bot · Coral$/.test(sa.title), sa.title);
    const fxCalls = await ta.ev(`window.__calls.filter(c => /\\/api\\/sessions\\/live\\/[^/]+\\/(capture|poll|send|keys|resize)(\\?|$)/.test(c.path) || /^\\/api\\/sessions\\/live\\/[^/]+$/.test(c.path)).map(c => ({ path: c.path, method: c.method, query: c.query, body: c.body }))`);
    const fxBad = fxCalls.filter(c => { const name = decodeURIComponent(c.path.split('/')[4]); let at = null, sid2 = null; if (c.method === 'GET') { const q = new URLSearchParams(c.query); at = q.get('agent_type'); sid2 = q.get('session_id'); } else { try { const b = JSON.parse(c.body || '{}'); at = b.agent_type; sid2 = b.session_id; } catch {} } return at !== 'claude' || sid2 !== A || name !== 'coral-go'; });
    check('4 fixture popout: every per-session REST call uses resolver name + both routing fields', fxCalls.length > 0 && fxBad.length === 0, JSON.stringify({ n: fxCalls.length, bad: fxBad.slice(0, 3) }));
    check('26 placeholder names the resolved identity', await ta.ev(`/^Sending to: Auto Bot/.test(document.getElementById('command-input').placeholder)`));
    // rename via WS diff
    await ta.ev(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [${JSON.stringify({ ...liveA, display_name: 'Renamed Bot', waiting_for_input: false, working: true })}] }); true`); await sleep(300);
    sa = await ta.ev(SHELL_PROBE);
    check('25 rename via live tick updates identity, pill and title in place', /^Renamed Bot/.test(sa.identity.trim()) && sa.pill === 'working' && /^● Renamed Bot · Coral$/.test(sa.title) && sa.hash === '', JSON.stringify({ identity: sa.identity.trim(), pill: sa.pill, title: sa.title, hash: sa.hash }));
    await ta.ev(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [{ name: 'coral-go', agent_type: 'claude', session_id: ${JSON.stringify(A)}, summary: 'Completely different summary' }] }); true`); await sleep(200);
    check('25 summary-only tick leaves identity alone', /^Renamed Bot/.test((await ta.ev(SHELL_PROBE)).identity.trim()));
    // unified state vocabulary on the popout pill + title glyph (tasks #165-#167)
    {
        const OFF = { working: false, waiting_for_input: false, awaiting_user: false, not_started: false, stuck: false, done: false, sleeping: false };
        const seen = [];
        for (const [patch, pill, text, glyph] of [[{ awaiting_user: true }, 'your-turn', 'Ready for input', '◯'], [{ awaiting_user: true, not_started: true }, 'check', 'Check terminal', '⏸'], [{ awaiting_user: true, waiting_for_input: true }, 'waiting', 'Needs input', '⏸'], [{ waiting_for_input: true, stuck: true }, 'stuck', 'Stuck', '!'], [{}, 'idle', 'Idle', '○'], [{ working: true }, 'working', 'Working', '●']]) {
            await ta.ev(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [${JSON.stringify({ ...liveA, display_name: 'Renamed Bot', ...OFF, ...patch })}] }); true`); await sleep(250);
            const p = await ta.ev(SHELL_PROBE);
            seen.push({ want: pill, pill: p.pill, text: p.pillText.trim(), okText: p.pillText.trim() === text, okTitle: p.title === `${glyph} Renamed Bot · Coral` });
        }
        await ta.ev(`document.querySelectorAll('.notification-toast').forEach(t => t.remove()); true`); // own-session toast from the Needs input step is expected; clear it for check 27
        check('state vocabulary: popout pill data-state, sentence-case text and title glyph follow the shared priority', seen.every(x => x.want === x.pill && x.okText && x.okTitle), JSON.stringify(seen));
    }
    // other-session toast suppressed
    await ta.ev(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [${JSON.stringify({ ...liveO, waiting_for_input: true, waiting_summary: 'needs you' })}] }); true`); await sleep(300);
    check('27 needs-input toast for another session is suppressed in the popout', (await ta.ev(SHELL_PROBE)).toasts === 0);
    // sleeping
    const wsBeforeSleep = await ta.ev(`window.__wsAttempts.filter(u => /\\/ws\\/terminal/.test(u)).length`);
    await ta.ev(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [${JSON.stringify({ ...liveA, display_name: 'Renamed Bot', sleeping: true, status: 'Sleeping', tmux_session: null, waiting_for_input: false, working: false })}] }); true`); await sleep(400);
    sa = await ta.ev(SHELL_PROBE);
    check('23 sleeping tick -> overlay + pill sleeping + title ◌, Wake control present', sa.overlays.sleeping === 'visible' && sa.pill === 'sleeping' && /^◌ /.test(sa.title) && await ta.ev(`!!document.querySelector('#session-sleeping-overlay button, #session-sleeping-overlay [onclick*="wake" i]')`), JSON.stringify({ sleeping: sa.overlays.sleeping, pill: sa.pill, title: sa.title }));
    check('23 sleeping does not attempt a terminal attach', (await ta.ev(`window.__wsAttempts.filter(u => /\\/ws\\/terminal/.test(u)).length`)) === wsBeforeSleep);
    // restart: A removed, same-name B appears -> ended, link, never follow
    await ta.ev(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [${JSON.stringify({ ...liveA, session_id: B, tmux_session: `claude-${B}`, display_name: 'Renamed Bot', sleeping: false, working: true })}], removed: [${JSON.stringify(A)}] }); true`); await sleep(500);
    sa = await ta.ev(SHELL_PROBE);
    check('22 target removed while a same-name session appears -> Ended, restart link offered, target unchanged', sa.overlays.ended === 'visible' && sa.pill === 'ended' && sa.restartedHref === `/agent/${B}` && sa.targetId === A && sa.inputDisabled, JSON.stringify({ ended: sa.overlays.ended, pill: sa.pill, href: sa.restartedHref, target: sa.targetId, disabled: sa.inputDisabled }));
    check('22 ended-after-restart also offers the history link for the ORIGINAL id', !!sa.history && sa.history.visible && sa.history.href === `/agent/${A}`.replace('/agent/', '/#session/'), JSON.stringify(sa.history));
    check('22 no attach to the restarted session\'s tmux name', (await ta.ev(`window.__wsAttempts.filter(u => u.includes('claude-' + ${JSON.stringify(B)})).length`)) === 0);
    const leakA = await ta.ev(LEAK_PROBE(A)); const leakB = await ta.ev(LEAK_PROBE(B));
    check('9 leak scan after transitions: ids only in URL/boot attr/hrefs', leakA.text.length === 0 && !leakA.title && leakA.attrs.length === 0 && leakB.text.length === 0 && !leakB.title && leakB.attrs.length === 0, JSON.stringify({ A: leakA, B: leakB }));
    check('C fixture tab: no exceptions across transitions', ta.exceptions.length === 0, JSON.stringify(ta.exceptions.slice(0, 3)));
    await ta.close();

    // D5: resolver is the sole routing authority. A 5xx or network error must
    // never fall back to /api/sessions/live or history for routing.
    for (const mode of [503, 'network']) {
        const R = randomUuid();
        // live-list entry deliberately carries WRONG routing fields: only the resolver may route
        const liveR = { ...liveA, session_id: R, name: 'wrong-name', tmux_session: 'claude-WRONG', display_name: 'Should Not Route' };
        const fxR = { live: [liveR], resolve: { [R]: { ...resolveA, session_id: R, tmux_session: `claude-${R}`, display_name: 'Recovered' } }, resolveFail: { [R]: mode }, history: { [R]: { session_id: R, display_name: 'Should Not Route', agent_type: 'claude' } } };
        const tr = await openTab(`${BASE}/agent/${R}`, { stub: stubSource(fxR) });
        await tr.waitFor(`window._coralPopout && window._coralPopout.getState && window._coralPopout.getState() !== 'loading'`, 6000); await sleep(1200);
        const sr = await tr.ev(SHELL_PROBE);
        const cr = await tr.ev(`({ ws: window.__wsAttempts, calls: window.__calls.map(c => c.path) })`);
        const routed = cr.calls.filter(pth => /\/api\/sessions\/live\/[^/]+\//.test(pth) || /\/api\/sessions\/history\//.test(pth));
        check(`D5 resolver ${mode}: no routing derived from the live list or history (no per-session or history calls)`, routed.length === 0, JSON.stringify(routed.slice(0, 4)));
        check(`D5 resolver ${mode}: reconnecting/retry state, not attached, input disabled, not-found hidden`, sr.pill === 'reconnecting' && sr.inputDisabled && sr.overlays.notFound !== 'visible' && cr.ws.filter(u => /\/ws\/terminal/.test(u)).length === 0 && !/Should Not Route/.test(sr.identity), JSON.stringify({ pill: sr.pill, disabled: sr.inputDisabled, nf: sr.overlays.notFound, terminalWs: cr.ws.filter(u => /\/ws\/terminal/.test(u)).length, identity: sr.identity.trim() }));
        const liveActions = await tr.ev(`Array.from(document.querySelectorAll('#command-toolbar button, #command-pane button')).filter(b => /sendCommand|sendRawKeys|cycleModeToggle|sendModeToggle|sendQuickCommand|executeMacro|sendBoardProtocol|resendInputPrompt|sendCommandWithTeam/.test(b.getAttribute('onclick') || '')).filter(b => !b.disabled && b.getAttribute('aria-disabled') !== 'true' && getComputedStyle(b).pointerEvents !== 'none').map(b => (b.textContent.trim() || b.getAttribute('aria-label') || 'btn').slice(0, 20))`);
        await tr.ev(`window.__calls = []; const inp = document.getElementById('command-input'); inp.disabled = false; inp.value = 'echo MUST-NOT-SEND'; try { window.sendCommand(); } catch {} try { window.sendRawKeys(['Enter']); } catch {} true`); await sleep(400);
        const blockedSends = await tr.ev(`window.__calls.filter(c => /\\/(send|keys)$/.test(c.path)).length + window.__wsAttempts.filter(u => /\\/ws\\/terminal/.test(u)).length`);
        check(`D5a resolver ${mode}: programmatic sendCommand/sendRawKeys are refused while reconnecting (no /send, /keys, or terminal socket)`, blockedSends === 0, String(blockedSends));
        check(`D5a resolver ${mode}: terminal quick actions that send input are disabled while reconnecting`, liveActions.length === 0, JSON.stringify(liveActions));
        const resolveCallsBefore = await tr.ev(`window.__resolveCalls`);
        await sleep(2500);
        const resolveCallsAfter = await tr.ev(`window.__calls.filter(c => /\\/resolve$/.test(c.path)).length`);
        check(`D5 resolver ${mode}: retries only the resolver`, resolveCallsAfter > resolveCallsBefore, `${resolveCallsBefore} -> ${resolveCallsAfter}`);
        // recovery: resolver comes back -> attach with the resolver's identity
        await tr.ev(`window.__fx.resolveFail = {}; true`);
        const recovered = await tr.waitFor(`window._coralPopout.getState && ['working','idle','waiting'].includes(window._coralPopout.getState())`, 8000);
        const srr = await tr.ev(SHELL_PROBE); const crr = await tr.ev(`window.__wsAttempts`);
        const crrCalls = await tr.ev(`window.__calls.filter(c => /\\/api\\/sessions\\/live\\/[^/]+\\/(capture|poll|send|keys|resize)/.test(c.path) || /^\\/api\\/sessions\\/live\\/[^/]+$/.test(c.path)).map(c => c.path)`);
        const wrongRouted = crrCalls.filter(pth => pth.includes('/live/wrong-name/')).length + crr.filter(u => u.includes('claude-WRONG')).length;
        check(`D5 resolver ${mode}: recovers and ROUTES from the resolver alone (socket claude-<id>, REST name coral-go; never the list's wrong-name/claude-WRONG)`, recovered && wrongRouted === 0 && crr.filter(u => /\/ws\/terminal/.test(u) && u.includes('claude-' + R) && u.includes('session_id=' + R)).length >= 1, JSON.stringify({ state: srr.state, wrongRouted, ws: crr.filter(u => /terminal/.test(u)), rest: crrCalls.slice(0, 3) }));
        const reEnabled = await tr.ev(`!document.getElementById('command-input').disabled && Array.from(document.querySelectorAll('#command-toolbar button')).some(b => !b.disabled && b.getAttribute('aria-disabled') !== 'true' && /sendCommand|sendRawKeys|cycleModeToggle/.test(b.getAttribute('onclick') || ''))`);
        check(`D5a resolver ${mode}: input and quick actions re-enabled after the resolver reports attachable state`, reEnabled === true, String(reEnabled));
        check(`D5 resolver ${mode}: no exceptions`, tr.exceptions.length === 0, JSON.stringify(tr.exceptions.slice(0, 2)));
        await tr.close();
    }

    // mobile 390px direct workspace
    const tm = await openTab(`${BASE}/agent/${A}`, { stub: stubSource(fx), width: 390, height: 844, mobile: true });
    await tm.waitFor(`window._coralPopout && window._coralPopout.getState && window._coralPopout.getState() !== 'loading'`, 8000); await sleep(500);
    const sm = await tm.ev(SHELL_PROBE);
    const mob = await tm.ev(`(() => { const tb = document.getElementById('command-toolbar'); const r = tb ? tb.getBoundingClientRect() : null; const btns = tb ? Array.from(tb.querySelectorAll('button')).map(b => b.getBoundingClientRect().height) : []; const hdr = document.querySelector('.terminal-header'); const hcs = hdr ? getComputedStyle(hdr) : null; const ta = document.getElementById('command-input'); return { toolbarVisible: !!r && r.height > 0, minBtn: btns.length ? Math.min(...btns.filter(h => h > 0)) : 0, scrollable: tb ? (tb.scrollWidth > tb.clientWidth || getComputedStyle(tb).overflowX === 'auto' || getComputedStyle(tb).overflowX === 'scroll') : false, headerPos: hcs ? hcs.position : null, fontSize: ta ? getComputedStyle(ta).fontSize : null, width: window.innerWidth }; })()`);
    check('31 390px: chrome absent, workspace direct (no tab bar / mobile list / overlay promotion)', sm.chrome.mobileBar !== 'visible' && sm.chrome.mobileList !== 'visible' && sm.workspace.live === 'visible' && sm.workspace.cmd === 'visible', JSON.stringify({ chrome: sm.chrome, ws: sm.workspace }));
    check('31 390px: quick-action strip visible and scrollable with >=44px targets', mob.toolbarVisible && mob.scrollable && mob.minBtn >= 44, JSON.stringify(mob));
    check('31 390px: header sticky, textarea 16px', (mob.headerPos === 'sticky' || mob.headerPos === 'fixed') && parseFloat(mob.fontSize || '0') >= 16, JSON.stringify({ pos: mob.headerPos, font: mob.fontSize }));
    await tm.ev(`document.getElementById('popout-panel-toggle-btn').click(); true`); await sleep(300);
    check('31 390px: panel toggle opens the tools pane as an overlay', await tm.ev(`(() => { const p = document.getElementById('agentic-state'); return !!p && (p.classList.contains('mobile-panel-overlay') || getComputedStyle(p).display !== 'none'); })()`));
    await tm.close();

    // 33: popout merge. popout.js applies Object.assign({}, liveRow, resolverRecord),
    // so resolver fields win. Feed it the REAL resolver reply captured from the
    // server above (idle agent) over an idle live row: the pill must be the
    // calm "Ready for input", with no Needs-input banner, before and after a WS tick.
    const I = randomUuid();
    const liveI = { name: 'coral-go', display_name: 'Idle Dev', agent_type: 'claude', session_id: I, tmux_session: `claude-${I}`, summary: 'finished', context_pct: 9, working_directory: '/repo/coral-go', board_project: null, working: false, waiting_for_input: false, awaiting_user: true, waiting_reason: null, waiting_summary: null, not_started: false, stuck: false, done: false, sleeping: false };
    const resolveI = { ...realIdleResolve, session_id: I, agent_type: 'claude', name: 'coral-go', tmux_session: `claude-${I}`, display_name: 'Idle Dev' };
    const ti = await openTab(`${BASE}/agent/${I}`, { stub: stubSource({ live: [liveI], resolve: { [I]: resolveI }, history: {} }) });
    await ti.waitFor(`window._coralPopout && window._coralPopout.getState && window._coralPopout.getState() !== 'loading'`, 8000); await sleep(500);
    let si = await ti.ev(SHELL_PROBE);
    check('33 popout: real idle resolver reply merged over the live row shows Ready for input, not Needs input', si.pill === 'your-turn' && si.overlays.waiting !== 'visible', JSON.stringify({ pill: si.pill, waiting: si.overlays.waiting, resolver: { waiting_for_input: resolveI.waiting_for_input, awaiting_user: resolveI.awaiting_user } }));
    await ti.ev(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [${JSON.stringify(liveI)}] }); true`); await sleep(300);
    si = await ti.ev(SHELL_PROBE);
    check('33 popout: still Ready for input after a WS tick', si.pill === 'your-turn' && si.overlays.waiting !== 'visible', JSON.stringify({ pill: si.pill, waiting: si.overlays.waiting }));
    check('33 popout idle fixture: no exceptions', ti.exceptions.length === 0, JSON.stringify(ti.exceptions.slice(0, 2)));
    await ti.close();

    // ── D. Main-app regression ────────────────────────────────────────────
    console.log('\n── D. main app');
    const fxMain = { live: [liveA, liveO, { ...liveO, session_id: randomUuid(), display_name: 'Sleeper', sleeping: true, tmux_session: null }], resolve: { [A]: resolveA } };
    const td = await openTab(`${BASE}/#chat/${A}`, { stub: stubSource(fxMain), width: 1440, height: 900 });
    await td.waitFor(`typeof window._coralSetLiveSessions === 'function'`, 8000); await sleep(1200);
    const dm = await td.ev(`(() => { const lv = document.getElementById('live-session-view'); const active = document.querySelector('#live-sessions-list .session-group-item.active'); return { liveShown: lv ? getComputedStyle(lv).display !== 'none' : false, activeSid: active ? active.dataset.sessionId : null, header: (document.getElementById('session-name') || {}).textContent, mode: document.body.getAttribute('data-entry-mode') }; })()`);
    check('34 main app: #chat/<sid> restores the live session on load', dm.liveShown && dm.activeSid === A && /Auto Bot/.test(dm.header || ''), JSON.stringify(dm));
    check('1 main app: dashboard boot attr is empty (not agent mode)', dm.mode === '' || dm.mode === null, String(dm.mode));
    const anchors = await td.ev(`(() => {
        const rows = Array.from(document.querySelectorAll('#live-sessions-list .session-group-item'));
        const info = rows.map(li => { const a = li.querySelector('a.overflow-menu-item.overflow-menu-open-window'); const menu = li.querySelector('.sidebar-kebab-menu'); const firstItem = menu ? Array.from(menu.children).find(c => c.matches('.overflow-menu-item')) : null; return { sid: li.dataset.sessionId, sleeping: li.classList.contains('sleeping') || !!li.querySelector('.session-sleeping, [class*="sleep"]'), href: a ? a.getAttribute('href') : null, label: a ? a.textContent.replace(/\s+/g, ' ').trim() : null, title: a ? a.getAttribute('title') : null, aria: a ? a.getAttribute('aria-label') : null, isFirst: !!a && firstItem === a, hasIcon: !!(a && a.querySelector('svg')), target: a ? a.getAttribute('target') : null, rel: a ? a.getAttribute('rel') : null }; });
        const hdr = document.getElementById('terminal-open-window-link');
        return { rows: info, header: hdr ? { tag: hdr.tagName, href: hdr.getAttribute('href'), target: hdr.getAttribute('target'), rel: hdr.getAttribute('rel'), title: hdr.getAttribute('title'), aria: hdr.getAttribute('aria-label'), hasIcon: !!hdr.querySelector('svg') } : null, oldWording: document.body.innerHTML.includes('Open in new window') || document.body.innerHTML.includes('Open this agent in a new window') };
    })()`);
    const rowA = anchors.rows.find(r => r.sid === A);
    check('15 kebab "Open Agent Tab" anchor on awake row: href /agent/<uuid>, _blank, noopener noreferrer', !!rowA && rowA.href === `/agent/${A}` && rowA.target === '_blank' && /noopener/.test(rowA.rel || '') && /noreferrer/.test(rowA.rel || ''), JSON.stringify(rowA));
    check('15 kebab anchor present on the sleeping row too', anchors.rows.filter(r => r.href).length >= 2, JSON.stringify(anchors.rows.map(r => ({ sid: r.sid.slice(0, 8), href: !!r.href }))));
    const withAnchor = anchors.rows.filter(r => r.href);
    check('15 "Open Agent Tab" is the FIRST actionable item in every awake/sleeping kebab menu', withAnchor.length >= 2 && withAnchor.every(r => r.isFirst), JSON.stringify(withAnchor.map(r => ({ sid: r.sid.slice(0, 8), isFirst: r.isFirst }))));
    check('15 kebab label/title/aria are exactly "Open Agent Tab" with the icon kept', withAnchor.every(r => r.label === 'Open Agent Tab' && r.title === 'Open Agent Tab' && r.aria === 'Open Agent Tab' && r.hasIcon), JSON.stringify(withAnchor.map(r => [r.label, r.title, r.aria, r.hasIcon])));
    check('15 workspace header carries the same anchor in the main app', !!anchors.header && anchors.header.tag === 'A' && anchors.header.href === `/agent/${A}` && anchors.header.target === '_blank' && /noopener/.test(anchors.header.rel || ''), JSON.stringify(anchors.header));
    check('15 workspace header opener accessible text is "Open Agent Tab" with the icon kept', !!anchors.header && anchors.header.title === 'Open Agent Tab' && anchors.header.aria === 'Open Agent Tab' && anchors.header.hasIcon, JSON.stringify(anchors.header));
    check('15 old "Open in new window" wording is absent from the main app DOM', !anchors.oldWording);
    // 35: null DOM targets must not throw
    await td.ev(`for (const id of ['nav-tab-agents-badge', 'terminal-header-label', 'session-name']) { const el = document.getElementById(id); if (el) el.remove(); } true`);
    const excBefore = td.exceptions.length;
    await td.ev(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [${JSON.stringify({ ...liveA, display_name: 'Null Guard', waiting_for_input: true })}] }); window._coralSetLiveSessions(window._coralGetLiveSessions()); true`); await sleep(300);
    check('35 main app: live tick with badge/header/name targets removed throws nothing', td.exceptions.length === excBefore, JSON.stringify(td.exceptions.slice(excBefore, excBefore + 2)));
    // done row must not offer the anchor: simulate killed row
    await td.ev(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [], removed: [${JSON.stringify(O)}] }); true`); await sleep(300);
    const doneRow = await td.ev(`(() => { const li = document.querySelector('#live-sessions-list [data-session-id="${O}"]'); return li ? { present: true, anchor: !!li.querySelector('a.overflow-menu-open-window') } : { present: false, anchor: false }; })()`);
    check('15 done row (killed) offers no open-in-window anchor', !doneRow.anchor, JSON.stringify(doneRow));
    await td.close();
}

run().then(() => {
    const failed = results.filter(r => r.verdict === 'FAIL');
    console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
    process.exit(failed.length ? 1 : 0);
}).catch(err => {
    console.error('HARNESS ERROR:', err);
    process.exit(2);
});
