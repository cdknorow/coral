// #3735 (found by a click-through of every control in each agent state): two controls that looked fine and were not.
//  - A sleeping or archived agent in the Terminal view (desktop) was an empty terminal with a "Click to type" hint and no
//    way to wake it: the pane that holds the Wake / Restart / Unarchive bar is hidden in the Terminal view. The pane stays
//    while the agent sleeps, the hint goes, and an awake agent's Terminal view is unchanged.
//  - View-only through the Coral Hub dims Send (and its menu) so it looks unavailable; the CSS named the inline onclick that
//    #3645 removed, so nothing was dimmed.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const { waitStable } = require('./helpers/wait').bind(ev);
    const MOD = `(window.__coralStatic || '/static/')`;
    const shown = sel => `(() => { const n = document.querySelector(${JSON.stringify(sel)}); if (!n) return false; const r = n.getBoundingClientRect(), cs = getComputedStyle(n); return r.width > 0 && r.height > 0 && cs.display !== 'none' && cs.visibility !== 'hidden'; })()`;
    const select = async name => {
        await ev(`window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(name + '-id')}, 'local'); true`);
        await waitFor(`document.querySelector('#live-session-view') && getComputedStyle(document.querySelector('#live-session-view')).display !== 'none'`, `${name} on screen`, 8000);
        await waitStable(`document.querySelectorAll('#live-history-messages > *').length`, 'the chat to settle', 200).catch(() => {});
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('cs-awake', 'A', 4, { sleeping: false }); window.__m.add('cs-sleep', 'S', 4, { sleeping: true }); window.__m.add('cs-arch', 'R', 4, { archived: true }); true`);
        assert.ok(await ev(`innerWidth >= 768`), `a desktop-width window (${await ev('innerWidth')})`);

        console.log('1. A sleeping or archived agent\'s Terminal view keeps its wake bar and drops the typing hint');
        for (const [name, buttons] of [['cs-sleep', ['#sleeping-wake-btn', '#sleeping-restart-btn']], ['cs-arch', ['#sleeping-unarchive-btn', '#sleeping-wake-btn']]]) {
            await select(name);
            await ev(`window.setLiveViewMode('terminal'); true`);
            await waitFor(`!!document.getElementById('xterm-container') && getComputedStyle(document.getElementById('xterm-container')).display !== 'none'`, 'the terminal view', 8000);
            assert.equal(await ev(shown('#sleeping-composer')), true, `${name}: the sleeping bar is visible in the Terminal view`);
            for (const b of buttons) assert.equal(await ev(shown(b)), true, `${name}: ${b} is visible`);
            assert.equal(await ev(`getComputedStyle(document.querySelector('#xterm-container .xterm-focus-hint')).display`), 'none', `${name}: no "Click to type" over a terminal that takes no typing`);
            await ev(`window.setLiveViewMode('chat'); true`);
        }
        await select('cs-awake');
        await ev(`window.setLiveViewMode('terminal'); true`);
        await waitFor(`!!document.getElementById('xterm-container') && getComputedStyle(document.getElementById('xterm-container')).display !== 'none'`, 'the awake terminal view', 8000);
        assert.equal(await ev(shown('#command-pane')), false, 'an awake agent\'s Terminal view still hides the composer pane (typing goes into the terminal)');
        assert.notEqual(await ev(`getComputedStyle(document.querySelector('#xterm-container .xterm-focus-hint')).display`), 'none', 'and still offers "Click to type"');
        await ev(`window.setLiveViewMode('chat'); true`);
        console.log('   ✓ sleeping and archived: wake bar and its buttons shown, hint hidden; awake: unchanged');

        console.log('2. View-only through the Hub dims Send and its menu, with the reason (#3769 marks the rest)');
        await select('cs-awake');
        const dim = () => ev(`(() => { const o = {}; for (const a of ['cmd-send', 'cmd-send-menu']) { const n = document.querySelector('#command-pane [data-on-click=' + a + ']'); const cs = getComputedStyle(n); o[a] = [n.classList.contains('hub-blocked'), cs.opacity, cs.pointerEvents, n.title.startsWith('Blocked:')]; } return o; })()`);
        assert.deepEqual(await dim(), { 'cmd-send': [false, '1', 'auto', false], 'cmd-send-menu': [false, '1', 'auto', false] }, 'normally Send is a normal control');
        await ev(`import(${MOD} + 'hub_access.js').then(async h => { const { state } = await import(${MOD} + 'state.js'); h.setHubReadOnly(state.currentSession, { hub: 'view', local: 'launch', effective: 'view', limitedBy: 'hub', label: 'qa-server' }, 'interact', true); return true; })`); // fromFrame: the hub said so on the terminal socket
        await waitFor(`document.getElementById('command-pane').classList.contains('hub-view-only')`, 'the view-only mark', 4000);
        // The buttons fade (a CSS transition): wait for the end of it before reading the opacity.
        await waitFor(`getComputedStyle(document.querySelector('#command-pane [data-on-click=cmd-send]')).opacity === '0.45'`, 'Send to finish dimming', 4000);
        assert.deepEqual(await dim(), { 'cmd-send': [true, '0.45', 'auto', true], 'cmd-send-menu': [true, '0.45', 'auto', true] }, 'view-only: Send and its menu are dimmed, clickable, and say why');
        await ev(`import(${MOD} + 'hub_access.js').then(async h => { const { state } = await import(${MOD} + 'state.js'); h.setHubReadOnly(state.currentSession, null); return true; })`);
        await waitFor(`!document.getElementById('command-pane').classList.contains('hub-view-only')`, 'the mark to clear', 4000);
        await waitFor(`getComputedStyle(document.querySelector('#command-pane [data-on-click=cmd-send]')).opacity === '1'`, 'Send to finish un-dimming', 4000);
        assert.deepEqual(await dim(), { 'cmd-send': [false, '1', 'auto', false], 'cmd-send-menu': [false, '1', 'auto', false] }, 'cleared: Send is normal again');
        console.log('   ✓ view-only: Send and Send options dimmed with the reason and still clickable; cleared: back to normal');

        console.log('\nAll agent-control-state checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
