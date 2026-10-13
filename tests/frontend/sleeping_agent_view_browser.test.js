// Sleeping agents (#3029): the transcript stays readable (scrollable, older
// pages autoload, nothing covers it), the composer is disabled and offers Wake
// and Restart… instead of sending, and waking re-enables it with one re-select.
// Local and hub-remote agents, the dashboard, a phone-width viewport and the
// popout.
//
// The agents' endpoints (/chat, /wake, /send, /keys) are answered by an in-page
// mock; everything else goes to the isolated test server.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;

const SLEEPING_PLACEHOLDER = 'Agent is sleeping — wake it to send commands';

(async () => {
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
            add(name, prefix, n, { sleeping = true, server = 'local', agent_type = 'claude', session_id = null } = {}) {
                const msgs = [];
                for (let i = 0; i < n; i++) msgs.push(make(prefix, i));
                const a = { name, session_id: session_id || name + '-id', prefix, msgs, sleeping, server, agent_type };
                this.agents[a.session_id] = a;
                if (window.__qaState) window.__qaState.liveSessions = (window.__qaState.liveSessions || []).filter(s => s.session_id !== a.session_id).concat([this.entry(a)]);
                return a;
            },
            entry(a) {
                return { session_id: a.session_id, name: a.name, display_name: a.name, agent_type: a.agent_type,
                    working_directory: '/tmp/' + a.name, server: a.server, sleeping: a.sleeping, status: a.sleeping ? 'sleeping' : 'idle' };
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
                            sleeping: ra.sleeping, agent_type: ra.agent_type, name: ra.name, tmux_session: ra.sleeping || ra.ended ? null : 'claude-' + ra.session_id,
                            display_name: ra.name, auto_name: '', board_job_title: '', board_project: null, icon: null,
                            working_directory: '/tmp/' + ra.name, status: ra.sleeping ? 'Sleeping' : 'Idle', waiting_for_input: false, stuck: false, not_started: false };
                    return Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } }));
                }
            }
            // The live list (reloaded on select and on a timer) comes from the mocked agents.
            if (/^\\/api\\/sessions\\/live(\\?.*)?$/.test(u) && (opts.method || 'GET').toUpperCase() === 'GET') {
                return Promise.resolve(new Response(JSON.stringify(Object.values(mock.agents).map(a => mock.entry(a))), { status: 200, headers: { 'content-type': 'application/json' } }));
            }
            const m = u.match(/^(?:\\/api\\/remote\\/([^/]+))?\\/api\\/sessions\\/live\\/([^/?]+)\\/(chat|wake|sleep|send|keys)(?:\\?(.*))?$/);
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
                        return resolve(json({ ok: true, sleeping: false }));
                    }
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
        window.T = { state, liveChat, ws };
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

    // The sleeping-agent checks shared by the local and hub-remote agents (A1–A4, B).
    async function sleepingAgentBasics(label, name, prefix, server) {
        await ev(`window.__m.add('${name}', '${prefix}', 600, { sleeping: true, server: '${server}' })`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'terminal')`); // sleeping overrides a persisted Terminal
        await clearReqs();
        await select(name, server);
        const ids = await firstPage();
        const pageSize = ids.length;
        expectRange(ids, prefix, 600 - pageSize, 600, `${label}: history`);
        let v = await view();
        assert.ok(v.chatVisible, `${label}: the chat pane is shown for a sleeping agent`);
        assert.equal(v.overlayInDom, false, `${label}: no sleeping overlay`);
        assert.ok(v.chatUncovered, `${label}: nothing covers the history (hit ${v.hitDesc})`);
        assert.equal(await ev(`localStorage.getItem('coral-live-view-mode')`), 'terminal', `${label}: the persisted view mode is left alone`);
        let r = await reqs();
        if (server !== 'local') assert.ok(r.filter(x => x.kind === 'chat').every(x => x.remote === server), `${label}: chat goes through /api/remote/${server}`);
        console.log(`   ✓ ${label}: history shown (${pageSize} msgs), uncovered, mode kept`);

        await clearReqs();
        await scrollToTop();
        await waitFor(`window.__m.requests.some(r => r.older && r.settled)`, `${label}: older page`, 5000, SCROLL_DIAG);
        await settled();
        await sleep(100);
        r = await reqs();
        assert.equal(r.filter(x => x.older).length, 1, `${label}: one older page per scroll-to-top`);
        const all = await ev('window.__rendered()');
        expectRange(all, prefix, 600 - all.length, 600, `${label}: after an older page`);
        assert.ok(all.length > pageSize, `${label}: older messages were prepended`);
        console.log(`   ✓ ${label}: scroll-up autoload (${all.length} msgs)`);

        v = await view();
        assert.ok(v.composer && v.wake && v.restart, `${label}: sleeping composer with Wake and Restart (${JSON.stringify(v)})`);
        assert.ok(v.paneSleeping, `${label}: #command-pane.agent-sleeping`);
        assert.equal(v.toolbarVisible, false, `${label}: toolbar (quick actions, send) hidden`);
        assert.equal(v.sendBtnsVisible, false, `${label}: send buttons and attachments hidden`);
        assert.equal(v.inputDisabled, true, `${label}: input disabled`);
        assert.equal(v.placeholder, SLEEPING_PLACEHOLDER, `${label}: sleeping placeholder`);
        console.log(`   ✓ ${label}: composer shows Wake + Restart, send controls hidden, input disabled`);

        // Nothing reaches a sleeping agent, whichever send path is used.
        await clearReqs();
        await ev(`(async () => {
            const input = document.getElementById('command-input');
            input.value = 'should not be sent';
            input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
            await window.sendCommand?.();
            window.sendQuickCommand?.('/compact');
            await window.sendRawKeys?.(['Enter'], { silent: true });
        })()`);
        await sleep(200);
        r = await reqs();
        const sent = r.filter(x => x.kind === 'send' || x.kind === 'keys');
        assert.equal(sent.length, 0, `${label}: nothing is sent to a sleeping agent (got ${JSON.stringify(sent.map(x => x.kind + ' ' + x.body))})`);
        await ev(`document.getElementById('command-input').value = 'my draft'`);
        console.log(`   ✓ ${label}: Enter / sendCommand / quick command / keys send nothing`);
        return pageSize;
    }

    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();

        console.log('A. Local dashboard');
        await sleepingAgentBasics('local', 'qa-sleep-a', 'A', 'local');

        console.log('A4. Wake: one POST, one re-select, input back with the awake placeholder and the draft');
        await ev(`window.__m.wakeDelay = 300`);
        // Chat stays the view after waking, so the one re-select shows as one first-page /chat.
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat')`);
        await clearReqs();
        await ev(`(() => { const b = document.getElementById('sleeping-wake-btn'); b.click(); b.click(); })()`);
        await waitFor(`document.getElementById('sleeping-wake-btn').textContent.includes('Waking')`, 'Waking… label', 1000);
        assert.equal(await ev(`document.getElementById('sleeping-wake-btn').disabled`), true, 'Wake is disabled while waking');
        await ev(`document.getElementById('sleeping-wake-btn').click()`);
        await waitFor(`!document.getElementById('command-input').disabled`, 'input re-enabled after wake', 5000);
        await sleep(400);
        await settled();
        let r = await reqs();
        assert.equal(r.filter(x => x.kind === 'wake').length, 1, 'one POST /wake for three clicks');
        assert.equal(await ev('window.__selCalls'), 1, 'exactly one re-select after wake');
        assert.equal(r.filter(x => x.first).length, 1, `exactly one first-page /chat after wake (got ${r.filter(x => x.first).length})`);
        let v = await view();
        assert.equal(v.composer, false, 'sleeping composer hidden after wake');
        assert.equal(v.paneSleeping, false, 'agent-sleeping class removed');
        assert.notEqual(v.placeholder, SLEEPING_PLACEHOLDER, 'awake placeholder restored');
        assert.ok(v.placeholder.length > 0, 'an awake placeholder is shown');
        assert.equal(v.inputValue, 'my draft', 'the draft survives waking');
        assert.equal(v.wakeText, 'Wake', 'Wake button label restored');
        const afterWake = await ev('window.__rendered()');
        assert.equal(new Set(afterWake).size, afterWake.length, 'history not duplicated by the wake');
        console.log('   ✓ 1 POST /wake, 1 first-page /chat, input + draft back');
        await ev(`window.__m.wakeDelay = 0`);

        console.log('A5. Wake failure keeps the agent asleep');
        for (const mode of ['notok', 'network']) {
            await ev(`window.__m.add('qa-sleep-f-${mode}', 'F', 50, { sleeping: true })`);
            await select(`qa-sleep-f-${mode}`);
            await firstPage();
            await ev(`window.__m.wakeResult = '${mode}'`);
            await clearReqs();
            await ev(`document.getElementById('sleeping-wake-btn').click()`);
            await waitFor(`window.__m.requests.some(r => r.kind === 'wake' && r.settled)`, 'wake attempt');
            await sleep(200);
            v = await view();
            assert.ok(v.composer && v.inputDisabled, `${mode}: still sleeping after a failed wake`);
            assert.equal(v.wakeText, 'Wake', `${mode}: Wake button restored`);
            assert.equal(v.wakeDisabled, false, `${mode}: Wake can be tried again`);
            assert.equal((await reqs()).filter(x => x.first).length, 0, `${mode}: no re-select after a failed wake`);
            console.log(`   ✓ wake ${mode}: stays asleep, button usable`);
        }
        await ev(`window.__m.wakeResult = 'ok'`);

        console.log('A6. Restart… opens the restart dialog');
        await ev(`window.__m.add('qa-sleep-r', 'R', 50, { sleeping: true })`);
        await select('qa-sleep-r');
        await firstPage();
        await ev(`document.getElementById('sleeping-restart-btn').click()`);
        await waitFor(`getComputedStyle(document.getElementById('restart-modal')).display !== 'none'`, 'restart modal', 2000);
        assert.match(await ev(`document.getElementById('restart-modal-name').textContent`), /qa-sleep-r/);
        await ev(`window.hideRestartModal ? window.hideRestartModal() : (document.getElementById('restart-modal').style.display = 'none')`);
        console.log('   ✓ restart modal opens for the sleeping agent');

        console.log('A7. Switching between sleeping and awake agents');
        await ev(`window.__m.add('qa-awake-b', 'B', 50, { sleeping: false })`);
        await ev(`window.__m.add('qa-sleep-c', 'C', 50, { sleeping: true })`);
        await select('qa-sleep-c');
        await firstPage();
        await select('qa-awake-b');
        v = await view();
        assert.equal(v.inputDisabled, false, 'awake agent: input enabled');
        assert.equal(v.composer, false, 'awake agent: no sleeping composer');
        assert.notEqual(v.placeholder, SLEEPING_PLACEHOLDER, 'awake agent: awake placeholder');
        await select('qa-sleep-c');
        v = await view();
        assert.ok(v.inputDisabled && v.composer, 'back to the sleeping agent: disabled + composer');
        // An input that was already disabled is not the sleeping view's to enable again.
        await select('qa-awake-b');
        await ev(`document.getElementById('command-input').disabled = true`);
        await select('qa-sleep-c');
        await select('qa-awake-b');
        assert.equal(await ev(`document.getElementById('command-input').disabled`), true, 'leaving a sleeping agent does not enable an input that was disabled before');
        await ev(`document.getElementById('command-input').disabled = false`);
        console.log('   ✓ composer and input follow the selected agent; an earlier disable is left alone');

        console.log('A8. Sleep and wake from elsewhere while viewing');
        await select('qa-awake-b');
        await firstPage();
        await clearReqs();
        await ev(`window.__feed('qa-awake-b-id', { sleeping: true })`);
        await waitFor(`window.__view().composer && window.__view().inputDisabled`, 'composer after a sleep from elsewhere', 3000);
        await ev(`window.__feed('qa-awake-b-id', { sleeping: false })`);
        await waitFor(`!window.__view().composer && !window.__view().inputDisabled`, 'awake after a wake from elsewhere', 3000);
        await sleep(300);
        // Wake clicked and the feed's flip arriving together: still one re-select.
        await ev(`window.__feed('qa-awake-b-id', { sleeping: true })`);
        await waitFor(`window.__view().composer`, 'sleeping again', 3000);
        await sleep(200);
        await settled();
        await clearReqs();
        await ev(`window.__m.wakeDelay = 150`);
        await ev(`(() => {
            document.getElementById('sleeping-wake-btn').click();
            setTimeout(() => window.__feed('qa-awake-b-id', { sleeping: false }), 50);
        })()`);
        await waitFor(`!window.__view().inputDisabled`, 'awake after wake + feed', 3000);
        await sleep(500);
        await settled();
        r = await reqs();
        assert.equal(await ev('window.__selCalls'), 1, `wake + feed flip together: one re-select (got ${await ev('window.__selCalls')})`);
        assert.equal(r.filter(x => x.first).length, 1, `wake + feed flip together: one first-page /chat (got ${r.filter(x => x.first).length})`);
        const ids = await ev('window.__rendered()');
        assert.equal(new Set(ids).size, ids.length, 'history not duplicated');
        await ev(`window.__m.wakeDelay = 0`);
        console.log('   ✓ view follows the feed; wake + feed = one re-select');

        console.log('A9. An agent opens at its newest message even after reading back in the previous one');
        for (let i = 0; i < 3; i++) {
            await ev(`window.__m.add('qa-read-x${i}', 'X', 600, { sleeping: true })`);
            await ev(`window.__m.add('qa-read-y${i}', 'Y', 600, { sleeping: true })`);
            await select(`qa-read-x${i}`);
            await firstPage();
            // The reader scrolls up a little (not to the sentinel): autoScroll goes false.
            await ev(`(() => { const c = document.getElementById('live-history-messages'); c.scrollTop = Math.max(c.scrollHeight - c.clientHeight - 1500, 400); c.dispatchEvent(new Event('scroll')); })()`);
            await waitFor(`T.state.autoScroll === false`, 'autoScroll off after reading back');
            await clearReqs();
            await select(`qa-read-y${i}`);
            await firstPage();
            await sleep(150);
            const at = await ev(`(() => { const c = document.getElementById('live-history-messages'); return { bottomGap: Math.round(c.scrollHeight - c.scrollTop - c.clientHeight), autoScroll: T.state.autoScroll }; })()`);
            const older = (await reqs()).filter(x => x.older);
            assert.ok(at.bottomGap < 60, `switch ${i}: the next agent opens at the bottom (gap ${at.bottomGap}px, autoScroll ${at.autoScroll})`);
            assert.equal(older.length, 0, `switch ${i}: no older page is fetched on opening (got ${older.length})`);
        }
        console.log('   ✓ next agent opens at the bottom, no older fetch (3 switches)');

        console.log('B. Hub-remote sleeping agent');
        await sleepingAgentBasics('remote', 'qa-sleep-remote', 'H', 'qa-remote-1');
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat')`);
        await clearReqs();
        await ev(`document.getElementById('sleeping-wake-btn').click()`);
        await waitFor(`!document.getElementById('command-input').disabled`, 'remote wake', 5000);
        await sleep(300);
        r = await reqs();
        const wakes = r.filter(x => x.kind === 'wake');
        assert.equal(wakes.length, 1, 'one remote POST /wake');
        assert.equal(wakes[0].remote, 'qa-remote-1', 'wake goes through /api/remote/<id>');
        assert.equal(await ev('window.__selCalls'), 1, 'one re-select after the remote wake');
        assert.equal(r.filter(x => x.first).length, 1, 'one first-page /chat after the remote wake');
        console.log('   ✓ remote wake via /api/remote/qa-remote-1, one re-select');

        console.log('C. Phone-width viewport');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`window.__m.add('qa-sleep-m', 'M', 300, { sleeping: true })`);
        await select('qa-sleep-m');
        await firstPage();
        await sleep(300);
        v = await view();
        assert.ok(v.chatVisible, 'phone: history visible');
        assert.ok(v.chatUncovered, `phone: nothing covers the history (hit ${v.hitDesc})`);
        assert.ok(v.wakeTappable, `phone: Wake on screen and not covered (${JSON.stringify(v)})`);
        assert.ok(v.restartTappable, 'phone: Restart on screen and not covered');
        assert.ok(v.inputDisabled, 'phone: input disabled');
        assert.equal(v.sendBtnsVisible, false, 'phone: +T / send hidden');
        assert.equal(await ev(`document.documentElement.scrollWidth <= innerWidth + 1`), true, 'phone: no horizontal page scroll');
        await clearReqs();
        await scrollToTop();
        await waitFor(`window.__m.requests.some(r => r.older && r.settled)`, 'phone: older page');
        console.log('   ✓ phone: history + autoload, Wake/Restart tappable, send hidden');
        await Emulation.clearDeviceMetricsOverride();

        console.log('D. Popout of a sleeping agent');
        const PID = '44444444-4444-4444-8444-555555555571';
        await Page.addScriptToEvaluateOnNewDocument({ source: `window.__qaSeed = [{ name: 'qa-pop', prefix: 'P', n: 300, sleeping: true, session_id: '${PID}' }];` });
        await Page.navigate({ url: `${BASE}/agent/${PID}` });
        await Page.loadEventFired();
        await waitFor(`window._coralPopout && window._coralPopout.getState && window._coralPopout.getState() !== 'loading'`, 'popout to resolve', 8000);
        await install();
        await firstPage();
        v = await view();
        assert.ok(v.chatVisible && v.chatUncovered, `popout: history shown and uncovered (hit ${v.hitDesc})`);
        assert.ok(v.composer && v.wake, 'popout: sleeping composer with Wake');
        assert.equal(v.restart, false, 'popout: no Restart (no destructive controls in the popout)');
        assert.equal(v.inputDisabled, true, 'popout: input disabled');
        assert.equal(v.placeholder, SLEEPING_PLACEHOLDER, 'popout: sleeping placeholder');
        assert.equal(await ev(`window.__wsAttempts.filter(u => /\\/ws\\/terminal/.test(u)).length`), 0, 'popout: no terminal attach while sleeping');
        await clearReqs();
        await ev(`document.getElementById('sleeping-wake-btn').click()`);
        await waitFor(`!window.__view().composer`, 'popout: composer hidden after wake', 8000);
        await waitFor(`!document.getElementById('command-input').disabled`, 'popout: input enabled after wake', 8000);
        r = await reqs();
        assert.equal(r.filter(x => x.kind === 'wake').length, 1, 'popout: one POST /wake');
        assert.ok(await ev(`window.__wsAttempts.some(u => /\\/ws\\/terminal/.test(u))`), 'popout: the terminal attaches after waking');
        console.log('   ✓ popout: history, Wake only, wake re-enables input and attaches');
        // Ended while asleep: the composer goes with it.
        await ev(`window.__feed('${PID}', { sleeping: true })`);
        await waitFor(`window.__view().composer`, 'popout: asleep again', 3000);
        await ev(`(() => { window.__m.agents['${PID}'].ended = true; window._coralHandleWsMessage({ type: 'coral_diff', changed: [], removed: ['${PID}'] }); })()`);
        await waitFor(`window._coralPopout.getState() === 'ended'`, 'popout: ended', 5000);
        v = await view();
        assert.equal(v.composer, false, 'popout: ended hides the sleeping composer');
        assert.equal(v.inputDisabled, true, 'popout: ended keeps the input disabled');
        console.log('   ✓ popout: ended hides the composer');

        console.log('\nAll sleeping agent view checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
