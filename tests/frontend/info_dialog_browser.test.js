// The Info dialog (#4684): showInfoModal fills the agent's session info from its info route and opens the dialog, with the prompt and the message board shown only when there are
// some, the board as a link that closes the dialog and opens the board; a refusal or a failure says so in a toast and opens nothing; hideInfoModal closes it and copyInfoCommand copies the
// tmux command. It was a block of modals.js and is its own module now; this pins what it does. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            window.__m_ = await import(st + 'modals.js'); window.__state_ = (await import(st + 'state.js')).state;
            window.__answer = { body: {}, status: 200 }; window.__reqs = []; window.__copied = []; window.__selected = [];
            const real = window.fetch;
            window.fetch = (u, o) => { const url = String(u); if (/\\/api\\/sessions\\/live\\/[^/]+\\/info/.test(url)) { window.__reqs.push(url); return Promise.resolve(new Response(JSON.stringify(window.__answer.body), { status: window.__answer.status, headers: { 'content-type': 'application/json' } })); } return real(u, o); };
            Object.defineProperty(navigator, 'clipboard', { value: { writeText: t => { window.__copied.push(t); return Promise.resolve(); } }, configurable: true });
            window.selectBoardProject = key => window.__selected.push(key);
            return true; })()`);
        const text = id => ev(`document.getElementById(${JSON.stringify(id)}).textContent`);
        const shown = id => ev(`document.getElementById(${JSON.stringify(id)}).style.display`);
        const toasts = () => ev(`[...document.querySelectorAll('.toast .toast-message')].map(t => t.textContent)`);
        const open = () => ev(`document.getElementById('info-modal').style.display`);
        const run = () => ev(`window.__m_.showInfoModal().then(() => true)`);

        console.log('No live session: a toast, nothing opens');
        await ev(`window.__state_.currentSession = null; true`);
        await run();
        assert.deepEqual(await toasts(), ['No live session selected']); assert.notEqual(await open(), 'flex');

        console.log('A live agent on a board, with a prompt: every field, the board link');
        await ev(`window.__state_.currentSession = { type: 'live', name: 'info a', agent_type: 'claude', session_id: 'sid-1', server: 'local' };
            window.__answer.body = { agent_name: 'info a', agent_type: 'claude', tmux_session_name: 'tm-1', tmux_command: 'tmux attach -t tm-1', working_directory: '/work/x', log_path: '/logs/x.log', pane_title: 'pane', git_repo: 'repo', git_branch: 'main', git_commit_hash: '0123456789abcdef', git_commit_subject: 'Fix it', prompt: 'You are a helper', board_name: 'team-a', board_label: 'Team A' }; true`);
        await run();
        assert.equal(await open(), 'flex');
        assert.match(await ev(`window.__reqs[0]`), /\/api\/sessions\/live\/info%20a\/info\?agent_type=claude&session_id=sid-1$/);
        assert.deepEqual(await Promise.all(['info-agent-name', 'info-agent-type', 'info-tmux-session', 'info-tmux-command', 'info-working-dir', 'info-log-path', 'info-pane-title', 'info-git-branch', 'info-git-commit', 'info-prompt'].map(text)),
            ['info a', 'claude', 'tm-1', 'tmux attach -t tm-1', '/work/x', '/logs/x.log', 'pane', 'repo : main', '01234567 Fix it', 'You are a helper']);
        assert.equal(await shown('info-prompt-label'), ''); assert.equal(await shown('info-board'), '');
        assert.equal(await ev(`document.querySelector('#info-board a').textContent`), 'Team A');
        await ev(`document.querySelector('#info-board a').click(); true`);
        assert.equal(await open(), 'none', 'the board link closes the dialog'); assert.deepEqual(await ev(`window.__selected`), ['team-a']);

        console.log('A bare agent: no prompt, no board, dashes for git');
        await ev(`window.__answer.body = { agent_name: 'info a', agent_type: 'claude', tmux_command: 'tmux attach -t tm-1', git_branch: 'dev' }; true`);
        await run();
        assert.equal(await shown('info-prompt-label'), 'none'); assert.equal(await shown('info-prompt'), 'none'); assert.equal(await shown('info-board-label'), 'none'); assert.equal(await shown('info-board'), 'none');
        assert.equal(await text('info-git-branch'), 'dev'); assert.equal(await text('info-git-commit'), '—');
        await ev(`window.__answer.body = { agent_name: 'x' }; true`); await run();
        assert.equal(await text('info-git-branch'), '—');

        console.log('Copy and close');
        await ev(`window.__answer.body = { agent_name: 'info a', tmux_command: 'tmux attach -t zz' }; true`); await run();
        await ev(`window.__m_.copyInfoCommand(); true`);
        await waitFor(`window.__copied.length === 1`, 'the copy');
        assert.deepEqual(await ev(`window.__copied`), ['tmux attach -t zz']);
        assert.ok((await toasts()).includes('Copied to clipboard'));
        await ev(`window.__m_.hideInfoModal(); true`); assert.equal(await open(), 'none');

        console.log('A refusal and a failure: a toast, the dialog stays closed');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); window.__answer.body = { error: 'No such agent' }; true`); await run();
        assert.deepEqual(await toasts(), ['No such agent']); assert.equal(await open(), 'none');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); window.fetch = () => Promise.reject(new Error('down')); true`); await run();
        assert.deepEqual(await toasts(), ['Failed to load session info']); assert.equal(await open(), 'none');
        console.log('PASS info dialog');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
