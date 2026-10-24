// The whole dashboard at 320 px in the light theme (#5273), the corner the other phone suites leave: phone_layout walks every view at 320 in the default theme (geometry only), and phone_pass walks
// tonight's new views at 320 in both themes (geometry and 44 px, no contrast). Here, light theme, 320 px, touch: every top tab, the agent's chat and terminal, the side panel's tabs, the Inbox, the Servers tab
// (a hub fixture), Settings (every tab) and every dialog, the Launch dialog's two steps, the artifact viewer with its comments sheet: the page is not wider than the screen, no control clipped off an edge,
// no text cut without an ellipsis, every control 44 x 44 px, and every piece of text 4.5:1 (3:1 for large text and icons) against what is behind it (helpers/contrast.js).
// CORAL_SCREENSHOT_DIR keeps one screenshot per scene. Run against an isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { SCAN } = require('./helpers/phone_scan');
const { installAudit, setTheme, auditRegion } = require('./helpers/contrast');
const { removeBoards } = require('./helpers/board_cleanup');

const FENCE = '`'.repeat(3);
const CHAT = [
    { type: 'user', content: 'Please check https://example.com/a/very/long/path/that/does/not/break/anywhere/and/keeps/going/on/and/on/for/a/while?with=query&and=more and fix /home/dev/work/project/internal/server/routes/some_long_file_name_handler_test.go', key: 'k0', timestamp: '2026-10-19T00:00:00Z' },
    { type: 'assistant', text: ['I read the handler and found the lock taken twice.', '', '- fix the error path', '- add a test', '', FENCE + 'go', 'func (s *Store) Update(ctx context.Context, id string, fields map[string]string) (retErr error) { return nil }', FENCE, '', '| file | change | lines |', '|---|---|---|', '| internal/server/routes/some_long_file_name.go | lock once | 12 |', '| store.go | return early | 3 |', '', 'See `internal/background/health_limit.go` and **bold text** with a [link](https://example.com).'].join('\n'), key: 'k1', timestamp: '2026-10-19T00:01:00Z' },
    { type: 'user', content: 'ok', key: 'k2', timestamp: '2026-10-19T00:02:00Z' },
    { type: 'assistant', text: 'Done.\n\n> a quoted line that is rather long and should wrap at phone width without any trouble at all\n\n1. first\n2. second\n3. third', key: 'k3', timestamp: '2026-10-19T00:03:00Z' }];
