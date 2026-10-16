// #3770 (from the #3735 sweep): two controls that did nothing and said nothing.
//  - Send with an empty box: the box takes focus and flashes once (class empty-send-nudge, which ends by itself); with text, or
//    with an attachment waiting, there is no nudge and the send goes ahead.
//  - The team launcher's Move up on the first agent card and Move down on the last are disabled, and follow the order as cards
//    are added, moved and removed.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('es-a', 'A', 3, { sleeping: false }); true`);
        await ev(`window.selectLiveSession('es-a', 'claude', 'es-a-id', 'local'); true`);
        await waitFor(`!!document.querySelector('#command-pane [data-on-click=cmd-send]') && !document.getElementById('command-input').disabled`, 'the composer', 8000);

        console.log('1. Send with an empty box focuses and flashes it; with text there is no nudge');
        const nudged = () => ev(`document.getElementById('command-input').classList.contains('empty-send-nudge')`);
        assert.equal(await nudged(), false, 'no nudge to begin with');
        await ev(`document.activeElement && document.activeElement.blur(); document.querySelector('#command-pane [data-on-click=cmd-send]').click(); true`);
        await waitFor(`document.getElementById('command-input').classList.contains('empty-send-nudge')`, 'the nudge after Send on an empty box', 3000);
        assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'command-input', 'the box has focus');
        assert.notEqual(await ev(`getComputedStyle(document.getElementById('command-input')).animationName`), 'none', 'it plays an animation');
        await waitFor(`!document.getElementById('command-input').classList.contains('empty-send-nudge')`, 'the nudge to end by itself', 3000);
        // Enter on an empty box stays quiet (it is not the Send button).
        await ev(`document.getElementById('command-input').focus(); true`);
        await Input.dispatchKeyEvent({ type: 'rawKeyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
        assert.equal(await nudged(), false, 'Enter on an empty box does not flash');
        // With text: Send goes ahead, no nudge.
        await Input.insertText({ text: 'hello there' });
        await ev(`document.querySelector('#command-pane [data-on-click=cmd-send]').click(); true`);
        assert.equal(await nudged(), false, 'no nudge when there is text to send');
        console.log('   ✓ empty: focused and flashed, flash ends; Enter quiet; with text no flash');

        console.log('2. The team launcher\'s Move up on the first card and Move down on the last are disabled');
        await ev(`(async () => { await import((window.__coralStatic || '/static/') + 'modals.js'); const el = document.getElementById('team-agents-list'); el.innerHTML = ''; for (const n of ['One', 'Two', 'Three']) window._addTeamAgent(n, 'prompt ' + n); return true; })()`);
        const moves = () => ev(`[...document.querySelectorAll('#team-agents-list > .team-agent-row')].map(r => [r.querySelector('.team-agent-role-name').textContent, r.querySelector('[data-dir=up]').disabled, r.querySelector('[data-dir=down]').disabled])`);
        await waitFor(`document.querySelectorAll('#team-agents-list > .team-agent-row').length === 3 && document.querySelector('#team-agents-list > .team-agent-row [data-dir=up]').disabled`, 'the end buttons to settle', 4000);
        assert.deepEqual(await moves(), [['One', true, false], ['Two', false, false], ['Three', false, true]], 'first: no up; last: no down');
        await ev(`document.querySelectorAll('#team-agents-list > .team-agent-row')[0].querySelector('[data-dir=down]').click(); true`);
        await waitFor(`document.querySelector('#team-agents-list > .team-agent-row .team-agent-role-name').textContent === 'Two'`, 'the move', 3000);
        await waitFor(`document.querySelectorAll('#team-agents-list > .team-agent-row')[0].querySelector('[data-dir=up]').disabled && !document.querySelectorAll('#team-agents-list > .team-agent-row')[1].querySelector('[data-dir=up]').disabled`, 'the buttons to follow the move', 3000);
        assert.deepEqual(await moves(), [['Two', true, false], ['One', false, false], ['Three', false, true]], 'after Move down on the first card, the ends follow');
        await ev(`document.querySelectorAll('#team-agents-list > .team-agent-row')[2].querySelector('.team-agent-remove').click(); true`);
        await waitFor(`document.querySelectorAll('#team-agents-list > .team-agent-row').length === 2 && document.querySelectorAll('#team-agents-list > .team-agent-row')[1].querySelector('[data-dir=down]').disabled`, 'the new last card to lose Move down', 3000);
        assert.deepEqual(await moves(), [['Two', true, false], ['One', false, true]], 'after removing the last card');
        await ev(`document.querySelectorAll('#team-agents-list > .team-agent-row')[1].querySelector('.team-agent-remove').click(); true`);
        await waitFor(`document.querySelectorAll('#team-agents-list > .team-agent-row').length === 1 && document.querySelector('#team-agents-list [data-dir=down]').disabled`, 'a single card', 3000);
        assert.deepEqual(await moves(), [['Two', true, true]], 'one card cannot move either way');
        console.log('   ✓ ends disabled, and they follow add, move and remove');

        console.log('\nAll empty-send and team-move checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
