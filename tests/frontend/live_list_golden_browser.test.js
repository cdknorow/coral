// The agent list's markup, whole (#4649): the list is drawn by one long function (render.js _renderLiveSessions), and splitting it must change nothing. This draws a fixture with every kind of
// group (teams, a workflow, folders, a pinned folder with no agent, terminals), every kind of row (working, waiting, sleeping, done, archived, stale) and every list mode (team first, folder
// first, hub with two servers grouped and not, a search, the sleeping and archived sections open), on a desktop and a phone, and compares the list's HTML with the one kept in
// fixtures/live_list_golden.json. CORAL_UPDATE_GOLDEN=1 rewrites that file (review the diff: it is the whole list). Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'live_list_golden.json');
const sha = t => require('node:crypto').createHash('sha256').update(t).digest('hex');
const SAMPLES = ['desktop: team first', 'desktop: hub, grouped by server'];

const S = (n, o = {}) => ({ name: 'n-' + n, session_id: '0000000' + n + '-0000-4000-8000-00000000000' + n, agent_type: 'claude', server: 'local', status: 'working', summary: 'Task ' + n, display_name: 'Agent ' + n,
    working_directory: '/repo/x', branch: 'main', staleness_seconds: 5, ...o });
const SESSIONS = [
    S(1, { board_project: 'team-a', working_directory: '/repo/a', display_name: 'Lead' }),
    S(2, { board_project: 'team-a', working_directory: '/repo/a', sleeping: true, display_name: 'Sleeper' }),
    S(3, { board_project: 'team-a', working_directory: '/repo/a', waiting_for_input: true, display_name: 'Waiting' }),
    S(4, { board_project: 'team-b', working_directory: '/repo/b', sleeping: true }),
    S(5, { board_project: 'team-b', working_directory: '/repo/b', sleeping: true }),
    S(6, { workflow_name: 'build-flow', workflow_run_id: 7 }),
    S(7, { workflow_name: 'build-flow', workflow_run_id: 7, status: 'idle' }),
    S(8, { working_directory: '/repo/x' }),
    S(9, { working_directory: '/repo/x', sleeping: true, display_name: 'Folder sleeper' }),
    S(10, { working_directory: '/repo/y', agent_type: 'terminal', display_name: 'term' }),
    S(11, { working_directory: '/repo/y', archived: true, display_name: 'Archived one' }),
    S(12, { working_directory: '/repo/z', display_name: 'Elsewhere', server: 's1' }),
    S(13, { board_project: 'team-c', working_directory: '/repo/c', display_name: 'Remote lead', server: 's1' }),
    S(14, { working_directory: '/repo/z', display_name: 'Stale remote', server: 's2', staleness_seconds: 4000 }),
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: layout and redraws settle over frames; there is no completion event
    const got = {};
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const open = async (w, mobile) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
        };
        const render = (settings, filter) => ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const ws = await import(st + 'websocket.js'); const render = await import(st + 'render.js'); const { state } = await import(st + 'state.js'); const pf = await import(st + 'pinned_folders.js');
            const filt = await import(st + 'agent_filter.js');
            if (state.coralWs) { state.coralWs.onclose = null; state.coralWs.onmessage = null; try { state.coralWs.close(); } catch {} }
            localStorage.clear(); for (const [k, v] of Object.entries(${JSON.stringify(settings)})) localStorage.setItem(k, v);
            const real = window.fetch; window.fetch = (u, o) => String(u).includes('/api/pinned-folders') ? Promise.resolve(new Response(JSON.stringify({ folders: [{ path: '/repo/pinned', name: 'pinned', exists: true }, { path: '/repo/x', name: 'x', exists: true }] }), { status: 200, headers: { 'content-type': 'application/json' } })) : real(u, o);
            await pf.loadPinnedFolders({ render: false }); window.fetch = real;
            filt.setAgentFilter(${JSON.stringify(filter || '')});
            state.settings = {}; // the list reads the server's display settings (show_session_server: unset = chips with 2+ servers); an earlier suite on the same server may have saved one (server_switcher leaves it False), so the fixture pins them
            const hub = ${JSON.stringify(settings.__hub || false)};
            state.hub = hub;
            ws.handleCoralMessage({ type: 'coral_update', servers: hub ? [{ id: 'local', label: 'here', status: 'online' }, { id: 's1', label: 'box-one', status: 'online' }, { id: 's2', label: 'box-two', status: 'unreachable' }] : [{ id: 'local', label: 'here', status: 'online' }], sessions: ${JSON.stringify(SESSIONS)}.filter(s => hub || s.server === 'local') });
            render.flushLiveSessionsRender(); render.renderLiveSessions(state.liveSessions);
            const el = document.getElementById('live-sessions-list') || document.getElementById('mobile-session-list');
            const empty = el && el.querySelector(':scope > .empty-state');
            return render.lastLiveListHtml() + (empty ? '\\n<!--empty: ' + empty.textContent + '-->' : '');
        })()`);
        const SCEN = [
            ['team first', {}],
            ['folder first', { 'coral-group-by-team': 'false' }],
            ['team first, sleeping and archived open', { 'coral-sleeping-expanded:team-b': 'true', 'coral-sleeping-expanded:/repo/x': 'true', 'coral-archived-expanded': 'true' }],
            ['team first, a team and a folder collapsed', { coral_collapsed_groups: JSON.stringify(['team-a', 'n-8', 'wf:build-flow', 'pinned']) }],
            ['folder first, a folder collapsed', { 'coral-group-by-team': 'false', coral_collapsed_groups: JSON.stringify(['n-8', 'team-a']) }],
            ['hub, servers shown', { __hub: true }],
            ['hub, grouped by server', { __hub: true, 'coral-group-by-server': 'true' }],
            ['hub, grouped by server, folder first', { __hub: true, 'coral-group-by-server': 'true', 'coral-group-by-team': 'false' }],
            ['a search', {}, 'agent'],
            ['a search with no match', {}, 'zzzz'],
        ];
        for (const [width, mobile, tag] of [[1440, false, 'desktop'], [390, true, 'phone']]) {
            await open(width, mobile);
            for (const [name, settings, filter] of SCEN) {
                const html = await render(settings, filter);
                assert.ok(typeof html === 'string' && html.length > 50, `${tag} ${name}: the list drew`);
                got[`${tag}: ${name}`] = html;
            }
        }
        // The list is the same every time (nothing in it depends on the clock).
        await open(1440, false);
        const again = await render({}, '');
        assert.equal(again, got['desktop: team first'], 'drawing it twice gives the same markup');
        if (process.env.CORAL_UPDATE_GOLDEN) {
            fs.mkdirSync(path.dirname(GOLDEN), { recursive: true });
            const hashes = Object.fromEntries(Object.entries(got).map(([k, v]) => [k, { sha256: sha(v), length: v.length }]));
            fs.writeFileSync(GOLDEN, JSON.stringify({ about: 'sha256 of the list markup for each scenario of live_list_golden_browser.test.js; the full text of two is kept to read a change against', hashes, samples: Object.fromEntries(SAMPLES.map(k => [k, got[k]])) }, null, 1) + '\n');
            console.log('golden written: ' + Object.keys(got).length + ' lists');
            return;
        }
        const want = JSON.parse(fs.readFileSync(GOLDEN, 'utf8'));
        assert.deepEqual(Object.keys(got), Object.keys(want.hashes), 'the same scenarios');
        for (const k of Object.keys(want.hashes)) {
            if (sha(got[k]) === want.hashes[k].sha256) continue;
            const was = want.samples[k];
            let detail = ` (now ${got[k].length} characters, was ${want.hashes[k].length}; CORAL_UPDATE_GOLDEN=1 on the earlier commit and this one, then diff the two lists)`;
            if (was) { let i = 0; while (i < got[k].length && got[k][i] === was[i]) i++; detail = ` at character ${i}:\n  now:  ...${got[k].slice(Math.max(0, i - 80), i + 160)}\n  was:  ...${was.slice(Math.max(0, i - 80), i + 160)}`; }
            assert.fail(`${k}: the list's markup changed${detail}`);
        }
        console.log(`PASS live list golden: ${Object.keys(want.hashes).length} lists identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
