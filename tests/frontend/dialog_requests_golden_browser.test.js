// The requests the Restart, New Agent and Agent Team dialogs send (#4950): the dialogs are rebuilt as a section list with the fields in sections, and Restart, Launch and Launch Team must send
// exactly the bodies they sent before. This drives each dialog the way a person does (the fields by their classes and ids, the real Restart / Launch / Launch Team buttons), answers every
// request itself, and compares the bodies with fixtures/dialog_requests_golden.json (recorded on the old, one-form dialogs). CORAL_UPDATE_GOLDEN=1 rewrites it.
// Fields are set through their elements, whichever section holds them: reaching them by clicking through the sections is in dialog_sections_browser.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'dialog_requests_golden.json');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const out = {};
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            window.__reqs = [];
            const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href), m = o.method || 'GET';
                if (m === 'POST' && /^\\/api\\/sessions\\/(launch|launch-team|live\\/[^/]+\\/restart)$/.test(p.pathname)) {
                    window.__reqs.push({ path: p.pathname, body: JSON.parse(o.body) });
                    if (p.pathname === '/api/sessions/launch-team') return json({ agents: [{ name: 'A', session_id: 's' }] });
                    if (p.pathname === '/api/sessions/launch') return json({ session_name: 'launched-1', session_id: 'sid-1' });
                    return json({ session_id: 'sid-r2', session_name: 'ag-r' });
                }
                if (p.pathname === '/api/board/projects') return json([{ project: 'taken' }]);
                if (p.pathname === '/api/filesystem/is-git') return json({ is_git: true });
                return real(u, o); };
            await window.showLaunchModal(); window.hideLaunchModal();
            return true; })()`);
        const last = () => ev(`window.__reqs.length`);
        const take = async (n0, what) => { await waitFor(`window.__reqs.length > ${n0}`, what, 8000); return ev(`window.__reqs[${n0}]`); };
        // Set a field of an agent config form: type or select (value + input + change), a chip (click), a pattern box.
        const set = (acf, cls, v) => ev(`(() => { const e = document.querySelector('#${acf} ${cls}'); if (!e) throw new Error('no ${cls} in ${acf}'); e.value = ${JSON.stringify(v)}; e.dispatchEvent(new Event('input', { bubbles: true })); e.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        const chip = (acf, type, cap) => ev(`(() => { const e = document.querySelector('#${acf} [data-type="${type}"][data-cap="${cap}"]'); if (!e) throw new Error('no chip'); e.click(); return true; })()`);
        const click = sel => ev(`(() => { const e = [...document.querySelectorAll(${JSON.stringify(sel)})].find(x => x.getClientRects().length) || document.querySelector(${JSON.stringify(sel)}); if (!e) throw new Error('no ' + ${JSON.stringify(sel)}); e.click(); return true; })()`);
        const fill = (id, v) => ev(`(() => { const e = document.getElementById(${JSON.stringify(id)}); e.value = ${JSON.stringify(v)}; e.dispatchEvent(new Event('input', { bubbles: true })); e.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        const acfOf = id => ev(`window.getAgentConfig(${JSON.stringify(id)})`);

        // ── Restart ──────────────────────────────────────────────────────────
        const session = { name: 'ag', display_name: 'Agent', session_id: 'sid-r', type: 'live', agent_type: 'claude', model: 'claude-opus-4', prompt: 'Be kind.', capabilities: { allow: ['file_read'], deny: [] }, tmux_session: 'ag' };
        const openRestart = async () => {
            await ev(`T.state.currentSession = ${JSON.stringify(session)}; window.restartSession(); true`);
            await waitFor(`getComputedStyle(document.getElementById('restart-modal')).display !== 'none' && !!document.querySelector('#restart-acf .acf-agent-type')`, 'the restart dialog');
        };
        console.log('Restart: unchanged, then everything changed');
        let n = await last();
        await openRestart();
        out.restart_prefilled = await acfOf('restart-acf');
        await click('#restart-modal [data-on-click="m-confirm-restart"]');
        out.restart_unchanged = (await take(n, 'the restart request')).body;
        n = await last();
        await openRestart();
        await set('restart-acf', '.acf-agent-type', 'codex');
        await set('restart-acf', '.acf-model', 'gpt-x');
        await set('restart-acf', '.acf-prompt', 'A new prompt.\nSecond line.');
        await set('restart-acf', '.acf-permission-mode', 'plan');
        await set('restart-acf', '.acf-flags', '--verbose --foo bar');
        await chip('restart-acf', 'allow', 'shell'); await chip('restart-acf', 'allow', 'web_access'); await chip('restart-acf', 'deny', 'git_write');
        await set('restart-acf', '.perms-shell-input', 'git *, npm *');
        out.restart_changed_config = await acfOf('restart-acf');
        await click('#restart-modal [data-on-click="m-confirm-restart"]');
        out.restart_changed = (await take(n, 'the restart request')).body;
        // Deny entries are kept for Claude (Codex has no deny list, so its chips are off).
        n = await last();
        await openRestart();
        await set('restart-acf', '.acf-permission-mode', 'acceptEdits');
        await chip('restart-acf', 'deny', 'git_write'); await chip('restart-acf', 'deny', 'web_access'); await chip('restart-acf', 'allow', 'shell');
        await click('#restart-modal [data-on-click="m-confirm-restart"]');
        out.restart_claude_deny = (await take(n, 'the restart request')).body;

        // ── New Agent ────────────────────────────────────────────────────────
        const openAgent = async () => {
            await ev(`window.showLaunchModal(); window._selectLaunchType('agent'); true`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none' && !!document.querySelector('#launch-agent-acf .acf-agent-type') && document.getElementById('launch-board-select').options.length > 2`, 'the agent step');
        };
        console.log('New Agent: just a folder, a full setup, an existing board');
        n = await last();
        await openAgent();
        await fill('launch-dir', '/work/repo');
        out.agent_default_config = await acfOf('launch-agent-acf');
        await click('#launch-step-agent [data-on-click="m-launch-session"]');
        out.agent_minimal = (await take(n, 'the launch request')).body;
        n = await last();
        await openAgent();
        await fill('launch-dir', '/work/other');
        await fill('launch-agent-name-input', 'QA Engineer');
        await set('launch-agent-acf', '.acf-agent-type', 'codex');
        await set('launch-agent-acf', '.acf-model', 'gpt-x');
        await set('launch-agent-acf', '.acf-prompt', 'Test everything.\nTwice.');
        await set('launch-agent-acf', '.acf-permission-mode', 'acceptEdits');
        await set('launch-agent-acf', '.acf-flags', '--verbose');
        await chip('launch-agent-acf', 'allow', 'shell'); await chip('launch-agent-acf', 'deny', 'web_access');
        await set('launch-agent-acf', '.perms-shell-input', 'git *');
        await fill('launch-board-select', '__new__');
        await fill('launch-board-name', 'my-board');
        await fill('launch-board-server', 'http://remote:8420');
        out.agent_full_config = await acfOf('launch-agent-acf');
        await click('#launch-step-agent [data-on-click="m-launch-session"]');
        out.agent_full = (await take(n, 'the launch request')).body;
        n = await last();
        await openAgent();
        await fill('launch-dir', '/work/repo');
        await fill('launch-board-select', 'taken');
        await click('#launch-step-agent [data-on-click="m-launch-session"]');
        out.agent_existing_board = (await take(n, 'the launch request')).body;

        // ── Agent Team ───────────────────────────────────────────────────────
        const openTeam = async () => {
            await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-team')).display !== 'none'`, 'the team step');
        };
        const addAgent = async cfg => {
            await ev(`window.showTeamAgentModal(); true`);
            await waitFor(`getComputedStyle(document.getElementById('team-agent-modal')).display !== 'none' && !!document.querySelector('#team-agent-modal-acf .acf-name')`, 'the team agent dialog');
            await set('team-agent-modal-acf', '.acf-name', cfg.name);
            if (cfg.type) await set('team-agent-modal-acf', '.acf-agent-type', cfg.type);
            if (cfg.model) await set('team-agent-modal-acf', '.acf-model', cfg.model);
            await set('team-agent-modal-acf', '.acf-prompt', cfg.prompt);
            if (cfg.mode) await set('team-agent-modal-acf', '.acf-permission-mode', cfg.mode);
            if (cfg.flags) await set('team-agent-modal-acf', '.acf-flags', cfg.flags);
            for (const [t, c] of cfg.chips || []) await chip('team-agent-modal-acf', t, c);
            await click('#team-agent-modal [data-on-click="m-save-team-agent-from-modal"]');
            await waitFor(`getComputedStyle(document.getElementById('team-agent-modal')).display === 'none'`, 'the team agent dialog to close');
        };
        console.log('Agent Team: two agents added through the dialog, one edited, a remote server and a worktree');
        n = await last();
        await openTeam();
        await ev(`document.getElementById('team-agents-list').innerHTML = ''; true`);
        await fill('team-board-name', 'alpha-team');
        await fill('launch-team-dir', '/work/repo');
        await waitFor(`getComputedStyle(document.getElementById('team-worktree-option')).display !== 'none'`, 'the worktree option');
        await ev(`{ const c = document.getElementById('launch-team-worktree'); c.checked = true; c.dispatchEvent(new Event('change', { bubbles: true })); } true`);
        await fill('launch-team-base-branch', 'develop');
        await fill('team-board-server', 'http://remote:8420');
        await fill('team-agent-type', 'codex');
        await fill('team-permission-mode', 'plan');
        await addAgent({ name: 'Lead', prompt: 'Lead the team.', type: 'claude', model: 'claude-opus-4', mode: 'bypassPermissions', flags: '--verbose', chips: [['allow', 'shell'], ['deny', 'web_access']] });
        await addAgent({ name: 'Dev', prompt: 'Write the code.\nCarefully.', type: 'codex', model: 'gpt-x', chips: [['deny', 'git_write']] });
        // edit the second: the dialog opens with its values and saves them back
        await ev(`window.showTeamAgentModal([...document.querySelectorAll('#team-agents-list .team-agent-row')].find(r => r.querySelector('.team-agent-role-name').textContent === 'Dev')); true`);
        await waitFor(`getComputedStyle(document.getElementById('team-agent-modal')).display !== 'none' && document.querySelector('#team-agent-modal-acf .acf-name').value === 'Dev'`, 'the edit dialog with its values');
        out.team_edit_prefilled = await acfOf('team-agent-modal-acf');
        await set('team-agent-modal-acf', '.acf-prompt', 'Write the code, edited.');
        await click('#team-agent-modal [data-on-click="m-save-team-agent-from-modal"]');
        await waitFor(`getComputedStyle(document.getElementById('team-agent-modal')).display === 'none'`, 'the edit dialog to close');
        await click('#launch-step-team [data-on-click="m-launch-team"]');
        out.team_full = (await take(n, 'the team request')).body;

        for (const k of Object.keys(out)) out[k] = JSON.parse(JSON.stringify(out[k]));
        if (process.env.CORAL_UPDATE_GOLDEN) { fs.writeFileSync(GOLDEN, JSON.stringify(out, null, 2) + '\n'); console.log('golden written'); }
        else assert.deepEqual(out, JSON.parse(fs.readFileSync(GOLDEN, 'utf8')), 'the dialogs send what they sent before');
        console.log('PASS dialog request bodies');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
