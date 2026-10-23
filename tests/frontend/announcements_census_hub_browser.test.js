// The screen-reader announcements census through Coral Hub (#6226): cloud mode under the hub's CSP, the stubbed two-computer Hub (helpers/hub_world.js), at 1280 and 390 px. What the live regions say on the Hub's own
// pages: opening a chat says nothing (no chatter), a computer going offline and coming back is said once each on the Servers page, (a new message on a board is said by the board's own announcer, which board_a11y covers: the Hub's board only reloads on a request) and a toast or an error is said once through
// the Notifications column. helpers/announce_census.js records every live region's visible changes; ANNOUNCE_REPORT=<file> writes every scene's log to the file instead of failing.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { watch, clear, log, problems, said } = require('./helpers/announce_census');
const { setupHubWorld, BASE } = require('./helpers/hub_world');
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    const scenes = []; const found = [];
    const wait = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: announcements are timed; there is no event to wait for
    const scene = async (name, run, { must = [], forbid = [] } = {}) => {
        await clear(ev);
        try { await run(); } catch (e) { scenes.push({ name, entries: [] }); found.push(`${name}: the scene did not run (${String(e && e.message || e).slice(0, 160)})`); return; }
        await wait(600); // sleep-ok: the last announcement lands
        const entries = await log(ev);
        scenes.push({ name, entries });
        found.push(...problems(name, entries, { forbid }));
        for (const [re, label, count = 1] of must) { const n = said(entries, re).length; if (n !== count) found.push(`${name}: ${label} must be announced ${count === 1 ? 'once' : count + ' times'} (it was ${n})`); }
    };
    try {
        await setupHubWorld({ client, Page, Emulation, width: 1280, height: 800, mobile: false });
        const run = async tag => {
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await waitFor(`typeof window.switchNavTab==='function' && !!window.__coralReady`, 'dashboard'); await ev(`window.__coralReady.startup('liveList','settings')`);
            await watch(ev);
            await scene(`${tag}: hub: a chat is opened`, async () => {
                await ev(`window.switchNavTab('history'); true`);
                await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Laptop conversation')`, 'history', 10000);
                await ev(`document.querySelector('#history-sessions-list [data-on-click="history-open"]').click(); true`);
                await waitFor(`document.getElementById('history-messages').textContent.includes('needle from')`, 'chat', 10000);
            }, { forbid: [/needle|Scoped to this computer/] });
            await scene(`${tag}: hub: a computer goes offline and comes back`, async () => {
                await ev(`window.switchNavTab('servers'); true`);
                await waitFor(`document.getElementById('servers-view').getClientRects().length > 0 && document.querySelector('#servers-tab-body .cs-name')`, 'servers', 10000);
                await wait(300); // sleep-ok: the list settles before the status changes
                await clear(ev);
                await ev(`window.__computers[0].online = false; window.__computers[0].status = 'offline'; true`);
                await wait(6000); // sleep-ok: the list asks again every 5 s
                await ev(`window.__computers[0].online = true; window.__computers[0].status = 'online'; true`);
                await wait(6000); // sleep-ok: the list asks again every 5 s
            }, { must: [[/Laptop is offline/, 'Laptop going offline'], [/Laptop is online/, 'Laptop coming back']] });
            await scene(`${tag}: hub: the Artifacts tab is opened`, async () => {
                await ev(`window.switchNavTab('artifacts'); true`);
                await waitFor(`!!document.getElementById('artifacts-frame')`, 'artifacts frame', 10000);
            }, { forbid: [/./] }); // the tab says nothing of its own: the hub's page in the frame is its own document
            await scene(`${tag}: hub: toasts`, async () => {
                await ev(`(async () => { const n = await import(${S} + 'notifications.js'); n.showToast('Saved to the board.'); n.showToast('Could not reach the computer.', true); return true; })()`);
                await wait(1500); // sleep-ok: the two are said one after the other
            }, { must: [[/Saved to the board/, 'a toast'], [/Could not reach the computer/, 'an error toast']] });
        };
        await run('desktop');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await run('phone');
    } finally { await client.close(); }
    if (process.env.ANNOUNCE_REPORT) { require('node:fs').writeFileSync(process.env.ANNOUNCE_REPORT, scenes.map(s => `## ${s.name}\n` + s.entries.map(e => `  ${String(e.t).padStart(5)} ms ${e.loud ? '!' : ' '} ${e.region}: ${e.text}`).join('\n')).join('\n') + '\n--\n' + found.join('\n') + '\n'); process.exit(0); }
    assert.deepEqual(found, [], 'screen-reader announcement problems through the Hub:\n  ' + found.join('\n  '));
    console.log(`PASS ${scenes.length} Hub scenes: each state is announced once, nothing chatters`);
})().catch(e => { console.error(e); process.exit(1); });
