// A folder group's Kill All kills exactly the agents that group shows (#3053):
// with Group by Team on, a folder group lists only standalone agents, so team
// members, workflow agents and archived agents that share the folder are never
// killed; with it off, the folder group nests the folder's agents, with team
// members in a nested team section that has its own Kill Team, so a folder's
// Kill All still leaves them alone. Another server's folder of the same name is
// a different group. A workflow group kills its workflow's agents, and an active
// search narrows Kill All to the agents the search shows.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, sleep, PRELOAD, install, reqs, clearReqs } = await connect();

    // Every agent is in a folder named 'repo' except o1; ids are session ids.
    const AGENTS = `[
        ['s1', { sleeping: false }],
        ['s2', { sleeping: true }],
        ['t1', { sleeping: false, board_project: 'team-k' }],
        ['w1', { sleeping: false, workflow_name: 'wf-a' }],
        ['a1', { archived: true }],
        ['r1', { sleeping: false, server: 'qa-remote-1' }],
        ['o1', { sleeping: false }, 'other'],
    ]`;
    const seed = () => ev(`(() => {
        T.state.liveSessions = []; window.__m.agents = {};
        for (const [id, opts, name] of ${AGENTS}) window.__m.add(name || 'repo', 'K', 5, { ...opts, session_id: id });
        T.render.renderLiveSessions(T.state.liveSessions);
        return true;
    })()`);
    // Clicks the group's Kill All and confirms; returns the confirm text and the killed session ids.
    const killAll = async group => {
        await clearReqs();
        const found = await ev(`(() => {
            const b = [...document.querySelectorAll('#live-sessions-list .overflow-menu-item')].find(x => x.getAttribute('onclick')?.includes("killGroup('${group}')"));
            if (!b) return false;
            b.click();
            return true;
        })()`);
        assert.ok(found, `Kill All for group ${group} is in its menu`);
        await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'confirm');
        const message = await ev(`document.getElementById('confirm-modal-message').textContent`);
        await ev(`document.getElementById('confirm-modal-yes').click()`);
        await sleep(300);
        const kills = (await reqs()).filter(r => r.kind === 'kill').map(r => ({ sid: JSON.parse(r.body).session_id, remote: r.remote }));
        return { message, kills };
    };
    const remaining = () => ev(`T.state.liveSessions.map(s => s.session_id).sort()`);

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat'); true`);

        console.log('1. Group by Team on: the folder group kills its standalone agents only');
        await seed();
        let r = await killAll('repo');
        assert.deepEqual(r.kills.map(k => k.sid).sort(), ['s1', 's2'], `killed exactly the standalone agents shown in 'repo' (got ${JSON.stringify(r.kills)})`);
        assert.match(r.message, /Kill all 2 agent/, `the confirm counts what will be killed: ${r.message}`);
        assert.deepEqual(await remaining(), ['a1', 'o1', 'r1', 't1', 'w1'], 'team, workflow, archived, remote and other-folder agents are untouched');
        assert.ok(await ev(`!!document.querySelector('#live-sessions-list li[data-session-id="t1"]')`), 'the team agent is still listed');
        console.log('   ✓ killed s1, s2; kept t1 (team), w1 (workflow), a1 (archived), r1 (remote), o1');

        console.log('2. Another server\'s folder of the same name is its own group');
        await seed();
        r = await killAll('@qa-remote-1/repo');
        assert.deepEqual(r.kills, [{ sid: 'r1', remote: 'qa-remote-1' }], `only the remote agent, through /api/remote (got ${JSON.stringify(r.kills)})`);
        assert.match(r.message, /Kill all 1 agent/);
        console.log('   ✓ remote repo: killed r1 only, via /api/remote/qa-remote-1');

        console.log('3. A workflow group kills its workflow\'s agents');
        await seed();
        r = await killAll('wf:wf-a');
        assert.deepEqual(r.kills.map(k => k.sid), ['w1'], `workflow group: w1 only (got ${JSON.stringify(r.kills)})`);
        console.log('   ✓ wf-a: killed w1 only');

        console.log('4. An active search narrows Kill All to what the search shows');
        await seed();
        await ev(`(() => { const row = T.state.liveSessions.find(s => s.session_id === 's1'); row.display_name = 'zeta-runner'; window.filterLiveAgents('zeta'); return true; })()`);
        const shownIds = await ev(`[...document.querySelectorAll('#live-sessions-list li[data-session-id]')].map(li => li.dataset.sessionId)`);
        r = await killAll('repo');
        assert.deepEqual(r.kills.map(k => k.sid).filter(id => !shownIds.includes(id)), [], `nothing hidden by the search is killed (shown ${JSON.stringify(shownIds)}, killed ${JSON.stringify(r.kills)})`);
        assert.deepEqual(r.kills.map(k => k.sid), ['s1'], `killed the matching standalone agent only (got ${JSON.stringify(r.kills)})`);
        await ev(`window.filterLiveAgents(''); true`);
        console.log('   ✓ search "zeta": killed s1 only');

        console.log('5. Group by Team off: team members sit in a nested team section; folder Kill All leaves them');
        await ev(`localStorage.setItem('coral-group-by-team', 'false'); true`);
        await seed();
        const nested = await ev(`(() => ({
            shown: !!document.querySelector('#live-sessions-list li[data-session-id="t1"]'),
            killTeam: !!document.querySelector('#live-sessions-list [onclick*="killBoard("]'),
        }))()`);
        assert.ok(nested.shown, 'the team member is listed in folder mode');
        assert.ok(nested.killTeam, 'its team section offers Kill Team');
        r = await killAll('repo');
        assert.deepEqual(r.kills.map(k => k.sid).sort(), ['s1', 's2', 'w1'], `folder Kill All: standalone and workflow agents in the folder (got ${JSON.stringify(r.kills)})`);
        assert.match(r.message, /Kill all 3 agent/, `the confirm counts what will be killed: ${r.message}`);
        assert.deepEqual(await remaining(), ['a1', 'o1', 'r1', 't1'], 'team member, archived, remote and other-folder agents are untouched');
        console.log('   ✓ killed s1, s2, w1; kept t1 (team, has Kill Team), a1, r1, o1');

        console.log('\nAll kill-group checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
