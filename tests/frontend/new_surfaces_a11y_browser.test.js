// Accessibility of the surfaces that shipped in the overnight run (#5042), read from Chrome's own accessibility tree (role, name, states) and driven by the keyboard,
// not by clicks, at desktop and at 390 px, then the contrast of what they draw in both themes (helpers/contrast.js):
//   1. The chat-bar model chip and its menu: a button whose name holds what it shows, a menu of menuitemradio items, Enter/Arrows/Home/End/Escape/Tab, focus
//      back on the chip after Escape and after a choice, and the "switches after this reply" note in a live region that is in the page when it is empty.
//   2. A sectioned dialog (Restart): a named tablist, tabs with their selected state, panels named by their tab, and a section's problem in its tab's name.
//   3. The Display panel and its summary line: a button with expanded state and a name that carries the count of what is on; Enter opens and moves into the panel, Escape closes.
//   4. The team health icon: a named button saying the monitor's state.
//   5. The Cost by-thinking table: column headers that are sort buttons (focusable, with aria-sort) and filter buttons with names.
//   6. A chat image chip's thumbnail: a named button the keyboard opens in the Preview pane.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const { Input, DOM, Accessibility } = client;
    const MOD = `(window.__coralStatic || '/static/')`;
    const LEVELS = ['low', 'medium', 'high', 'xhigh', 'max'];
    const OPUS = { value: 'claude-opus-5-5', display_name: 'Opus 5.5', efforts: LEVELS };
    const SONNET = { value: 'claude-sonnet-5-5', display_name: 'Sonnet 5.5', efforts: ['low', 'medium', 'high'] };
    const TEAM = 'a11y-' + Date.now().toString(36);
    const SUMMARY = { totals: { input_tokens: 10, output_tokens: 20, cost_usd: 4 }, by_model: [], by_agent: [], by_thinking: [
        { agent_type: 'claude', model: 'claude-opus-5-5', thinking: 'high', input_tokens: 2000, output_tokens: 300, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 4000, cost_usd: 2.0, num_calls: 3 },
        { agent_type: 'claude', model: 'claude-opus-5-5', thinking: 'low', input_tokens: 800, output_tokens: 60, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, cost_usd: 1.0, num_calls: 2 }] };
    const KEYS = { Enter: 13, Escape: 27, Tab: 9, ArrowDown: 40, ArrowUp: 38, Home: 36, End: 35, ' ': 32 };
    const key = async (k, shift = false) => {
        const base = { key: k, code: k === ' ' ? 'Space' : k, windowsVirtualKeyCode: KEYS[k], modifiers: shift ? 8 : 0 };
        const text = k === 'Enter' ? '\r' : k === ' ' ? ' ' : undefined;
        await Input.dispatchKeyEvent({ type: text ? 'keyDown' : 'rawKeyDown', ...base, ...(text ? { text } : {}) });
        await Input.dispatchKeyEvent({ type: 'keyUp', ...base });
    };
    // role, name and states of the first element matching sel, as Chrome computes them
    const ax = async sel => {
        const { root } = await DOM.getDocument({ depth: 0 });
        const { nodeId } = await DOM.querySelector({ nodeId: root.nodeId, selector: sel });
        assert.ok(nodeId, 'no element for ' + sel);
        const { nodes } = await Accessibility.getPartialAXTree({ nodeId, fetchRelatives: false });
        const n = nodes[0];
        return { role: n.role && n.role.value, name: (n.name && n.name.value) || '', ignored: !!n.ignored, props: Object.fromEntries((n.properties || []).map(p => [p.name, p.value && p.value.value])) };
    };
    const active = () => ev(`(() => { const a = document.activeElement; return a ? (a.id || a.className || a.tagName) : ''; })()`);
    const focus = sel => ev(`document.querySelector(${JSON.stringify(sel)}).focus(); true`);
    const open = async (w, h, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat'); true`);
    };
    const stubs = () => ev(`(() => { const R = window.fetch; window.__posts = [];
        const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
        window.fetch = (u, o = {}) => { const url = new URL(String(u && u.url || u), location.origin); const m = (o.method || 'GET').toUpperCase();
            if (url.pathname === '/api/agents/models') return json({ agent_type: 'claude', models: [${JSON.stringify(OPUS)}, ${JSON.stringify(SONNET)}], thinking: ${JSON.stringify(LEVELS)} });
            if (/\\/api\\/sessions\\/live\\/[^/]+\\/model$/.test(url.pathname) && m === 'POST') { window.__posts.push(JSON.parse(o.body)); return json({ ok: true }, 202); }
            if (url.pathname.includes('/api/token-usage/summary')) return json(${JSON.stringify(SUMMARY)});
            if (url.pathname.startsWith('/api/token-usage/')) return json({});
            return R(u, o); };
        window.__m.add('a11y-a', 'A', 3, { sleeping: false, agent_type: 'claude' });
        T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
    const feed = extra => ev(`(() => { const a = window.__m.agents['a11y-a-id']; T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(a), ...${JSON.stringify(extra)} }] }); return true; })()`);

    try {
        await Page.enable(); await DOM.enable(); await Accessibility.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });

        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            const L = `${w} px`;
            await open(w, h, mobile);
            await stubs();

            console.log(`${L}: 1. the model chip, the thinking chip and their menus`);
            await feed({ agent_type: 'claude', model: OPUS.value, thinking_actual: 'high', thinking: 'high' });
            await select('a11y-a');
            await waitFor(`!document.getElementById('model-chip-bar').hidden && document.getElementById('model-chip-text').textContent.includes('Opus') && document.getElementById('thinking-chip-text').textContent === 'High'`, 'the chips', 8000);
            if (!mobile) { // (the phone has no chip bar: the sidebar's menu is not drawn at that width)
                const noteEmpty = await ax('#model-chip-note');
                assert.ok(!noteEmpty.ignored, 'the live region is in the accessibility tree while empty, so a note that appears is announced');
                assert.equal(await ev(`getComputedStyle(document.getElementById('model-chip-note')).display`) !== 'none', true);
                for (const [kind, nameHas, itemsMin, choose] of [['model', ['Model', 'Opus 5.5'], 1, null], ['thinking', ['Thinking', 'High'], 4, 'low']]) {
                    const chip = `#${kind}-chip`, menuSel = `#${kind}-chip-menu`, id = `${kind}-chip`;
                    let c = await ax(chip);
                    assert.equal(c.role, 'button');
                    for (const part of nameHas) assert.ok(c.name.includes(part), `the ${kind} chip's name holds what it shows (label in name): "${c.name}"`);
                    assert.equal(c.props.hasPopup, 'menu'); assert.equal(c.props.expanded, false);
                    await focus(chip); await key('Enter');
                    await waitFor(`!document.querySelector(${JSON.stringify(menuSel)}).hidden && document.querySelectorAll(${JSON.stringify(menuSel + ' .model-chip-item')}).length > ${itemsMin}`, `the ${kind} menu opens from the keyboard`, 5000);
                    assert.equal((await ax(chip)).props.expanded, true);
                    const menu = await ax(menuSel); assert.equal(menu.role, 'menu'); assert.ok(menu.name);
                    const first = await ax(menuSel + ' .model-chip-item');
                    assert.equal(first.role, 'menuitemradio'); assert.ok('checked' in first.props, 'a radio item says whether it is checked');
                    assert.equal(await ev(`document.activeElement.classList.contains('model-chip-item')`), true, 'focus is in the menu once it is open');
                    const names = () => ev(`document.activeElement.textContent.replace('check', '').trim()`);
                    const n0 = await names(); await key('ArrowDown'); const n1 = await names(); assert.notEqual(n0, n1, 'ArrowDown moves');
                    await key('End'); const last = await ev(`[...document.querySelectorAll(${JSON.stringify(menuSel + ' .model-chip-item')})].at(-1) === document.activeElement`);
                    assert.ok(last, 'End goes to the last item'); await key('Home');
                    assert.equal(await ev(`document.querySelector(${JSON.stringify(menuSel + ' .model-chip-item')}) === document.activeElement`), true, 'Home goes to the first');
                    await key('Escape');
                    assert.equal(await ev(`document.querySelector(${JSON.stringify(menuSel)}).hidden`), true); assert.equal(await active(), id, 'Escape gives focus back to its own chip');
                    await key('Enter'); await waitFor(`!document.querySelector(${JSON.stringify(menuSel)}).hidden`, 'reopened', 4000);
                    await key('Tab');
                    assert.equal(await ev(`document.querySelector(${JSON.stringify(menuSel)}).hidden`), true, 'Tab closes the menu');
                    if (choose) {
                        await focus(chip); await key('Enter'); await waitFor(`!document.querySelector(${JSON.stringify(menuSel)}).hidden && !!document.querySelector(${JSON.stringify(menuSel + ' [data-level="low"]')})`, 'reopened again', 4000);
                        await ev(`document.querySelector(${JSON.stringify(menuSel + ' [data-level="low"]')}).focus(); true`); await key('Enter');
                        await waitFor(`window.__posts.length === 1`, 'the choice is sent', 4000);
                        assert.equal(await active(), id, 'after a choice focus is on its own chip, not lost with the menu');
                    }
                }
                await feed({ agent_type: 'claude', model: OPUS.value, thinking: 'high', model_switch: { state: 'pending', thinking: 'low', after_reply: true } });
                await waitFor(`document.getElementById('model-chip-note').textContent.length > 0`, 'the note', 4000);
                assert.match(await ev(`document.getElementById('model-chip-note').textContent`), /after this reply/i);
                const note = await ax('#model-chip-note'); assert.equal(note.role, 'status'); assert.ok(!note.ignored, 'the note is in the tree');
                await feed({ agent_type: 'claude', model: OPUS.value, thinking: 'low', thinking_actual: 'low', model_switch: null });
            }

            console.log(`${L}: 3. the Display panel and its summary line`);
            const hdr = mobile ? '.mobile-list-header' : '.sidebar-header';
            if (mobile) await ev(`window.switchNavTab('agents'); window.syncMobileAgentList && window.syncMobileAgentList(); true`);
            const btn = `${hdr} [data-display-button]`;
            let d = await ax(btn);
            assert.equal(d.role, 'button'); assert.ok(/Display options/.test(d.name), d.name); assert.equal(d.props.expanded, false);
            await focus(btn); await key('Enter');
            assert.equal((await ax(btn)).props.expanded, true, 'Enter opens the panel');
            assert.equal(await ev(`!!document.activeElement.closest('${hdr} [data-display-panel]')`), true, 'and moves the keyboard into it');
            const panel = await ax(`${hdr} [data-display-panel]`); assert.equal(panel.role, 'group'); assert.ok(panel.name, 'the panel is named');
            await ev(`(() => { const s = document.querySelector('${hdr} [data-display-panel] select'); const o = [...s.options].find(x => x.value); s.value = o.value; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            d = await ax(btn); assert.match(d.name, /Display options, 1 filter on/, 'the button says a filter is on: ' + d.name);
            await key('Escape');
            assert.equal((await ax(btn)).props.expanded, false, 'Escape closes it'); assert.equal(await ev(`!!document.activeElement.closest('${btn}') || document.activeElement.matches('${btn}')`), true, 'and focus is back on the Display button');
            assert.equal(await ev(`document.querySelector('${hdr} [data-agent-list-summary]') === null`), true, 'no summary line under the search (#6597): the button names what is on');

            console.log(`${L}: 6. a chat chip's thumbnail`);
            await ev(`import(${MOD} + 'live_chat.js').then(m => { document.getElementById('a11y-box')?.remove(); const box = document.createElement('div'); box.id = 'a11y-box'; box.className = 'chat-messages chat-prose';
                box.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px'; document.body.appendChild(box);
                m.renderTranscript([{ type: 'user', content: '[Image #1]', attachments: [{ name: '[Image #1]', file: 'shot.png' }] }], box, 'codex');
                return true; })`);
            await waitFor(`!!document.querySelector('#a11y-box .chat-attachment-thumb')`, 'the chip', 5000);
            await ev(`(() => { const b = document.querySelector('#a11y-box .chat-attachment-thumb'); b.hidden = false; b.querySelector('img').src = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7'; return true; })()`);
            const th = await ax('#a11y-box .chat-attachment-thumb');
            assert.equal(th.role, 'button'); assert.match(th.name, /^Open .+/, th.name);
            assert.equal(await ev(`document.querySelector('#a11y-box .chat-attachment-thumb img').alt.length > 0`), true, 'the image has alt text');
            await ev(`document.getElementById('a11y-box').remove(); true`);

            console.log(`${L}: 5. the Cost by-thinking table`);
            await ev(`window.switchNavTab('tokens'); true`);
            await waitFor(`document.querySelectorAll('#cost-by-thinking tbody tr').length === 2`, 'the thinking table', 8000);
            const ths = await ev(`[...document.querySelectorAll('#cost-by-thinking th')].map(t => ({ sort: t.getAttribute('aria-sort'), btn: !!t.querySelector('button.cost-sort-btn'), filter: [...t.querySelectorAll('button.cost-col-filter')].map(b => b.getAttribute('aria-label')) }))`);
            assert.ok(ths.length === 11 && ths.every(t => t.btn && ['none', 'ascending', 'descending'].includes(t.sort)), 'every column header is a sort button with aria-sort: ' + JSON.stringify(ths));
            assert.ok(ths[0].filter[0] && /Filter/.test(ths[0].filter[0]), 'a filter button has a name: ' + JSON.stringify(ths[0]));
            assert.equal(ths.filter(t => t.sort !== 'none').length, 1, 'exactly one column is the sort');
            const lvl = `#cost-by-thinking th[data-field="level"] button.cost-sort-btn`;
            const before = await ev(`document.querySelector('#cost-by-thinking th[data-field="level"]').getAttribute('aria-sort')`);
            await focus(lvl); await key('Enter');
            await waitFor(`document.querySelector('#cost-by-thinking th[data-field="level"]').getAttribute('aria-sort') !== ${JSON.stringify(before)}`, 'Enter sorts by the column', 4000);
            const sb = await ax(lvl); assert.equal(sb.role, 'button'); assert.match(sb.name, /^thinking level/i, sb.name);
            assert.equal(await ev(`document.querySelectorAll('#cost-by-thinking th[aria-sort]:not([aria-sort="none"])').length`), 1);
            assert.equal(await ev(`document.activeElement.classList.contains('cost-sort-btn') && document.activeElement.closest('th') === document.querySelector('#cost-by-thinking th[data-field="level"]')`), true, 'after a sort the keyboard is still on that column\'s button (the table is drawn again)');
            // the filter buttons of the headings open a list and filter the rows (they opened nothing)
            const rowsBefore = await ev(`document.querySelectorAll('#cost-by-thinking tbody tr').length`);
            await ev(`document.querySelector('#cost-by-thinking th[data-field="level"] button.cost-col-filter').click(); true`);
            await waitFor(`document.querySelectorAll('.cost-filter-dropdown .cost-filter-item input').length >= 2`, 'the Thinking level filter list', 4000);
            await ev(`document.querySelector('.cost-filter-dropdown .cost-filter-item input').click(); true`);
            await waitFor(`document.querySelectorAll('#cost-by-thinking tbody tr').length < ${rowsBefore}`, 'unticking a level hides its rows', 4000);
            await ev(`document.body.click(); true`);
        }

        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            console.log(`${w} px: 4. the team health icon`);
            await open(w, h, mobile);
            await ev(`import(${MOD} + 'render.js').then(async r => { const { state } = await import(${MOD} + 'state.js');
                state.liveSessions = ${JSON.stringify([1, 2].map(i => ({ session_id: `hl-${i}`, name: `hl-agent-${i}`, display_name: `Agent ${i}`, agent_type: 'claude', working_directory: '/tmp/hl', board_project: TEAM, status: 'idle', team_health_on: i === 1 })))};
                r.flushLiveSessionsRender(); r.renderLiveSessions(state.liveSessions); return true; })`);
            await waitFor(`!!document.querySelector('.team-health-btn')`, 'the health icon', 5000);
            const hb = await ax('.team-health-btn');
            assert.equal(hb.role, 'button'); assert.match(hb.name, /Team health monitor: (on|off)/, hb.name);
            assert.equal(await ev(`document.querySelector('.team-health-btn').tabIndex >= 0`), true, 'the keyboard reaches it');
            assert.equal(await ev(`document.querySelector('.team-health-btn').hasAttribute('aria-pressed')`), false, 'it opens the settings, it does not toggle: its state is in its name, not a pressed state');
        }

        console.log('2. a sectioned dialog (Restart): tablist, tabs, panels, a section with a problem');
        await open(1280, 900, false);
        await ev(`T.state.currentSession = ${JSON.stringify({ name: 'ag', display_name: 'QA Engineer', session_id: 'sid-r', type: 'live', agent_type: 'claude', model: 'claude-opus-4', prompt: 'Be kind.', capabilities: { allow: ['file_read'] }, tmux_session: 'ag' })}; window.restartSession(); true`);
        await waitFor(`!!document.querySelector('#restart-modal .settings-tabs [role=tab]') && !!document.querySelector('#restart-acf .acf-agent-type')`, 'the restart dialog', 8000);
        const tl = await ax('#restart-modal .settings-tabs'); assert.equal(tl.role, 'tablist'); assert.ok(tl.name, 'the tablist is named'); assert.equal(tl.props.orientation, 'vertical');
        const tabs = await ev(`[...document.querySelectorAll('#restart-modal .settings-tabs [role=tab]')].map(t => ({ id: t.id, sel: t.getAttribute('aria-selected'), tab: t.tabIndex, ctl: t.getAttribute('aria-controls') }))`);
        assert.ok(tabs.length >= 3 && tabs.filter(t => t.sel === 'true').length === 1 && tabs.filter(t => t.tab === 0).length === 1, 'one selected tab is the one in the Tab order (roving): ' + JSON.stringify(tabs));
        const panelOf = await ev(`(() => { const p = document.getElementById(document.querySelector('#restart-modal .settings-tabs [aria-selected=true]').getAttribute('aria-controls')); return { role: p.getAttribute('role'), labelled: p.getAttribute('aria-labelledby') }; })()`);
        assert.equal(panelOf.role, 'tabpanel'); assert.ok(panelOf.labelled, 'the panel is named by its tab');
        await focus('#restart-modal .settings-tabs [aria-selected=true]'); await key('ArrowDown');
        assert.equal(await ev(`document.activeElement.getAttribute('aria-selected')`), 'true', 'an arrow moves to the next section and opens it');
        // a field with a problem in another section: its tab's name says so, and the status the dialog gives is in the tree
        await ev(`(() => { const f = document.querySelector('#restart-acf .acf-prompt'); f.setAttribute('aria-invalid', 'true'); return true; })()`);
        await waitFor(`!![...document.querySelectorAll('#restart-modal .settings-tabs [role=tab]')].find(t => /has a problem/.test(t.getAttribute('aria-label') || ''))`, 'the problem is in a tab name', 4000);
        const bad = await ev(`[...document.querySelectorAll('#restart-modal .settings-tabs [role=tab]')].find(t => /has a problem/.test(t.getAttribute('aria-label') || '')).id`);
        const badAx = await ax('#' + bad); assert.match(badAx.name, /has a problem/, badAx.name);
        assert.equal(await ev(`document.querySelector('#${bad} .sd-dot').getAttribute('aria-hidden')`), 'true', 'the dot itself is decoration: the name carries the problem');
        await ev(`document.querySelector('#restart-acf .acf-prompt').removeAttribute('aria-invalid'); true`);

        console.log('the contrast of all of it, light and dark');
        await open(1280, 900, false); await stubs(); await installAudit(ev);
        await feed({ agent_type: 'claude', model: OPUS.value, thinking_actual: 'high', thinking: 'high', model_switch: { state: 'pending', thinking: 'low', after_reply: true } });
        await select('a11y-a'); await waitFor(`!document.getElementById('model-chip-bar').hidden`, 'chip', 8000);
        for (const theme of ['light', 'dark']) {
            await setTheme(ev, theme);
            for (const [state, extra] of [['pending', { model_switch: { state: 'pending', thinking: 'low', after_reply: true } }], ['failed', { model_switch: { state: 'failed', message: 'The CLI refused the change.' } }], ['requested', { model_switch: null, thinking: 'low', thinking_actual: null }]]) {
                await feed({ agent_type: 'claude', model: OPUS.value, ...extra });
                await waitFor(`document.getElementById('thinking-chip').dataset.state === ${JSON.stringify(state)}`, 'the chip state ' + state, 4000);
                const r = await auditRegion(ev, ['#model-chip-bar', '.sidebar-header'], { focus: ['#model-chip', '#thinking-chip'] });
                assert.deepEqual(r.fails, [], `${theme}, ${state}: the chip bar and the list header reach contrast: ` + JSON.stringify(r.fails));
            }
            for (const kind of ['model', 'thinking']) {
                await ev(`document.getElementById('${kind}-chip').click(); true`);
                await waitFor(`!document.getElementById('${kind}-chip-menu').hidden && document.querySelectorAll('#${kind}-chip-menu .model-chip-item').length > 1`, 'the ' + kind + ' menu', 4000);
                const m = await auditRegion(ev, [`#${kind}-chip-menu`], { focus: [`#${kind}-chip-menu .model-chip-item`] });
                assert.deepEqual(m.fails, [], `${theme}: the ${kind} chip's menu reaches contrast: ` + JSON.stringify(m.fails));
                await ev(`document.getElementById('${kind}-chip').click(); true`);
            }
        }
        // the Display panel open (its fields) and shut with a filter on (the summary line), the sectioned dialog's list, and a chip's thumbnail
        await ev(`window.switchNavTab('agents'); true`);
        for (const theme of ['light', 'dark']) {
            await setTheme(ev, theme);
            await ev(`(() => { const b = document.querySelector('.sidebar-header [data-display-button]'); if (b.getAttribute('aria-expanded') !== 'true') b.click(); return true; })()`);
            await ev(`(() => { const s = document.querySelector('.sidebar-header [data-display-panel] select'); const o = [...s.options].find(x => x.value); s.value = o.value; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            let r = await auditRegion(ev, ['.sidebar-header'], { focus: ['.sidebar-header [data-display-button]', '.sidebar-header [data-display-panel] select'] });
            assert.deepEqual(r.fails, [], `${theme}: the Display panel open reaches contrast: ` + JSON.stringify(r.fails));
            await ev(`document.querySelector('.sidebar-header [data-display-button]').click(); true`);
            await waitFor(`!document.querySelector('.sidebar-header [data-display-badge]').hidden`, 'the filter dot', 4000);
            r = await auditRegion(ev, ['.sidebar-header'], { focus: ['.sidebar-header [data-display-button]'] });
            assert.deepEqual(r.fails, [], `${theme}: the closed header with the filter dot reaches contrast: ` + JSON.stringify(r.fails));
            await ev(`(() => { const s = document.querySelector('.sidebar-header [data-display-panel] select'); s.value = ''; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        }
        await ev(`import(${MOD} + 'live_chat.js').then(m => { document.getElementById('a11y-box')?.remove(); const box = document.createElement('div'); box.id = 'a11y-box'; box.className = 'chat-messages chat-prose';
            box.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px'; document.body.appendChild(box);
            m.renderTranscript([{ type: 'user', content: 'look', attachments: [{ name: '[Image #1]', file: 'shot.png' }] }], box, 'codex'); return true; })`);
        await waitFor(`!!document.querySelector('#a11y-box .chat-attachment-thumb')`, 'the chip', 5000);
        await ev(`(() => { const b = document.querySelector('#a11y-box .chat-attachment-thumb'); b.hidden = false; b.querySelector('img').src = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7'; return true; })()`);
        for (const theme of ['light', 'dark']) {
            await setTheme(ev, theme);
            const r = await auditRegion(ev, ['#a11y-box'], { focus: ['#a11y-box .chat-attachment-thumb'] });
            assert.deepEqual(r.fails, [], `${theme}: the chip and its thumbnail reach contrast: ` + JSON.stringify(r.fails));
        }
        await ev(`document.getElementById('a11y-box').remove(); true`);
        await ev(`T.state.currentSession = ${JSON.stringify({ name: 'ag', display_name: 'QA Engineer', session_id: 'sid-r', type: 'live', agent_type: 'claude', model: 'claude-opus-4', prompt: 'Be kind.', capabilities: { allow: ['file_read'] }, tmux_session: 'ag' })}; window.restartSession(); true`);
        await waitFor(`!!document.querySelector('#restart-modal .settings-tabs [role=tab]')`, 'the restart dialog', 8000);
        await ev(`document.querySelector('#restart-acf .acf-prompt').setAttribute('aria-invalid', 'true'); true`);
        for (const theme of ['light', 'dark']) {
            await setTheme(ev, theme);
            const r = await auditRegion(ev, ['#restart-modal .settings-nav'], { focus: ['#restart-modal .settings-tabs [aria-selected=true]'] });
            assert.deepEqual(r.fails, [], `${theme}: the sectioned dialog's list (with a problem dot) reaches contrast: ` + JSON.stringify(r.fails));
        }
        await ev(`document.querySelector('#restart-acf .acf-prompt').removeAttribute('aria-invalid'); window.hideRestartModal && window.hideRestartModal(); true`);
        await ev(`window.switchNavTab('tokens'); true`); await waitFor(`document.querySelectorAll('#cost-by-thinking tbody tr').length === 2`, 'cost table', 8000);
        for (const theme of ['light', 'dark']) {
            await setTheme(ev, theme);
            const r = await auditRegion(ev, ['#cost-by-thinking'], { focus: ['#cost-by-thinking button.cost-sort-btn'] });
            assert.deepEqual(r.fails, [], `${theme}: the cost table reaches contrast: ` + JSON.stringify(r.fails));
        }
        console.log('PASS new surfaces: roles, names, keyboard and contrast of the chip, sectioned dialogs, Display panel, health icon, cost table and thumbnails');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
