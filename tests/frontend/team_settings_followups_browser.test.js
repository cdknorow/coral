// Team settings follow-ups (UI audit #5589, finding 13, and the #5611 review):
//   - Prompt inspection starts short: the effective system and action texts are shown, the other blocks (defaults, override)
//     are closed details, and nothing scrolls inside the page; Copy still takes every text of the role;
//   - on a phone the Save footer is one short row, and nothing the person scrolls to or focuses sits under it
//     (the end of the Workflow tab, and the Edit presets editor).
// Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const TEAM = 'ts-phone-' + Date.now().toString(36);
    const shot = async name => { if (!process.env.CORAL_SCREENSHOT_DIR) return; fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); await new Promise(r => setTimeout(r, 250)); // sleep-ok: a screenshot settles layout, there is no event for it
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const open = async (w, h, mobile) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: mobile ? 2 : 1, mobile });
            await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            const sub = await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' }) }).then(r => r.status)`);
            assert.ok(sub < 300);
            await ev(`import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
            await waitFor(`!!document.querySelector('form.working-mode-form') && document.querySelector('form.working-mode-form').dataset.initialState !== undefined`, 'the Team settings', 15000);
        };

        console.log('1. Prompt inspection starts short');
        await open(1280, 900, false);
        await ev(`document.querySelector('[data-settings-tab=prompts]').click(); true`);
        await waitFor(`!!document.querySelector('.prompt-inspection-role')`, 'the inspection', 15000);
        const pi = await ev(`(() => { const c = document.querySelector('.prompt-inspection-card:not([hidden])'); const vis = e => e.checkVisibility({ checkVisibilityCSS: true });
            return { shown: [...c.querySelectorAll('section.prompt-inspection-fragment h5')].map(h => h.textContent), folded: [...c.querySelectorAll('details.prompt-inspection-folded')].map(d => [d.querySelector('summary').textContent, d.open]),
                boxes: [...document.querySelectorAll('.prompt-inspection-content pre')].filter(p => getComputedStyle(p).overflowY !== 'visible').length,
                hiddenPre: [...c.querySelectorAll('details.prompt-inspection-folded pre')].filter(p => vis(p)).length }; })()`);
        assert.deepEqual(pi.shown, ['Effective system', 'Effective action']);
        assert.deepEqual(pi.folded, [['System default', false], ['Action default', false], ['Override', false]]);
        assert.equal(pi.boxes, 0); assert.equal(pi.hiddenPre, 0, 'the folded blocks are closed');
        await ev(`window.__copied = []; Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: t => { window.__copied.push(t); return Promise.resolve(); } } }); document.querySelector('.prompt-inspection-copy').click(); true`);
        await waitFor(`window.__copied.length === 1`, 'one copy', 5000);
        const copied = await ev(`window.__copied[0]`);
        for (const t of ['Effective system', 'Effective action', 'System default', 'Action default', 'Override']) assert.ok(copied.includes(t), `Copy takes the folded text too: ${t}`);
        await ev(`document.querySelector('.prompt-inspection-folded > summary').click(); true`);
        assert.equal(await ev(`document.querySelector('.prompt-inspection-folded').open`), true, 'a folded block opens');
        assert.equal(await ev(`[...document.querySelectorAll('.prompt-inspection-content pre')].filter(p => getComputedStyle(p).overflowY !== 'visible').length`), 0, 'still no scroll box');
        await shot('prompts-desktop');

        console.log('2. On a phone: a short Save footer, and nothing under it');
        await open(390, 844, true);
        const foot = await ev(`(() => { const f = document.querySelector('.team-settings-actions'); const r = f.getBoundingClientRect(); const sp = f.querySelector('span'); return { h: Math.round(r.height), caption: getComputedStyle(sp).whiteSpace, title: sp.title.length > 20, wrap: r.width }; })()`);
        assert.ok(foot.h <= 64, 'the footer is one short row: ' + JSON.stringify(foot)); assert.equal(foot.caption, 'nowrap'); assert.ok(foot.title, 'the whole caption stays in its title');
        const wide = await ev(`(() => { const f = document.querySelector('.working-mode-form'); const over = [...f.querySelectorAll('*')].filter(e => e.getClientRects().length && e.getBoundingClientRect().right > f.getBoundingClientRect().right + 1).slice(0, 4).map(e => e.tagName + '.' + (e.className || e.name || '')); return { sw: f.scrollWidth, cw: f.clientWidth, over }; })()`);
        assert.ok(wide.sw <= wide.cw + 1, 'the form does not scroll sideways on a phone: ' + JSON.stringify(wide));
        // the end of the Workflow tab, and the Edit presets editor: the last control scrolled to is above the footer, not under it
        await ev(`document.querySelector('.preset-editor > summary').click(); true`);
        const covered = async sel => ev(`(() => { const el = document.querySelector(${JSON.stringify(sel)}); el.focus(); el.scrollIntoView({ block: 'nearest' }); const r = el.getBoundingClientRect(); const f = document.querySelector('.team-settings-actions').getBoundingClientRect(); const top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); return { onTop: top === el || el.contains(top), above: r.bottom <= f.top + 1 }; })()`);
        for (const sel of ['.preset-editor > summary', '.preset-save']) { const c = await covered(sel); assert.ok(c.onTop, `${sel} is not under the footer: ` + JSON.stringify(c)); }
        await ev(`document.querySelector('.preset-save').focus(); document.querySelector('.working-mode-form').scrollTop = 1e6; true`);
        const end = await ev(`(() => { const last = [...document.querySelectorAll('#team-settings-workflow .preset-editor > *')].pop().getBoundingClientRect(); const f = document.querySelector('.team-settings-actions').getBoundingClientRect(); return { lastBottom: Math.round(last.bottom), footTop: Math.round(f.top) }; })()`);
        assert.ok(end.lastBottom <= end.footTop + 1, 'scrolled to the end, the last line is above the footer: ' + JSON.stringify(end));
        await shot('workflow-phone-end');
        console.log('   ✓');
    } finally { await removeBoards(ev, [TEAM]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
