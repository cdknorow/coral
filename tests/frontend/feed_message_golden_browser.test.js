// The live feed's messages (#4702): handleCoralMessage (websocket.js) is a 208-line function that takes what the feed sends (a full update, or a diff of changed, removed and removed-remote rows)
// and merges it into the list of agents, seeds and detects who just started waiting (a toast, not for the agent being typed into, not when switched off), pushes the notifications and the running
// jobs, redraws the sidebar (or skips that for a drift that only ages the agents) and updates the header, status, branch, model and the sleeping view of the agent being viewed. Splitting it must
// change nothing. This sends it messages in every situation and records the list, the servers, the header, the toasts, what was reloaded or selected and whether the sidebar was scheduled.
// Compared with fixtures/feed_message_golden.json (sha256 per step, with the readable lines). CORAL_UPDATE_GOLDEN=1 rewrites it. Drives Chrome over CDP through agent_view_harness.js against the
// isolated dev server; nothing is sent over a socket, the messages are handed to the function.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'feed_message_golden.json');
const sha = t => require('node:crypto').createHash('sha256').update(t).digest('hex');

const S = (name, o = {}) => ({ name, session_id: name + '-id', agent_type: 'claude', status: 'Working', summary: 'sum ' + name, display_name: null, working_directory: '/work/' + name, branch: 'main', repo_name: 'repo', model: 'opus',
    waiting_for_input: false, sleeping: false, staleness_seconds: 5, commands: ['/x'], changed_file_count: 2, ...o });

