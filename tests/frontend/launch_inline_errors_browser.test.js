// UI audit #5589, findings 25 and 4/22: a launch problem shows under its field with what to do next (the toast stays),
// and controls that do nothing or repeat are gone.
//   - a missing working directory, a team name, an agent name and a folder the server did not find: a line under the field
//     (aria-describedby), cleared by the next edit;
//   - Claude in Bypass Permissions mode: no Allow row (it pre-approves nothing); the chips say pressed or not, in groups
//     named Allow and Deny;
//   - the Remind agent button is not on a terminal;
//   - the launch dialogs say their agent type and permission mode start from Settings;
//   - the Agent Team dialog has no health check option (its home is Team settings);
//   - a build without database encryption shows no encryption row in Settings.
// Drives Chrome over CDP against the isolated dev server; every launch request is answered by the test.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const guard = await require('./helpers/settings_guard').guardSettings();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            window.__launches = []; window.__answer = { status: 200, body: { session_name: 's1', session_id: 'sid-s1' } };
            const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href), m = o.method || 'GET';
                if (p.pathname === '/api/sessions/launch' && m === 'POST') { window.__launches.push(JSON.parse(o.body)); return json(window.__answer.body, window.__answer.status); }
                if (p.pathname === '/api/board/projects') return json([]);
                if (p.pathname === '/api/system/cli-check') return json({ found: true, version: '1' });
                if (p.pathname === '/api/board/availability-team/status') return json({ observed_at: new Date().toISOString(), summary: { available: 1, unavailable: 1 }, unassigned_tasks: [], health_report: [], agents: [
                    { name: 'Dev', subscriber_id: 'Dev', agent_type: 'claude', availability: 'available', available: true, reason: 'Idle', tasks: [] },
                    { name: 'Shell', subscriber_id: 'Shell', agent_type: 'terminal', availability: 'unavailable', available: false, reason: 'Terminal, not an agent', tasks: [] }] });
                if (p.pathname === '/api/system/database-security') return json({ encryption_compiled: window.__encCompiled !== false, feature_available: window.__encCompiled !== false, saved_mode: 'disabled', effective_mode: 'disabled', unlock_surface: 'tty' });
                return real(u, o); };
            await window.showLaunchModal(); window.hideLaunchModal();
            return true; })()`);
        const shot = async name => { if (!process.env.CORAL_SCREENSHOT_DIR) return; require('node:fs').mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); await new Promise(r => setTimeout(r, 250)); // sleep-ok: a screenshot settles layout, there is no event for it
            require('node:fs').writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
        const inline = id => ev(`(() => { const f = document.getElementById(${JSON.stringify(id)}); const e = document.getElementById(${JSON.stringify(id + '-error')}); return { text: e && e.textContent, described: f && (f.getAttribute('aria-describedby') || '').split(' ').includes(${JSON.stringify(id + '-error')}), invalid: f && f.getAttribute('aria-invalid'), visible: !!e && e.getClientRects().length > 0 }; })()`);

        console.log('1. A missing working directory: a line under the field with the next step; typing clears it');
        await ev(`window.showLaunchModal(); window._selectLaunchType('agent'); true`);
        await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none'`, 'the agent step');
        await ev(`document.getElementById('launch-dir').value = ''; window.launchSession(); true`);
        let f = await inline('launch-dir');
        assert.match(f.text, /Choose a folder.*Browse/, JSON.stringify(f)); assert.ok(f.described && f.visible && f.invalid === 'true', JSON.stringify(f));
        assert.ok(!(await ev(`[...document.querySelectorAll('.toast')].some(t => /Choose a folder/.test(t.textContent))`)), 'said once: no toast repeats the line under the field (#6029)');
        assert.deepEqual(await ev(`(() => { const f = document.getElementById('launch-dir'), e = document.getElementById('launch-dir-error'); return { inLabel: f.closest('label').contains(e), nameHasError: f.closest('label').textContent.includes(e.textContent), after: f.closest('label').nextElementSibling === e }; })()`), { inLabel: false, nameHasError: false, after: true }, 'the line is after the label, so it is not part of the field name');
        await ev(`document.getElementById('launch-dir').value = '/x'; document.getElementById('launch-dir').dispatchEvent(new Event('input', { bubbles: true })); true`);
        f = await inline('launch-dir'); assert.equal(f.text, null, 'cleared by the edit'); assert.equal(f.invalid, null);
        assert.equal(await ev(`document.getElementById('launch-dir').getAttribute('aria-describedby')`), null, 'and the description with it');

        console.log('2. A folder the server does not find: its message and the way out');
        await ev(`window.__answer = { status: 400, body: { error: 'directory not found: /nonexistent/path/x', field: 'working_dir' } }; document.getElementById('launch-dir').value = '/nonexistent/path/x'; window.launchSession(); true`);
        await waitFor(`!!document.getElementById('launch-dir-error')`, 'the line under the field');
        f = await inline('launch-dir');
        assert.equal(f.text, 'Folder not found: /nonexistent/path/x. Pick one with Browse.', JSON.stringify(f));
        await shot('inline-folder-not-found');
        await ev(`window.hideLaunchModal(); true`);
        assert.equal(await ev(`!!document.getElementById('launch-dir-error')`), false, 'closing the dialog clears it');

        console.log('3. Team name and working directory in the Agent Team dialog');
        await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`);
        await waitFor(`getComputedStyle(document.getElementById('launch-step-team')).display !== 'none'`, 'the team step');
        await ev(`document.getElementById('team-board-name').value = ''; window.launchTeam(); true`);
        f = await inline('team-board-name');
        assert.match(f.text, /Type a name for the team/, JSON.stringify(f)); assert.ok(f.described && f.invalid === 'true');
        assert.equal(await ev(`!!document.getElementById('team-board-name').closest('.settings-pane')`), false, 'the name is above the sections, in view on every tab');
        assert.equal(await ev(`!!document.getElementById('launch-team-health') || /health monitor/i.test(document.getElementById('launch-step-team').textContent)`), false, 'no health check option in the launch dialog');
        await ev(`document.getElementById('team-board-name').value = 'inline-err'; window._validateTeamName(); document.getElementById('launch-team-dir').value = ''; window.launchTeam(); true`);
        assert.equal(await ev(`!!document.getElementById('launch-team-dir').closest('.settings-pane')`), false, 'the folder is above the sections (#5989), in view on every tab');
        f = await inline('launch-team-dir'); assert.match(f.text, /Choose a folder/, JSON.stringify(f));
        await ev(`window.hideLaunchModal(); true`);

        console.log('4. The launch dialogs say their defaults come from Settings');
        await ev(`window.showLaunchModal(); window._selectLaunchType('agent'); true`);
        await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none'`, 'the agent step');
        const notes = await ev(`[...document.querySelectorAll('#launch-step-agent .acf-default-note')].map(n => n.textContent.trim())`);
        assert.deepEqual(notes, [], 'no line restates the default (#6012): ' + JSON.stringify(notes));
        await ev(`window.hideLaunchModal(); window.showLaunchModal(); window._selectLaunchType('team'); true`);
        assert.deepEqual(await ev(`[...document.querySelectorAll('#launch-step-team .acf-default-note')].map(n => n.textContent.trim())`), []);
        await ev(`window.hideLaunchModal(); true`);

        console.log('4b. A new agent starts in Auto for Claude and as before for the others; a mode the person picked is kept (#5836)');
        await ev(`window.showLaunchModal(); window._selectLaunchType('agent'); document.getElementById('launch-agent-more').dispatchEvent(new CustomEvent('sd-more-open', { bubbles: true })); true`);
        await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none'`, 'the agent step');
        const pm = `document.getElementById('launch-agent-acf').querySelector('.acf-permission-mode')`;
        const setType = t => ev(`(() => { const s = document.getElementById('launch-agent-acf').querySelector('.acf-agent-type'); s.value = ${JSON.stringify(t)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await setType('claude');
        assert.equal(await ev(`${pm}.value`), 'auto', 'Claude: Auto, not a bypass of every prompt');
        await setType('codex');
        assert.equal(await ev(`${pm}.value`), 'auto', 'Codex starts in its workspace sandbox while the mode is still the default');
        await setType('claude');
        assert.equal(await ev(`${pm}.value`), 'auto', 'and Claude again');
        await ev(`(() => { const m = ${pm}; m.value = 'plan'; m.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await setType('codex');
        assert.equal(await ev(`${pm}.value`), 'plan', 'a mode the person picked stays when the agent type changes');
        await setType('claude');
        console.log('   \u2713');

        console.log('5. Bypass Permissions: no Allow row; the chips say pressed or not, in named groups');
        await ev(`window.showLaunchModal(); window._selectLaunchType('agent'); document.getElementById('launch-agent-more').dispatchEvent(new CustomEvent('sd-more-open', { bubbles: true })); true`); // New Agent opens on the folder: its sections are behind More options
        await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none'`, 'the agent step');
        const acf = `document.getElementById('launch-agent-acf')`;
        await ev(`(() => { const sel = ${acf}.querySelector('.acf-permission-mode'); sel.value = 'bypassPermissions'; sel.dispatchEvent(new Event('change', { bubbles: true })); ${acf}.querySelector('.acf-agent-type').value = 'claude'; ${acf}.querySelector('.acf-agent-type').dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await ev(`(() => { const tab = [...document.querySelectorAll('#launch-step-agent [role=tab]')].find(t => /Permissions/.test(t.textContent)); if (tab) tab.click(); return true; })()`);
        await ev(`(() => { const t = ${acf}.querySelector('.permissions-toggle-btn'); if (t && getComputedStyle(${acf}.querySelector('.permissions-editor')).display === 'none') t.click(); return true; })()`);
        const groups = () => ev(`(() => { const g = id => { const e = ${acf}.querySelector('[id$="-perms-' + id + '"]'); const grp = e.closest('.perms-group'); return { shown: grp.getClientRects().length > 0, role: e.getAttribute('role'), name: document.getElementById(e.getAttribute('aria-labelledby')).textContent.replace(/\\s+/g, ' ').trim() }; }; return { allow: g('allow'), deny: g('deny') }; })()`);
        let g = await groups();
        await shot('permissions-bypass-no-allow');
        assert.equal(g.allow.shown, false, 'Bypass Permissions: Allow pre-approves nothing, so no Allow row: ' + JSON.stringify(g));
        assert.equal(g.deny.shown, true); assert.equal(g.deny.role, 'group'); assert.match(g.deny.name, /^Deny/);
        await ev(`(() => { const sel = ${acf}.querySelector('.acf-permission-mode'); sel.value = 'default'; sel.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        g = await groups();
        assert.equal(g.allow.shown, true, 'any other mode shows Allow: ' + JSON.stringify(g)); assert.equal(g.allow.role, 'group'); assert.match(g.allow.name, /^Allow/);
        await ev(`${acf}.querySelector('[id$="-perms-allow"] .perm-chip[data-cap="file_read"]').click(); true`);
        assert.deepEqual(await ev(`[...${acf}.querySelectorAll('.perm-chip')].map(c => [c.dataset.type + ':' + c.dataset.cap, c.getAttribute('aria-pressed'), c.classList.contains('active')]).filter(([k, p, a]) => (p === 'true') !== a)`), [], 'aria-pressed always matches the chip state');
        assert.equal(await ev(`${acf}.querySelector('[id$="-perms-allow"] .perm-chip[data-cap="file_read"]').getAttribute('aria-pressed')`), 'true');
        assert.equal(await ev(`${acf}.querySelector('.perm-chip').type`), 'button');
        await ev(`(() => { const sel = ${acf}.querySelector('.acf-permission-mode'); sel.value = 'bypassPermissions'; sel.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        assert.equal((await groups()).allow.shown, false);
        assert.equal(await ev(`${acf}.querySelector('[id$="-perms-allow"] .perm-chip[data-cap="file_read"]').classList.contains('active')`), true, 'a hidden Allow row keeps its state: looking at a preset does not change it');
        // The shell patterns are Allow entries: with Shell allowed, Bypass hides the patterns field with the Allow row, and the other modes bring it back with its value.
        const patterns = () => ev(`(() => { const p = ${acf}.querySelector('.perms-shell-patterns'); return { shown: p.getClientRects().length > 0, value: p.querySelector('.perms-shell-input').value }; })()`);
        const mode = m => ev(`(() => { const sel = ${acf}.querySelector('.acf-permission-mode'); sel.value = ${JSON.stringify(m)}; sel.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await mode('default');
        await ev(`${acf}.querySelector('[id$="-perms-allow"] .perm-chip[data-cap="shell"]').click(); { const i = ${acf}.querySelector('.perms-shell-input'); i.value = 'git status'; } true`);
        assert.deepEqual(await patterns(), { shown: true, value: 'git status' }, 'Shell allowed: its patterns are asked for');
        await mode('bypassPermissions');
        assert.equal((await patterns()).shown, false, 'Bypass: no Allow row, so no patterns field either');
        await mode('acceptEdits');
        assert.deepEqual(await patterns(), { shown: true, value: 'git status' }, 'any other mode: the field is back with what was typed');
        await ev(`window.hideLaunchModal(); true`);

        console.log('6. Team view: Remind agent is on an agent, not on a terminal');
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'team_availability.js'); await m.showTeamAvailability('availability-team'); return true; })()`);
        await waitFor(`document.querySelectorAll('#team-availability-dialog .availability-agent').length === 2`, 'two agent cards');
        await waitFor(`document.querySelectorAll('#team-availability-dialog .availability-agent button').length >= 1`, 'the reminder buttons');
        const remind = await ev(`[...document.querySelectorAll('#team-availability-dialog .availability-agent')].map(c => [c.querySelector('h3, strong, .availability-agent-heading')?.textContent.trim().split(/\\s/)[0], [...c.querySelectorAll('button')].map(b => b.textContent)])`);
        assert.deepEqual(remind.map(r => r[1]), [['Remind agent'], []], 'a button on the agent, none on the terminal: ' + JSON.stringify(remind));
        await ev(`document.getElementById('team-availability-dialog').close(); true`);

        console.log('7. A build without database encryption shows no encryption row');
        await ev(`window.__encCompiled = false; (async () => { await window.showSettingsModal('security'); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings');
        await waitFor(`document.getElementById('settings-database-encryption').closest('.settings-row').hidden === true`, 'the encryption row to be hidden', 8000);
        assert.equal(await ev(`document.getElementById('settings-database-encryption').closest('.settings-row').getClientRects().length`), 0);
        await ev(`window.hideSettingsModal(); window.__encCompiled = true; (async () => { await window.showSettingsModal('security'); return true; })()`);
        await waitFor(`document.getElementById('settings-database-encryption').closest('.settings-row').hidden === false`, 'a build with it shows the row', 8000);
        await ev(`window.hideSettingsModal(); true`);
        console.log('   ✓');
    } finally { await guard.restore?.(); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