const BOARD = ['hello team', 'A long board message with a path /home/dev/work/project/internal/server/routes/some_long_file_name_handler_test.go and a link https://example.com/very/long/link/that/goes/on', FENCE + '\ncode block in a board message with a quite long line that overflows the width of the screen easily\n' + FENCE, '- a\n- b'];
const TASKS = ['Fix the lock taken twice in the update path of the store', 'Write the regression test and run the whole package'];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { settle } = require('./helpers/wait').bind(ev);
    const board = 'phone-light-' + Date.now();
    const problems = [], info = [];
    const S = `(window.__coralStatic || '/static/')`;
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name.replace(/[^a-z0-9-]+/gi, '-') + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    // one scene: geometry (the shared scan), then contrast of the text in it
    const scan = async (name, root) => {
        await ev(`window.__scanRoot = ${JSON.stringify(root || '')}; true`);
        const r = await ev(SCAN);
        if (r.missing) { problems.push(`${name}: scene not found`); return; }
        for (const [what, list, n] of [['wider than the screen', r.wide, r.nWide], ['clipped', r.clipped, r.nClipped], ['smaller than 44 px', r.small, r.nSmall]]) if (n) problems.push(`${name}: ${n} ${what}: ${list.slice(0, 4).join('; ')}`);
        if (r.docOver > 0 && !root) problems.push(`${name}: the page scrolls sideways by ${r.docOver} px`);
        const a = await auditRegion(ev, [root || 'body']);
        info.push(`${name}: ${a.checked} texts and borders checked, theme ${await ev(`document.documentElement.getAttribute('data-theme')`)}, scanned ${r.nWide + r.nSmall + r.nClipped} flagged`);
        for (const f of a.fails) problems.push(`${name}: contrast ${f.ratio} (needs ${f.need}) ${f.what} "${f.text}" ${f.sel} ${f.fg} on ${f.bg}`);
        await shot(name);
    };
    // the view picker names the current view: its name is whole (not cut to "Age...")
    const picker = async what => {
        const cut = await ev(`(() => { const l = document.getElementById('nav-select-label'); if (!l || !l.getClientRects().length) return null; return { text: l.textContent.trim(), cut: l.scrollWidth > l.clientWidth + 1, w: l.clientWidth, need: l.scrollWidth }; })()`);
        if (cut && cut.cut) problems.push(`view picker (${what}): the name is cut to fit: "${cut.text}" needs ${cut.need} px and has ${cut.w}`);
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const F = window.fetch; window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
            if (window.__hubStub && p === '/api/health') return Promise.resolve(new Response(JSON.stringify({ status: 'ok', hub: true, version: '1.4.69' }), { status: 200 }));
            if (window.__hubStub && p === '/api/servers' && m === 'GET') return Promise.resolve(new Response(JSON.stringify(window.__hubStub), { status: 200 }));
            return F(u, o); }; })();` });
        await Emulation.setDeviceMetricsOverride({ width: 320, height: 800, deviceScaleFactor: 1, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: 'light' }] });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await setTheme(ev, 'light'); await installAudit(ev);
        await ev(`(async () => { for (const c of ['hello team', 'a longer second message with some words']) await fetch('/api/board/${board}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'pl', as: 'QA', content: c }) });
            localStorage.setItem('coral-live-view-mode', 'chat'); const bot = window.__m.add('phone-bot-with-a-long-name', 'A', 8, { sleeping: false, board_project: '${board}' }); bot.msgs = ${JSON.stringify(CHAT)};
            for (const [i, c] of ${JSON.stringify(BOARD)}.entries()) await fetch('/api/board/${board}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'pl', as: 'Developer ' + i, content: c }) });
            for (const t of ${JSON.stringify(TASKS)}) await fetch('/api/board/${board}/tasks', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title: t, body: 'Detail of the task that is a bit longer than a title and has a path internal/server/routes/some_long_file_name.go', priority: 'high', created_by: 'pl' }) });
            window.__m.add('second-bot', 'B', 5, { sleeping: true }); return true; })()`);
        await settle(900);
        await scan('agents list'); await picker('agent list');
        await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await settle(1800);
        await scan('agent chat'); await picker('agent chat');
        await ev(`document.querySelector('.code-block') && document.querySelector('.code-block').scrollIntoView({ block: 'center' }); true`); await settle(500); await scan('agent chat, code block');
        await ev(`window.setLiveViewMode('terminal'); true`); await settle(1500); await scan('agent terminal'); await picker('agent terminal'); await ev(`window.setLiveViewMode('chat'); true`); await settle(500);
        for (const tab of ['history', 'workflows', 'jobs', 'tokens', 'docs', 'board', 'hub-inbox']) { await ev(`window.switchNavTab('${tab}'); true`).catch(() => {}); await settle(1500); await scan('tab ' + tab); await picker('tab ' + tab); }
        await ev(`window.switchNavTab('agents'); true`); await settle(500);
        await ev(`window.switchNavTab('agents'); true`); await settle(500);
        // the side panel: Tasks, Board, Files, Notes
        await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await settle(1200);
        await ev(`(document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); true`); await settle(800);
        for (const tab of ['tasks', 'board', 'files', 'notes']) { await ev(`window.switchAgenticTab('${tab}', 'top'); true`); await settle(900); await scan('side panel ' + tab, '#agentic-state'); }
        await ev(`(document.getElementById('agentic-mobile-close-btn') || document.createElement('i')).click(); true`); await settle(500);
        // Settings, every tab
        await ev(`(async () => { const m = await import(${S} + 'modals.js'); await m.showSettingsModal(); return true; })()`); await settle(1500);
        const groups = await ev(`[...document.querySelectorAll('#settings-group-select option')].map(o => o.value)`);
        for (const g of groups) { await ev(`(() => { const s = document.getElementById('settings-group-select'); s.value = ${JSON.stringify(g)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); await settle(500); await scan('settings ' + g, '#settings-modal'); }
        await ev(`window.hideSettingsModal(); true`);
        // every dialog
        const ids = await ev(`[...document.querySelectorAll('.modal[role=dialog]')].map(m => m.id).filter(id => id !== 'settings-modal')`);
        for (const id of ids) {
            await ev(`document.getElementById(${JSON.stringify(id)}).style.display = 'flex'; true`); await settle(150);
            await scan('dialog ' + id, '#' + id);
            await ev(`document.getElementById(${JSON.stringify(id)}).style.display = 'none'; true`);
        }
        // the Launch dialog, step 1 and 2
        await ev(`document.querySelector('[data-on-click="launch-show"]').click(); true`); await settle(1000);
        await scan('launch dialog step 1', '#launch-modal');
        await ev(`{ const c = [...document.querySelectorAll('.launch-type-card, .launch-type-label')].find(e => /New agent/.test(e.textContent)); (c.closest('[data-on-click]') || c).click(); } true`); await settle(1500);
        await scan('launch dialog step 2', '#launch-modal');
        await ev(`document.getElementById('launch-modal').style.display = 'none'; true`);
        // the Servers tab with a hub fixture
        const now = new Date().toISOString();
        const SERVERS = [{ id: 'local', label: 'studio-mac.local', local: true, status: 'online' }, { id: 'm1', label: 'workstation.lan', url: 'https://workstation.lan:8420', status: 'online', version: '1.4.69', last_seen: now },
            { id: 'm2', label: 'Old Laptop with a rather long display name', url: 'http://192.168.1.50:8420', status: 'unreachable', last_seen: now, last_error: 'dial tcp 192.168.1.50:8420: i/o timeout' },
            { id: 'm3', label: 'epoch', url: 'https://epoch.example.net:8420', status: 'unauthorized', last_seen: now, last_error: 'the server rejected the API key' }];
        await ev(`window.__hubStub = ${JSON.stringify(SERVERS)}; (async () => { const s = await import(${S} + 'state.js'); s.state.hub = true; s.state.servers = ${JSON.stringify(SERVERS)}; const t = await import(${S} + 'servers_tab.js'); t.updateServersTabVisibility(); return true; })()`);
        await ev(`window.switchNavTab('servers'); true`); await waitFor(`!!document.querySelector('#server-row-m2')`, 'the servers list', 8000); await settle(500);
        await scan('servers tab', '#servers-view');
        await ev(`document.querySelector('[data-action=add]').click(); true`); await settle(600);
        await scan('servers tab, Add form', '#servers-view');
        await ev(`window.switchNavTab('agents'); true`); await settle(400);
        // the artifact viewer with 500 comments, the sheet shut and open
        const HEX = 'e'.repeat(64);
        const MD = ['# Migration plan', '', '## Steps', '', '1. Add the new columns behind a flag and backfill in batches of 500 rows.', '2. Switch reads to the new columns.', '', '## Risks', '', 'The backfill takes a write lock on large databases, so it must run off-peak.'].join('\n');
        const comments = [
            { id: 1, parent_id: null, anchor: { quote: 'backfill in batches of 500 rows.', prefix: '', suffix: '' }, body: 'Batches of 500 is too small. Try 5,000.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 3600000).toISOString() },
            { id: 2, parent_id: 1, anchor: null, body: 'Agreed. Last run took 9 s per 5,000.', author: { kind: 'hub', issuer: 'coral-hub', subject: 'hub-user-3f2a', name: 'Reviewer' }, created_at: new Date(Date.now() - 60000).toISOString() },
            { id: 3, parent_id: null, anchor: { quote: 'takes a write lock on large databases', prefix: '', suffix: '' }, body: 'Define off-peak: 02:00 to 05:00 UTC?', author: { kind: 'hub', issuer: 'coral-hub', subject: 'hub-user-3f2a', name: 'Reviewer' }, created_at: new Date(Date.now() - 7200000).toISOString() },
            { id: 4, parent_id: null, anchor: { quote: 'Switch reads to the new columns.' }, body: 'Fine.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 86400000).toISOString(), resolved_at: new Date().toISOString() },
        ];
        await ev(`(() => { const real = window.fetch; const cs = ${JSON.stringify(comments)}; const json = (o, st) => Promise.resolve(new Response(JSON.stringify(o), { status: st || 200, headers: { 'Content-Type': 'application/json' } }));
            window.fetch = (u, o) => { const url = String(u), m = (o && o.method) || 'GET';
                if (url.includes('/api/artifacts/${HEX}/comments')) return m === 'GET' ? json({ comments: cs.map(c => ({ ...c, deleted: false, mine: c.author.kind === 'owner', can: { reply: c.parent_id == null, edit: c.author.kind === 'owner', delete: c.author.kind === 'owner', resolve: c.parent_id == null } })), locked: false, you: { kind: 'owner', name: 'Operator', can_comment: true, reason: '', can_lock: true }, limits: { body: 8192, quote: 2048, comments: 500 } }) : json({ error: 'nope' }, 404);
                if (url.includes('/api/artifacts/${HEX}')) return Promise.resolve(new Response(${JSON.stringify(MD)}, { status: 200, headers: { 'Content-Type': 'text/markdown' } }));
                return real(u, o); }; return true; })()`);
        await ev(`import(${S} + 'artifact_viewer.js').then(v => v.openArtifactViewer(${JSON.stringify(HEX)}, { filename: 'plan.md' })); true`);
        await waitFor(`document.querySelectorAll('.ac-card').length > 0`, 'the comments', 20000); await settle(600);
        await scan('artifact viewer', '#artifact-viewer');
        await ev(`document.querySelector('.ac-toggle').click(); true`); await settle(700);
        await scan('artifact viewer, comments sheet', '#artifact-viewer');
        if (process.env.SWEEP_REPORT) { fs.writeFileSync(process.env.SWEEP_REPORT, problems.join('\n') + '\n'); fs.writeFileSync(process.env.SWEEP_REPORT + '.info', info.join('\n') + '\n'); console.log(problems.length + ' problems written'); }
        else assert.deepEqual(problems, [], 'phone light sweep problems:\n  ' + problems.join('\n  '));
        console.log('\nPASS phone light sweep: 320 px, light theme, every view and dialog');
    } finally { await removeBoards(ev, [board]); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
