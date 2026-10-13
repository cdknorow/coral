// Shared CDP harness for the agent-view suites (sleeping #3029, archived #3030): an in-page mock of
// the live list, the per-agent endpoints (chat, wake, sleep, archive, unarchive, send, keys), the
// popout resolver and the bulk wakes, installed before any app script runs; plus helpers to read
// what the reader sees. Everything else goes to the isolated test server.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

async function connect() {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const waitFor = async (expression, what, timeoutMs = 5000, diag = null) => {
        const until = Date.now() + timeoutMs;
        for (;;) {
            if (await ev(expression)) return;
            if (Date.now() > until) throw new Error(`timed out waiting for ${what}` + (diag ? ' ' + JSON.stringify(await ev(diag)) : ''));
            await new Promise(r => setTimeout(r, 20));
        }
    };
    const sleep = ms => new Promise(r => setTimeout(r, ms));

    // Installed before any app script runs, so the page's own first live-list load already sees
    // the mocked agents (a mock installed after load races it and loses).
    const PRELOAD = `(() => {
        const filler = 'lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua';
        const make = (prefix, i) => i % 2 === 0
            ? { type: 'user', content: prefix + i + ' ' + filler }
            : { type: 'assistant', text: prefix + i + ' ' + filler };

        const mock = window.__m = {
            agents: {},          // session_id -> agent
            requests: [],        // every mocked request, in order
            wakeDelay: 0,
            wakeResult: 'ok',    // 'ok' | 'notok' | 'network'
            add(name, prefix, n, { sleeping = true, archived = false, server = 'local', agent_type = 'claude', session_id = null, board_project = null } = {}) {
                const msgs = [];
                for (let i = 0; i < n; i++) msgs.push(make(prefix, i));
                const a = { name, session_id: session_id || name + '-id', prefix, msgs, sleeping: sleeping || archived, archived, server, agent_type, board_project };
                this.agents[a.session_id] = a;
                if (window.__qaState) window.__qaState.liveSessions = (window.__qaState.liveSessions || []).filter(s => s.session_id !== a.session_id).concat([this.entry(a)]);
                return a;
            },
            entry(a) {
                return { session_id: a.session_id, name: a.name, display_name: a.name, agent_type: a.agent_type,
                    working_directory: '/tmp/' + a.name, server: a.server, sleeping: a.sleeping, archived: !!a.archived, board_project: a.board_project || null,
                    status: a.sleeping ? 'sleeping' : 'idle' };
            },
            byPathSeg(seg) {
                seg = decodeURIComponent(seg);
                return this.agents[seg] || Object.values(this.agents).find(a => a.name === seg);
            },
        };

        // Agents a popout page needs before it boots (registered by a later preload script).
        mock.seed = () => { for (const a of (window.__qaSeed || []).splice(0)) mock.add(a.name, a.prefix, a.n, a); };
        if (location.pathname.startsWith('/agent/')) {
            window.__coralPopoutDelayScale = 0.1;
            window.__wsAttempts = [];
            const Real = window.WebSocket;
            function Dead(u) { this.url = String(u); this.readyState = 0; }
            Dead.prototype.send = function () {}; Dead.prototype.close = function () {};
            Dead.prototype.addEventListener = function () {}; Dead.prototype.removeEventListener = function () {};
            function W(u, p) { const s = String(u); if (/\\/ws\\/(coral|terminal)/.test(s)) { window.__wsAttempts.push(s); return new Dead(s); } return p === undefined ? new Real(u) : new Real(u, p); }
            W.prototype = Real.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3;
            window.WebSocket = W;
        }
        const origFetch = window.fetch.bind(window);
        window.fetch = (url, opts = {}) => {
            mock.seed();
            const u = String(url);
            {
                const rm = u.match(/^\\/api\\/sessions\\/([^/?]+)\\/(resolve|status)(\\?|$)/);
                const ra = rm && mock.agents[decodeURIComponent(rm[1])];
                if (ra) {
                    mock.requests.push({ kind: rm[2], agent: ra.name, method: 'GET', settled: true });
                    const body = rm[2] === 'status'
                        ? { session_id: ra.session_id, active: !ra.sleeping, sleeping: ra.sleeping }
                        : { session_id: ra.session_id, state: ra.ended ? 'finished' : (ra.sleeping ? 'sleeping' : 'active'), active: !ra.sleeping && !ra.ended,
                            sleeping: ra.sleeping, archived: !!ra.archived, agent_type: ra.agent_type, name: ra.name, tmux_session: ra.sleeping || ra.ended ? null : 'claude-' + ra.session_id,
                            display_name: ra.name, auto_name: '', board_job_title: '', board_project: ra.board_project || null, icon: null,
                            working_directory: '/tmp/' + ra.name, status: ra.sleeping ? 'Sleeping' : 'Idle', waiting_for_input: false, stuck: false, not_started: false };
                    return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } }));
                }
            }
            // The live list (reloaded on select and on a timer) comes from the mocked agents.
            if (/^\\/api\\/sessions\\/live(\\?.*)?$/.test(u) && (opts.method || 'GET').toUpperCase() === 'GET') {
                window.__liveListServed = (window.__liveListServed || 0) + 1;
                return Promise.resolve(new Response(JSON.stringify(Object.values(mock.agents).map(a => mock.entry(a))), { status: 200, headers: { 'content-type': 'application/json' } }));
            }
            // Bulk wakes skip archived agents, as the server does (#3030).
            {
                const bm = u.match(/^\\/api\\/sessions\\/live\\/(?:wake-all|team\\/([^/]+)\\/(wake|sleep))$/);
                if (bm && (opts.method || 'GET').toUpperCase() === 'POST') {
                    const wake = !bm[2] || bm[2] === 'wake';
                    const hit = Object.values(mock.agents).filter(a => !a.archived && (!bm[1] || a.board_project === decodeURIComponent(bm[1])));
                    for (const a of hit) a.sleeping = !wake;
                    mock.requests.push({ kind: bm[1] ? 'team-' + bm[2] : 'wake-all', method: 'POST', settled: true });
                    const body = { ok: true, sleeping: !wake, sessions_relaunched: wake ? hit.length : 0, sessions_affected: hit.length };
                    return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } }));
                }
            }
            const m = u.match(/^(?:\\/api\\/remote\\/([^/]+))?\\/api\\/sessions\\/live\\/([^/?]+)\\/(chat|wake|sleep|send|keys|archive|unarchive)(?:\\?(.*))?$/);
            const a = m && mock.byPathSeg(m[2]);
            if (!a) return origFetch(url, opts);
            const kind = m[3];
            const p = new URLSearchParams(m[4] || '');
            const after = Number(p.get('after') || 0);
            const req = { kind, agent: a.name, remote: m[1] || null, method: (opts.method || 'GET').toUpperCase(),
                after, older: kind === 'chat' && after === 0 && (p.has('before') || Number(p.get('offset') || 0) > 0),
                first: kind === 'chat' && after === 0 && !p.has('before') && Number(p.get('offset') || 0) === 0,
                body: typeof opts.body === 'string' ? opts.body : null, settled: false };
            mock.requests.push(req);
            if (kind === 'chat' && after === 0) {
                const c = document.getElementById('live-history-messages');
                const sn = c && c.querySelector('.chat-history-sentinel');
                (mock.log = mock.log || []).push({ t: Math.round(performance.now()), agent: a.name, older: req.older, before: p.get('before'),
                    scrollTop: c ? Math.round(c.scrollTop) : null, sh: c ? c.scrollHeight : null, sTop: sn && c ? Math.round(sn.getBoundingClientRect().top - c.getBoundingClientRect().top) : null });
            }
            const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });
            return new Promise((resolve, reject) => {
                const respond = () => {
                    req.settled = true;
                    if (kind === 'chat') {
                        const all = a.msgs;
                        if (after > 0) return resolve(json({ messages: all.slice(Math.min(after, all.length)), total: all.length }));
                        const limit = Number(p.get('limit')) > 0 ? Number(p.get('limit')) : 100;
                        const end = p.has('before') ? Math.min(Number(p.get('before')), all.length) : Math.max(all.length - Number(p.get('offset') || 0), 0);
                        const start = Math.max(end - limit, 0);
                        return resolve(json({ messages: all.slice(start, end), total: all.length, has_more: start > 0, start }));
                    }
                    if (kind === 'wake') {
                        if (mock.wakeResult === 'network') return reject(new TypeError('Failed to fetch'));
                        if (mock.wakeResult === 'notok') return resolve(json({ ok: false, error: 'wake failed (mock)' }));
                        a.sleeping = false;
                        a.archived = false; // waking an archived agent unarchives it
                        return resolve(json({ ok: true, sleeping: false }));
                    }
                    if (kind === 'archive') { a.archived = true; a.sleeping = true; return resolve(json({ ok: true, archived: true, sleeping: true })); }
                    if (kind === 'unarchive') { a.archived = false; return resolve(json({ ok: true, archived: false, sleeping: true })); }
                    // send / keys / sleep: record only
                    return resolve(json({ ok: true }));
                };
                if (kind === 'wake' && mock.wakeDelay) setTimeout(respond, mock.wakeDelay); else respond();
            });
        };

    })();`;

    // Installs the mock and helpers into the current page.
    const install = () => ev(`(async () => {
        const { state } = await import('/static/state.js');
        const liveChat = await import('/static/live_chat.js');
        const ws = await import('/static/websocket.js');
        // Let the page's own first live-list load finish (and the deep-link restore that follows it)
        // before a test selects anything: a late restore would re-select whatever #chat/<id> the hash
        // holds by then. Popout pages restore through their resolver instead.
        if (!location.pathname.startsWith('/agent/')) {
            for (let i = 0; i < 500 && !window.__liveListServed; i++) await new Promise(r => setTimeout(r, 10));
            await new Promise(r => setTimeout(r, 100));
        }
        const render = await import('/static/render.js');
        window.T = { state, liveChat, ws, render };
        window.__qaState = state;
        // Count re-selects from every caller (Wake, the feed, the test itself).
        { const orig = window.selectLiveSession; window.__selCalls = 0; window.selectLiveSession = (...a) => { window.__selCalls++; return orig(...a); }; }
        // The real feed would replace the mocked agents with the test server's (empty) list:
        // detach it and drive sleeping/awake changes through handleCoralMessage instead.
        ws.feedHooks.schedule = () => {};
        if (state.coralWs) { state.coralWs.onmessage = null; state.coralWs.onclose = null; state.coralWs.close(); }

        window.__rendered = () => {
            const out = [];
            for (const el of document.querySelectorAll('#live-history-messages .message-text')) {
                const m = el.textContent.match(/^\\s*([A-Z])(\\d+) lorem/);
                if (m) out.push(m[1] + m[2]);
            }
            return out;
        };
        const vis = el => {
            if (!el) return false;
            const cs = getComputedStyle(el);
            if (cs.display === 'none' || cs.visibility === 'hidden' || el.hidden) return false;
            const r = el.getBoundingClientRect();
            return r.width > 0 && r.height > 0;
        };
        window.__vis = sel => vis(document.querySelector(sel));
        // What a reader sees of the composer and the center pane.
        window.__view = () => {
            const input = document.getElementById('command-input');
            const c = document.getElementById('live-history-messages');
            const r = c.getBoundingClientRect();
            const hit = r.width > 0 ? document.elementFromPoint(r.left + r.width / 2, r.top + Math.min(r.height / 2, 200)) : null;
            const toolbar = document.querySelector('#command-pane .command-pane-toolbar');
            const wake = document.getElementById('sleeping-wake-btn');
            const restart = document.getElementById('sleeping-restart-btn');
            const inViewport = el => { if (!vis(el)) return false; const b = el.getBoundingClientRect(); return b.top >= 0 && b.left >= 0 && b.bottom <= innerHeight && b.right <= innerWidth; };
            const tappable = el => { if (!inViewport(el)) return false; const b = el.getBoundingClientRect(); const top = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2); return !!top && (top === el || el.contains(top)); };
            return {
                chatVisible: vis(c) && c.clientHeight > 0,
                chatUncovered: !!hit && c.contains(hit),
                hitDesc: hit ? (hit.id || hit.className || hit.tagName) : null,
                overlayInDom: !!document.getElementById('session-sleeping-overlay'),
                composer: vis(document.getElementById('sleeping-composer')),
                wake: vis(wake), restart: vis(restart),
                wakeTappable: tappable(wake), restartTappable: tappable(restart),
                wakeText: wake ? wake.textContent.trim() : null, wakeDisabled: wake ? wake.disabled : null,
                toolbarVisible: vis(toolbar),
                sendBtnsVisible: [...document.querySelectorAll('#command-pane .mobile-send-btn, #command-pane .image-attachments')].some(vis),
                inputDisabled: input ? input.disabled : null,
                placeholder: input ? input.placeholder : null,
                inputValue: input ? input.value : null,
                paneSleeping: document.getElementById('command-pane')?.classList.contains('agent-sleeping') || false,
                paneArchived: document.getElementById('command-pane')?.classList.contains('agent-archived') || false,
                unarchive: vis(document.getElementById('sleeping-unarchive-btn')),
                unarchivePrimary: document.getElementById('sleeping-unarchive-btn')?.classList.contains('btn-primary') || false,
                wakePrimary: wake ? wake.classList.contains('btn-primary') : null,
                composerText: document.getElementById('sleeping-composer-text')?.textContent.trim() || null,
            };
        };
        window.__feed = (session_id, patch) => {
            const a = window.__m.agents[session_id];
            Object.assign(a, patch);
            T.ws.handleCoralMessage({ type: 'coral_diff', changed: [window.__m.entry(a)] });
        };
    })()`);

    const select = (name, server = 'local') => ev(`(async () => {
        await window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(name + '-id')}, ${JSON.stringify(server)});
    })()`);
    const view = () => ev('window.__view()');
    const reqs = () => ev('window.__m.requests.map(r => ({...r}))');
    const clearReqs = () => ev('window.__m.requests.length = 0; window.__selCalls = 0');
    const expectRange = (ids, prefix, from, to, what) => {
        const want = [];
        for (let i = from; i < to; i++) want.push(prefix + i);
        assert.deepEqual(ids, want, `${what}: expected ${prefix}${from}..${prefix}${to - 1} (got ${ids.length}, first ${ids[0]}, last ${ids[ids.length - 1]})`);
    };
    const firstPage = async () => {
        await waitFor(`window.__rendered().length > 0`, 'history to render');
        return ev('window.__rendered()');
    };
    const SCROLL_DIAG = `(() => {
        const c = document.getElementById('live-history-messages');
        const s = c.querySelector('.chat-history-sentinel');
        const cr = c.getBoundingClientRect(), sr = s ? s.getBoundingClientRect() : null;
        return { scrollTop: c.scrollTop, sh: c.scrollHeight, ch: c.clientHeight, sentinel: !!s, sTop: sr && sr.top - cr.top, sBottom: sr && sr.bottom - cr.top,
            hasOlder: T.liveChat.chatHasOlder(), rendered: window.__rendered().length, cur: T.state.currentSession && T.state.currentSession.name,
            log: (window.__m.log || []).filter(x => x.agent === (T.state.currentSession && T.state.currentSession.name)), now: Math.round(performance.now()),
            reqs: window.__m.requests.slice(-6).map(r => r.kind + (r.first ? ':first' : '') + (r.older ? ':older' : '') + (r.after ? ':after' : '') + ':' + r.agent + (r.settled ? '' : ':pending')) };
    })()`;
    const scrollToTop = () => ev(`(() => { const c = document.getElementById('live-history-messages'); (window.__m.log = window.__m.log || []).push({ t: Math.round(performance.now()), agent: T.state.currentSession && T.state.currentSession.name, test: 'scrollToTop', scrollTop: Math.round(c.scrollTop) }); c.scrollTop = 0; c.dispatchEvent(new Event('scroll')); })()`);
    const settled = () => waitFor(`window.__m.requests.every(r => r.settled)`, 'mocked requests to settle');


    return { client, Page, Runtime, Emulation, ev, waitFor, sleep, PRELOAD, install, select, view, reqs, clearReqs,
        expectRange, firstPage, SCROLL_DIAG, scrollToTop, settled };
}

module.exports = { connect };
