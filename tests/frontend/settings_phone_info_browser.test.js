// Settings on a phone (#5057): each row's (i) help button sits beside its label, at 44x44, not on a row of its own under it (that made a toggle's row 105 px instead of 80, and
// the General page about 50 px taller), and a switch's toggle is level with that (i) (#5109: the box sat at the top of its 44 px area, about 17 px above the (i)'s centre: the area is a label whose generic 12 px bottom margin was part of what got centred). Every section is checked at 390 px with a coarse pointer: wherever a row has an (i), it is level with the label and to its right.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setEmulatedMedia({ features: [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        const l = Page.loadEventFired(); await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await l; await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.showSettingsModal().then(() => true)`);
        await waitFor(`(m => !!m && getComputedStyle(m).display !== 'none' && m.querySelectorAll('.settings-row').length > 3 && !!m.querySelector('.settings-group-pick select'))(document.getElementById('settings-modal'))`, 'Settings', 10000);
        const groups = await ev(`[...document.querySelector('#settings-modal .settings-group-pick select').options].map(o => o.value)`);
        assert.ok(groups.length >= 5, 'the sections: ' + JSON.stringify(groups));

        const rows = () => ev(`(() => { const out = []; for (const row of document.querySelectorAll('#settings-modal .settings-pane:not([hidden]) .settings-row')) {
            const info = row.querySelector('.settings-info-btn'); if (!info || !info.getClientRects().length) continue;
            const dt = row.querySelector('dt, :scope > .net-group-head'), label = dt.querySelector('label, .net-group-title'); let a; // (the Network and Hub groups are sections with a heading, #6619)
            if (label) a = label.getBoundingClientRect(); else { const t = [...dt.childNodes].find(n => n.nodeType === 3 && n.textContent.trim()) || dt.querySelector('span'); /* the Agent tools folder row (shown when the folder is not a place only an administrator can write, as a dev build's is) labels itself with a span */ const rg = document.createRange(); rg.selectNodeContents(t); a = rg.getBoundingClientRect(); } // a row whose title is bare text in the dt
            const b = info.getBoundingClientRect();
            const tog = row.querySelector(':scope > dd > .check-hit input, :scope > .net-group-head > .check-hit input, :scope > .net-group-head > .settings-hub-mode-toggle input'), tb = tog && tog.getBoundingClientRect();
            out.push({ label: dt.textContent.replace(/info/, '').trim().slice(0, 30), off: tb ? Math.round(Math.abs((tb.top + tb.bottom) / 2 - (b.top + b.bottom) / 2) * 10) / 10 : null, info: [Math.round(b.width), Math.round(b.height)], level: b.top < a.bottom && b.bottom > a.top, right: b.left >= a.right - 2, row: Math.round(row.getBoundingClientRect().height), dt: Math.round(dt.getBoundingClientRect().width), dd: row.querySelector('dd, :scope > .net-body') ? Math.round(row.querySelector('dd, :scope > .net-body').getBoundingClientRect().width) : 0, cols: getComputedStyle(row).gridTemplateColumns });
        } return out; })()`);
        let seen = 0, general = 0, toggles = 0;
        for (const g of groups) {
            await ev(`(() => { const s = document.querySelector('#settings-modal .settings-group-pick select'); s.value = ${JSON.stringify(g)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await waitFor(`!!document.querySelector('#settings-modal .settings-pane:not([hidden])')`, 'the section ' + g, 4000);
            await new Promise(r => setTimeout(r, 200)); // sleep-ok: the pane lays out after it is shown
            const rs = await rows();
            for (const r of rs) {
                assert.ok(r.level && r.right, `${g}: the (i) is beside its label "${r.label}", not under it: ${JSON.stringify(r)}`);
                if (r.off !== null) { toggles++; assert.ok(r.off <= 2, `${g}: the toggle is level with the (i) beside "${r.label}" (centres ${r.off} px apart; it sat about 19 px above, #5109): ${JSON.stringify(r)}`); }
                assert.ok(r.info[0] >= 44 && r.info[1] >= 44, `${g}: the (i) beside "${r.label}" is a 44 px target: ${JSON.stringify(r)}`);
            }
            seen += rs.length;
            if (g === groups[0]) general = (await ev(`[...document.querySelectorAll('#settings-modal .settings-pane:not([hidden]) .settings-row')].filter(r => r.getClientRects().length).reduce((a, r) => a + r.getBoundingClientRect().height, 0)`));
        }
        assert.ok(toggles >= 8, 'switch rows were checked for the toggle/(i) alignment: ' + toggles);
        assert.ok(seen >= 8, 'rows with an (i) were checked across the sections: ' + seen);
        assert.ok(general > 0 && general <= 660, 'the first section (General) is no taller than 660 px of rows (it was about 683): ' + general);
        if (process.env.CORAL_SCREENSHOT_DIR) { require('node:fs').mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); await ev(`(() => { const s = document.querySelector('#settings-modal .settings-group-pick select'); s.value = ${JSON.stringify(groups[0])}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); await new Promise(r => setTimeout(r, 300)); /* sleep-ok: the pane lays out after it is shown */ require('node:fs').writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/settings-phone-toggle-level.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
        console.log(`PASS ${seen} rows with an (i) beside their labels (${toggles} with a toggle level with it) across ${groups.length} sections; General rows ${Math.round(general)} px`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
