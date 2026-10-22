// Team settings follow-ups (UI audit #5589, finding 13, and the #5611 review):
//   - Prompt inspection starts short: all effective texts, defaults and overrides start in closed details, and nothing scrolls inside the page; Copy still takes every text of the role;
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
    const { Input } = client;
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
        assert.deepEqual(pi.shown, []);
        assert.deepEqual(pi.folded, [['Effective system', false], ['Effective action', false], ['System default', false], ['Action default', false], ['Override', false]]);
        assert.equal(pi.boxes, 0); assert.equal(pi.hiddenPre, 0, 'the folded blocks are closed');
        assert.equal(await ev(`document.querySelector('.team-settings-actions').hidden`), true, 'read-only tab has no Save footer');
        const metadata = await ev(`(() => { const b = document.querySelector('.prompt-inspection-card:not([hidden]) .settings-info-btn'); const box = document.getElementById(b.getAttribute('aria-controls')); const before = box.hidden; b.click(); const result = {before, expanded:b.getAttribute('aria-expanded'), hidden:box.hidden, text:box.textContent}; b.click(); return result; })()`);
        assert.equal(metadata.before, true); assert.equal(metadata.expanded, 'true'); assert.equal(metadata.hidden, false);
        assert.ok(metadata.text.includes('Scope:') && metadata.text.includes('Applies:'));
        await ev(`window.__copied = []; Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: t => { window.__copied.push(t); return Promise.resolve(); } } }); document.querySelector('.prompt-inspection-copy').click(); true`);
        await waitFor(`window.__copied.length === 1`, 'one copy', 5000);
        const copied = await ev(`window.__copied[0]`);
        for (const t of ['Effective system', 'Effective action', 'System default', 'Action default', 'Override']) assert.ok(copied.includes(t), `Copy takes the folded text too: ${t}`);
        await ev(`document.querySelector('.prompt-inspection-folded > summary').click(); true`);
        assert.equal(await ev(`document.querySelector('.prompt-inspection-folded').open`), true, 'a folded block opens');
        assert.equal(await ev(`[...document.querySelectorAll('.prompt-inspection-content pre')].filter(p => getComputedStyle(p).overflowY !== 'visible').length`), 0, 'still no scroll box');
        // Copy preserves the complete body of every role even while all its sections are folded.
        for (const role of ['orchestrator', 'worker', 'task']) {
            const expected = await ev(`(() => { const select = document.querySelector('.prompt-inspection-role'); select.value = '${role}'; select.dispatchEvent(new Event('change', {bubbles:true})); const card = document.querySelector('.prompt-inspection-card:not([hidden])'); card.querySelectorAll('details').forEach(d => d.open = false); return [...card.querySelectorAll('pre')].map(p => p.textContent); })()`);
            await ev(`document.querySelector('.prompt-inspection-copy').click();true`);
            await waitFor(`window.__copied.length === ${role === 'orchestrator' ? 2 : role === 'worker' ? 3 : 4}`, 'role copy');
            const text = await ev(`window.__copied.at(-1)`);
            for (const body of expected) assert.ok(text.includes(body), `Copy preserves ${role} body`);
        }
        assert.equal(await ev(`document.querySelector('.prompt-inspection-card:not([hidden]) .prompt-inspection-meta span').textContent`), 'Applies on first task claim.');
        await ev(`document.querySelector('.prompt-inspection-card:not([hidden]) .settings-info-btn').focus();true`);
        await Input.dispatchKeyEvent({type:'keyDown',key:'Enter',code:'Enter',windowsVirtualKeyCode:13,text:'\r'});
        await Input.dispatchKeyEvent({type:'keyUp',key:'Enter',code:'Enter',windowsVirtualKeyCode:13});
        assert.equal(await ev(`document.activeElement.getAttribute('aria-expanded')`), 'true', 'info opens from keyboard');
        await shot('prompts-desktop');

        console.log('2. On a phone: a short Save footer, and nothing under it');
        await open(390, 844, true);
        const foot = await ev(`(() => { const f = document.querySelector('.team-settings-actions'); const r = f.getBoundingClientRect(); return { h: Math.round(r.height), wrap: r.width }; })()`);
        assert.ok(foot.h <= 64, 'the footer is short: ' + JSON.stringify(foot)); 
        assert.equal(await ev(`[...document.querySelector('.working-mode-form > .team-settings-actions').children].filter(c => c.tagName === 'SPAN').length`), 0, 'no caption beside the Save button: it says what it saves (#5998)');
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
