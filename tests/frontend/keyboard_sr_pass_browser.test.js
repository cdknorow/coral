// Keyboard and screen-reader pass over the v1.4.74 UI (#4272): the tab chooser says what a move or a hide did and lets Tab leave it, the
// sidebar's display menu closes on Escape (focus back on its button) and a server's details menu takes the arrow keys, the Health check
// tab's controls are named by their own title (the explanation is the description, the number's limits too, and a rule's numbers sit in
// a group named for the rule), and a folded steps group opens by keyboard, keeps focus while it grows and is not a live region.
// Drives the real dashboard over CDP; run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const { Input, Accessibility, DOM } = client;
    const KEYS = { Tab: 9, ArrowUp: 38, ArrowDown: 40, Escape: 27, Enter: 13, Home: 36, End: 35 };
    const key = async (k, mods = 0) => {
        const o = { key: k, code: k, windowsVirtualKeyCode: KEYS[k], modifiers: mods };
        await Input.dispatchKeyEvent({ type: 'keyDown', ...o, text: k === 'Enter' ? '\r' : undefined });
        await Input.dispatchKeyEvent({ type: 'keyUp', ...o });
    };
    const SHIFT = 8;
    const ok = name => console.log(`   ✓ ${name}`);
    const active = () => ev(`(() => { const a = document.activeElement; return a ? (a.id || a.className.split(' ')[0] || a.tagName) + ':' + (a.dataset.tab || a.dataset.displayInput || '') : null; })()`);

    let TEAM = ''; // the board this suite makes, taken away again in the finally (#4959)
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.removeItem('coral-agentic-tab-prefs'); window.__m.add('kb-bot', 'A', 3, { sleeping: false }); true`);
        await sleep(600); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('kb-bot', 'claude', 'kb-bot-id', 'local'); true`);
        await ev(`(() => { const p = document.getElementById('agentic-state'); if (p.classList.contains('collapsed') || document.getElementById('agentic-tab-files').getBoundingClientRect().width === 0) window.toggleAgenticPanel(true); return true; })()`);
        await waitFor(`document.getElementById('agentic-tab-files').getBoundingClientRect().width > 0`, 'side panel open', 8000);

        // 1. The tab chooser: a status line says the result of a move, a hide and a reset; Tab past either end closes it and returns focus.
        await ev(`document.getElementById('agentic-tab-customize').focus(); true`);
        await key('Enter');
        await waitFor(`!!document.getElementById('tab-customize')`, 'chooser opens');
        const live = await ev(`(() => { const s = document.querySelector('#tab-customize .tc-status'); return { role: s.getAttribute('role'), live: s.getAttribute('aria-live'), text: s.textContent }; })()`);
        assert.deepEqual(live, { role: 'status', live: 'polite', text: '' });
        const first = await ev(`document.querySelector('#tab-customize .tc-row:nth-child(2)').dataset.tab`);
        const firstName = await ev(`document.getElementById('agentic-tab-' + ${JSON.stringify(first)}).getAttribute('aria-label')`);
        await ev(`document.querySelector('#tab-customize .tc-move[data-tab="${first}"][data-dir="1"]').click(); true`);
        const moved = await ev(`document.querySelector('#tab-customize .tc-status').textContent`);
        assert.equal(moved, `${firstName} moved to position 3 of ${await ev(`document.querySelectorAll('#tab-customize .tc-row').length`)}`);
        await ev(`document.querySelector('#tab-customize .tc-check[data-tab="${first}"]').click(); true`);
        assert.equal(await ev(`document.querySelector('#tab-customize .tc-status').textContent`), `${firstName} hidden`);
        await ev(`document.querySelector('#tab-customize .tc-reset').click(); true`);
        assert.equal(await ev(`document.querySelector('#tab-customize .tc-status').textContent`), 'Tabs reset to the default');
        assert.equal(await ev(`getComputedStyle(document.querySelector('#tab-customize .tc-status')).width`), '1px', 'spoken, never drawn');
        ok('a move, a hide and a reset are each said in the chooser\'s polite status line (which is not drawn)');

        await ev(`document.querySelector('#tab-customize .tc-reset').focus(); true`);
        await key('Tab');
        assert.equal(await ev(`!!document.getElementById('tab-customize')`), false, 'Tab past the last control closes the chooser');
        assert.equal(await active(), 'agentic-tab-customize:', 'focus returns to the Customize tabs button');
        await key('Enter');
        await waitFor(`!!document.getElementById('tab-customize')`, 'chooser reopens');
        await ev(`document.querySelector('#tab-customize input:not(:disabled), #tab-customize button').focus(); true`);
        await key('Tab', SHIFT);
        assert.equal(await ev(`!!document.getElementById('tab-customize')`), false, 'Shift+Tab before the first control closes it');
        assert.equal(await active(), 'agentic-tab-customize:');
        ok('Tab past the last control and Shift+Tab before the first close the chooser and put focus on its button');

        // 2. The Display panel (#4947, an expander): the button, a disclosure (aria-expanded, aria-controls), opens it and puts the keyboard in its first control; Escape closes it with focus back on the button.
        assert.equal(await ev(`document.getElementById('sidebar-summary-btn').getAttribute('aria-expanded') + '|' + document.getElementById('sidebar-summary-btn').getAttribute('aria-controls')`), 'false|display-panel', 'a disclosure button naming its panel');
        await ev(`document.getElementById('sidebar-summary-btn').focus(); document.getElementById('sidebar-summary-btn').click(); true`);
        await waitFor(`!document.getElementById('display-panel').hidden && document.getElementById('sidebar-summary-btn').getAttribute('aria-expanded') === 'true'`, 'display panel opens');
        await waitFor(`document.activeElement && document.getElementById('display-panel').contains(document.activeElement)`, 'a control in the panel is focused');
        await key('Escape');
        assert.equal(await ev(`document.getElementById('display-panel').hidden`), true, 'Escape closes the display panel');
        assert.equal(await active(), 'sidebar-summary-btn:', 'focus is back on the Display options button');
        ok('display options: the button opens the panel and focuses its first control, Escape closes and returns focus');

        // 3. A server's details menu (the markup server_status.js draws, as a fixture): Down opens, Up/Down/Home/End move over its buttons, Escape returns focus.
        await ev(`(() => { const d = document.createElement('details'); d.id = 'kb-server-details'; d.className = 'server-group-more'; d.setAttribute('data-server-details', 'kb');
            d.innerHTML = '<summary id="kb-sum" aria-label="Details for kb">⋮</summary><div class="server-switcher-panel"><button type="button" id="kb-b1">Launch team</button><button type="button" id="kb-b2" disabled>Gone</button><button type="button" id="kb-b3">Open in Servers</button><button type="button" id="kb-b4">Collapse</button></div>';
            document.body.appendChild(d); document.getElementById('kb-sum').focus(); return true; })()`);
        await key('ArrowDown');
        await waitFor(`document.activeElement.id === 'kb-b1'`, 'first button focused', 3000, `document.activeElement.id`);
        await key('ArrowDown'); assert.equal(await ev(`document.activeElement.id`), 'kb-b3', 'a disabled button is skipped');
        await key('End'); assert.equal(await ev(`document.activeElement.id`), 'kb-b4');
        await key('ArrowDown'); assert.equal(await ev(`document.activeElement.id`), 'kb-b4', 'Down stops at the last');
        await key('Home'); assert.equal(await ev(`document.activeElement.id`), 'kb-b1');
        await key('ArrowUp'); assert.equal(await ev(`document.activeElement.id`), 'kb-b1', 'Up stops at the first');
        await key('Escape');
        assert.equal(await ev(`document.getElementById('kb-server-details').open`), false);
        assert.equal(await ev(`document.activeElement.id`), 'kb-sum');
        await ev(`document.getElementById('kb-server-details').remove(); true`);
        ok('a server\'s details menu takes Down, Up, Home and End (skipping a disabled button) and Escape returns focus to its button');

        // 4. A folded steps group: a summary that Enter opens, focus stays on it while it grows, and nothing around it is a live region.
        const fold = await ev(`import((window.__coralStatic || '/static/') + 'chat_content.js').then(m => {
            const box = document.createElement('div'); box.id = 'kb-chat'; document.body.appendChild(box);
            const step = t => { const d = document.createElement('div'); d.className = 'chat-bubble'; d.textContent = t; return d; };
            m.appendWork(box, step('one')); const g = box.querySelector('.work-group'); window.__kbGroup = g;
            window.__kbAdd = () => m.appendWork(box, step('more'));
            return { tag: g.querySelector('summary').tagName, live: !!box.closest('[aria-live], [role=log], [role=status], [role=alert]') || !!g.querySelector('[aria-live]'), open: g.open };
        })`);
        assert.deepEqual(fold, { tag: 'SUMMARY', live: false, open: false });
        await ev(`document.querySelector('#kb-chat summary').focus(); true`);
        await key('Enter');
        assert.equal(await ev(`window.__kbGroup.open`), true, 'Enter opens the group');
        await ev(`window.__kbAdd(); true`);
        assert.equal(await ev(`document.activeElement === window.__kbGroup.querySelector('summary')`), true, 'focus stays on the summary when a step is added');
        assert.match(await ev(`window.__kbGroup.querySelector('.work-group-count').textContent`), /^2 steps$/);
        assert.equal(await ev(`getComputedStyle(window.__kbGroup.querySelector('summary')).outlineStyle !== 'none'`), true, 'the focused summary has a visible ring');
        await key('Enter');
        assert.equal(await ev(`window.__kbGroup.open`), false, 'Enter closes it again');
        await ev(`document.getElementById('kb-chat').remove(); true`);
        ok('a steps group opens and closes by Enter, keeps focus and a visible ring while it grows, and is not in a live region');

        // 5. The Health check tab's names: the title only (the explanation and the limits are descriptions), numbers inside a group named for their rule.
        TEAM = 'kb-health-team';
        for (const [id, title] of [['Boss', 'Orchestrator'], ['Dev One', 'Developer']]) {
            const r = await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: ${JSON.stringify(id)}, job_title: ${JSON.stringify(title)}, session_name: ${JSON.stringify(id.toLowerCase().replace(' ', '-') + '-session')} }) }).then(r => r.status)`);
            assert.ok(r < 300, `subscribe ${id}: ${r}`);
        }
        await ev(`import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
        await waitFor(`!!document.querySelector('[data-settings-tab=health]')`, 'the Health check tab', 15000);
        await ev(`document.querySelector('[data-settings-tab=health]').click(); true`);
        await waitFor(`!document.getElementById('team-settings-health').hidden`, 'the panel shows');
        await DOM.enable(); await Accessibility.enable();
        const doc = await DOM.getDocument({ depth: -1 });
        const ax = async selector => {
            const { nodeId } = await DOM.querySelector({ nodeId: doc.root.nodeId, selector });
            const { nodes } = await Accessibility.getPartialAXTree({ nodeId, fetchRelatives: false });
            const n = nodes[0];
            return { name: n.name?.value || '', desc: n.description?.value || '' };
        };
        const P = '#team-settings-health';
        const on = await ax(`${P} input[name=on]`);
        assert.equal(on.name, 'Send health check'); assert.match(on.desc, /Uses tokens each time\./); assert.match(on.desc, /Checks in on the team every few minutes/); // the hint, and the explanation behind the (i)
        const every = await ax(`${P} input[name=interval_min]`);
        assert.equal(every.name, 'Every minutes'); assert.equal(every.desc, '5 to 1440');
        await ev(`document.querySelector('${P} .health-advanced').open = true; true`); // Reviews and the automatic rules are behind one disclosure (#5589 finding 11)
        const rev = await ax(`${P} input[name=default_reviewers]`);
        assert.equal(rev.name, 'Default reviewers'); assert.match(rev.desc, /^Names of agents on this team/);
        const claim = await ax(`${P} input[name=claim_nudge_on]`);
        assert.equal(claim.name, 'Nudge an idle agent to claim its work'); assert.match(claim.desc, /^An agent that is idle/);
        const after = await ax(`${P} input[name=claim_nudge_after_min]`);
        assert.equal(after.name, 'After minutes idle'); assert.equal(after.desc, '5 to 1440');
        const groups = await ev(`[...document.querySelectorAll('${P} .health-rule')].map(g => [g.getAttribute('role'), document.getElementById(g.getAttribute('aria-labelledby')).textContent, g.querySelectorAll('input[type=number]').length])`);
        assert.deepEqual(groups.map(g => g[0]), groups.map(() => 'group'));
        assert.ok(groups.some(g => g[1] === 'Nudge an idle agent to claim its work' && g[2] === 2), JSON.stringify(groups));
        ok('Health check controls are named by their title, explained by their description, and each rule\'s numbers sit in a group named for the rule');
    } catch (e) {
        console.error(e);
        process.exitCode = 1;
    } finally {
        await removeBoards(ev, [TEAM]);
        await client.close();
    }
})();
