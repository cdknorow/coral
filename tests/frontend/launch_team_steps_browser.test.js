// Launch Agent Team in three steps, Team, Members, Start (#6397): the tabs and the step said once, a template picker that searches and scrolls, a members table with named Edit and Remove
// and an Undo, the prompt opening inline, and Mode starting at Default. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => { window.__teamLaunches = [];
            const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href), m = o.method || 'GET';
                if (p.pathname === '/api/sessions/launch-team' && m === 'POST') { window.__teamLaunches.push(JSON.parse(o.body)); return json({ agents: [{ name: 'A', session_id: 's' }] }); }
                if (p.pathname === '/api/board/projects') return json([]);
                return real(u, o); };
            await window.showLaunchModal(); window.hideLaunchModal(); return true; })()`);
        const open = async () => {
            await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-team')).display !== 'none' && document.querySelectorAll('#team-template-list [role=option]').length > 0`, 'the team step');
        };
        const step = () => ev(`document.querySelector('#launch-step-team .team-step-tab[aria-selected=true]').dataset.step`);
        const vis = sel => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && !e.hidden && e.getBoundingClientRect().height > 0; })()`);
        const rows = () => ev(`[...document.querySelectorAll('#team-agents-list .team-agent-row .team-agent-role-name')].map(r => r.textContent)`);
        const click = sel => ev(`document.querySelector(${JSON.stringify(sel)}).click(); true`);

        console.log('Three steps, said once: tabs for Team, Members, Start; Step N of 3 is for screen readers only');
        await open();
        assert.deepEqual(await ev(`[...document.querySelectorAll('#launch-step-team .team-step-tab')].map(t => t.textContent)`), ['Team', 'Members', 'Start']);
        assert.equal(await step(), 'team');
        assert.equal(await ev(`document.getElementById('team-step-live').textContent`), 'Step 1 of 3');
        assert.equal(await ev(`getComputedStyle(document.getElementById('team-step-live')).position`), 'absolute', 'the step number is not drawn');
        assert.equal(await vis('#launch-team-next'), true); assert.equal(await vis('#launch-team-go'), false, 'Launch Team waits for the last step'); assert.equal(await vis('#launch-team-back'), false);

        console.log('Next needs a name and a folder, and says which');
        await click('#launch-team-next');
        assert.equal(await step(), 'team', 'no name: stays');
        assert.equal(await ev(`document.getElementById('team-board-name').getAttribute('aria-invalid')`), 'true');
        await ev(`{ const i = document.getElementById('team-board-name'); i.value = 'crew-1'; i.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        await click('#launch-team-next');
        assert.equal(await step(), 'team', 'no folder: stays');
        assert.equal(await ev(`document.getElementById('launch-team-dir').getAttribute('aria-invalid')`), 'true');

        console.log('The template picker searches, counts, and has an empty case');
        const total = await ev(`document.querySelectorAll('#team-template-list [role=option]').length`);
        assert.ok(total >= 3);
        assert.equal(await ev(`document.getElementById('team-template-count').textContent`), '', 'no counter while nothing is typed');
        await ev(`{ const s = document.getElementById('team-template-search'); s.value = 'team'; s.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        assert.equal(await ev(`document.getElementById('team-template-count').textContent`), `${await ev(`document.querySelectorAll('#team-template-list [role=option]').length`)} of ${total}`);
        await ev(`{ const s = document.getElementById('team-template-search'); s.value = 'zzzz'; s.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        assert.equal(await ev(`document.querySelectorAll('#team-template-list [role=option]').length`), 0);
        assert.match(await ev(`document.getElementById('team-template-none').textContent`), /^No template matches "zzzz"/);
        await ev(`{ const s = document.getElementById('team-template-search'); s.value = 'dungeon'; s.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        await ev(`document.getElementById('team-template-search').focus(); true`);
        await client.Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
        await client.Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
        await waitFor(`document.querySelectorAll('#team-agents-list .team-agent-row').length === 7`, 'Enter picks the highlighted template');
        assert.equal(await ev(`document.querySelector('#team-template-list [aria-selected=true]').dataset.name`), 'Dungeon Master', 'the chosen one is marked');

        console.log('Clearing the search brings every template back');
        await ev(`{ const s = document.getElementById('team-template-search'); s.value = 'zzzz'; s.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        assert.equal(await ev(`document.querySelectorAll('#team-template-list [role=option]').length`), 0, 'no match: no rows');
        await ev(`{ const s = document.getElementById('team-template-search'); s.value = ''; s.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        assert.ok(await ev(`document.querySelectorAll('#team-template-list [role=option]').length`) >= 3 && await ev(`document.getElementById('team-template-none').hidden`), 'an empty search lists every template again, and the no-match line is gone (textOf("") is null: the filter threw)');
        console.log('The list scrolls after four rows');
        await ev(`{ const s = document.getElementById('team-template-search'); s.value = ''; s.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        const list = await ev(`(() => { const l = document.getElementById('team-template-list'); return [l.clientHeight, l.scrollHeight, getComputedStyle(l).overflowY]; })()`);
        assert.ok(list[0] <= 4 * 44 + 4 && (total <= 4 || (list[1] > list[0] && list[2] === 'auto')), JSON.stringify(list));

        console.log('Members: a table with named Edit and Remove, an inline prompt, an Undo');
        await ev(`{ const d = document.getElementById('launch-team-dir'); d.value = '/work/repo'; } true`);
        await click('#launch-team-next');
        assert.equal(await step(), 'members');
        assert.equal(await ev(`document.getElementById('team-step-live').textContent`), 'Step 2 of 3');
        assert.deepEqual(await ev(`[...document.querySelectorAll('.team-members-table th')].map(t => t.textContent.trim())`), ['Role', 'Provider', 'Model', 'Thinking', 'Mode', 'Actions']);
        const first = await ev(`(() => { const r = document.querySelector('#team-agents-list .team-agent-row'); return [r.querySelector('.team-agent-edit-btn').getAttribute('aria-label'), r.querySelector('.team-agent-remove').getAttribute('aria-label'), r.querySelector('.team-agent-mode').textContent, r.querySelector('.team-agent-thinking').textContent, r.querySelector('.team-agent-model').textContent]; })()`);
        // the cells show what will be used, with a mark where the member did not choose it: Mode is the team's, the model and level are the CLI's own
        assert.deepEqual(first, ['Edit Dungeon Master', 'Remove Dungeon Master', 'Default team', 'CLI default', 'CLI default']);
        assert.equal(await ev(`document.querySelector('#team-agents-list .team-agent-row .team-agent-mode .team-default-mark').textContent`), 'team');
        assert.deepEqual(await ev(`[...document.querySelectorAll('#team-agents-list .team-agent-row:nth-child(2) .team-agent-reorder-btn')].map(b => b.getAttribute('aria-label'))`), ['Move Warrior up', 'Move Warrior down']);
        assert.equal(await ev(`Math.round(document.querySelector('#team-agents-list .team-agent-reorder-btn').getBoundingClientRect().width)`), 32, 'the arrows are 32 px on a pointer screen (44 on touch)');
        assert.equal(await vis('#team-agents-list .team-agent-prompt'), false);
        await click('#team-agents-list .team-agent-row .team-agent-role-name');
        assert.equal(await vis('#team-agents-list .team-agent-row .team-agent-prompt'), true, 'the prompt opens inline');
        assert.equal(await ev(`document.querySelector('#team-agents-list .team-agent-role-name').getAttribute('aria-expanded')`), 'true');
        assert.match(await ev(`document.querySelector('#team-agents-list .team-agent-prompt').textContent`), /Dungeon Master/);
        await click('#team-agents-list .team-agent-row .team-agent-role-name');
        assert.equal(await vis('#team-agents-list .team-agent-row .team-agent-prompt'), false, 'and closes');
        await click('#team-agents-list .team-agent-row:nth-child(2) .team-agent-remove');
        assert.equal((await rows()).length, 6);
        assert.equal(await ev(`document.getElementById('team-undo').textContent`), 'Removed Warrior. Undo');
        await click('#team-undo button');
        assert.equal((await rows())[1], 'Warrior', 'Undo puts it back where it was');
        assert.equal(await ev(`document.getElementById('team-undo').textContent`), '', 'the undo line says nothing again (it stays in the page, out of the layout)');

        console.log('A member added in the Add member dialog follows the team Mode (Default), not Auto (#6412)');
        await ev(`window.showTeamAgentModal(); true`);
        assert.equal(await ev(`document.querySelector('#team-agent-modal-acf .acf-permission-mode').value`), 'default', 'the new member starts at the team Mode');
        await ev(`{ const n = document.querySelector('#team-agent-modal-acf .acf-name'); n.value = 'Scribe'; n.dispatchEvent(new Event('input', { bubbles: true })); window.saveTeamAgentFromModal(); } true`);
        assert.equal(await ev(`[...document.querySelectorAll('#team-agents-list .team-agent-row')].pop().querySelector('.team-agent-mode').textContent`), 'Default team');
        await ev(`document.getElementById('team-permission-mode').value = 'plan'; document.getElementById('team-permission-mode').dispatchEvent(new Event('change', { bubbles: true })); window.showTeamAgentModal(); true`);
        assert.equal(await ev(`document.querySelector('#team-agent-modal-acf .acf-permission-mode').value`), 'plan', 'and follows a team Mode that was changed');
        await ev(`window.hideTeamAgentModal(); document.getElementById('team-permission-mode').value = 'default'; document.getElementById('team-permission-mode').dataset.pickedByPerson = ''; true`);
        await click('#team-agents-list .team-agent-row:last-child .team-agent-remove'); await ev(`document.getElementById('team-undo').textContent = ''; true`);

        console.log('Start: Mode defaults to Default, Launch Team is here');
        await click('#launch-team-next');
        assert.equal(await step(), 'start');
        assert.equal(await ev(`document.getElementById('team-step-live').textContent`), 'Step 3 of 3');
        assert.equal(await ev(`document.getElementById('team-permission-mode').value`), 'default');
        assert.equal(await vis('#launch-team-go'), true); assert.equal(await vis('#launch-team-next'), false); assert.equal(await vis('#launch-team-back'), true);
        await click('#launch-team-go');
        await waitFor(`window.__teamLaunches.length === 1`, 'the launch request');
        const sent = await ev(`window.__teamLaunches[0]`);
        assert.equal(sent.board_name, 'crew-1'); assert.equal(sent.agents.length, 7); assert.deepEqual(sent.flags, [], 'Default mode adds no permission flag');

        console.log('The Mode a member shows is what launches (#6412): Claude and Codex');
        const launches = n => waitFor(`window.__teamLaunches.length === ${n}`, 'the launch request ' + n);
        const modeOf = name => ev(`[...document.querySelectorAll('#team-agents-list .team-agent-row')].find(r => r.querySelector('.team-agent-role-name').textContent === ${JSON.stringify(name)}).querySelector('.team-agent-mode').textContent`);
        await open();
        await ev(`document.getElementById('team-board-name').value = 'crew-2'; document.getElementById('launch-team-dir').value = '/work/repo'; true`);
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'team_dialogs.js');
            document.getElementById('team-agents-list').innerHTML = ''; document.getElementById('team-permission-mode').value = 'default';
            m._addTeamAgent('Plain', 'p', null, 'claude', '', '');
            m._addTeamAgent('Planner', 'p', null, 'claude', '', '--permission-mode plan');
            m._addTeamAgent('Open', 'p', { allow: ['file_read', 'file_write', 'shell', 'git_write'] }, 'codex', '', '');
            m._addTeamAgent('Careful', 'p', { allow: ['file_read'] }, 'codex', '', '');
            m._refreshTeamSummaries(); return true; })()`);
        assert.equal(await modeOf('Plain'), 'Default team'); assert.equal(await modeOf('Planner'), 'Plan');
        // the rule (#6425): an explicit Mode (the member's own, else the team's) is applied and capabilities only narrow it; with no Mode the capabilities decide
        assert.equal(await modeOf('Open'), 'Sandboxed', 'team Mode Default: the capabilities decide, and four chips are no longer an unsandboxed agent');
        assert.equal(await modeOf('Careful'), 'Read-only sandbox');
        const setTeamMode = v => ev(`(() => { const e = document.getElementById('team-permission-mode'); e.value = ${JSON.stringify(v)}; import((window.__coralStatic || '/static/') + 'team_dialogs.js').then(m => m._refreshTeamSummaries()); return true; })()`);
        await setTeamMode('plan'); await waitFor(`[...document.querySelectorAll('#team-agents-list .team-agent-row')].find(r => r.querySelector('.team-agent-role-name').textContent === 'Open').querySelector('.team-agent-mode').textContent.startsWith('Plan')`, 'Plan');
        assert.equal(await modeOf('Open'), 'Plan team', 'team Plan: four chips do not widen it');
        await setTeamMode('bypassPermissions'); await waitFor(`[...document.querySelectorAll('#team-agents-list .team-agent-row')].find(r => r.querySelector('.team-agent-role-name').textContent === 'Open').querySelector('.team-agent-mode').textContent.startsWith('Bypass')`, 'Bypass');
        assert.equal(await modeOf('Open'), 'Bypass Permissions team', 'team Bypass: Shell restricts nothing, the mode stands'); assert.equal(await ev(`[...document.querySelectorAll('#team-agents-list .team-agent-row')].find(r => r.querySelector('.team-agent-role-name').textContent === 'Open').querySelector('.team-agent-mode').classList.contains('team-mode-warn')`), true, 'and it is a warning');
        assert.equal(await modeOf('Careful'), 'Read-only sandbox', 'team Bypass: read-only chips narrow it');
        await setTeamMode('default'); await waitFor(`[...document.querySelectorAll('#team-agents-list .team-agent-row')].find(r => r.querySelector('.team-agent-role-name').textContent === 'Open').querySelector('.team-agent-mode').textContent === 'Sandboxed'`, 'Default again');
        await ev(`window.launchTeam(); true`);
        await launches(2);
        const sent2 = await ev(`window.__teamLaunches[1]`);
        const by = n => sent2.agents.find(a => a.name === n);
        assert.deepEqual(sent2.flags, [], 'the team Mode is Default: no permission flag'); assert.equal(by('Plain').flags, undefined, 'Plain carries no flags (not auto)'); assert.equal(by('Plain').capabilities, undefined);
        assert.deepEqual(by('Planner').flags, ['--permission-mode', 'plan']);
        assert.deepEqual(by('Open').capabilities.allow.sort(), ['file_read', 'file_write', 'git_write', 'shell']);

        console.log('The Edit dialog says it beside the Mode too (#6412, #6425)');
        await ev(`window.showTeamAgentModal([...document.querySelectorAll('#team-agents-list .team-agent-row')].find(r => r.querySelector('.team-agent-role-name').textContent === 'Open')); true`);
        // the Mode sits in one of the dialog's sections: open the one that has it, as a person looking for the Mode does
        const showModeSection = () => ev(`import((window.__coralStatic || '/static/') + 'section_dialog.js').then(m => { const shell = document.querySelector('#team-agent-modal .settings-shell'); const pane = document.querySelector('#team-agent-modal-acf .acf-permission-mode').closest('.settings-pane'); m.showSection(shell, pane.dataset.section); return true; })`);
        const line = () => ev(`(() => { const e = document.querySelector('#team-agent-modal-acf .acf-mode-override'); return !!e && !e.hidden && e.getClientRects().length > 0 ? e.textContent : ''; })()`);
        await showModeSection();
        assert.equal(await line(), 'No Mode chosen, so the capabilities decide: Sandboxed.', 'the Mode says Default and the chips decide: said beside it');
        await ev(`for (const cap of ['shell', 'git_write', 'file_write']) document.querySelector('#team-agent-modal-acf .perm-chip[data-type="allow"][data-cap="' + cap + '"]').click(); true`);
        assert.equal(await line(), 'No Mode chosen, so the capabilities decide: Read-only sandbox.', 'it follows the chips');
        await ev(`{ const m = document.querySelector('#team-agent-modal-acf .acf-permission-mode'); m.value = 'bypassPermissions'; m.dispatchEvent(new Event('change', { bubbles: true })); } true`);
        assert.equal(await line(), 'The capabilities narrow this Mode: Read-only sandbox.', 'an explicit Mode is narrowed, not replaced');
        await ev(`{ const m = document.querySelector('#team-agent-modal-acf .acf-permission-mode'); m.value = 'plan'; m.dispatchEvent(new Event('change', { bubbles: true })); } true`);
        assert.equal(await line(), '', 'Plan is already read-only: nothing to say');
        await ev(`window.hideTeamAgentModal(); window.showTeamAgentModal([...document.querySelectorAll('#team-agents-list .team-agent-row')].find(r => r.querySelector('.team-agent-role-name').textContent === 'Plain')); true`);
        await showModeSection();
        assert.equal(await line(), '', 'a Claude member has none');
        await ev(`window.hideTeamAgentModal(); window.hideLaunchModal(); true`);

        console.log('A template launched under team Plan: no member carries a mode of its own, so every member follows the team (#6440)');
        await open();
        await ev(`document.getElementById('team-board-name').value = 'crew-3'; document.getElementById('launch-team-dir').value = '/work/repo'; window._loadTeamTemplateFromSelect('Coding Team'); { const e = document.getElementById('team-permission-mode'); e.value = 'plan'; e.dispatchEvent(new Event('change', { bubbles: true })); } true`);
        await ev(`window.launchTeam(); true`);
        await waitFor(`window.__teamLaunches.length === 3`, 'the template launch');
        const tmpl = await ev(`window.__teamLaunches[2]`);
        assert.deepEqual(tmpl.flags, ['--permission-mode', 'plan'], 'the team Mode is the team flags');
        assert.equal(tmpl.agents.length, 5);
        assert.deepEqual(tmpl.agents.filter(a => /--permission-mode|--mode|--full-auto|--sandbox/.test((a.flags || []).join(' '))).map(a => a.name), [], 'no template member carries a mode of its own');
        assert.deepEqual([...new Set(await ev(`[...document.querySelectorAll('#team-agents-list .acf-permission-mode')].map(s => s.value)`))], ['default'], 'and every member form is at Default');
        await ev(`window.hideLaunchModal(); true`);

        console.log('A member added or edited in the dialog carries a mode of its own only if the person changed it (#6440)');
        await open();
        await ev(`document.getElementById('team-agents-list').innerHTML = ''; { const e = document.getElementById('team-permission-mode'); e.value = 'plan'; } window.showTeamAgentModal(); true`);
        assert.equal(await ev(`document.querySelector('#team-agent-modal-acf .acf-permission-mode').value`), 'plan', 'the new member shows the team Mode it will launch with');
        await ev(`{ const n = document.querySelector('#team-agent-modal-acf .acf-name'); n.value = 'Follower'; n.dispatchEvent(new Event('input', { bubbles: true })); window.saveTeamAgentFromModal(); } true`);
        const ownMode = name => ev(`[...document.querySelectorAll('#team-agents-list .team-agent-row')].find(r => r.querySelector('.team-agent-role-name').textContent === ${JSON.stringify(name)}).querySelector('.acf-permission-mode').value`);
        assert.equal(await ownMode('Follower'), 'default', 'saved unchanged: it has no mode of its own, so it follows the team');
        await ev(`window.showTeamAgentModal([...document.querySelectorAll('#team-agents-list .team-agent-row')][0]); true`);
        assert.equal(await ev(`document.querySelector('#team-agent-modal-acf .acf-permission-mode').value`), 'plan', 'Edit shows the Mode the member follows');
        await ev(`{ const m = document.querySelector('#team-agent-modal-acf .acf-permission-mode'); m.value = 'acceptEdits'; m.dispatchEvent(new Event('change', { bubbles: true })); window.saveTeamAgentFromModal(); } true`);
        assert.equal(await ownMode('Follower'), 'acceptEdits', 'a Mode the person picked is the member\'s own');
        await ev(`window.hideLaunchModal(); true`);

        console.log('The team Mode Default says what it does; a member with no provider of its own follows the team provider (#6440)');
        await open();
        assert.equal(await ev(`document.querySelector('#team-panel-start .perm-mode-desc').textContent`), "Asks before acting (each CLI's normal mode)", 'Default is the CLI\'s confirm mode, not "reads only"');
        await ev(`document.getElementById('team-board-name').value = 'crew-4'; document.getElementById('launch-team-dir').value = '/work/repo'; window._loadTeamTemplateFromSelect('Coding Team'); true`);
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'team_dialogs.js'); m._addTeamAgent('Own', 'p', null, 'agy', '', ''); return true; })()`);
        const types = () => ev(`[...document.querySelectorAll('#team-agents-list .team-agent-row')].map(r => r.querySelector('.acf-agent-type').value)`);
        assert.deepEqual([...new Set((await types()).slice(0, 5))], ['claude'], 'the template rows start at the team provider');
        await ev(`{ const e = document.getElementById('team-agent-type'); e.value = 'codex'; e.dispatchEvent(new Event('change', { bubbles: true })); } true`);
        assert.deepEqual(await types(), ['codex', 'codex', 'codex', 'codex', 'codex', 'agy'], 'the team provider reaches every member that has none of its own; a member that has one keeps it');
        await ev(`(async () => { (await import((window.__coralStatic || '/static/') + 'team_dialogs.js'))._refreshTeamSummaries(); return true; })()`);
        assert.deepEqual(await ev(`[...document.querySelectorAll('#team-agents-list .team-agent-provider')].map(c => c.textContent)`), ['Codex', 'Codex', 'Codex', 'Codex', 'Codex', 'Antigravity'], 'and the table says so');
        await ev(`window.launchTeam(); true`);
        await waitFor(`window.__teamLaunches.length === 4`, 'the launch');
        assert.deepEqual(await ev(`window.__teamLaunches[3].agents.map(a => a.agent_type)`), ['codex', 'codex', 'codex', 'codex', 'codex', 'agy']);
        await ev(`window.hideLaunchModal(); true`);

        console.log('Phone width: the table stacks and the actions keep 44 px targets');
        await ev(`window.hideLaunchModal(); true`);
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await open();
        await ev(`window._loadTeamTemplateFromSelect('Coding Team'); document.getElementById('team-board-name').value = 'p'; document.getElementById('launch-team-dir').value = '/x'; true`);
        await click('#launch-team-next'); await click('#launch-team-next'.replace('next', 'next'));
        assert.equal(await step(), 'start');
        await click('#launch-team-back');
        const phone = await ev(`(() => { const b = document.querySelector('#team-agents-list .team-agent-remove').getBoundingClientRect(); return [b.height >= 43.5, document.documentElement.scrollWidth <= innerWidth]; })()`);
        assert.deepEqual(phone, [true, true]);
        await Emulation.clearDeviceMetricsOverride();
        console.log('launch_team_steps: ok');
    } finally { try { await client.close(); } catch {} }
})().catch(e => { console.error(e); process.exit(1); });
