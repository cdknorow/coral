// The big HTML builders, before they are split (#4895, from #4893 proposal C): what team_artifacts.js render, needsInputHtml, renderArtifact, _renderSessionItem and renderRunDetail
// produce for a table of states, serialized with the volatile parts fixed (the time zone and locale, the clock, ids that count up) and compared exactly with fixtures/html_builders_golden.json.
// Each builder is reached the way the dashboard reaches it (a stubbed answer, a state, a click), not by an export made for the test, so splitting a builder into smaller ones must leave every
// case byte for byte as it is. For team_artifacts the listeners the render attaches are recorded too (a split that drops a click handler changes no markup). CORAL_UPDATE_GOLDEN=1 rewrites
// the golden. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { compareGolden } = require('./helpers/builder_golden');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'html_builders_golden.json');
const HEX = c => c.repeat(64);

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const cases = {};
    const S = `(window.__coralStatic||'/static/')`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1200, height: 900, deviceScaleFactor: 1, mobile: false });
        await Emulation.setTimezoneOverride({ timezoneId: 'UTC' });
        await Emulation.setLocaleOverride({ locale: 'en-US' });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);

        // ── team_artifacts.js render ──────────────────────────────────────────────
        const TEAM = 'golden-team';
        const ITEMS = [
            { id: HEX('a'), uri: 'coral://artifacts/' + HEX('a'), name: 'report.md', media_type: 'text/markdown', size: 2048, task_id: 21, task_title: 'Write the report', created_at: '2026-10-01T10:00:00Z', produced_by: 'writer', reference_count: 2, source: 'local' },
            { id: HEX('b'), uri: 'coral://artifacts/' + HEX('b'), name: HEX('b') + '.json', media_type: 'application/json', size: 12, created_at: '2026-10-02T11:30:00Z', task_id: 0 },
            { id: HEX('c'), uri: '/api/artifacts/' + HEX('c'), name: 'photo.png', media_type: 'image/png', size: 3 * 1024 * 1024, created_at: '2026-10-03T08:15:00Z', source: 'shared', shared_to: 'other-team', sent_by: 'lead' },
            { id: HEX('d'), uri: 'coral://artifacts/' + HEX('d'), name: 'from-afar.txt', media_type: 'text/plain', size: 900, created_at: '2026-10-04T09:00:00Z', source: 'received', received_from: 'laptop', from_board: 'their-board', also_received_from: ['desk/board-x', 'server only'], sent_by: 'peer' },
            { id: HEX('e'), uri: 'coral://artifacts/' + HEX('e'), name: 'cited.txt', provenance: 'unverified', created_at: '2026-10-05T09:00:00Z', task_id: 7, task_title: 'Cites a file' },
            { id: HEX('f'), uri: 'https://example.com/docs/spec.html', name: 'spec (link)', media_type: 'text/html', created_at: 'not a date' },
            { id: HEX('9'), uri: 'coral://artifacts/' + HEX('9'), name: 'gone.bin', available: false, media_type: 'application/octet-stream', size: 5 },
            { id: HEX('8'), uri: 'mailto:nobody', name: '', media_type: '', task_id: 3, created_at: '2026-10-06T00:00:00Z' },
            { id: HEX('7'), inline: true, content_url: `/api/board/${TEAM}/artifacts/inline-1/content`, uri: 'inline:1', name: 'inline-note.md', media_type: 'text/markdown', size: 40, created_at: '2026-10-07T12:00:00Z' },
        ];
        await ev(`(() => { const R = window.fetch; window.__ta = { mode: 'normal', items: [], kept: [], extra: {}, personal: [] };
            const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = (o.method || 'GET').toUpperCase(); const p = url.pathname, T = window.__ta;
                if (p.startsWith('/api/board/') && p.endsWith('/artifacts') && m === 'GET') {
                    const body = () => json({ project: decodeURIComponent(p.split('/')[3]), artifacts: T.items, ...T.extra });
                    if (T.mode === 'error') return json({}, 500); if (T.mode === 'deferred') return new Promise(res => { T.release = () => res(body()); }); return body(); }
                if (p === '/api/session-artifacts') { if (T.mode === 'error') return json({}, 500); return json({ session_id: url.searchParams.get('session_id'), artifacts: T.personal }); }
                if (p === '/api/system/artifact-store/kept') return json({ kept: T.kept });
                { const d = /^\\/api\\/artifacts\\/([a-f0-9]{64})$/.exec(p); if (d && m === 'DELETE') return json({ deleted: true }); }
                if (p.startsWith('/api/')) return json({}); return R(u, o); };
            // every listener a render attaches, as "tag.class:type"
            window.__listeners = []; const add = EventTarget.prototype.addEventListener;
            EventTarget.prototype.addEventListener = function (t, ...r) { const d = this instanceof Element ? this.tagName.toLowerCase() + '.' + String(this.className).trim().replace(/\\s+/g, '.') : (this === window ? 'window' : this === document ? 'document' : 'other'); window.__listeners.push(d + ':' + t); return add.call(this, t, ...r); };
            return true; })()`);
        await ev(`(async () => { const st = ${S}; window.TA = await import(st + 'team_artifacts.js'); window.ST = (await import(st + 'state.js')).state; const {showView} = await import(st + 'utils.js'); showView('live-session-view');
            const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px'; return true; })()`);
        const ROOT = id => `document.getElementById('agentic-panel-${id}-view')`;
        // the markup of a panel, with the listeners its last render attached (sorted, one line each)
        const snap = async id => ev(`(() => { const r = ${ROOT(id)}; return r.outerHTML + '\\n<!-- listeners\\n' + [...new Set(window.__listeners.filter(l => !/^(window|document|other):/.test(l)))].sort().join('\\n') + '\\n-->'; })()`);
        const settled = id => waitFor(`${ROOT(id)}.getAttribute('aria-busy') === 'false'`, 'the panel to settle: ' + id, 8000);
        const teamState = (t, session) => ev(`(() => { window.ST.selectedTeam = ${JSON.stringify(t)}; window.ST.currentSession = ${JSON.stringify(session)}; window.TA.syncFilesSourceTeam(); return true; })()`);
        const refresh = id => ev(`window.__listeners.length = 0; ${ROOT(id)}.querySelector('.team-artifacts-refresh').click(); true`);
        const SESSION = { type: 'live', name: 'golden-agent', session_id: 'golden-sid', board_project: TEAM };

        // no team: the line says to pick one
        await ev(`localStorage.removeItem('coral-agentic-tab-top'); window.TA.selectFilesSource('team-artifacts'); true`);
        await teamState(null, null);
        cases['team artifacts: no team'] = await snap('team-artifacts');
        // a team, a list that is still loading, then the answer
        await teamState(TEAM, SESSION);
        await settled('team-artifacts');
        await ev(`window.__ta.mode = 'deferred'; true`); await refresh('team-artifacts');
        await waitFor(`${ROOT('team-artifacts')}.getAttribute('aria-busy') === 'true'`, 'the loading state');
        cases['team artifacts: loading'] = await snap('team-artifacts');
        await ev(`window.__ta.release(); window.__ta.mode = 'normal'; true`); await settled('team-artifacts');
        cases['team artifacts: empty'] = await snap('team-artifacts');
        await ev(`window.__ta.mode = 'error'; true`); await refresh('team-artifacts'); await settled('team-artifacts');
        cases['team artifacts: error'] = await snap('team-artifacts');
        await ev(`window.__ta.mode = 'normal'; window.__ta.items = ${JSON.stringify(ITEMS)}; window.__ta.kept = []; true`); await refresh('team-artifacts'); await settled('team-artifacts');
        cases['team artifacts: rows'] = await snap('team-artifacts');
        await ev(`window.__ta.extra = { has_more: true, next_cursor: 'c2', total: 9, all: 40 }; true`); await refresh('team-artifacts'); await settled('team-artifacts');
        cases['team artifacts: more rows to load, 9 of 40'] = await snap('team-artifacts');
        // a later page that fails keeps the rows already shown and says so, with Retry (UX's note on #4895: the Retry button is built from the rows it is given)
        await ev(`window.__ta.mode = 'error'; ${ROOT('team-artifacts')}.querySelector('.team-artifacts-more').click(); true`);
        await waitFor(`!!${ROOT('team-artifacts')}.querySelector('.team-artifacts-retry') && ${ROOT('team-artifacts')}.getAttribute('aria-busy') === 'false'`, 'the failed page and its Retry');
        cases['team artifacts: a later page failed (the rows stay, with Retry)'] = await snap('team-artifacts');
        await ev(`window.__ta.mode = 'normal'; true`);
        await ev(`window.__ta.extra = { truncated: true, total: 9, all: 9 }; true`); await refresh('team-artifacts'); await settled('team-artifacts');
        cases['team artifacts: the server cut the list'] = await snap('team-artifacts');
        await ev(`window.__ta.extra = {}; window.__ta.kept = [${JSON.stringify(HEX('a'))}]; true`); await refresh('team-artifacts'); await settled('team-artifacts');
        cases['team artifacts: one artifact kept'] = await snap('team-artifacts');
        await ev(`document.body.classList.add('popout-mode'); true`); await refresh('team-artifacts'); await settled('team-artifacts');
        cases['team artifacts: pop-out (no Delete or Keep)'] = await snap('team-artifacts');
        await ev(`document.body.classList.remove('popout-mode'); true`);
        // the outcome of a delete: the notice line and the live region
        await ev(`window.__ta.items = ${JSON.stringify(ITEMS)}.slice(0, 2); true`); await refresh('team-artifacts'); await settled('team-artifacts');
        await ev(`${ROOT('team-artifacts')}.querySelector('.team-artifact-delete').click(); true`);
        await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation');
        await ev(`window.__ta.items = window.__ta.items.slice(1); document.getElementById('confirm-modal-yes').click(); true`);
        await waitFor(`!!${ROOT('team-artifacts')}.querySelector('.team-artifacts-notice')`, 'the notice');
        cases['team artifacts: after a delete'] = (await snap('team-artifacts')) + '\n' + await ev(`(document.getElementById('agentic-panel-team-artifacts-live') || {}).textContent`);
        // the personal list (a standalone agent: no team)
        await ev(`window.__ta.personal = ${JSON.stringify(ITEMS.slice(0, 3))}; window.__ta.mode = 'normal'; window.TA.selectFilesSource('artifacts'); true`);
        await teamState(null, { type: 'live', name: 'solo', session_id: 'solo-sid' });
        await settled('artifacts');
        cases['agent artifacts: a standalone agent'] = await snap('artifacts');
        await teamState(null, null);
        cases['agent artifacts: no agent'] = await snap('artifacts');

        // ── needs_input_card.js needsInputHtml (through syncNeedsInputCard, which the chat calls on every poll) ──
        await ev(`(async () => { const st = ${S}; window.NI = await import(st + 'needs_input_card.js'); return true; })()`);
        const sel = (n, label, extra = {}) => ({ n, label, action: 'select', ...extra });
        const ASK = { tool_name: 'AskUserQuestion', input: { questions: [{ question: 'Which database should the service use?', options: [{ label: 'Postgres', description: 'Full SQL, needs a server' }, { label: 'SQLite', description: 'One file, no server' }, 'Redis'] }] } };
        const NEEDS = [
            ['a question with the options on screen and descriptions from the hook', { agent_type: 'claude' }, { pt: ASK, screen: { question: 'Which database should the service use?', options: [sel(1, 'Postgres', { selected: true }), sel(2, 'SQLite'), sel(3, 'Redis'), { n: 4, label: 'Type something.', action: 'text' }, { n: 5, label: 'Chat about this', action: 'chat' }] } }],
            ['a question the screen cut short (the hook has the full text)', { agent_type: 'claude' }, { pt: { tool_name: 'AskUserQuestion', input: { questions: [{ question: 'Should I delete the generated files before running the build again?', options: [{ label: 'Yes, delete them first', description: 'Cleaner' }, { label: 'No, keep them' }] }] } }, screen: { question: 'delete the generated files before running the build again?', options: [sel(1, 'Yes, delete'), sel(2, 'No, keep')] } }],
            ['a question with no options on screen yet (the hook\'s questions are listed)', { agent_type: 'claude' }, { pt: ASK, screen: null }],
            ['a question at its review step (Submit and Cancel)', { agent_type: 'claude' }, { pt: ASK, screen: { question: 'Review your answers', options: [sel(1, 'Submit answers'), sel(2, 'Cancel')], review: [{ question: 'Database?', answer: 'Postgres' }, { question: 'Cache?', answer: '' }] } }],
            ['a plan to approve, with the plan text', { agent_type: 'claude' }, { pt: { tool_name: 'ExitPlanMode', input: { plan: '## Plan\n\n1. Split the file\n2. Run the tests\n\n`code` and **bold**' } }, screen: { question: 'Would you like to proceed?', options: [sel(1, 'Yes, and auto-accept edits', { selected: true }), sel(2, 'Yes, manually approve edits'), sel(3, 'No, keep planning')] } }],
            ['a plan with nothing on screen', { agent_type: 'claude' }, { pt: { tool_name: 'ExitPlanMode', input: {} }, screen: null }],
            ['a permission prompt for a shell command with its description', { agent_type: 'claude', waiting: true }, { pt: { tool_name: 'Bash', input: { command: 'rm -rf build/ && make', description: 'Clean and rebuild' } }, screen: { question: 'Do you want to proceed?', options: [sel(1, 'Yes', { selected: true }), sel(2, "Yes, and don't ask again for rm commands"), sel(3, 'No, and tell Claude what to do differently')] } }],
            ['a permission prompt for an edit of a file', { agent_type: 'claude', waiting: true }, { pt: { tool_name: 'Edit', input: { file_path: '/tmp/project/main.go' } }, screen: null }],
            ['a permission prompt with only the notification text', { agent_type: 'claude', waiting: true, summary: 'Notification: Claude needs your permission to use WebFetch' }, { pt: null, screen: null }],
            ['a Codex prompt read from the screen', { agent_type: 'codex' }, { pt: null, screen: { question: 'Allow Codex to run this command?', options: [sel(1, 'Yes, proceed', { selected: true }), sel(2, 'No, and tell Codex what to do differently')] } }],
            ['an Antigravity prompt with a pending tool', { agent_type: 'agy' }, { pt: { tool_name: 'run_command', input: { command: 'ls' } }, screen: { question: '', options: [sel(1, 'Allow'), sel(2, 'Deny')] } }],
            ['the agent has not started (check the terminal)', { agent_type: 'claude', not_started: true }, { pt: null, screen: null }],
            ['a sign-in with the CLI\'s reason', { agent_type: 'claude' }, { login: { reason: 'Please run /login · API Error: 401' } }],
            ['a sign-in the row reports, with no reason', { agent_type: 'pi', needs_login: true }, {}],
            ['a sleeping agent shows no card', { agent_type: 'claude', sleeping: true }, { pt: ASK, screen: null }],
        ];
        for (const [name, row, maps] of NEEDS) {
            cases['needs input: ' + name] = await ev(`(() => { const NI = window.NI, st = window.ST; const sid = 'ni-sid';
                const row = Object.assign({ session_id: sid, name: 'ni-agent', agent_type: 'claude', waiting_for_input: ${!!row.waiting}, waiting_summary: ${JSON.stringify(row.summary || '')} }, ${JSON.stringify(row)});
                delete row.waiting; delete row.summary;
                st.liveSessions = [row];
                NI.pendingToolBySession.clear(); NI.promptOptionsBySession.clear(); NI.loginBySession.clear();
                const m = ${JSON.stringify(maps)}; if (m.pt) NI.pendingToolBySession.set(sid, m.pt); if (m.screen) NI.promptOptionsBySession.set(sid, m.screen); if (m.login) NI.loginBySession.set(sid, m.login);
                const box = document.createElement('div'); NI.syncNeedsInputCard(box, row); return box.innerHTML; })()`);
        }

        // ── artifact_preview.js renderArtifact: one case per kind of answer ──────────────
        await ev(`(async () => { const st = ${S}; window.AP = await import(st + 'artifact_preview.js'); return true; })()`);
        const D = HEX('5');
        const URI = 'coral://artifacts/' + D;
        const MANIFEST = JSON.stringify([{ name: 'one.md', uri: 'coral://artifacts/' + HEX('1'), media_type: 'text/markdown' }, { name: 'two.png', uri: 'coral://artifacts/' + HEX('2') }]);
        const REPORT = JSON.stringify({ task: 12, outcome: 'success', summary: 'All good', verification: [{ check: 'tests', result: 'pass' }] });
        const ART = [
            ['an address that is not an artifact', 'not-a-uri', {}, null],
            ['an external https link', URI, { externalURL: 'https://example.com/page.html', filename: 'page.html' }, null],
            ['an answer of 404', URI, {}, { status: 404 }],
            ['an answer that never comes (network failure)', URI, {}, { fail: 'connection reset' }],
            ['HTML (sandboxed)', URI, { filename: 'page.html' }, { type: 'text/html; charset=utf-8', body: '<h1>Hi</h1><script>1</script>' }],
            ['HTML declared by the artifact, served as plain text', URI, { mediaType: 'text/html', filename: 'x' }, { type: 'text/plain', body: '<p>declared html</p>' }],
            ['Markdown by type', URI, { filename: 'notes' }, { type: 'text/markdown', body: '# Title\n\ntext' }],
            ['Markdown by file name', URI, { filename: 'README.md' }, { type: 'application/octet-stream', body: '# Title' }],
            ['plain source text', URI, { filename: 'main.go' }, { type: 'text/plain', body: 'package main\n' }],
            ['plain text named by the Content-Disposition header', URI, {}, { type: 'text/plain', body: 'body', disposition: 'attachment; filename="from-header.txt"' }],
            ['a file collection (JSON manifest)', URI, { filename: 'manifest.json' }, { type: 'application/json', body: MANIFEST }],
            ['a report (JSON by name)', URI, { filename: 'acceptance_report.json' }, { type: 'application/json', body: REPORT }],
            ['a report by its schema, unnamed', URI, {}, { type: 'application/json', body: REPORT }],
            ['JSON that is neither', URI, { filename: 'data.json' }, { type: 'application/json', body: '{"a":1}' }],
            ['an image', URI, { filename: 'p.png' }, { type: 'image/png', body: 'x' }],
            ['an SVG image', URI, { filename: 'p.svg' }, { type: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg"/>' }],
            ['audio', URI, {}, { type: 'audio/mpeg', body: 'x' }],
            ['video', URI, {}, { type: 'video/mp4', body: 'x' }],
            ['a binary file (not rendered inline)', URI, { filename: 'tool.bin' }, { type: 'application/octet-stream', body: 'x', length: 4096 }],
            ['text too large to preview', URI, { filename: 'big.log' }, { type: 'text/plain', body: 'x', length: 5 * 1024 * 1024 }],
            ['an inline artifact of a board', URI, { contentURL: `/api/board/${TEAM}/artifacts/inline-1/content`, mediaType: 'text/markdown', filename: 'inline.md' }, { type: 'text/plain', body: 'inline body' }],
        ];
        for (const [name, uri, options, answer] of ART) {
            cases['artifact preview: ' + name] = await ev(`(async () => { const body = document.createElement('div'), calls = [];
                const R = window.fetch, a = ${JSON.stringify(answer)};
                window.fetch = (u, o) => { if (!a) return R(u, o); if (a.fail) return Promise.reject(new Error(a.fail)); if (a.status) return Promise.resolve(new Response('', { status: a.status }));
                    const h = { 'Content-Type': a.type }; if (a.disposition) h['Content-Disposition'] = a.disposition; if (a.length) h['Content-Length'] = String(a.length);
                    return Promise.resolve(new Response(a.body, { status: 200, headers: h })); };
                const ctx = { signal: undefined, isStale: () => false, renderContent: (b, content, path) => { calls.push(['renderContent', content, path]); b.textContent = '[rendered ' + path + ']'; }, openEntry: e => calls.push(['openEntry', e]), setMeta: m => calls.push(['setMeta', m]) };
                try { await window.AP.renderArtifact(body, ${JSON.stringify(uri)}, ${JSON.stringify(options)}, ctx); } finally { window.fetch = R; }
                return body.innerHTML + '\\n<!-- ' + JSON.stringify(calls) + ' -->'; })()`);
        }

        // ── session_rows.js _renderSessionItem: one agent row per state ──────────────────
        await ev(`(async () => { const st = ${S}; window.SR = await import(st + 'session_rows.js'); return true; })()`);
        const ROWS = [
            ['an idle agent', {}],
            ['a working agent with a goal', { working: true, summary: 'Refactoring the settings dialog' }],
            ['a permission prompt', { waiting_for_input: true, waiting_kind: 'permission', waiting_summary: 'Notification: Claude needs your permission to use Bash' }],
            ['a stuck agent', { stuck: true }],
            ['a sleeping agent', { sleeping: true }],
            ['an agent that has not started', { not_started: true }],
            ['a sign-in', { needs_login: true }],
            ['a display name and a job title', { display_name: 'Release Engineer', board_job_title: 'DevOps', board_project: 'ship-team' }],
            ['a very long name', { name: 'release-engineering-coordinator-agent-number-4-9', display_name: 'release-engineering-coordinator-agent-number-4-9' }],
            ['a terminal (no goal line)', { agent_type: 'terminal' }],
            ['Codex, with a model', { agent_type: 'codex', model: 'gpt-5-codex' }], // the provider, model and thinking level options are on as it starts (#5742): the agent line is drawn
            ['Codex, with a model, with the agent line turned off', { agent_type: 'codex', model: 'gpt-5-codex' }, 'coral-show-agent-type=false'], // the old single switch, off: all three options off
            ['Gemini', { agent_type: 'gemini' }],
            ['Antigravity', { agent_type: 'agy' }],
            ['context at 85 percent', { context_pct: 85, context_window: 200000 }],
            ['context full', { context_pct: 100 }],
            ['a worktree on its own branch', { branch: 'fix/login', git_state: { is_worktree: true, base_branch: 'main' } }], // Show branches is off, as it starts (#5117): no worktree line
            ['a worktree on its own branch, with Show branches on', { branch: 'fix/login', git_state: { is_worktree: true, base_branch: 'main' } }, 'coral-show-branches'],
            ['a model on a claude agent', { model: 'claude-opus-4-1' }],
            ['a model and a thinking level on a claude agent', { model: 'claude-opus-4-1', thinking_actual: 'high' }],
            ['a thinking level only, with the model option off', { model: 'claude-opus-4-1', thinking: 'medium' }, 'coral-show-model=false'],
            ['an agent with a prompt only (no summary)', { first_prompt: 'Please fix the login bug', summary: '' }],
            ['an agent on a stale server', { server: 'gone-box', stale: true }],
        ];
        for (const [name, extra, setting] of ROWS) for (const [flavour, args] of [['', [false, false]], [' (compact, collapsed)', [true, true]]]) {
            cases['agent row: ' + name + flavour] = await ev(`(() => { const st = window.ST; const sid = 'row-sid';
                const s = Object.assign({ session_id: sid, name: 'row-agent', agent_type: 'claude', working_directory: '/tmp/proj', type: 'live' }, ${JSON.stringify(extra)})
                st.liveSessions = [s]; st.selectedTeam = null; st.currentSession = null;
                const [key, value] = ${JSON.stringify(setting || 'x')}.split('=');
                if (${JSON.stringify(setting || '')}) localStorage.setItem(key, value || 'true');
                try { return window.SR._renderSessionItem(s, 'group-a', ${args[0]}, ${args[1]}, '/tmp/proj'); } finally { localStorage.removeItem(key); } })()`);
        }
        cases['agent row: the open agent'] = await ev(`(() => { const st = window.ST; const s = { session_id: 'row-sid', name: 'row-agent', agent_type: 'claude', working_directory: '/tmp/proj', type: 'live', summary: 'Open now' };
            st.liveSessions = [s]; st.selectedTeam = null; st.currentSession = s; const html = window.SR._renderSessionItem(s, 'group-a', false, false, '/tmp/proj'); st.currentSession = null; return html; })()`);

        // ── workflows.js renderRunDetail (through selectWorkflowRun, with the clock fixed: a running run shows how long it has run) ──
        await ev(`(async () => { const st = ${S}; window.WF = await import(st + 'workflows.js'); return true; })()`);
        const T0 = '2026-10-10T11:00:00Z';
        const step = (o) => ({ status: 'completed', type: 'shell', started_at: T0, finished_at: '2026-10-10T11:00:42Z', ...o });
        const RUNS = [
            ['a finished run with every kind of step', { id: 41, workflow_id: 7, workflow_name: 'Nightly build', status: 'completed', trigger_type: 'schedule', started_at: T0, finished_at: '2026-10-10T11:07:30Z', steps: [
                step({ name: 'checkout', exit_code: 0, output_tail: 'Cloning into repo...\nDone <b>&</b>', files: ['log.txt', 'out/<x>.bin'] }),
                step({ name: 'agent review', type: 'agent', session_id: 'sess-9', session_name: 'reviewer-1' }),
                step({ name: 'notify', type: 'http', status: 'completed', started_at: null, finished_at: null }),
                step({ index: 3, type: 'shell', status: 'pending', started_at: null, finished_at: null })] }],
            ['a failed run with the failing step\'s error', { id: 42, workflow_id: 7, workflow_name: 'Deploy', status: 'failed', trigger_type: 'api', started_at: T0, finished_at: '2026-10-10T11:01:00Z', error_msg: 'step test failed', steps: [
                step({ name: 'build', exit_code: 0 }),
                step({ name: 'test', status: 'failed', exit_code: 2, output_tail: 'running 3 tests\nFAIL', error_tail: 'panic: boom\n\ngoroutine 1 [running]:\nmain.main()  \n' }),
                step({ name: 'ship', status: 'pending', started_at: null, finished_at: null })] }],
            ['a killed run', { id: 43, workflow_id: 7, workflow_name: 'Long job', status: 'killed', started_at: T0, finished_at: '2026-10-10T11:30:00Z', steps: [step({ name: 'wait', status: 'killed', error_tail: 'terminated' })] }],
            ['a running run (Kill, a step still going)', { id: 44, workflow_id: 7, workflow_name: 'Live', status: 'running', trigger_type: 'manual', started_at: '2026-10-10T11:58:00Z', steps: [
                step({ name: 'one', started_at: '2026-10-10T11:58:00Z', finished_at: '2026-10-10T11:58:30Z' }),
                step({ name: 'two', status: 'running', started_at: '2026-10-10T11:58:30Z', finished_at: null })] }],
            ['a pending run with no start', { id: 45, workflow_id: 7, workflow_name: 'Queued', status: 'pending', steps: [] }],
            ['a run with no steps and no workflow', { id: 46, workflow_name: '', status: 'completed', started_at: T0, finished_at: T0, steps: [] }],
        ];
        for (const [name, run] of RUNS) {
            cases['workflow run: ' + name] = await ev(`(async () => { const R = window.fetch, RD = Date, FIXED = RD.parse('2026-10-10T12:00:00Z'), run = ${JSON.stringify(run)};
                window.Date = class extends RD { constructor(...a) { if (a.length) super(...a); else super(FIXED); } static now() { return FIXED; } };
                window.fetch = (u, o) => String(u).includes('/api/workflows/runs/') ? Promise.resolve(new Response(JSON.stringify(run), { status: 200, headers: { 'content-type': 'application/json' } })) : R(u, o);
                try { await window.WF.selectWorkflowRun(run.id); return document.getElementById('wf-run-detail').innerHTML; }
                finally { window.Date = RD; window.fetch = R; window.WF.workflowsBackToList(); } })()`);
        }

        const problems = compareGolden(GOLDEN, cases, 'What the big dashboard HTML builders produced for a table of states, serialized, for html_builders_golden_browser.test.js. CORAL_UPDATE_GOLDEN=1 rewrites it.');
        if (problems.length) { console.error(problems.join('\n')); process.exitCode = 1; }
        else console.log(`PASS html builders golden: ${Object.keys(cases).length} cases identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
