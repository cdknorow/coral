// UI audit #5589, findings 9, 10 and 12: Team settings uses the room it has and has one scroll.
//   - the side panel is clamp(420px, 30vw, 640px) wide until the person drags it (a saved width is kept);
//   - Workflow: Team working mode first; the preset editor is behind "Edit presets", closed to begin with, and opens;
//   - Prompt inspection: one read-only page: effective texts, defaults and overrides folded (closed), no scroll boxes inside it, one role at a time (a select),
//     and Copy copies the text of the role shown.
// Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const TEAM = 'ts-layout-' + Date.now().toString(36);
    const shot = async name => { if (!process.env.CORAL_SCREENSHOT_DIR) return; fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); await new Promise(r => setTimeout(r, 250)); // sleep-ok: a screenshot settles layout, there is no event for it
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const load = async (w, h, mobile = false) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
        };

        console.log('1. The side panel: clamp(420px, 30vw, 640px) by default');
        // The panel is only drawn with an agent or a team open: a probe with the panel's own class gets the stylesheet's width (a width the person dragged is an inline style set by sidebar.js, which is not changed).
        for (const [w, h, want] of [[1280, 800, 420], [1920, 1080, 576], [2560, 1440, 640]]) {
            await load(w, h);
            const got = await ev(`(() => { const p = document.createElement('div'); p.className = 'agentic-state'; p.style.cssText = 'position:fixed;visibility:hidden;top:0;left:0'; document.body.append(p); const x = Math.round(p.getBoundingClientRect().width); p.remove(); return x; })()`);
            assert.ok(Math.abs(got - want) <= 2, `at ${w} the panel is ${want}, found ${got}`);
        }

        console.log('2. Workflow: the working mode first, the preset editor behind Edit presets');
        await load(1280, 900);
        const sub = await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' }) }).then(r => r.status)`);
        assert.ok(sub < 300);
        await ev(`import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
        await waitFor(`!!document.querySelector('form.working-mode-form') && document.querySelector('form.working-mode-form').dataset.initialState !== undefined`, 'the Team settings', 15000);
        const wf = await ev(`(() => { const sections = [...document.querySelectorAll('#team-settings-workflow .team-settings-section')].map(s => s.tagName.toLowerCase() + ':' + (s.querySelector('legend, summary')?.textContent.trim())); const ed = document.querySelector('.preset-editor'); const vis = e => e.checkVisibility({ checkVisibilityCSS: true });
            return { sections, open: ed.open, nameVisible: vis(document.querySelector('[name=preset_name]')), modeVisible: vis(document.querySelector('[name=mode]')) }; })()`);
        assert.deepEqual(wf.sections, ['fieldset:Team working mode', 'details:Edit presets'], JSON.stringify(wf));
        assert.equal(wf.open, false); assert.equal(wf.nameVisible, false, 'the preset editor is closed'); assert.equal(wf.modeVisible, true);
        await shot('team-settings-workflow-closed');
        await ev(`document.querySelector('.preset-editor > summary').click(); true`);
        assert.equal(await ev(`document.querySelector('[name=preset_name]').checkVisibility({ checkVisibilityCSS: true })`), true, 'Edit presets opens the editor');
        await shot('team-settings-workflow-open');

        console.log('3. Prompt inspection: one read-only page, one role at a time, Copy');
        await ev(`document.querySelector('[data-settings-tab=prompts]').click(); true`);
        await waitFor(`!!document.querySelector('.prompt-inspection-role')`, 'the inspection', 15000);
        const pi = await ev(`(() => ({ details: [...document.querySelectorAll('.prompt-inspection-content details')].filter(d => d.open || !d.classList.contains('prompt-inspection-folded')).length, boxes: [...document.querySelectorAll('.prompt-inspection-content pre')].filter(p => getComputedStyle(p).overflowY !== 'visible').length,
            shown: [...document.querySelectorAll('.prompt-inspection-card')].map(c => !c.hidden), roles: [...document.querySelectorAll('.prompt-inspection-role option')].map(o => o.textContent) }))()`);
        assert.equal(pi.details, 0, 'only the folded blocks are details, and they start closed: ' + JSON.stringify(pi)); assert.equal(pi.boxes, 0, 'no scroll box inside the page'); assert.deepEqual(pi.shown, [true, false, false]);
        assert.deepEqual(pi.roles, ['Orchestrator', 'Agent / worker', 'Task defaults']);
        await ev(`(() => { const s = document.querySelector('.prompt-inspection-role'); s.value = 'task'; s.dispatchEvent(new Event('change', { bubbles: true })); })()`);
        assert.deepEqual(await ev(`[...document.querySelectorAll('.prompt-inspection-card')].map(c => !c.hidden)`), [false, false, true]);
        await ev(`window.__copied = []; Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: t => { window.__copied.push(t); return Promise.resolve(); } } }); document.querySelector('.prompt-inspection-copy').click(); true`);
        await waitFor(`window.__copied.length === 1`, 'one copy', 5000);
        const copied = await ev(`window.__copied[0]`);
        assert.ok(/Task defaults/.test(copied) && /Effective composed task preview/.test(copied), 'Copy takes the role shown: ' + copied.slice(0, 80));
        assert.ok(!/System default/.test(copied), 'and only that role');
        await waitFor(`[...document.querySelectorAll('.toast .toast-message')].some(t => /^Copied$/.test(t.textContent.trim()))`, 'the Copied toast', 5000);
        await shot('team-settings-prompts');
        console.log('   ✓');
    } finally { await removeBoards(ev, [TEAM]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
