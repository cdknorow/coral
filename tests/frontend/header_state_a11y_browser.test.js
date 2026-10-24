// The agent header's status dot (#4327, #4276 finding 3): its tip and accessible name say the state in the sidebar's words and follow a
// change of state from the live feed; the word beside it is drawn on a touch screen wider than a phone (no hover there) and is aria-hidden,
// so a screen reader reads the state once (from the dot, in the accessibility tree). Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const { Accessibility, DOM } = client;
    const ok = name => console.log(`   ✓ ${name}`);
    const dot = () => ev(`(() => { const d = document.getElementById('terminal-status-dot'); const w = document.getElementById('terminal-state-word');
        return { cls: d.className, title: d.title, role: d.getAttribute('role'), label: d.getAttribute('aria-label'), word: w.dataset.word, wordHidden: w.getAttribute('aria-hidden'),
            wordShown: w.checkVisibility({ checkVisibilityCSS: true }) && getComputedStyle(w).display !== 'none', textNodes: w.textContent }; })()`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('dot-bot', 'A', 2, { sleeping: false, session_id: 'dot-sid' }); true`);
        await sleep(600); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('dot-bot', 'claude', 'dot-sid', 'local'); true`);
        await waitFor(`document.getElementById('terminal-status-dot').getAttribute('aria-label')`, 'the dot to be named', 8000);

        let d = await dot();
        assert.equal(d.role, 'img'); assert.equal(d.title, d.label); assert.ok(d.label && d.label.length > 2, 'a state word: ' + d.label);
        assert.equal(d.word, d.label, 'the word beside it is the same state'); assert.equal(d.textNodes, '', 'it is generated content: the identity\'s own text is unchanged'); assert.equal(d.wordHidden, 'true', 'the word is hidden from assistive technology');
        assert.equal(d.wordShown, true, 'a mouse screen draws the word beside the dot too (#7065), not only the tip');
        ok('the dot has a role, a tip and an accessible name with the sidebar\'s state word; the word beside it is aria-hidden and drawn beside the dot');

        // it follows the live feed
        for (const [patch, word] of [[{ working: true }, 'Working'], [{ working: false, waiting_for_input: true }, 'Needs input'], [{ waiting_for_input: false, stuck: true }, 'Stuck']]) {
            await ev(`(() => { const a = window.__m.agents['dot-sid']; T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(a), ...${JSON.stringify(patch)} }] }); return true; })()`);
            await waitFor(`document.getElementById('terminal-status-dot').getAttribute('aria-label') === ${JSON.stringify(word)}`, `the dot to say ${word}`, 5000, `document.getElementById('terminal-status-dot').getAttribute('aria-label')`);
            d = await dot();
            assert.equal(d.title, word); assert.equal(d.word, word); assert.match(d.cls, /terminal-status-dot /);
        }
        ok('the name, the tip and the word follow a state change from the live feed (Working, Needs input, Stuck)');

        // a screen wider than a phone shows the word (a mouse one too since #7065); a phone does not (it has its own chip)
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await Emulation.setDeviceMetricsOverride({ width: 900, height: 800, deviceScaleFactor: 1, mobile: true }); await sleep(400); // sleep-ok: layout settles after the emulation change
        assert.equal((await dot()).wordShown, true, 'on a touch screen at 900 px the state word is drawn beside the dot');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 800, deviceScaleFactor: 1, mobile: true }); await sleep(400); // sleep-ok: layout settles after the emulation change
        assert.equal((await dot()).wordShown, false, 'a phone does not draw it');
        ok('the word is drawn on a touch screen at 900 px and not on a phone');

        // the accessibility tree: the dot is one image named for the state, the word is not exposed
        await DOM.enable(); await Accessibility.enable();
        const doc = await DOM.getDocument({ depth: -1 });
        const q = async selector => { const { nodeId } = await DOM.querySelector({ nodeId: doc.root.nodeId, selector }); const { nodes } = await Accessibility.getPartialAXTree({ nodeId, fetchRelatives: false }); return nodes[0]; };
        const dn = await q('#terminal-status-dot');
        assert.equal(dn.role.value, 'image'); assert.equal(dn.name.value, 'Stuck');
        assert.equal((await q('#terminal-state-word')).ignored, true, 'the word beside the dot is not in the accessibility tree');
        ok('in the accessibility tree the dot is an image named "Stuck" and the word beside it is ignored');
    } catch (e) {
        console.error(e);
        process.exitCode = 1;
    } finally {
        await client.close();
    }
})();