const KNOWN = `T.state.liveSessions = ${JSON.stringify([S('a1'), S('a2'), S('n1'), S('r1', { server: 'm1' })])};`; // the list a step starts from, where an earlier step emptied it
const STEPS = [
    ['full update of two agents', { type: 'coral_update', sessions: [S('a1'), S('a2', { status: 'Idle' })] }],
    ['a full update merges the fields it omits', { type: 'coral_update', sessions: [{ name: 'a1', session_id: 'a1-id', status: 'Done' }, S('a2', { status: 'Idle' })] }],
    ['servers are kept only on a hub', { type: 'coral_update', sessions: [S('a1'), S('a2')], servers: [{ id: 'm1', label: 'box', status: 'online' }] }],
    ['servers on a hub', { type: 'coral_update', sessions: [S('a1'), S('a2'), S('r1', { server: 'm1' })], servers: [{ id: 'm1', label: 'box', status: 'online' }] }, 'T.state.hub = true;'],
    ['an agent starts waiting', { type: 'coral_update', sessions: [S('a1', { waiting_for_input: true, waiting_summary: 'Run <rm>?' }), S('a2'), S('r1', { server: 'm1' })] }],
    ['still waiting is not a new toast', { type: 'coral_update', sessions: [S('a1', { waiting_for_input: true }), S('a2'), S('r1', { server: 'm1' })] }],
    ['waiting with the notification switched off', { type: 'coral_update', sessions: [S('a1'), S('a2', { waiting_for_input: true }), S('r1', { server: 'm1' })] }, "T.state.settings.notify_needs_input = false;"],
    ['waiting on a remote agent', { type: 'coral_update', sessions: [S('a1'), S('a2'), S('r1', { server: 'm1', waiting_for_input: true, display_name: 'Remote one' })] }, "T.state.settings.notify_needs_input = true;"],
    ['a diff changes one agent and adds one', { type: 'coral_diff', changed: [{ name: 'a1', session_id: 'a1-id', status: 'Idle' }, S('n1')] }],
    ['a field-level diff of a known row', { type: 'coral_diff', changed: [{ name: 'a2', session_id: 'a2-id', summary: 'only this', _partial: true }] }],
    ['a field-level diff of an unknown row reloads the list', { type: 'coral_diff', changed: [{ name: 'ghost', session_id: 'ghost-id', summary: 'half', _partial: true }] }],
    ['a diff removes a local agent', { type: 'coral_diff', removed: ['n1-id'] }, KNOWN],
    ['a diff removes a remote agent', { type: 'coral_diff', removed_remote: [{ server: 'm1', key: 'r1-id' }] }, KNOWN],
    ['a diff that only ages the agents', { type: 'coral_diff', changed: [{ name: 'a1', session_id: 'a1-id', staleness_seconds: 9 }, { name: 'a2', session_id: 'a2-id', staleness_seconds: 9 }] }, KNOWN],
    ['a diff with servers is not drift', { type: 'coral_diff', changed: [{ name: 'a1', session_id: 'a1-id', staleness_seconds: 12 }], servers: [{ id: 'm1', label: 'box', status: 'offline' }] }, KNOWN],
    ['notifications and running jobs', { type: 'coral_update', sessions: [S('a1'), S('a2')], notifications: [{ type: 'alert', title: 'Heads up', message: 'Disk <full>', level: 'warning', link: { url: '/docs' } }, { type: 'workflow', title: 'Done', message: 'Flow finished', level: 'info' }], active_runs: [] }],
    ['viewing a live agent: its header follows the feed', { type: 'coral_update', sessions: [S('a1', { display_name: 'Ada', summary: 'new goal', branch: 'feat', status: 'Needs input', changed_file_count: 7 }), S('a2')] },
        "T.state.currentSession = { name: 'a1', type: 'live', session_id: 'a1-id', agent_type: 'claude' };"],
    ['viewing an agent that was renamed', { type: 'coral_update', sessions: [S('a1-renamed', { session_id: 'a1-id', tmux_session: 'tmx-1' }), S('a2')] }],
    ['viewing an agent matched by name only adopts the new id', { type: 'coral_update', sessions: [S('b1', { session_id: 'b1-new' }), S('a2')] }, "T.state.currentSession = { name: 'b1', type: 'live', session_id: 'b1-old', agent_type: 'claude' };"],
    ['the viewed agent falls asleep', { type: 'coral_update', sessions: [S('b1', { session_id: 'b1-new', sleeping: true }), S('a2')] }],
    ['the viewed agent stays asleep and is archived', { type: 'coral_update', sessions: [S('b1', { session_id: 'b1-new', sleeping: true, archived: true }), S('a2')] }, "T.state.currentSession.sleeping = true;"],
    ['the viewed agent is gone from the feed', { type: 'coral_update', sessions: [S('a2')] }],
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const out = {};
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const T = window.T = { state: (await import(st + 'state.js')).state, ws: await import(st + 'websocket.js') };
            window.__calls = [];
            window.selectLiveSession = (...a) => { window.__calls.push('select ' + JSON.stringify(a)); return Promise.resolve(); };
            const realRaf = window.requestAnimationFrame; window.__raf = 0; window.requestAnimationFrame = cb => { window.__raf++; return realRaf(cb); };
            const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            const real = window.fetch;
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href); if (p.pathname === '/api/sessions/live') { window.__calls.push('reload ' + p.pathname); return json([]); } return real(u, o); };
            T.state.settings = T.state.settings || {}; T.state.settings.notify_needs_input = true;
            T.ws._resetWaitingSeedForTests(); T.state.liveSessions = []; T.state.servers = []; T.state.hub = false; T.state.currentSession = null;
            return true; })()`);
        const SNAP = `(() => { const q = id => document.getElementById(id), T = window.T, cs = T.state.currentSession;
            return { sessions: (T.state.liveSessions || []).map(s => [s.name, s.session_id, s.server || 'local', s.status, !!s.waiting_for_input, s.summary, s.display_name, !!s.sleeping, s.staleness_seconds, s.branch, s.model, JSON.stringify(s.commands || null), s.archived || false].join('/')),
                servers: JSON.stringify(T.state.servers), serversKnown: T.state.serversKnown,
                header: [(q('session-name') || {}).textContent, (q('terminal-header-label') || {}).textContent, (q('command-input') || {}).placeholder].join(' | '),
                current: cs ? [cs.name, cs.session_id, cs.tmux_session, cs.display_name, cs.summary].join('/') : null,
                toasts: [...document.querySelectorAll('[class*="toast"]')].map(e => e.textContent.replace(/\\s+/g, ' ').trim()).filter(Boolean).join(' || '),
                calls: window.__calls.join(' ; '), raf: window.__raf, prevWaiting: JSON.stringify(Object.fromEntries(Object.entries(T.state.prevWaitingState || {}).sort())) }; })()`;
        for (const [name, msg, setup] of STEPS) {
            await ev(`window.__calls.length = 0; window.__raf = 0; document.querySelectorAll('[class*="toast"]').forEach(e => e.remove()); ${setup || ''} T.ws.handleCoralMessage(${JSON.stringify(msg)}); true`);
            await ev(`new Promise(r => setTimeout(r, 80))`); // sleep-ok: a resync and the toasts land over a few frames; there is no completion event
            out[name] = await ev(SNAP);
        }
        assert.match(out['an agent starts waiting'].toasts, /a1/);
        assert.equal(out['still waiting is not a new toast'].toasts, '');
        assert.match(out['a field-level diff of an unknown row reloads the list'].calls, /reload/);
        const hashes = {}, lines = {};
        for (const [name, s] of Object.entries(out)) { hashes[name] = sha(JSON.stringify(s)); lines[name] = `${s.sessions.join(' ; ')} | servers ${s.servers} | header ${s.header} | current ${s.current} | toasts ${s.toasts} | calls ${s.calls} | raf>0 ${s.raf > 0}`; }
        if (process.env.CORAL_UPDATE_GOLDEN) {
            fs.mkdirSync(path.dirname(GOLDEN), { recursive: true });
            fs.writeFileSync(GOLDEN, JSON.stringify({ about: 'what handleCoralMessage did with each feed message, for feed_message_golden_browser.test.js: sha256 of the list, servers, header, toasts, calls and render count, and the readable lines. CORAL_UPDATE_GOLDEN=1 rewrites it.', hashes, lines }, null, 2) + '\n');
            console.log('golden written: ' + Object.keys(hashes).length + ' steps');
            return;
        }
        const want = JSON.parse(fs.readFileSync(GOLDEN, 'utf8'));
        assert.deepEqual(Object.keys(hashes), Object.keys(want.hashes), 'the same steps');
        for (const k of Object.keys(want.hashes)) if (hashes[k] !== want.hashes[k]) assert.fail(`${k}: changed.\n  now:  ${lines[k]}\n  was:  ${want.lines[k]}`);
        console.log(`PASS feed message golden: ${Object.keys(want.hashes).length} steps identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
