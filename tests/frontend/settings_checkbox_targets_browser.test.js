// Every checkbox in Settings is tapped through a 44 px target on a phone or a touch screen (#4252, from QA's phone pass #4231): the
// box is 13 px and its label for= is in the other column of the row, so each box is wrapped in its own label (.check-hit). The whole
// label toggles the box (a tap beside the box, inside the label), and a mouse layout is unchanged (the label hugs the box).
//
// Drives Chrome over CDP through agent_view_harness.js against an isolated dev server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { acrossGroups } = require('./helpers/settings_groups');

const BASE = require('./test_base').BASE;

const CHECKBOXES = `(() => [...document.querySelectorAll('#settings-modal input[type=checkbox], #settings-modal input[type=radio]')]
    .filter(el => getComputedStyle(el).display !== 'none' && !el.closest('[hidden]') && el.getBoundingClientRect().width > 0)
    .map(el => { const hit = el.closest('label') || el; const r = hit.getBoundingClientRect(); return { id: el.id || el.name || '', w: Math.round(r.width), h: Math.round(r.height), label: !!el.closest('label') }; }))()`;

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });

        console.log('1. Phone 390 px with a coarse pointer: every Settings checkbox has a 44 x 44 target');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
        await ev(`(async () => { await window.showSettingsModal(); return true; })()`);
        const boxes = await acrossGroups(ev, CHECKBOXES);
        assert.ok(boxes.length >= 12, `the Settings checkboxes are found (${boxes.length})`);
        const small = boxes.filter(b => b.w < 44 || b.h < 44);
        assert.deepEqual(small, [], 'no checkbox target under 44 px: ' + JSON.stringify(small));
        console.log(`   ✓ ${boxes.length} checkboxes, all >= 44 x 44`);

        console.log('2. A tap beside the box, inside its label, toggles it');
        const toggled = await ev(`(() => { const out = [];
            for (const id of ['settings-telemetry-enabled', 'settings-auto-resume', 'settings-show-scrollbars', 'settings-agent-low-priority']) {
                const el = document.getElementById(id); document.getElementById('settings-tab-' + el.closest('.settings-pane').dataset.group).click(); const label = el.closest('label'); label.scrollIntoView({ block: 'center', behavior: 'instant' });
                const r = label.getBoundingClientRect(); const b = el.getBoundingClientRect();
                // The point in the label's far corner, outside the 13 px box (which sits at its left).
                const x = r.right - 3, y = r.bottom - 3; const hit = document.elementFromPoint(x, y);
                const before = el.checked; hit.click(); out.push({ id, outsideBox: x > b.right + 1 || y > b.bottom + 1, onLabel: hit === label || label.contains(hit), toggled: el.checked !== before });
                el.checked = before; }
            return out; })()`);
        for (const t of toggled) assert.deepEqual([t.outsideBox, t.onLabel, t.toggled], [true, true, true], `a tap in the corner of ${t.id}'s label toggles it: ${JSON.stringify(t)}`);
        console.log(`   ✓ ${toggled.length} checked`);

        console.log('3. A mouse layout is unchanged: the label hugs the box');
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
        await ev(`(async () => { await window.showSettingsModal(); return true; })()`);
        const desk = (await acrossGroups(ev, CHECKBOXES)).filter(b => b.label);
        const wide = desk.filter(b => b.w > 30 || b.h > 30).map(b => `${b.id} ${b.w}x${b.h}`);
        // Labels with text of their own (a switch row, the cloud switches) are wider by design: only the bare wrappers are checked.
        const wrappers = await acrossGroups(ev, `[...document.querySelectorAll('#settings-modal .check-hit')].map(l => { const r = l.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height) }; }).filter(r => r.w > 0)`);
        // The box is a switch (2.5rem x 1.4rem, #4566): the wrapper hugs it.
        assert.ok(wrappers.length >= 8 && wrappers.every(r => r.w <= 44 && r.h <= 30), `the wrappers hug the switch with a mouse: ${JSON.stringify(wrappers)}`);
        void wide;
        console.log(`   ✓ ${wrappers.length} wrappers at ${wrappers[0].w} x ${wrappers[0].h}`);

        console.log('\nAll Settings checkbox target checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
