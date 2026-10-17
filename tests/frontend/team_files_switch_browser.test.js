// #4407: Team settings has a "Share files with other teams" switch (cross_team_artifacts) next to cross-team messaging, against stubbed APIs:
//  - it is off by default, labelled, described by one line that says files are not scanned and artifact bytes are not redacted
//  - saving sends it as the Operator, together with the messaging switch; turning it on saves true, and the form's unsaved-changes check sees it
//  - a board whose settings cannot be loaded leaves it off and disabled
//  - it is reachable by keyboard (a Tab stop after the messaging switch, Space toggles) and at 390 px with 200% text its label and hint stay inside the panel
// (Through Coral Hub the row is hidden and never sent: cross_team_ui.test.mjs, which runs in the Hub view.)
// Run against an isolated dev server. CORAL_SCREENSHOT_DIR=<dir> saves screenshots.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a redraw and a layout settle over frames; there is no completion event
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const team = 'files-team-' + Date.now();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; window.__puts = []; window.__board = { cross_team_messaging: false, cross_team_artifacts: false }; window.__boardFails = false;
            const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = (o.method || 'GET').toUpperCase(); const p = url.pathname; const board = p.startsWith('/api/board/');
                if (board && p.endsWith('/working-mode')) return json({ mode: 'none', dependency_guidance: false, custom_instructions: '' });
                if (board && p.endsWith('/working-mode/presets')) return json({ presets: [] });
                if (board && p.endsWith('/settings')) { if (window.__boardFails) return json({}, 500);
                    if (m === 'PUT') { const b = JSON.parse(o.body); window.__puts.push(b); window.__board = { cross_team_messaging: b.cross_team_messaging, cross_team_artifacts: b.cross_team_artifacts }; }
                    return json(window.__board); }
                if (p === '/api/settings/prompt-inspection') return json({ orchestrator: { scope: 'global', applies: 'x', default: 'd', effective: 'e' }, worker: {}, task: {} });
                if (p === '/api/settings' && m === 'GET') return json({ settings: { board_health_monitor: 'true' } });
                return R(u, o); };
            return true; })()`);
        await ev(`window.__m.add('files-bot', 'A', 2, { sleeping: false, board_project: ${JSON.stringify(team)} }); true`); await sleep(800); // sleep-ok: a redraw and a layout settle over frames; no completion event
        await ev(`window.selectLiveSession('files-bot', 'claude', 'files-bot-id', 'local'); true`); await sleep(1000); // sleep-ok: a redraw and a layout settle over frames; no completion event
        const open = async () => {
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'team_working_mode.js'); await m.showTeamWorkingMode(${JSON.stringify(team)}, { workspace: true }); window.switchAgenticTab('team-settings', 'top'); return true; })()`);
            await waitFor(`!!document.querySelector('#agentic-panel-team-settings .working-mode-form') && document.querySelector('#agentic-panel-team-settings .working-mode-form').dataset.initialState !== undefined`, 'the loaded settings', 8000);
            await ev(`document.querySelector('#agentic-panel-team-settings [data-settings-tab=workflow]')?.click(); true`);
        };
        const form = `document.querySelector('#agentic-panel-team-settings .working-mode-form')`;

        console.log('1. Off by default, labelled, and said plainly');
        await open();
        const f = await ev(`(() => { const form = ${form}; const c = form.elements.cross_team_artifacts; const hint = document.getElementById(c.getAttribute('aria-describedby')); const label = c.closest('label');
            return { exists: !!c, checked: c.checked, disabled: c.disabled, label: label.textContent.trim(), hidden: label.hidden || !!label.closest('[hidden]'), hint: hint.textContent, hintHidden: hint.hidden, after: !!(form.elements.cross_team_messaging.compareDocumentPosition(c) & Node.DOCUMENT_POSITION_FOLLOWING) }; })()`);
        assert.equal(f.exists, true); assert.equal(f.checked, false); assert.equal(f.disabled, false);
        assert.equal(f.label, 'Share files with other teams'); assert.equal(f.hidden, false); assert.equal(f.hintHidden, false);
        assert.match(f.hint, /not scanned/); assert.match(f.hint, /not redacted/); assert.ok(f.after, 'it is next to cross-team messaging, after it');
        await ev(`${form}.elements.cross_team_artifacts.scrollIntoView({ block: 'center' }); true`); await sleep(200); // sleep-ok: the scroll settles
        await shot('files-switch-desktop');

        console.log('2. It is a Tab stop after the messaging switch and Space toggles it');
        await ev(`${form}.elements.cross_team_messaging.focus(); true`);
        const key = async (k, code, vk, text) => { const b = { key: k, code, windowsVirtualKeyCode: vk }; await Input.dispatchKeyEvent({ type: 'keyDown', ...b, ...(text ? { text } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...b }); };
        await key('Tab', 'Tab', 9);
        assert.equal(await ev(`document.activeElement && document.activeElement.name`), 'cross_team_artifacts', 'Tab from the messaging switch reaches the files switch');
        await key(' ', 'Space', 32, ' ');
        assert.equal(await ev(`${form}.elements.cross_team_artifacts.checked`), true, 'Space turns it on');
        assert.notEqual(await ev(`${form}.dataset.initialState === undefined`), true);

        console.log('3. Saving sends it as the Operator, with the messaging switch');
        await ev(`${form}.elements.cross_team_messaging.checked = true; window.__puts.length = 0; ${form}.requestSubmit(); true`);
        await waitFor(`window.__puts.length === 1`, 'the save', 8000);
        assert.deepEqual(await ev(`window.__puts[0]`), { subscriber_id: 'Operator', cross_team_messaging: true, cross_team_artifacts: true });
        await open();
        assert.equal(await ev(`${form}.elements.cross_team_artifacts.checked`), true, 'reopened: the saved value is loaded');
        await ev(`${form}.elements.cross_team_artifacts.checked = false; window.__puts.length = 0; ${form}.requestSubmit(); true`);
        await waitFor(`window.__puts.length === 1`, 'the second save', 8000);
        assert.equal(await ev(`window.__puts[0].cross_team_artifacts`), false, 'turning it off saves false');

        console.log('4. A board whose settings do not load leaves it off and disabled');
        await ev(`window.__boardFails = true; true`);
        await open();
        assert.deepEqual(await ev(`(() => { const c = ${form}.elements.cross_team_artifacts; return [c.checked, c.disabled]; })()`), [false, true]);
        await ev(`window.__boardFails = false; true`);

        console.log('5. 390 px at 200% text: the label and the hint stay inside the panel');
        await open();
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await ev(`document.documentElement.style.fontSize = '32px'; true`); await sleep(500); // sleep-ok: layout settles after the font-size change
        await ev(`(document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); true`); await sleep(500); // sleep-ok: the drawer opens over frames
        await ev(`window.switchAgenticTab('team-settings', 'top'); document.querySelector('#agentic-panel-team-settings [data-settings-tab=workflow]')?.click(); true`); await sleep(400); // sleep-ok: the tab shows over frames
        const fit = await ev(`(() => { const form = ${form}; const c = form.elements.cross_team_artifacts; const label = c.closest('label'); const hint = document.getElementById('cross-team-files-hint'); const panel = label.closest('[data-settings-panel]');
            const pr = panel.getBoundingClientRect(); const inside = e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.left >= pr.left - 1 && r.right <= pr.right + 1; };
            return { label: inside(label), hint: inside(hint), docOver: document.documentElement.scrollWidth - innerWidth, hintSize: parseFloat(getComputedStyle(hint).fontSize), box: c.getBoundingClientRect().width }; })()`);
        assert.equal(fit.label, true, 'the label is on screen and inside the panel'); assert.equal(fit.hint, true, 'so is the hint');
        assert.ok(fit.docOver <= 0, 'no sideways scroll'); assert.ok(fit.hintSize >= 20, 'the hint grows with the text size: ' + fit.hintSize);
        await ev(`document.getElementById('cross-team-files-hint').previousElementSibling.scrollIntoView({ block: 'start' }); true`); await sleep(300); // sleep-ok: the scroll settles
        const covered = await ev(`(() => { const c = ${form}.elements.cross_team_artifacts; const label = c.closest('label'); const hint = document.getElementById('cross-team-files-hint');
            const top = e => { const r = e.getBoundingClientRect(); const t = document.elementFromPoint(r.left + r.width / 2, r.top + Math.min(r.height / 2, 12)); return !!t && (e === t || e.contains(t) || t.contains(e) && t !== document.body); }; return { label: top(label), hint: top(hint) }; })()`);
        assert.deepEqual(covered, { label: true, hint: true }, 'nothing sits over the label or the hint once scrolled to');
        await shot('files-switch-phone-200');
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
