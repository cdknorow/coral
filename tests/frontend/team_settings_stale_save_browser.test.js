// #4423: Team settings saves only what the person changed, against the real server (another client is a request straight to it):
//   - a working-mode instruction and the files switch another client changed while the dialog was open survive a Save of the
//     messaging switch (saved from the Messaging tab, which sends only what changed), and nothing else is sent;
//   - a working-mode field changed in the dialog is saved onto the server's current working mode (the other client's instruction stays);
//   - the Health check tab saves the person's change onto the server's current health object: a rule another client changed stays;
//   - a Save with nothing changed sends nothing and says so.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const TEAM = 'stale-save-' + Date.now();
    const api = (path, opts) => ev(`fetch('/api/board/${TEAM}${path}', ${JSON.stringify(opts || {})}).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const put = (path, body) => api(path, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const form = `document.querySelector('form.working-mode-form')`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const sub = await api('/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' }) });
        assert.ok(sub.status < 300, JSON.stringify(sub));
        assert.equal((await put('/working-mode', { mode: 'none', dependency_guidance: false, custom_instructions: 'as the dialog opens' })).status, 200);

        const open = async () => {
            // The previous dialog goes now, before the import resolves, so the wait below can only see the new one (it
            // otherwise passed on the old dialog, which the new one then replaced under the next click).
            await ev(`document.getElementById('team-settings-workspace')?.remove(); document.getElementById('team-working-mode-dialog')?.remove(); import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
            await waitFor(`!!${form} && ${form}.dataset.initialState !== undefined && !!document.querySelector('[data-settings-tab=health]')`, 'the loaded Team settings', 15000);
        };
        const submit = async () => {
            await ev(`document.querySelector('.working-mode-status').textContent = ''; ${form}.requestSubmit(); true`);
            await waitFor(`/Saved|failed/.test(document.querySelector('.working-mode-status').textContent)`, 'the save', 10000);
            assert.match(await ev(`document.querySelector('.working-mode-status').textContent`), /^Saved/);
        };

        console.log('1. A change made elsewhere while the dialog is open survives a Save of an unrelated switch');
        // the two cross-team switches default to on (#5916): this team starts with both explicitly off
        assert.equal((await put('/settings', { subscriber_id: 'Operator', cross_team_messaging: false, cross_team_artifacts: false })).status, 200);
        await open();
        assert.equal((await put('/working-mode', { mode: 'none', dependency_guidance: false, custom_instructions: 'from another client' })).status, 200);
        assert.equal((await put('/settings', { subscriber_id: 'Operator', cross_team_artifacts: true })).status, 200);
        // the messaging switch lives on the Messaging tab (#5572), which saves only what changed with its own Save
        await ev(`document.querySelector('[data-settings-tab=messaging]').click(); true`);
        await waitFor(`!!document.querySelector('#team-settings-messaging input[name=cross_team_messaging]')`, 'the Messaging tab', 8000);
        await ev(`(() => { const c = document.querySelector('#team-settings-messaging input[name=cross_team_messaging]'); c.checked = true; c.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await ev(`document.querySelector('#team-settings-messaging [type=submit]').click(); true`);
        await waitFor(`document.querySelector('#team-settings-messaging .xt-state')?.textContent === 'Saved.'`, 'the Messaging save', 10000);
        let mode = (await api('/working-mode')).body, board = (await api('/settings')).body;
        assert.equal(mode.custom_instructions, 'from another client', 'the working mode the dialog showed is not written back');
        assert.equal(board.cross_team_messaging, true, 'the switch the person turned on is saved');
        assert.equal(board.cross_team_artifacts, true, 'the files switch another client turned on stays on');

        console.log('2. A working-mode field changed here is saved onto what the server has now');
        await open();
        assert.equal(await ev(`${form}.elements.custom_instructions.value`), 'from another client', 'the dialog opens with the server\'s values');
        assert.equal((await put('/working-mode', { mode: 'none', dependency_guidance: false, custom_instructions: 'second edit elsewhere' })).status, 200);
        await ev(`${form}.elements.dependency_guidance.checked = true; true`);
        await submit();
        mode = (await api('/working-mode')).body;
        assert.equal(mode.dependency_guidance, true, 'the field the person changed is saved');
        assert.equal(mode.custom_instructions, 'second edit elsewhere', 'the instruction changed elsewhere meanwhile stays');

        console.log('3. The Health check tab keeps a rule another client changed');
        await open();
        await ev(`document.querySelector('[data-settings-tab=health]').click(); true`);
        await waitFor(`!!document.querySelector('#team-settings-health form')`, 'the Health check form', 10000);
        const health = (await api('/settings')).body.health;
        assert.equal((await put('/settings', { subscriber_id: 'Operator', health: { ...health, claim_nudge: { ...health.claim_nudge, after_min: 30 } } })).status, 200);
        await ev(`(() => { const f = document.querySelector('#team-settings-health form'); const i = f.elements.interval_min; i.value = '15'; i.dispatchEvent(new Event('input', { bubbles: true })); f.requestSubmit(); return true; })()`);
        await waitFor(`/Saved/.test(document.querySelector('#team-settings-health .health-state')?.textContent || '')`, 'the health save', 10000);
        const saved = (await api('/settings')).body.health;
        assert.equal(saved.interval_min, 15, 'the field the person changed is saved');
        assert.equal(saved.claim_nudge.after_min, 30, 'the rule another client changed stays');
        console.log('4. Saved with nothing changed: no request, and the person is told');
        await open();
        await ev(`(() => { const R = window.fetch; window.__teamPuts = 0; window.fetch = (u, o = {}) => { if ((o.method || '').toUpperCase() === 'PUT') window.__teamPuts++; return R(u, o); }; window.__restoreFetch = () => { window.fetch = R; }; return true; })()`);
        await ev(`document.querySelector('.working-mode-status').textContent = ''; ${form}.requestSubmit(); true`);
        await waitFor(`/Nothing to save/.test(document.querySelector('.working-mode-status').textContent)`, 'the nothing-to-save line', 10000);
        assert.equal(await ev(`window.__teamPuts`), 0, 'no request');
        await ev(`window.__restoreFetch(); true`);
        console.log('PASS Team settings saves only what changed');
    } finally {
        await removeBoards(ev, [TEAM]);
        try { await client.close(); } catch (_) {}
    }
})().catch(error => { console.error(error); process.exit(1); });
