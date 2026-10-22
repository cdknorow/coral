// #5916: the Messaging tab tells the two reaches of the cross-team messaging switch apart, against the real server. A board that never set the switch shows it on: "Teams on other computers:
// on (needs a Coral Hub link)", and "Teams on this computer: off" with a button to turn that on, because two teams on one computer have no link to hold and need an explicit on. The button
// saves the switch as an explicit on (the board's state then says so); an explicit off shows both reaches off and no button.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const TEAM = 'reach-' + Date.now();
    const api = (path, opts) => ev(`fetch('/api/board/${TEAM}${path}', ${JSON.stringify(opts || {})}).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const put = (path, body) => api(path, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const P = '#team-settings-messaging';
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const sub = await api('/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' }) });
        assert.ok(sub.status < 300, JSON.stringify(sub));
        const open = async () => {
            await ev(`document.getElementById('team-settings-workspace')?.remove(); document.getElementById('team-working-mode-dialog')?.remove(); import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
            await waitFor(`!!document.querySelector('[data-settings-tab=health]')`, 'the loaded Team settings', 15000);
            // the dialog's first focus is its first tab (Workflow), not the Rename board button in its header (#5962)
            await waitFor(`document.activeElement && document.activeElement.id === 'team-settings-tab-workflow'`, 'the first focus on the first tab', 8000);
            await ev(`document.querySelector('[data-settings-tab=messaging]').click(); true`);
            await waitFor(`!!document.querySelector('${P} .xt-reach')`, 'the two reaches', 8000);
        };
        const reach = () => ev(`(() => { const q = s => document.querySelector('${P} ' + s); return { here: q('.xt-reach-here').textContent.replace(/\\s+/g, ' ').trim(), button: !!q('[data-act=turn-on-here]'), msg: q('input[name=cross_team_messaging]').checked }; })()`);
        const state = async () => (await api('/cross-team-state')).body;

        // a board that never set the switch
        await open();
        let r = await reach();
        assert.equal(r.msg, true, 'the switch shows on by default');
        assert.match(r.here, /^Message teams on this computer: off/);
        assert.equal(r.button, true, 'a button to turn it on for this computer');
        let st = await state();
        assert.equal(st.messaging_elsewhere, true);
        assert.equal(st.messaging_here, false);
        console.log('PASS a board that never set the switch: other computers on (needs a link), this computer off');

        // the button saves an explicit on
        await ev(`document.querySelector('${P} [data-act=turn-on-here]').click(); true`);
        await waitFor(`document.querySelector('${P} .xt-reach-here')?.textContent.trim() === 'Message teams on this computer: on'`, 'this computer on', 8000);
        r = await reach();
        assert.equal(r.button, false);
        st = await state();
        assert.equal(st.messaging_here, true, 'saved as an explicit on');
        assert.equal(((await api('/settings')).body || {}).cross_team_messaging, true);
        console.log('PASS the button saves an explicit on');

        // an explicit off: both off, no button
        assert.equal((await put('/settings', { subscriber_id: 'Operator', cross_team_messaging: false })).status, 200);
        await open();
        r = await reach();
        assert.equal(r.msg, false);
        assert.equal(r.here, 'Message teams on this computer: off');
        assert.equal(r.button, false);
        st = await state();
        assert.equal(st.messaging_elsewhere, false);
        assert.equal(st.messaging_here, false);
        console.log('PASS an explicit off: both off, no button');
        console.log('PASS Messaging tab reaches');
    } finally {
        await removeBoards(ev, [TEAM]);
        try { await client.close(); } catch (_) {}
    }
})().catch(error => { console.error(error); process.exit(1); });
