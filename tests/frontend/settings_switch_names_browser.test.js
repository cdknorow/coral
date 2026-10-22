// Every switch in Settings has an accessible name when its row is visible, and its description adds something the label does not say (#5708, from #5687's notes):
// the name and description are read from the browser's accessibility tree, not from the markup. Rows that are hidden until a state holds (the Hub switches, the
// terminal backend's auto-wake) are made visible first. CAPTURE_OUT=<file> writes the table. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const STOP = new Set(['a', 'an', 'the', 'of', 'to', 'in', 'on', 'for', 'by', 'or', 'and', 'with', 'this', 'that', 'is', 'are', 'it']);
const words = s => (s.toLowerCase().match(/[a-z0-9]+/g) || []).filter(w => !STOP.has(w));
// A description repeats its label when most of the label's words are in how it opens (its first dozen words, after "On by default." or "Off by default."):
// a long description may use a label word further in without echoing the label.
const repeats = (label, desc) => {
    const opening = desc.replace(/^(on|off) by default[^.]*\.\s*/i, '').split(/\s+/).slice(0, 12).join(' ');
    const l = words(label), d = new Set(words(opening));
    return l.length > 0 && l.filter(w => d.has(w)).length / l.length >= 0.6;
};
// Labels whose description is the state or a hint the label cannot hold, reviewed: none yet.
const REPEAT_OK = new Set([]);

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Accessibility, DOM } = client;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        const statusReads = () => ev(`performance.getEntriesByType('resource').filter(e => e.name.includes('/api/cloud/status')).length`);
        const readsBefore = await statusReads();
        await ev(`(async () => { await window.showSettingsModal(); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings opens', 10000);
        // The Hub block re-hides its rows while it reads the status after Settings opens: wait for that first render, then show every pane and every row that is hidden until a state holds, so each switch is in the accessibility tree.
        await waitFor(`!!document.querySelector('#settings-cloud[data-cloud-bound]')`, 'the Hub block is bound', 10000);
        // data-cloud-bound is set when the block is built, before its status is read: the render that re-hides the rows follows the status answer, which Resource Timing reports once it has arrived.
        await waitFor(`performance.getEntriesByType('resource').filter(e => e.name.includes('/api/cloud/status')).length > ${readsBefore}`, 'the Hub block read its status', 10000);
        await ev(`new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))`); // the render that follows the answer has run
        const reveal = () => ev(`(() => { const m = document.getElementById('settings-modal'); for (const e of m.querySelectorAll('[hidden]')) { if (e.querySelector('input[type=checkbox]') || e.matches('input[type=checkbox]') || e.closest('[data-cloud], .settings-cloud-switch-row, #tb-autowake')) e.hidden = false; }
            for (const e of m.querySelectorAll('.settings-pane')) { e.hidden = false; e.style.display = 'block'; } for (const id of ['tb-autowake', 'settings-terminal-backend']) { const e = document.getElementById(id); if (e) { e.hidden = false; e.style.display = 'block'; } }
            for (const e of m.querySelectorAll('[hidden]')) if (e.contains(m.querySelector('input[type=checkbox]:not([data-x])')) === false && e.querySelector('input[type=checkbox]')) e.hidden = false; return true; })()`);
        await reveal();
        await Accessibility.enable(); await DOM.enable();
        const { root } = await DOM.getDocument({ depth: 0 });
        const ids = await ev(`[...document.querySelectorAll('#settings-modal input[type=checkbox]')].map((e, i) => { e.setAttribute('data-ax-probe', i); return e.id || e.name || ('#' + i); })`);
        const rows = [];
        for (let i = 0; i < ids.length; i++) {
            const { nodeId } = await DOM.querySelector({ nodeId: root.nodeId, selector: `#settings-modal [data-ax-probe="${i}"]` });
            const { nodes } = await Accessibility.getPartialAXTree({ nodeId, fetchRelatives: false });
            const n = nodes[0] || {};
            rows.push({ id: ids[i], ignored: !!n.ignored, name: ((n.name || {}).value || '').trim(), desc: ((n.description || {}).value || '').trim() });
        }
        if (process.env.CAPTURE_OUT) fs.writeFileSync(process.env.CAPTURE_OUT, rows.map(r => `${r.id.padEnd(36)} ${r.ignored ? 'IGNORED ' : ''}name=${JSON.stringify(r.name)}\n${' '.repeat(36)} desc=${JSON.stringify(r.desc.slice(0, 160))}`).join('\n') + '\n');
        const named = rows.filter(r => !r.ignored);
        assert.deepEqual(rows.filter(r => r.ignored).map(r => r.id), [], `every switch is in the accessibility tree (${named.length} of ${rows.length}); one that is out of it is never checked`);
        assert.ok(named.length >= 20, `the switches are in the accessibility tree (${named.length} of ${rows.length})`);
        const nameless = named.filter(r => !r.name).map(r => r.id);
        assert.deepEqual(nameless, [], 'every visible switch has an accessible name');
        const echoes = named.filter(r => r.desc && !REPEAT_OK.has(r.id) && repeats(r.name, r.desc)).map(r => `${r.id}: "${r.name}" / "${r.desc.slice(0, 80)}"`);
        assert.deepEqual(echoes, [], 'no description repeats its label');
        console.log('   ✓', named.length, 'switches named; descriptions add to their labels');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
