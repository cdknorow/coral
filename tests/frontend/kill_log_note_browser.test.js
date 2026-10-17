// #4399 (from Security's #4371 table): every Kill confirmation says that Kill deletes the agent's terminal log on the tmux backend, which search reads,
// and that Sleep or Archive keeps it: Kill Session (the agent header and the sidebar row), Kill All Agents (a folder group), Kill Team and Reset Team (which
// kills and restarts its agents). Cancel sends no kill. The code facts: tmux Client.KillSession removes the log, KillSessionOnly (Sleep, Archive) does not.
// KILL_NOTE_SHOT=<dir> saves a screenshot of each confirmation. Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const NOTE = "On the tmux terminal backend this also deletes the agent's terminal log, which search uses. Sleep or Archive keeps it.";

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install, reqs, clearReqs } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat');
            T.state.liveSessions = []; window.__m.agents = {};
            window.__m.add('repo', 'K', 3, { sleeping: false, session_id: 's1' });
            window.__m.add('repo', 'K', 3, { sleeping: false, session_id: 's2' });
            window.__m.add('team-agent', 'K', 3, { sleeping: false, session_id: 't1', board_project: 'team-k' });
            T.render.renderLiveSessions(T.state.liveSessions); true`);
        const open = `getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`;
        const asked = async (label, start, title) => {
            await clearReqs();
            await ev(`(async () => { ${start}; return true; })()`);
            await waitFor(open, `the ${label} confirmation`, 8000);
            const d = await ev(`({ title: document.getElementById('confirm-modal-title').textContent, message: document.getElementById('confirm-modal-message').textContent })`);
            if (process.env.KILL_NOTE_SHOT) require('node:fs').writeFileSync(`${process.env.KILL_NOTE_SHOT}/${title.replace(/ /g, '-').toLowerCase()}-${label.includes('header') ? 'header' : 'x'}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
            assert.equal(d.title, title, `${label}: title`);
            assert.ok(d.message.endsWith(NOTE), `${label}: the message ends with the log note: ${d.message}`);
            assert.equal(d.message.split(NOTE).length, 2, `${label}: the note is said once`);
            await ev(`document.querySelector('#confirm-modal .btn:not(#confirm-modal-yes)').click(); true`);
            await waitFor(`!(${open})`, `the ${label} confirmation to close`, 4000);
            assert.equal((await reqs()).filter(r => r.kind === 'kill').length, 0, `${label}: Cancel sends no kill`);
            console.log(`   ✓ ${label}: ${d.message}`);
        };
        console.log('Every Kill confirmation says what happens to the terminal log');
        await asked('Kill Session (sidebar row)', `T.render.killSessionDirect('repo', 'claude', 's1')`, 'Kill Session');
        await asked('Kill Session (agent header)', `T.state.currentSession = { type: 'live', name: 'repo', session_id: 's1', agent_type: 'claude' }; (await import((window.__coralStatic || '/static/') + 'controls.js')).killSession()`, 'Kill Session');
        await asked('Kill All Agents (folder)', `T.render.killGroup('repo')`, 'Kill All Agents');
        await asked('Kill Team', `T.render.killBoard('team-k')`, 'Kill Team');
        await asked('Reset Team', `window.resetTeam('team-k')`, 'Reset Team');
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
