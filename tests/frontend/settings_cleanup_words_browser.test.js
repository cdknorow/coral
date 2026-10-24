// Settings > Storage and cleanup (#7041, UI/UX's #7036 finding 5): "Automatic cleanup [0] days [Saved diffs]" never said that 0 is off, and its select looked live while it did nothing; the two
// selects (what Free space prunes, what Automatic cleanup prunes) were named only for a screen reader, so a sighted person read "Saved diffs" with no question. Now: 0 reads "Off" and the kind
// select is off with it; each select has a visible label, and the cleanup's days field says "Older than". Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled(mobile ? { enabled: true, maxTouchPoints: 5 } : { enabled: false });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`window.showSettingsModal('cleanup'); true`);
            await waitFor(`document.getElementById('settings-retention-days') && document.getElementById('settings-retention-days').dataset.loaded === '1'`, 'automatic cleanup loaded', 10000);
            const tag = `${w} px`;
            const off = await ev(`(() => ({ status: document.getElementById('settings-retention-status').textContent.trim(), kindDisabled: document.getElementById('settings-retention-kind').disabled, days: document.getElementById('settings-retention-days').value }))()`);
            assert.equal(off.days, '0', `${tag}: automatic cleanup starts off`);
            assert.match(off.status, /^Off\b/, `${tag}: 0 days says Off: ` + JSON.stringify(off));
            assert.equal(off.kindDisabled, true, `${tag}: the kind select is off with it`);
            await ev(`(() => { const d = document.getElementById('settings-retention-days'); d.value = '7'; d.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
            await waitFor(`!document.getElementById('settings-retention-kind').disabled`, 'the kind select on', 3000);
            assert.ok(!/^Off\b/.test(await ev(`document.getElementById('settings-retention-status').textContent.trim()`)), `${tag}: 7 days is no longer "Off"`);
            // each control says what it is where a person can see it
            for (const id of ['settings-prune-days', 'settings-prune-kind', 'settings-retention-days', 'settings-retention-kind']) {
                const labelled = await ev(`(() => { const c = document.getElementById('${id}'); return [...c.labels].filter(l => l.getClientRects().length && l.textContent.trim()).map(l => l.textContent.trim().replace(/\\s+/g, ' ')); })()`);
                assert.ok(labelled.length > 0, `${tag}: #${id} has a visible label: ` + JSON.stringify(labelled));
                if (id === 'settings-retention-days') assert.ok(labelled.some(t => /older than/i.test(t)), `${tag}: the cleanup's days say "Older than": ` + JSON.stringify(labelled));
                if (id.endsWith('-kind')) assert.ok(labelled.some(t => /what to prune/i.test(t)), `${tag}: #${id} is labelled "What to prune": ` + JSON.stringify(labelled));
            }
        }
        console.log('PASS settings cleanup words');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
