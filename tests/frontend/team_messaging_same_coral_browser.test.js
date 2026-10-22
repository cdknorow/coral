// #5969: a send between two teams on THIS computer needs an explicit on (#5916), against the real server. Two teams that never set the cross-team switch show it on for "other computers" (it needs a Coral Hub link there) and off for
// "this computer"; a send from one to the other is refused with the turn-it-on message naming each team that is off, until BOTH have saved an explicit on with the Messaging tab's "Turn on for teams on this computer" button; then it is
// delivered. An explicit off on one team refuses it again, naming that team. The default must never open two teams of one computer to each other.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const stamp = Date.now();
    const A = 'same-a-' + stamp, B = 'same-b-' + stamp;
    const P = '#team-settings-messaging';
    const api = (team, path, opts) => ev(`fetch('/api/board/${team}${path}', ${JSON.stringify(opts || {})}).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const json = body => ({ method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    const send = (from, to, content = 'hello across teams') => api(to, '/external-messages', json({ from_board: from, subscriber_id: 'Operator', content }));
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        for (const team of [A, B]) {
            const sub = await api(team, '/subscribe', json({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session-' + team }));
            assert.ok(sub.status < 300, JSON.stringify(sub));
        }
        const open = async team => {
            await ev(`document.getElementById('team-settings-workspace')?.remove(); document.getElementById('team-working-mode-dialog')?.remove(); import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(team)}, { workspace: false })); true`);
            await waitFor(`!!document.querySelector('[data-settings-tab=health]')`, 'the loaded Team settings', 15000);
            await ev(`document.querySelector('[data-settings-tab=messaging]').click(); true`);
            await waitFor(`!!document.querySelector('${P} .xt-reach')`, 'the two reaches', 8000);
        };
        const here = () => ev(`document.querySelector('${P} .xt-reach-here').textContent.replace(/\\s+/g, ' ').trim()`);
        const turnOn = async team => {
            await open(team);
            assert.match(await here(), /^Message teams here: off/, team + ' starts off on this computer');
            await ev(`document.querySelector('${P} [data-act=turn-on-here]').click(); true`);
            await waitFor(`document.querySelector('${P} .xt-reach-here')?.textContent.trim() === 'Message teams here: on'`, team + ' on', 8000);
            assert.equal(((await api(team, '/cross-team-state')).body || {}).messaging_here, true, team + ': saved as an explicit on');
        };
        const refusedFor = async (res, teams, label) => {
            assert.equal(res.status, 403, label + ': ' + JSON.stringify(res));
            const text = (res.body && res.body.error) || '';
            assert.match(text, /^Cross-team messaging is off on /, label + ': ' + text);
            assert.ok(text.endsWith('. Turn it on in Team settings > Messaging.'), label + ': tells where to turn it on: ' + text);
            const named = [A, B].filter(t => text.includes(JSON.stringify(t)));
            assert.deepEqual(named, teams, label + ': names the teams that are off: ' + text);
        };

        // 1. Neither team ever set the switch: refused both ways, naming both teams
        assert.equal(((await api(A, '/cross-team-state')).body || {}).messaging_here, false);
        await refusedFor(await send(A, B), [A, B], 'A to B, neither on');
        await refusedFor(await send(B, A), [A, B], 'B to A, neither on');
        console.log('PASS two teams that never set the switch: a send is refused, naming both, with where to turn it on');

        // 2. A turned on with the button: still refused, now naming only B
        await turnOn(A);
        await refusedFor(await send(A, B), [B], 'A on, B off');
        await refusedFor(await send(B, A), [B], 'B to A, A on, B off');
        console.log('PASS the button on one team: the send is refused, naming only the team that is still off');

        // 3. B turned on too: delivered to the target's Orchestrator
        await turnOn(B);
        const ok = await send(A, B);
        assert.equal(ok.status, 200, JSON.stringify(ok));
        assert.deepEqual(ok.body.delivered_to, ['Boss']);
        console.log('PASS both teams on: the send is delivered to the other team\'s Orchestrator');

        // 4. An explicit off on A refuses it again, naming A
        assert.equal((await api(A, '/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Operator', cross_team_messaging: false }) })).status, 200);
        await refusedFor(await send(B, A, 'is anyone there'), [A], 'A explicitly off');
        console.log('PASS an explicit off on one team refuses the send again, naming it');
        console.log('PASS same-Coral send');
    } finally {
        await removeBoards(ev, [A, B]);
        try { await client.close(); } catch (_) {}
    }
})().catch(error => { console.error(error); process.exit(1); });
