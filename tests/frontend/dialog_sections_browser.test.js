// The sectioned dialogs (#4950): Restart, New Agent, Agent Team and the team's agent dialog are the Settings dialog's shell: a section list with icons on the left, the open section's heading and
// one-line description over its fields, a fixed footer with the primary action; on a phone the list is a selector at the top. Every field is reachable by clicking through the sections, the values stay
// when the person moves between sections, the arrow keys move in the list, a problem found on Launch opens its section and marks it, and nothing scrolls sideways. Drives Chrome over CDP through
// agent_view_harness.js against the isolated dev server; every request is answered by the test.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    // (a capture on the emulated phone scrolled the pane by itself mid-shot, which cut a heading in the evidence: nothing is focused and the panes cannot scroll while it is taken)
    const shot = async (name, { keepFocus = false } = {}) => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await ev(`${keepFocus ? '' : 'document.activeElement && document.activeElement.blur();'} document.querySelectorAll('.settings-panes').forEach(b => { b.style.overflow = 'hidden'; }); true`); await new Promise(r => setTimeout(r, 350)); /* sleep-ok: the list's highlight is a transition: a shot taken at once shows the old and the new section both lit */ fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); await ev(`document.querySelectorAll('.settings-panes').forEach(b => { b.style.overflow = ''; }); true`); };
    const key = async (k, code, vk) => { const b = { key: k, code, windowsVirtualKeyCode: vk }; await Input.dispatchKeyEvent({ type: 'keyDown', ...b }); await Input.dispatchKeyEvent({ type: 'keyUp', ...b }); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const open = async (w, mobile) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: 800, deviceScaleFactor: 1, mobile });
            await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            await ev(`(async () => {
                window.__reqs = [];
                const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
                window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href), m = o.method || 'GET';
                    if (m === 'POST' && /^\\/api\\/sessions\\/(launch|launch-team|live\\/[^/]+\\/restart)$/.test(p.pathname)) { window.__reqs.push({ path: p.pathname, body: JSON.parse(o.body) }); return json({ session_name: 'x', session_id: 'sid', agents: [{ name: 'A', session_id: 's' }] }); }
                    if (p.pathname === '/api/board/projects') return json([{ project: 'taken' }]);
                    if (p.pathname === '/api/filesystem/is-git') return json({ is_git: true });
                    return real(u, o); };
                await window.showLaunchModal(); window.hideLaunchModal(); return true; })()`);
        };
        const vis = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && !e.hidden && e.getClientRects().length > 0; })()`;
        // The state of a sectioned dialog: its list (names, icons, the open one), the open pane's heading and description, the footer.
        const view = root => ev(`(() => { const m = document.querySelector(${JSON.stringify(root)}); const tabs = [...m.querySelectorAll('.settings-tabs [role=tab]')];
            const pane = [...m.querySelectorAll('.settings-pane[data-section]')].find(p => !p.hidden);
            const sel = m.querySelector('.settings-group-pick select'); const shown = e => !!e && e.getClientRects().length > 0;
            const foot = m.querySelector('.modal-footer, .modal-actions'); const content = m.closest('.modal-content') || m.querySelector('.modal-content');
            return { tabs: tabs.map(t => ({ name: t.querySelector('.settings-tab-name').textContent, icon: t.querySelector('.material-icons').textContent, on: t.getAttribute('aria-selected') === 'true', err: t.dataset.error === '1' })),
                listShown: shown(m.querySelector('.settings-tabs')), pickShown: shown(sel), pick: sel && sel.value, open: pane && pane.dataset.section, title: pane && pane.querySelector('.settings-pane-title').textContent, desc: pane && pane.querySelector('.settings-pane-sub').textContent,
                panesVisible: [...m.querySelectorAll('.settings-pane[data-section]')].filter(p => shown(p)).length, footBottom: foot && Math.round(foot.getBoundingClientRect().bottom), contentBottom: Math.round(content.getBoundingClientRect().bottom), footShown: shown(foot),
                overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
        // The pane's highest scroll over the next second: a card scrolled into view animates (smooth), so one look right after opening sees nothing.
        const topStays = async root => { let max = 0; for (let i = 0; i < 20; i++) { max = Math.max(max, await ev(`document.querySelector('${root} .settings-panes').scrollTop`)); await new Promise(r => setTimeout(r, 50)); } return max; }; // sleep-ok: watching an animation that has no completion event
        const go = (root, id) => ev(`(() => { const m = document.querySelector(${JSON.stringify(root)}); const t = [...m.querySelectorAll('.settings-tabs [role=tab]')].find(x => x.dataset.section === ${JSON.stringify(id)}); if (getComputedStyle(m.querySelector('.settings-tabs')).display !== 'none') t.click(); else { const s = m.querySelector('.settings-group-pick select'); s.value = ${JSON.stringify(id)}; s.dispatchEvent(new Event('change', { bubbles: true })); } return true; })()`);
        const session = { name: 'ag', display_name: 'QA Engineer', session_id: 'sid-r', type: 'live', agent_type: 'claude', model: 'claude-opus-4', prompt: 'Be kind.\nAlways.', capabilities: { allow: ['file_read'] }, tmux_session: 'ag' };

        console.log('Restart: the list, the descriptions, the fixed footer, and every field reached through its section');
        await open(1440, false);
        await ev(`T.state.currentSession = ${JSON.stringify(session)}; window.restartSession(); true`);
        await waitFor(`${vis('#restart-modal .settings-tabs')} && !!document.querySelector('#restart-acf .acf-agent-type')`, 'the restart dialog');
        let v = await view('#restart-modal');
        assert.deepEqual(v.tabs.map(t => [t.name, t.icon]), [['Agent', 'smart_toy'], ['Prompt', 'description'], ['Permissions', 'lock'], ['Advanced', 'tune']]);
        assert.equal(v.open, 'agent'); assert.equal(v.panesVisible, 1, 'one section at a time'); assert.equal(v.title, 'Agent'); assert.equal(v.desc, '', 'no pane sub-title (#6012)');
        assert.ok(v.listShown && !v.pickShown, 'a wide dialog has the list, not the selector');
        assert.equal(v.footShown && v.footBottom <= v.contentBottom, true, 'the footer is inside the dialog, at its bottom');
        assert.equal(await ev(`document.getElementById('restart-modal-name').textContent`), 'Restarting: QA Engineer');
        await shot('restart-agent-desktop');
        await go('#restart-modal', 'prompt');
        v = await view('#restart-modal'); assert.deepEqual([v.open, v.title], ['prompt', 'Prompt']);
        assert.equal(await ev(`document.querySelector('#restart-acf .acf-prompt').value`), 'Be kind.\nAlways.', 'the prompt is there to read and edit');
        assert.ok(await ev(`document.querySelector('#restart-acf .acf-prompt').getBoundingClientRect().height > 200`), 'with room');
        await shot('restart-prompt-desktop');
        await ev(`{ const p = document.querySelector('#restart-acf .acf-prompt'); p.value = 'Edited prompt'; p.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        await go('#restart-modal', 'permissions'); await shot('restart-permissions-desktop');
        // Claude in Bypass Permissions mode never asks, so Allow pre-approves nothing and its row is not shown (UI audit #5589); any other mode shows it.
        if (await ev(`document.querySelector('#restart-acf .acf-permission-mode').value === 'bypassPermissions'`)) {
            assert.equal(await ev(vis('#restart-acf [data-type="allow"][data-cap="shell"]')), false, 'Bypass Permissions: no Allow row');
            await ev(`{ const m = document.querySelector('#restart-acf .acf-permission-mode'); m.value = 'default'; m.dispatchEvent(new Event('change', { bubbles: true })); } true`);
        }
        assert.ok(await ev(vis('#restart-acf .acf-permission-mode')) && await ev(vis('#restart-acf [data-type="allow"][data-cap="shell"]')) && await ev(vis('#restart-acf [data-type="deny"][data-cap="web_access"]')), 'mode, allow and deny are all shown (no fold)');
        await ev(`document.querySelector('#restart-acf [data-type="allow"][data-cap="shell"]').click(); true`);
        assert.ok(await ev(vis('#restart-acf .perms-shell-input')), 'the shell patterns box appears with Shell');
        await go('#restart-modal', 'advanced'); assert.ok(await ev(vis('#restart-acf .acf-flags')));
        await ev(`{ const f = document.querySelector('#restart-acf .acf-flags'); f.value = '--verbose'; f.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        await go('#restart-modal', 'agent');
        assert.equal(await ev(`document.querySelector('#restart-acf .acf-prompt').value`), 'Edited prompt', 'values stay when the sections change');
        assert.equal(await ev(`document.querySelector('#restart-acf .acf-flags').value`), '--verbose');
        assert.equal(await ev(`document.querySelector('#restart-acf [data-type="allow"][data-cap="shell"]').classList.contains('active')`), true);

        console.log('Restart: keys in the list');
        await ev(`document.getElementById(document.querySelector('#restart-modal .settings-tabs [aria-selected=true]').id).focus(); true`);
        await key('ArrowDown', 'ArrowDown', 40);
        assert.equal((await view('#restart-modal')).open, 'prompt', 'ArrowDown opens the next section');
        await key('End', 'End', 35); assert.equal((await view('#restart-modal')).open, 'advanced');
        await key('ArrowDown', 'ArrowDown', 40); assert.equal((await view('#restart-modal')).open, 'agent', 'it wraps');
        await key('ArrowUp', 'ArrowUp', 38); assert.equal((await view('#restart-modal')).open, 'advanced');
        await key('Home', 'Home', 36); assert.equal((await view('#restart-modal')).open, 'agent');
        assert.equal(await ev(`document.activeElement.getAttribute('role')`), 'tab', 'the keyboard stays in the list');
        assert.equal(await ev(`[...document.querySelectorAll('#restart-modal .settings-tabs [role=tab]')].map(t => t.tabIndex).join()`), '0,-1,-1,-1', 'only the open section is a tab stop');
        const tp = await ev(`(() => { const t = document.querySelector('#restart-modal .settings-tabs [aria-selected=true]'); const p = document.getElementById(t.getAttribute('aria-controls')); return [p.getAttribute('role'), p.getAttribute('aria-labelledby') === t.id]; })()`);
        assert.deepEqual(tp, ['tabpanel', true], 'the pane is a tab panel named by its tab');
        await key('Escape', 'Escape', 27);
        await waitFor(`getComputedStyle(document.getElementById('restart-modal')).display === 'none'`, 'Escape closes the dialog');

        console.log('Restart on a phone: the selector at the top, 44 px, nothing sideways');
        await open(390, true);
        await ev(`T.state.currentSession = ${JSON.stringify(session)}; window.restartSession(); true`);
        await waitFor(`!!document.querySelector('#restart-acf .acf-agent-type') && ${vis('#restart-modal .settings-group-pick select')}`, 'the phone restart dialog');
        v = await view('#restart-modal');
        assert.ok(v.pickShown && !v.listShown, 'the selector, not the list'); assert.equal(v.overflow, 0);
        assert.equal(await ev(`[...document.querySelectorAll('#restart-modal .settings-group-pick select, #restart-modal .modal-footer .btn, #restart-acf .acf-agent-type')].every(e => e.getBoundingClientRect().height >= 44)`), true, 'targets of 44 px');
        await go('#restart-modal', 'prompt'); v = await view('#restart-modal'); assert.equal(v.open, 'prompt'); assert.equal(v.pick, 'prompt');
        assert.equal(v.footShown && v.footBottom <= v.contentBottom, true, 'the footer stays in view');
        await shot('restart-prompt-phone');
        await go('#restart-modal', 'permissions'); await shot('restart-permissions-phone');
        assert.equal((await view('#restart-modal')).overflow, 0, 'nothing sideways on the phone');

        const names = root => view(root).then(v => v.tabs.map(t => [t.name, t.icon]));
        const dot = async (root, name) => (await view(root)).tabs.find(t => t.name === name).err;
        const openAgent = async () => {
            await ev(`window.showLaunchModal(); window._selectLaunchType('agent'); true`);
            await waitFor(`${vis('#launch-step-agent')} && !!document.querySelector('#launch-agent-acf .acf-agent-type') && document.getElementById('launch-board-select').options.length > 2`, 'the New Agent dialog');
        };
        const click = sel => ev(`document.querySelector(${JSON.stringify(sel)}).click(); true`);
        const fillId = (id, v) => ev(`(() => { const e = document.getElementById(${JSON.stringify(id)}); e.value = ${JSON.stringify(v)}; e.dispatchEvent(new Event('input', { bubbles: true })); e.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);

        console.log('New Agent: the folder alone first (the keyboard in it), the rest of the form in its sections behind More options, a problem opens its section');
        await open(1440, false);
        await openAgent();
        const NA = '#launch-step-agent';
        const moreState = () => ev(`(() => ({ hidden: document.getElementById('launch-agent-more').hidden, simple: document.querySelector('#launch-modal .modal-content').classList.contains('launch-simple') }))()`);
        assert.deepEqual(await moreState(), { hidden: true, simple: true }, 'New Agent opens on the folder alone (step 1): the sections are step 2');
        assert.equal(await ev(`document.activeElement.id`), 'launch-dir', 'the keyboard starts in the folder');
        assert.ok(await ev(vis('#launch-dir')) && await ev(vis('#launch-agent-next')), 'the folder and Next are in view');
        assert.equal(await ev(`[...document.querySelectorAll('#launch-agent-more input, #launch-agent-more select')].some(e => e.getClientRects().length)`), false, 'no other field is shown');
        assert.ok(await ev(`document.querySelector('#launch-modal .modal-content').getBoundingClientRect().height < 400`), 'the dialog is as tall as its folder row');
        await shot('new-agent-closed-desktop');
        await fillId('launch-dir', '/work/repo'); await click('#launch-agent-next');
        assert.deepEqual(await moreState(), { hidden: false, simple: false }, 'Next opens the sections (step 2)');
        assert.deepEqual(await names(NA), [['Name and persona', 'badge'], ['Agent', 'smart_toy'], ['Prompt', 'description'], ['Permissions', 'lock'], ['Advanced', 'tune']]);
        v = await view(NA); assert.deepEqual([v.open, v.title], ['workspace', 'Name and persona']); assert.equal(v.desc, '', 'the pane has no sub-title (#6012)');
        assert.ok(v.footBottom <= v.contentBottom, 'Launch is in the fixed footer, in view');
        await ev(`{ const m = document.querySelector('#launch-agent-acf .acf-permission-mode'); m.value = 'bypassPermissions'; m.dispatchEvent(new Event('change', { bubbles: true })); } true`); // a Claude agent now starts in Auto (#5836): this suite is about the Bypass line, so it picks Bypass
        assert.equal(await ev(`!!document.querySelector('[data-acf-enforced-for]')`), false, 'the Bypass line is said on Permissions only: there is no copy under the persona (#6012)');
        assert.ok(await ev(`!document.querySelector('#launch-agent-title').closest('.launch-modal-step').querySelector('[data-on-click="m-select-launch-type"]').hidden`), 'the way back to the choices is in the header');
        await shot('new-agent-workspace-desktop');
        // the agent is named once, by the radio group of step 2 (#6407): the form's own select follows it and is not on screen, so Agent reaches the group, once it is built (the CLI probes answer when they do)
        await waitFor(`document.getElementById('launch-agent-acf').classList.contains('has-agent-pick')`, 'the agent choice is built', 8000);
        for (const [id, sel] of [['workspace', '#launch-agent-name-input'], ['agent', '#launch-agent-pick input[type=radio]:checked'], ['agent', '#launch-agent-acf .acf-model-wrap'], ['prompt', '#launch-agent-acf .acf-prompt'], ['permissions', '#launch-agent-acf .acf-permission-mode'], ['permissions', '#launch-agent-acf [data-type="allow"][data-cap="shell"]'], ['advanced', '#launch-board-select'], ['advanced', '#launch-agent-acf .acf-flags']]) {
            await go(NA, id);
            // The Allow row is not shown in Claude's Bypass Permissions mode (it pre-approves nothing, #5589): any other mode shows it.
            if (sel.includes('[data-type="allow"]')) await ev(`{ const m = document.querySelector('#launch-agent-acf .acf-permission-mode'); m.value = 'default'; m.dispatchEvent(new Event('change', { bubbles: true })); } true`);
            assert.ok(await ev(vis(sel)), `${sel} is reached in ${id}`);
        }
        await ev(`{ const m = document.querySelector('#launch-agent-acf .acf-permission-mode'); m.value = 'bypassPermissions'; m.dispatchEvent(new Event('change', { bubbles: true })); } true`); // the mode this suite started with
        await go(NA, 'advanced'); await shot('new-agent-advanced-desktop');
        await fillId('launch-board-select', '__new__'); assert.ok(await ev(vis('#launch-board-name')), 'a new board asks for its name in Advanced');
        await fillId('launch-board-name', 'bad name!');
        await fillId('launch-dir', '/work/repo');
        await go(NA, 'prompt'); await ev(`{ const p = document.querySelector('#launch-agent-acf .acf-prompt'); p.value = 'Kept'; p.dispatchEvent(new Event('input', { bubbles: true })); } true`);
        const nBefore = await ev(`window.__reqs.length`);
        await click('#launch-step-agent [data-on-click="m-launch-session"]');
        await waitFor(`!!document.querySelector('#launch-board-name[aria-invalid="true"]')`, 'the bad board name flagged');
        v = await view(NA); assert.equal(v.open, 'advanced', 'Launch opens the section with the problem');
        assert.equal(await ev(`document.activeElement.id`), 'launch-board-name', 'and focuses the field');
        assert.equal(await dot(NA, 'Advanced'), true, 'and marks it in the list'); assert.equal(await dot(NA, 'Name and persona'), false);
        assert.equal(await ev(`window.__reqs.length`), nBefore, 'nothing was sent');
        await fillId('launch-board-name', 'good-name'); assert.equal(await dot(NA, 'Advanced'), false, 'the mark goes when the field is edited');
        await fillId('launch-dir', '');
        await go(NA, 'prompt'); assert.equal(await ev(`document.querySelector('#launch-agent-acf .acf-prompt').value`), 'Kept', 'values stay across the sections');
        await click('#launch-step-agent [data-on-click="m-launch-session"]'); // a cleared folder: Launch takes the person back to step 1 and flags it
        await waitFor(`document.activeElement.id === 'launch-dir'`, 'the missing folder focused');
        assert.equal(await ev(`document.getElementById('launch-dir').getAttribute('aria-invalid')`), 'true', 'a missing folder is flagged on the folder field, which is never behind More options');
        await ev(`window.hideLaunchModal(); true`);

        console.log('A problem in a section opens More options');
        await openAgent();
        assert.equal((await moreState()).hidden, true, 'closed again for each New Agent');
        await fillId('launch-dir', '/work/repo'); await fillId('launch-board-select', '__new__'); await fillId('launch-board-name', 'bad name!');
        await click('#launch-step-agent [data-on-click="m-launch-session"]');
        await waitFor(`!!document.querySelector('#launch-board-name[aria-invalid="true"]')`, 'the bad board name flagged');
        assert.equal((await moreState()).hidden, false, 'Launch opens More options on the field with the problem');
        assert.equal(await ev(`document.activeElement.id`), 'launch-board-name', 'and focuses it');
        await ev(`window.hideLaunchModal(); true`);

        console.log('New Agent on a phone');
        await open(390, true);
        await openAgent();
        assert.equal(await ev(`[...document.querySelectorAll('#launch-step-agent .modal-actions .btn, #launch-dir, #btn-browse')].filter(e => e.getClientRects().length).every(e => e.getBoundingClientRect().height >= 44)`), true, 'targets of 44 px on the first screen');
        v = await view(NA); assert.equal(v.overflow, 0); assert.ok(v.footShown && v.footBottom <= v.contentBottom, 'the footer is in view');
        await shot('new-agent-closed-phone');
        await fillId('launch-dir', '/work/repo'); await click('#launch-agent-next');
        v = await view(NA); assert.ok(v.pickShown && !v.listShown, 'the selector at the top'); assert.equal(v.overflow, 0);
        assert.equal(await ev(`[...document.querySelectorAll('#launch-step-agent .settings-group-pick select, #launch-step-agent .modal-actions .btn, #launch-dir, #launch-agent-name-input')].filter(e => e.getClientRects().length).every(e => e.getBoundingClientRect().height >= 44)`), true, 'targets of 44 px');
        assert.ok(v.footShown && v.footBottom <= v.contentBottom, 'the footer is in view');
        await shot('new-agent-workspace-phone');
        await go(NA, 'advanced'); await shot('new-agent-advanced-phone');
        assert.equal((await view(NA)).overflow, 0);
        await ev(`window.hideLaunchModal(); true`);

        console.log('Agent Team: the name above the sections; Members, Board & workspace, Defaults; the agent dialog is the same shell');
        await open(1440, false);
        await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`);
        await waitFor(vis('#launch-step-team'), 'the team step');
        const TM = '#launch-step-team';
        assert.deepEqual(await names(TM), [['Members', 'group'], ['Board & workspace', 'forum'], ['Defaults', 'tune']]);
        v = await view(TM); assert.equal(v.open, 'members'); assert.equal(v.desc, '', 'no pane sub-title (#6012)');
        assert.equal(await topStays(TM), 0, 'the Members section opens at its top and stays there: its heading and the template picker are in view, not scrolled past by the default agents being added');
        assert.ok(await ev(vis('#team-agents-list')) && await ev(vis('#launch-step-team [data-on-click="m-show-team-agent-modal"]')) && await ev(vis('#team-template-selector')), 'the agents, Add Agent and the templates are in Members');
        await shot('team-members-desktop');
        assert.ok(await ev(vis('#team-board-name')), 'the team name is in view on Members, above the sections'); assert.equal(await ev(`document.activeElement.id`), 'team-board-name', 'and the keyboard starts in it');
        for (const [id, sel] of [['board', '#team-board-name'], ['board', '#launch-team-dir'], ['board', '#team-board-server'], ['defaults', '#team-agent-type'], ['defaults', '#team-permission-mode']]) { await go(TM, id); assert.ok(await ev(vis(sel)), `${sel} is reached in ${id}`); }
        await go(TM, 'board'); await shot('team-board-desktop');
        await fillId('team-board-name', 'bad name!');
        await go(TM, 'members');
        assert.ok(await ev(vis('#team-board-name')), 'a bad name is in view while the person is in Members, and no section is marked: it is not in one');
        await click('#launch-step-team [data-on-click="m-launch-team"]');
        await waitFor(`document.activeElement.id === 'team-board-name'`, 'the bad name focused');
        assert.equal((await view(TM)).open, 'members', 'Launch Team leaves the section alone: the name is above them');
        await fillId('team-board-name', 'ok-team');
        await ev(`window.hideLaunchModal(); true`);
        console.log('Agent Team on a phone');
        await open(390, true);
        await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`);
        await waitFor(vis('#launch-step-team'), 'the phone team step');
        v = await view(TM); assert.ok(v.pickShown && !v.listShown, 'the selector at the top'); assert.equal(v.overflow, 0);
        assert.equal(await topStays(TM), 0, 'and on a phone');
        assert.ok(v.footShown && v.footBottom <= v.contentBottom, 'Launch Team stays in view');
        assert.equal(await ev(`[...document.querySelectorAll('#launch-step-team .settings-group-pick select, #launch-step-team .modal-actions .btn')].every(e => e.getBoundingClientRect().height >= 44)`), true, 'targets of 44 px');
        await shot('team-members-phone');
        await ev(`document.querySelector('#team-agents-list').scrollIntoView({ block: 'start' }); true`); await shot('team-cards-phone');
        await ev(`document.querySelector('#launch-step-team .launch-back-btn').focus({ focusVisible: true }); true`); await shot('team-back-focus-phone', { keepFocus: true });
        await go(TM, 'board');
        const scrollOf = () => ev(`(() => { const p = document.querySelector('#launch-step-team .settings-pane:not([hidden])'); const box = p.closest('.settings-panes'); return [Math.round(p.querySelector('.settings-pane-title').getBoundingClientRect().top - box.getBoundingClientRect().top), box.scrollTop]; })()`);
        assert.deepEqual((await scrollOf()).map(n => n >= 0), [true, true], 'the section heading is not scrolled out of the pane'); assert.equal((await scrollOf())[1], 0);
        await shot('team-board-phone');
        assert.equal((await view(TM)).overflow, 0);
        await ev(`window.hideLaunchModal(); true`);
        await open(1440, false);
        await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`);
        await waitFor(vis('#launch-step-team'), 'the team step again');
        console.log('The team agent dialog');
        await ev(`window.showLaunchModal(); window._selectLaunchType('team'); window.showTeamAgentModal(); true`);
        await waitFor(`${vis('#team-agent-modal .settings-tabs')} && !!document.querySelector('#team-agent-modal-acf .acf-name')`, 'the agent dialog');
        const TA = '#team-agent-modal';
        assert.deepEqual(await names(TA), [['Role', 'badge'], ['Agent', 'smart_toy'], ['Prompt', 'description'], ['Permissions', 'lock'], ['Advanced', 'tune']]);
        await go(TA, 'agent');
        // The agent form fills in after it opens (the models arrive and it is drawn again), which can replace the name field the problem was marked on: Save is pressed again until the empty name has the focus.
        await waitFor(`(() => { if (document.activeElement.classList.contains('acf-name')) return true; document.querySelector('#team-agent-modal [data-on-click="m-save-team-agent-from-modal"]').click(); return false; })()`, 'the empty name focused');
        v = await view(TA); assert.equal(v.open, 'role', 'Save without a name opens Role'); assert.equal(await dot(TA, 'Role'), true);
        await shot('team-agent-role-desktop');
        await ev(`window.hideTeamAgentModal(); window.hideLaunchModal(); true`);
        console.log('PASS dialog sections: Restart, New Agent, Agent Team');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
