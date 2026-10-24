// The agent view's own Kill Session (#6892, R1 of #6877): the four kill paths (the sidebar's, the agent view's, a folder's Kill All, a team's Kill Team) shared the same fetch and follow-up written four times, and had
// drifted. This drives the agent view's Kill Session end to end (controls.js killSession, through the confirmation): the request names the open agent, its row goes at once, the welcome screen shows, the capture refresh
// stops, a kept worktree is said; and the two latent differences that one shared drop removes: a session id the page knows on two servers drops only the server that was killed (the agent view dropped both), and killing
// the open agent from the sidebar stops its capture refresh (the sidebar's kill left it polling a dead agent). Drives Chrome over CDP through agent_view_harness.js; the kill answer is the page's fetch.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the confirmation and the redraw settle
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const S = `(window.__coralStatic || '/static/')`;
    const row = (id, over = {}) => ({ session_id: 'sid-' + id, name: 'agent-' + id, display_name: 'Agent ' + id, agent_type: 'claude', working_directory: '/repo/' + id, status: 'idle', server: '', ...over });
    const open = async rows => {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { const real = window.fetch.bind(window); window.__posts = [];
            window.fetch = (u, o = {}) => { const url = String(u), m = (o.method || 'GET').toUpperCase();
                if (m === 'POST' && /\\/api\\/sessions\\/live\\/team\\/[^/]+\\/kill$/.test(url)) { window.__posts.push({ url, body: null }); return Promise.resolve(new Response(JSON.stringify(window.__teamAnswer || { ok: true, sessions_killed: 2 }), { status: window.__teamStatus || 200, headers: { 'content-type': 'application/json' } })); }
                if (m === 'POST' && /\\/api\\/sessions\\/live\\/[^/]+\\/kill$/.test(url)) { window.__posts.push({ url, body: JSON.parse(o.body) }); return Promise.resolve(new Response(JSON.stringify(window.__killAnswer || { ok: true }), { status: 200, headers: { 'content-type': 'application/json' } })); }
                return real(u, o); };
            window.__killAnswer = null; window.__teamAnswer = null; window.__teamStatus = 0; return true; })()`);
        await ev(`(async () => { const st = await import(${S} + 'state.js'); const r = await import(${S} + 'render.js'); st.state.settings = {}; st.state.liveSessions = ${JSON.stringify(rows)}; r.flushLiveSessionsRender(); r.renderLiveSessions(st.state.liveSessions); return true; })()`);
    };
    const select = id => ev(`(async () => { const st = await import(${S} + 'state.js'); const cs = require_row(st, ${JSON.stringify(id)}); return true; function require_row(st, sid) { const r = st.state.liveSessions.find(s => s.session_id === sid); st.state.currentSession = { type: 'live', ...r }; st.state.captureInterval = setInterval(() => {}, 100000); return r; } })()`);
    const ids = () => ev(`import(${S} + 'state.js').then(st => st.state.liveSessions.map(s => s.session_id + '@' + (s.server || 'local')).sort())`);
    const yes = async () => { await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation', 8000); const msg = await ev(`document.getElementById('confirm-modal-message').textContent`); await ev(`document.getElementById('confirm-modal-yes').click(); true`); await sleep(500); return msg; }; // sleep-ok: the confirmation and the redraw settle
    // a kept worktree is a compact bar (#6943): "Worktree kept" and its Details; the why, the path and the branch are behind Details, which this opens
    const keptBar = async path => { await ev(`(() => { const d = document.querySelector('#worktree-dock .worktree-dock-details'); if (d && d.getAttribute('aria-expanded') !== 'true') d.click(); return true; })()`); await sleep(250); /* sleep-ok: the details open */ const t = await ev(`document.getElementById('worktree-dock').innerText`); return /kept/i.test(t) && t.includes(path); };
    try {
        // 1. the agent view's Kill Session
        await open([row(1), row(2)]);
        await select('sid-1');
        await ev(`window.__killAnswer = { ok: true, worktree: { state: 'kept', path: '/tmp/wt/agent-1', branch: 'coral-team/x', reason: 'it has uncommitted changes' } }; import(${S} + 'controls.js').then(m => m.killSession()); true`);
        const msg = await yes();
        check('1. the confirmation names the agent and the logs note', /Kill session "agent-1"/.test(msg) && /terminal log is kept/.test(msg), msg.slice(0, 80));
        const posts = await ev(`window.__posts`);
        check('1. one request, to the open agent, with its type and session id', posts.length === 1 && /\/live\/agent-1\/kill$/.test(posts[0].url) && posts[0].body.session_id === 'sid-1' && posts[0].body.agent_type === 'claude', JSON.stringify(posts));
        check('1. its row is gone at once, the other stays', JSON.stringify(await ids()) === JSON.stringify(['sid-2@local']), JSON.stringify(await ids()));
        check('1. the welcome screen shows and no agent is open', (await ev(`getComputedStyle(document.getElementById('welcome-screen')).display !== 'none'`)) && (await ev(`import(${S} + 'state.js').then(st => st.state.currentSession === null)`)));
        check('1. the capture refresh stopped', await ev(`import(${S} + 'state.js').then(st => st.state.captureInterval === null)`));
        check('1. a kept worktree is said in the bar, with its path behind Details', await keptBar('/tmp/wt/agent-1'));

        // 2. a session id on two servers: only the killed server's row goes (the agent view dropped both)
        await open([row(7), row(7, { server: 'remote-1', name: 'agent-7' }), row(8)]);
        await select('sid-7');
        await ev(`import(${S} + 'controls.js').then(m => m.killSession()); true`); await yes();
        check('2. the same session id on another server stays', JSON.stringify(await ids()) === JSON.stringify(['sid-7@remote-1', 'sid-8@local']), JSON.stringify(await ids()));

        // 3. the sidebar's Kill of the OPEN agent stops its capture refresh (it left the interval polling a dead agent)
        await open([row(3), row(4)]);
        await select('sid-3');
        await ev(`import(${S} + 'agent_actions.js').then(m => m.killSessionDirect('agent-3', 'claude', 'sid-3')); true`); await yes();
        check('3. the open agent killed from the sidebar: the capture refresh stopped', await ev(`import(${S} + 'state.js').then(st => st.state.captureInterval === null)`));
        check('3. and the welcome screen shows', await ev(`getComputedStyle(document.getElementById('welcome-screen')).display !== 'none'`));
        // 4. Kill Team goes through the team route, once for the whole team (so the team is marked stopped as an API kill does), and says what became of the worktree; a refused stop is said
        await open([row(5, { board_project: 'crew' }), row(6, { board_project: 'crew' }), row(9)]);
        await ev(`window.__teamAnswer = { ok: true, sessions_killed: 2, worktree: { state: 'kept', path: '/tmp/wt/crew', branch: 'coral-team/crew', reason: 'it has uncommitted changes' } }; import(${S} + 'team_actions.js').then(m => m.killBoard('crew')); true`); await yes();
        const tp = await ev(`window.__posts`);
        check('4. one request, to the team route, no per-agent kill', tp.length === 1 && /\/live\/team\/crew\/kill$/.test(tp[0].url), JSON.stringify(tp));
        check('4. both members go, the other agent stays', JSON.stringify(await ids()) === JSON.stringify(['sid-9@local']), JSON.stringify(await ids()));
        check('4. the kept worktree is said in the bar, with its path behind Details', await keptBar('/tmp/wt/crew'));
        await open([row(5, { board_project: 'crew' }), row(6, { board_project: 'crew' })]);
        await ev(`window.__teamStatus = 500; window.__teamAnswer = { error: 'could not stop the team' }; import(${S} + 'team_actions.js').then(m => m.killBoard('crew')); true`); await yes();
        check('4. a refused stop is said, not silent', /could not stop the team/i.test(await ev(`document.body.innerText`)));
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} kill-session checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
