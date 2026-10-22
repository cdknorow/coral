// Claude's permission-mode cycle and what the Mode button counts key presses from (#5951, #5979). Pure, no DOM: node mode_cycle.test.js
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');

(async () => {
    const m = await import(pathToFileURL(path.resolve(__dirname, '../../coral-go/pkg/frontend/static/mode_cycle.js')).href);
    // Claude's own names, from its footer
    for (const [text, mode] of [['⏸ manual mode on (shift+tab to cycle)', 'default'], ['⏵⏵ accept edits on', 'acceptEdits'], ['⏸ plan mode on', 'plan'], ['⏵⏵ auto mode on (shift+tab to cycle)', 'auto'], ['⏵⏵ bypass permissions on', 'bypassPermissions'], ["⏵⏵ don't ask on", 'dontAsk'], ['? for shortcuts', 'default']]) assert.strictEqual(m.modeFromFooterText(text), mode, text);
    assert.strictEqual(m.modeFromFooterText(''), null); assert.strictEqual(m.modeFromFooterText('> some prompt, no mode line'), null); assert.strictEqual(m.modeFromFooterText('no capture available'), null);
    assert.strictEqual(m.modeFromFooterText('we talked about plan mode\n' + 'x\n'.repeat(12) + '⏵⏵ accept edits on'), 'acceptEdits', 'only the last lines count');
    assert.strictEqual(m.modeFromFooterText('⏸ plan mode on\n' + 'x\n'.repeat(12)), null, 'words higher up are history');
    // presses are counted over the steps that hold in every session, from the SCREEN's mode
    assert.deepStrictEqual(m.MODE_CYCLE, ['default', 'acceptEdits', 'plan']);
    assert.strictEqual(m.pressesFor('plan', 'acceptEdits'), 2, 'Plan -> Default -> Accept edits: two, wherever the agent says it is');
    assert.strictEqual(m.pressesFor('default', 'acceptEdits'), 1);
    assert.strictEqual(m.pressesFor('default', 'plan'), 2);
    assert.strictEqual(m.pressesFor('acceptEdits', 'default'), 2);
    assert.strictEqual(m.pressesFor('plan', 'default'), 1);
    assert.strictEqual(m.pressesFor('plan', 'plan'), 3, 'a full turn');
    // no current mode (no mode line on the screen), or a mode outside the always-there steps: no count, never a guess; an agent's report is not an input at all
    assert.strictEqual(m.pressesFor(null, 'plan'), null);
    assert.strictEqual(m.pressesFor('auto', 'plan'), null);
    assert.strictEqual(m.pressesFor('plan', 'auto'), null);
    assert.strictEqual(m.pressesFor.length, 2, 'two inputs: the screen mode and the target');
    // the table: Claude's names and the steps that always hold
    assert.strictEqual(m.MODES.default.label, 'Manual'); assert.strictEqual(m.MODES.auto.label, 'Auto'); assert.strictEqual(m.MODES.acceptEdits.label, 'Accept Edits');
    assert.strictEqual(m.MODES.default.next, 'acceptEdits'); assert.strictEqual(m.MODES.acceptEdits.next, 'plan'); assert.strictEqual(m.MODES.plan.next, null); assert.strictEqual(m.MODES.auto.next, 'default');
    console.log('mode_cycle.test.js: ok');
})().catch(e => { console.error(e); process.exit(1); });
