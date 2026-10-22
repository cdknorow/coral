// The Health check tab of Team settings (#4230), against the real server's step 2 API (be-4175): the tab and its keyboard order, every control
// labelled, the limits as the inputs' limits, the defaults, saving through the real PUT (and what the server stored), a number out of range
// refused before any request, a rule's switch disabling its numbers, Reset to defaults not saving by itself, unsaved changes guarded, the main
// Save bar hidden on this tab, a server refusal shown in words, no tab when the GET leaves "health" out (through Coral Hub), the held-delivery
// card when the server returns held_notice, and no sideways scroll on a phone or at 200% text. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a layout and a redraw settle over frames; there is no completion event
    const key = async (k, code, vk) => { for (const type of ['keyDown', 'keyUp']) await Input.dispatchKeyEvent({ type, key: k, code, windowsVirtualKeyCode: vk }); };
    const ok = name => console.log(`   ✓ ${name}`);
    const TEAM = 'health-team-' + Date.now().toString(36) + Math.floor(Math.random() * 1296).toString(36); // one board per run: a repeat on the same server (--stress) must not see the last run's saved recipient
    const P = `#team-settings-health`;
    const api = (path, opts) => ev(`fetch('/api/board/${TEAM}${path}', ${JSON.stringify(opts || {})}).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        // A team with two members, through the real API.
        for (const [id, title] of [['Boss', 'Orchestrator'], ['Dev One', 'Developer']]) {
            const r = await api('/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: id, job_title: title, session_name: id.toLowerCase().replace(' ', '-') + '-session' }) });
            assert.ok(r.status < 300, `subscribe ${id}: ${JSON.stringify(r)}`);
        }
        const open = async () => {
            await ev(`document.getElementById('team-settings-workspace')?.remove(); import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
            await waitFor(`!!document.querySelector('[data-settings-tab=health]')`, 'the Health check tab', 15000);
            await waitFor(`!!document.querySelector('[data-settings-tab=messaging]')`, 'the Messaging tab (drawn after the board\'s cross-team state is read, #5516)', 15000); // so the tab order read below is the settled one
        };
        // The dialog can be drawn again after the Health check tab first appears (the page's settings arrive after it): a click at that instant finds no tab (null),
        // or lands on the one about to be replaced, and the panel's form is drawn only after the board's settings are read, so a read of it straight after finds null. So click in the same evaluation that finds the tab, until the panel and its form are showing (#4992: TypeError null.click and null.elements, ~10%).
        const clickHealthTab = () => waitFor(`(() => { const t = document.querySelector('[data-settings-tab=health]'); if (!t) return false; t.click(); const p = document.getElementById('team-settings-health'); return !!p && !p.hidden && !!p.querySelector('form'); })()`, 'the Health check tab clicked and its panel and form showing', 15000);
        await open();

        // 1. the tab: named, in the tablist, panel linked, roving order includes it
        const tab = await ev(`(() => { const t = document.querySelector('[data-settings-tab=health]'); const p = document.getElementById(t.getAttribute('aria-controls')); return { role: t.getAttribute('role'), text: t.textContent, ctl: !!p, panelRole: p.getAttribute('role'), label: p.getAttribute('aria-labelledby'), tabIndex: t.tabIndex, hidden: p.hidden, order: [...document.querySelectorAll('[role=tablist] [role=tab][data-settings-tab]')].map(x => x.dataset.settingsTab) }; })()`);
        assert.deepEqual(tab, { role: 'tab', text: 'Health check', ctl: true, panelRole: 'tabpanel', label: 'team-settings-tab-health', tabIndex: -1, hidden: true, order: ['workflow', 'prompts', 'health', 'messaging'] });
        await ev(`document.querySelector('[data-settings-tab=prompts]').focus(); true`);
        await key('ArrowRight', 'ArrowRight', 39);
        assert.equal(await ev(`document.activeElement.dataset.settingsTab`), 'health', 'ArrowRight from Prompt inspection reaches Health check');
        await key('Home', 'Home', 36); await key('End', 'End', 35);
        assert.equal(await ev(`document.activeElement.dataset.settingsTab`), 'messaging', 'End reaches the last tab, Messaging (#5516)');
        await clickHealthTab();
        const shown = await ev(`(() => ({ panel: !document.getElementById('team-settings-health').hidden, mainForm: document.querySelector('form.working-mode-form').hidden, mainSave: !!document.querySelector('form.working-mode-form [type=submit]')?.getClientRects().length }))()`);
        assert.deepEqual(shown, { panel: true, mainForm: true, mainSave: false });
        ok('a Health check tab after Global, reached by the arrow keys; the main Save bar is hidden on it');

        // 2. defaults, labels and the server's limits as the inputs' limits
        const form = await ev(`(() => { const f = document.querySelector('${P} form'); const v = n => f.elements[n]; const nums = [...f.querySelectorAll('input[type=number]')].map(i => [i.name, i.value, i.min, i.max]);
            const unlabelled = [...f.querySelectorAll('input, select')].filter(e => !(e.closest('label') || e.getAttribute('aria-label'))).map(e => e.name);
            return { on: v('on').checked, allClear: v('all_clear').checked, rcpt: v('recipient').value, opts: [...v('recipient').options].map(o => o.value + '=' + o.textContent), nums, unlabelled, rev: v('default_reviewers').value, rules: ['auto_delegate', 'claim_nudge', 'status_nudge', 'mention_renudge'].map(r => v(r + '_on').checked), held: !!document.querySelector('[data-rule=held_notice]'), save: f.querySelector('[type=submit]').disabled }; })()`);
        assert.equal(form.on, false, 'a new team has the check off (#4962)'); assert.equal(form.allClear, true); assert.equal(form.rcpt, '');
        assert.deepEqual(form.nums, [['interval_min', '10', '5', '1440'], ['claim_nudge_after_min', '10', '5', '1440'], ['claim_nudge_nudges', '2', '1', '5'], ['status_nudge_after_min', '20', '5', '1440'], ['status_nudge_escalate_after_min', '10', '5', '1440'], ['held_notice_after_min', '10', '5', '1440'], ['mention_renudge_after_min', '10', '5', '1440']]); // the server returns held_notice since Lead's #4225: its card is in the defaults
        assert.deepEqual(form.unlabelled, [], 'every control has a label');
        assert.deepEqual(form.rules, [true, true, true, true]);
        assert.ok(form.opts.some(o => o.startsWith('Boss=')) && form.opts.some(o => o.startsWith('Dev One=')) && form.opts[0] === '=The orchestrator (default)', JSON.stringify(form.opts));
        assert.equal(form.held, true, 'the held-delivery card shows: the server returns held_notice (#4225)');
        assert.equal(form.save, true, 'Save starts disabled');
        ok('defaults from the server, limits 5 to 1440 minutes and 1 to 5 nudges on the inputs, every control labelled, members offered as recipients');
        // The fields' accessible names and descriptions as the browser computes them (#5790: the number boxes and the reviewers box come from field_row.js): the label and the unit name a number, the limits describe it.
        await ev(`document.querySelector('${P} details').open = true; true`);
        await waitFor(`!!document.querySelector('${P} [name=claim_nudge_after_min]')?.getClientRects().length`, 'the folded rules showing', 5000);
        const axNodes = (await client.Accessibility.getFullAXTree()).nodes.filter(n => !n.ignored && ['spinbutton', 'combobox', 'textbox'].includes(n.role && n.role.value));
        const axPairs = axNodes.map(n => [n.name && n.name.value, n.description && n.description.value]).filter(([name]) => name);
        assert.deepEqual(axPairs.filter(([name]) => /min|nudges|^Default reviewers$/.test(name)).map(([name, d]) => [name, /^Names of agents/.test(d) ? 'Names of agents' : d]), [
            ['Every minutes', '5 to 1440'], ['Default reviewers', 'Names of agents'], ['After minutes idle', '5 to 1440'], ['Nudges before telling the orchestrator nudges', '1 to 5'],
            ['Ask after minutes quiet', '5 to 1440'], ['Tell the orchestrator minutes later', '5 to 1440'], ['After minutes held', '5 to 1440'], ['After minutes', '5 to 1440']]);
        await ev(`document.querySelector('${P} details').open = false; true`);
        ok('every number box is named by its label and unit and described by its limits; the reviewers box by its label, described by its sentence');

        // 3. an out-of-range number is refused before any request
        await ev(`window.__puts = 0; { const w = window.fetch; window.fetch = (u, o = {}) => { if ((o.method || 'GET') === 'PUT' && /board\\/[^/]+\\/settings$/.test(String(u))) window.__puts++; return w(u, o); }; } true`);
        await ev(`{ const f = document.querySelector('${P} form'); if (!f.elements.on.checked) f.elements.on.click(); /* the number is live only while the check is on */ f.elements.interval_min.value = '4'; f.elements.interval_min.dispatchEvent(new Event('input', { bubbles: true })); f.querySelector('[type=submit]').click(); } true`);
        const bad = await ev(`({ text: document.querySelector('${P} .health-state').textContent, focus: document.activeElement.name, err: document.querySelector('${P} .health-state').classList.contains('health-error'), puts: window.__puts })`);
        assert.match(bad.text, /Every: use a whole number from 5 to 1440/); assert.equal(bad.focus, 'interval_min'); assert.equal(bad.err, true); assert.equal(bad.puts, 0);
        ok('4 minutes is refused in words, focus goes to the field, and nothing is sent');

        // 4. save through the real PUT; the server's stored copy
        await ev(`{ const f = document.querySelector('${P} form'); f.elements.interval_min.value = '15'; f.elements.recipient.value = 'Dev One'; f.elements.recipient.dispatchEvent(new Event('change', { bubbles: true })); f.elements.interval_min.dispatchEvent(new Event('input', { bubbles: true }));
            f.elements.claim_nudge_on.checked = false; f.elements.claim_nudge_on.dispatchEvent(new Event('change', { bubbles: true })); f.elements.default_reviewers.value = 'Boss, Dev One, Boss'; f.elements.default_reviewers.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        const rule = await ev(`(() => { const card = document.querySelector('${P} [data-rule=claim_nudge]'); return { off: card.classList.contains('off'), disabled: [...card.querySelectorAll('.health-knobs input')].every(i => i.disabled), save: document.querySelector('${P} [type=submit]').disabled, unsaved: false }; })()`);
        assert.deepEqual([rule.off, rule.disabled, rule.save], [true, true, false], 'a rule that is off disables its numbers; a change enables Save');
        const unsaved = await ev(`import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.hasUnsavedTeamSettings())`);
        assert.equal(unsaved, true, 'unsaved health changes are guarded when leaving');
        await ev(`document.querySelector('${P} [type=submit]').click(); true`);
        await waitFor(`document.querySelector('${P} .health-state')?.textContent === 'Saved.'`, 'the save', 8000);
        const stored = (await api('/settings')).body.health;
        assert.equal(stored.interval_min, 15); assert.equal(stored.recipient, 'Dev One'); assert.equal(stored.claim_nudge.on, false);
        assert.deepEqual(stored.default_reviewers, ['Boss', 'Dev One'], 'duplicates dropped');
        const after = await ev(`(() => { const f = document.querySelector('${P} form'); return { interval: f.elements.interval_min.value, rcpt: f.elements.recipient.value, save: f.querySelector('[type=submit]').disabled, claimOff: !f.elements.claim_nudge_on.checked }; })()`);
        assert.deepEqual(after, { interval: '15', rcpt: 'Dev One', save: true, claimOff: true });
        assert.equal(await ev(`import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.hasUnsavedTeamSettings())`), false, 'saved: nothing unsaved');
        ok('Save sends the PUT, the server keeps it (15 minutes, Dev One, claim nudge off, reviewers de-duplicated), the form shows what was kept, nothing left unsaved');

        // 5. reopening shows the saved values; Reset fills the defaults without saving
        await open(); await clickHealthTab();
        assert.equal(await ev(`document.querySelector('${P} form').elements.interval_min.value`), '15', 'a reopened tab shows what was saved');
        await ev(`window.__puts = 0; document.querySelector('${P} .health-reset').click(); true`);
        const reset = await ev(`(() => { const f = document.querySelector('${P} form'); return { interval: f.elements.interval_min.value, rcpt: f.elements.recipient.value, claimOn: f.elements.claim_nudge_on.checked, save: f.querySelector('[type=submit]').disabled, text: document.querySelector('${P} .health-state').textContent, puts: window.__puts, focus: document.activeElement.name }; })()`);
        assert.deepEqual(reset, { interval: '10', rcpt: '', claimOn: true, save: false, text: 'The defaults are filled in. Save to apply them.', puts: 0, focus: 'on' });
        assert.equal((await api('/settings')).body.health.interval_min, 15, 'Reset alone saved nothing');
        ok('a reopened tab shows the saved values; Reset to defaults fills the form, enables Save and saves nothing');

        // 6. a server refusal is shown in words; more than 8 reviewers refused before a request
        await ev(`{ const w = window.fetch; window.fetch = (u, o = {}) => (o.method === 'PUT' && /board\\/[^/]+\\/settings$/.test(String(u))) ? Promise.resolve(new Response(JSON.stringify({ error: 'health.recipient: "Gone" is not a member of this team' }), { status: 400, headers: { 'content-type': 'application/json' } })) : w(u, o); } true`);
        await ev(`document.querySelector('${P} [type=submit]').click(); true`);
        await waitFor(`/is not a member of this team/.test(document.querySelector('${P} .health-state').textContent)`, 'the refusal text', 5000);
        assert.equal(await ev(`document.querySelector('${P} [type=submit]').disabled`), false, 'Save is available again after a refusal');
        await ev(`{ const f = document.querySelector('${P} form'); f.elements.default_reviewers.value = 'a,b,c,d,e,f,g,h,i'; f.elements.default_reviewers.dispatchEvent(new Event('input', { bubbles: true })); f.querySelector('[type=submit]').click(); } true`);
        assert.match(await ev(`document.querySelector('${P} .health-state').textContent`), /at most 8 names/);
        ok('a refusal from the server is shown as its words; nine reviewers are refused before a request');

        // 7. held-delivery card and unknown fields: when the server returns held_notice the card shows and a PUT sends it back
        await open();
        await ev(`{ window.__putBody = null; const w = window.fetch; window.fetch = (u, o = {}) => { const url = String(u);
            if (/board\\/[^/]+\\/settings$/.test(url) && (o.method || 'GET') === 'GET') return w(u, o).then(async r => { const b = await r.json(); b.health.held_notice = { on: true, after_min: 15 }; b.health.future_rule = { on: true, x: 3 }; return new Response(JSON.stringify(b), { status: 200, headers: { 'content-type': 'application/json' } }); });
            if (/board\\/[^/]+\\/settings$/.test(url) && o.method === 'PUT') { window.__putBody = JSON.parse(o.body); const h = window.__putBody.health; return Promise.resolve(new Response(JSON.stringify({ health: h }), { status: 200, headers: { 'content-type': 'application/json' } })); }
            return w(u, o); }; } true`);
        await open(); await clickHealthTab();
        const held = await ev(`(() => { const c = document.querySelector('${P} [data-rule=held_notice]'); const i = c && c.querySelector('input[type=number]'); return c ? { title: c.querySelector('.health-rule-head').textContent.trim(), after: i.value, lo: i.min, hi: i.max } : null; })()`);
        assert.deepEqual(held, { title: 'Tell the Operator about a held delivery', after: '15', lo: '5', hi: '1440' });
        await ev(`{ const f = document.querySelector('${P} form'); f.elements.held_notice_after_min.value = '30'; f.elements.held_notice_after_min.dispatchEvent(new Event('input', { bubbles: true })); f.querySelector('[type=submit]').click(); } true`);
        await waitFor(`!!window.__putBody`, 'the PUT', 5000);
        const body = await ev(`window.__putBody`);
        assert.equal(body.subscriber_id, 'Operator'); assert.equal(body.health.held_notice.after_min, 30); assert.deepEqual(body.health.future_rule, { on: true, x: 3 }, 'a field the form does not know goes back as it was');
        // Reset to defaults fills the held card with the design's 10 minutes (Lead's #4225 default).
        await ev(`document.querySelector('${P} .health-reset').click(); true`);
        assert.equal(await ev(`document.querySelector('${P} form').elements.held_notice_after_min.value`), '10', 'the held-delivery default is 10 minutes');
        ok('the held-delivery card appears when the server has held_notice and saves; a field the form does not know is sent back unchanged');

        // 8. through Coral Hub the GET leaves "health" out: no tab
        await ev(`{ const w = window.fetch; window.fetch = (u, o = {}) => (/board\\/[^/]+\\/settings$/.test(String(u)) && (o.method || 'GET') === 'GET') ? w(u, o).then(async r => { const b = await r.json(); delete b.health; return new Response(JSON.stringify(b), { status: 200, headers: { 'content-type': 'application/json' } }); }) : w(u, o); } true`);
        await ev(`document.getElementById('team-working-mode-dialog')?.remove(); document.getElementById('team-settings-workspace')?.remove(); import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
        await waitFor(`!!document.querySelector('[type=submit]') && !document.querySelector('[type=submit]').disabled`, 'the settings to load', 15000);
        await sleep(400); // sleep-ok: the tab would appear from a late answer, so wait past the load for it not to; no completion event
        assert.equal(await ev(`!!document.querySelector('[data-settings-tab=health]') || !!document.getElementById('team-settings-health')`), false);
        ok('no Health check tab when the settings GET has no health (through Coral Hub)');

        // 9. phone and 200% text: no sideways scroll in the panel
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        for (const [label, w, big] of [['phone 390', 390, false], ['phone 390 at 200% text', 390, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: 844, deviceScaleFactor: 1, mobile: true });
            await ev(`document.documentElement.style.fontSize = ${big ? "'200%'" : "''"}; true`);
            await open(); await clickHealthTab();
            await sleep(500); // sleep-ok: a layout settles over frames; there is no completion event
            const m = await ev(`(() => { const p = document.getElementById('team-settings-health'); const d = document.querySelector('#team-working-mode-dialog') || p.parentElement; const over = [...p.querySelectorAll('input, select, button, label')].filter(e => e.getClientRects().length && e.getBoundingClientRect().right > innerWidth + 1).map(e => e.name || e.className); return { over, panelScroll: [p.scrollWidth, p.clientWidth], dialogScroll: [d.scrollWidth, d.clientWidth] }; })()`);
            assert.deepEqual(m.over, [], `${label}: nothing runs past the screen: ${JSON.stringify(m)}`);
            assert.ok(m.panelScroll[0] <= m.panelScroll[1] + 1, `${label}: no sideways scroll in the panel ${JSON.stringify(m)}`);
            ok(`${label}: no control past the screen edge, no sideways scroll`);
        }
        console.log('\nPASS the Health check tab of Team settings');
    } finally { await removeBoards(ev, [TEAM]); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
