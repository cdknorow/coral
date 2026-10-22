// The Working mode list in Team settings says what the chosen mode means (#5760): a one-line description under it, read from the preset the server lists (its `description`),
// tied to the list by aria-describedby so a screen reader reads it with the list. None, Shared checkout, Worktrees and Merge queue have one; a team's own preset has none, and the
// line is hidden (not an empty paragraph). Drives Chrome over CDP against the isolated dev server, at 1280 and 390 px.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const TEAM = 'wm-desc-' + Date.now().toString(36);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            console.log(`${w} px`);
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            const call = (m, p, body) => ev(`fetch('/api/board/${TEAM}/${p}', { method: ${JSON.stringify(m)}, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(${JSON.stringify(body)}) }).then(r => r.status)`);
            assert.ok(await call('POST', 'subscribe', { subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' }) < 300);
            if (!mobile) assert.equal(await call('POST', 'working-mode/presets', { id: 'mine', name: 'Mine', instructions: 'My way.', subscriber_id: 'Boss' }), 201);
            await ev(`import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
            await waitFor(`!!document.querySelector('form.working-mode-form') && document.querySelector('form.working-mode-form').dataset.initialState !== undefined`, 'the Team settings', 15000);
            await waitFor(`[...document.querySelector('form.working-mode-form select[name=mode]').options].some(o => o.value === 'merge_queue')`, 'the presets in the list', 15000);
            const read = id => ev(`(() => { const f = document.querySelector('form.working-mode-form'); const sel = f.elements.mode; sel.value = ${JSON.stringify(id)}; sel.dispatchEvent(new Event('change', { bubbles: true })); const d = document.getElementById(sel.getAttribute('aria-describedby')); const r = d.getBoundingClientRect(); return { text: d.textContent, hidden: d.hidden || !d.getClientRects().length, right: r.right, vw: innerWidth, h: Math.round(r.height) }; })()`);
            const want = { none: 'No extra guidance.', shared_checkout: 'Everyone works in one checkout; agents coordinate who edits which files.', worktrees: 'Each agent works in its own Git worktree and branch from the agreed base.', merge_queue: 'Agents hand in patches for review; one role lands them with coral-board queue.' };
            for (const [id, text] of Object.entries(want)) {
                const r = await read(id);
                assert.equal(r.text, text, `${id}: the line says what the mode means`);
                assert.ok(!r.hidden && r.right <= r.vw + 1, `${id}: the line shows and fits the window: ${JSON.stringify(r)}`);
            }
            const link = await ev(`(() => { const s = document.querySelector('form.working-mode-form select[name=mode]'); const id = s.getAttribute('aria-describedby'); return { id, ok: !!id && document.querySelectorAll('#' + id).length === 1 }; })()`);
            assert.ok(link.ok, 'the list is described by exactly one element: ' + JSON.stringify(link));
            if (!mobile) { const r = await read('mine'); assert.equal(r.text, '', 'a team\'s own preset has no description'); assert.ok(r.hidden, 'and the line is hidden, not an empty paragraph'); }
            if (process.env.CORAL_SCREENSHOT_DIR) { // CORAL_SCREENSHOT_DIR=<dir>: the Workflow tab with Merge queue chosen, for a look
                await read('merge_queue'); require('node:fs').mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
                require('node:fs').writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/working-mode-${w}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
            }
            // choosing a preset in Edit presets changes the mode, and the line follows
            await ev(`(() => { const p = document.querySelector('form.working-mode-form select[name=preset]'); p.value = 'worktrees'; p.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            assert.equal(await ev(`document.getElementById('working-mode-desc').textContent`), want.worktrees, 'the line follows the preset chosen for editing');
            { // the description and the Workflow tab hold 4.5:1 in both themes, the real light one included (#5781)
                const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
                await installAudit(ev); await read('merge_queue');
                for (const theme of ['dark', 'light']) {
                    await setTheme(ev, theme);
                    const au = await auditRegion(ev, ['#team-settings-workflow']);
                    assert.ok(au.checked > 0 && !au.fails.length, `${w} px, ${theme}: the Workflow tab holds 4.5:1: ${JSON.stringify(au.fails).slice(0, 500)}`);
                    if (process.env.CORAL_SCREENSHOT_DIR) require('node:fs').writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/working-mode-${w}-${theme}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
                }
                await setTheme(ev, 'dark');
            }
            await ev(`document.querySelectorAll('dialog[open]').forEach(d => d.close()); true`);
        }
        console.log('   ✓');
    } finally { await removeBoards(ev, [TEAM]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
