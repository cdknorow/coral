// Killing an agent removes its row at once (#4946): no crossed-out row waits to be dismissed, there is no Dismiss
// item in an agent's or a team's menu, and Kill All on a team takes every member. The kill is the only request: nothing
// of the agent's history is deleted. Driven through the real menus and the confirm dialog, with the kill endpoint and the
// live list answered in the page. Run against an isolated dev server; set KILL_SHOT_DIR to save screenshots.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const S = (id, over) => ({ session_id: 'sid-' + id, name: 'agent-' + id, display_name: 'Agent ' + id, agent_type: 'claude', working_directory: '/repo/' + id, status: 'idle', ...over });
const SESSIONS = [
    S(1, { working_directory: '/repo/solo', display_name: 'Solo' }),
    S(2, { working_directory: '/repo/other', display_name: 'Other' }),
    S(3, { board_project: 'crew', working_directory: '/repo/crew', display_name: 'Crew lead' }),
    S(4, { board_project: 'crew', working_directory: '/repo/crew', display_name: 'Crew two' }),
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const MOD = `(window.__coralStatic || '/static/')`;
    const open = async (width, height, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        // The page's own live list and kill endpoint are answered here; every other request is recorded and passes.
        await ev(`(async () => {
            const render = await import(${MOD} + 'render.js'); const { state } = await import(${MOD} + 'state.js');
            window.__served = ${JSON.stringify(SESSIONS)}; window.__reqs = [];
            const real = window.fetch.bind(window);
            window.fetch = (u, o = {}) => {
                const url = String(u), method = (o.method || 'GET').toUpperCase();
                window.__reqs.push(method + ' ' + url);
                if (method === 'POST' && /\\/api\\/sessions\\/live\\/[^/]+\\/kill$/.test(url)) {
                    const sid = JSON.parse(o.body).session_id; window.__served = window.__served.filter(s => s.session_id !== sid);
                    return Promise.resolve(new Response('{"ok":true}', { status: 200, headers: { 'content-type': 'application/json' } }));
                }
                if (method === 'GET' && /^\\/api\\/sessions\\/live(\\?|$)/.test(url)) return Promise.resolve(new Response(JSON.stringify(window.__served), { status: 200, headers: { 'content-type': 'application/json' } }));
                return real(u, o);
            };
            state.settings = {}; state.liveSessions = JSON.parse(JSON.stringify(window.__served));
            render.flushLiveSessionsRender(); render.renderLiveSessions(state.liveSessions); return true; })()`);
    };
    const shot = async name => {
        if (!process.env.KILL_SHOT_DIR) return;
        const { data } = await Page.captureScreenshot({ format: 'png' });
        require('node:fs').writeFileSync(`${process.env.KILL_SHOT_DIR}/${name}.png`, Buffer.from(data, 'base64'));
    };
    const rows = () => ev(`[...document.querySelectorAll('.session-group-item[data-session-id]')].filter(r => r.offsetParent !== null || true).map(r => r.dataset.sessionId).sort()`);
    const redraw = () => ev(`import(${MOD} + 'render.js').then(async r => { const { state } = await import(${MOD} + 'state.js'); r.renderLiveSessions(state.liveSessions); return true; })`);
    const confirm = () => ev(`(() => { document.getElementById('confirm-modal-yes').click(); return true; })()`);
    const killReqs = () => ev(`window.__reqs.filter(r => /\\/kill$/.test(r))`);
    const noKilledUi = async w => {
        const left = await ev(`({ done: document.querySelectorAll('.session-done').length, dismiss: [...document.querySelectorAll('[data-on-click="agent-dismiss"], [data-on-click="team-dismiss"]')].length,
            text: /Dismiss/.test(document.getElementById('live-sessions-list')?.innerHTML || '') })`);
        assert.deepEqual(left, { done: 0, dismiss: 0, text: false }, `${w}: no crossed-out row and no Dismiss control`);
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            await open(w, h, mobile);
            assert.deepEqual(await rows(), ['sid-1', 'sid-2', 'sid-3', 'sid-4'], `${w}: all four agents listed`);

            await shot(`before-${w}`);
            // One agent: its menu's Kill Session, then the dialog's confirm.
            await ev(`document.querySelector('.session-group-item[data-session-id="sid-1"] .sidebar-kebab-btn').click()`);
            await waitFor(`!!document.querySelector('.session-group-item[data-session-id="sid-1"] [data-on-click="agent-kill"]')`, `${w}: the agent's menu`);
            assert.equal(await ev(`!!document.querySelector('.session-group-item[data-session-id="sid-1"] [data-on-click="agent-dismiss"], .session-group-item[data-session-id="sid-1"] [data-on-click="agent-history"]')`), false, `${w}: the menu has no Dismiss or History item`);
            await ev(`document.querySelector('.session-group-item[data-session-id="sid-1"] [data-on-click="agent-kill"]').click()`);
            await confirm();
            await waitFor(`!document.querySelector('.session-group-item[data-session-id="sid-1"]')`, `${w}: the killed agent's row to go`);
            assert.deepEqual(await rows(), ['sid-2', 'sid-3', 'sid-4'], `${w}: the others stay`);
            await noKilledUi(w);
            await shot(`one-killed-${w}`);
            // A later redraw (the live list refreshes) does not bring it back.
            await redraw();
            assert.deepEqual(await rows(), ['sid-2', 'sid-3', 'sid-4'], `${w}: still gone after a redraw`);

            // A team: Kill All takes every member and the team's card.
            await ev(`document.querySelector('.session-board-card .group-kebab-btn').click()`);
            await waitFor(`!!document.querySelector('[data-on-click="team-kill"]')`, `${w}: the team's menu`);
            assert.equal(await ev(`!!document.querySelector('[data-on-click="team-dismiss"]')`), false, `${w}: no Dismiss All in the team menu`);
            await ev(`document.querySelector('[data-on-click="team-kill"]').click()`);
            await confirm();
            await waitFor(`!document.querySelector('.session-group-item[data-session-id="sid-3"]') && !document.querySelector('.session-group-item[data-session-id="sid-4"]')`, `${w}: both members to go`);
            assert.deepEqual(await rows(), ['sid-2'], `${w}: only the agent that was not killed is left`);
            assert.equal(await ev(`document.querySelectorAll('.session-board-card').length`), 0, `${w}: the team card goes with its last member`);
            await shot(`team-killed-${w}`);
            await noKilledUi(w);
            await redraw();
            assert.deepEqual(await rows(), ['sid-2'], `${w}: still gone after a redraw`);

            // Only the kill requests were sent: nothing of the history is deleted.
            assert.deepEqual((await killReqs()).sort(), ['POST /api/sessions/live/agent-1/kill', 'POST /api/sessions/live/team/crew/kill'], `${w}: the agent's own kill, and one request to the team route for the whole team (#6892: the team is marked stopped as an API kill does)`);
            assert.equal(await ev(`window.__reqs.some(r => /^(DELETE|PUT|PATCH) /.test(r) || /history|chats/.test(r) && !/^GET /.test(r))`), false, `${w}: no request deletes or edits history`);
        }
        console.log('PASS kill removes the row: one agent and a whole team, desktop and phone, no Dismiss, no history request');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
