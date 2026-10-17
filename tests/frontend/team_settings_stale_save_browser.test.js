// #4423: Team settings saves only what the person changed, against the real server (another client is a request straight to it):
//   - a working-mode instruction and the files switch another client changed while the dialog was open survive a Save of the
//     messaging switch, and nothing else is sent;
//   - a working-mode field changed in the dialog is saved onto the server's current working mode (the other client's instruction stays);
//   - the Health check tab saves the person's change onto the server's current health object: a rule another client changed stays.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

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
            await ev(`document.getElementById('team-settings-workspace')?.remove(); import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
            await waitFor(`!!${form} && ${form}.dataset.initialState !== undefined && !!document.querySelector('[data-settings-tab=health]')`, 'the loaded Team settings', 15000);
        };
        const submit = async () => {
            await ev(`document.querySelector('.working-mode-status').textContent = ''; ${form}.requestSubmit(); true`);
            await waitFor(`/Saved|failed/.test(document.querySelector('.working-mode-status').textContent)`, 'the save', 10000);
            assert.match(await ev(`document.querySelector('.working-mode-status').textContent`), /^Saved/);
        };

        console.log('1. A change made elsewhere while the dialog is open survives a Save of an unrelated switch');
        await open();
        assert.equal((await put('/working-mode', { mode: 'none', dependency_guidance: false, custom_instructions: 'from another client' })).status, 200);
        assert.equal((await put('/settings', { subscriber_id: 'Operator', cross_team_artifacts: true })).status, 200);
        await ev(`${form}.elements.cross_team_messaging.checked = true; true`);
        await submit();
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
        console.log('PASS Team settings saves only what changed');
    } finally {
        try { await client.close(); } catch (_) {}
    }
})().catch(error => { console.error(error); process.exit(1); });
