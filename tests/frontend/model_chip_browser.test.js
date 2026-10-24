// The model and thinking-level chip by the composer (#4969). Against stubbed /api/agents/models and POST .../model:
//   - the chip says the model and the level the agent actually runs (the row's model, thinking_actual); the level it was started with is
//     shown as requested (italic, a note) until a reply shows one; no chip for a sleeping agent or a terminal;
//   - its menu lists the models and the levels the chosen model takes (the models' own efforts narrow them); Claude switches live,
//     another type shows "Apply with restart" and opens the Restart dialog with the choice filled in;
//   - choosing a level sends {thinking}; the chip says "Switching to …" and "Switches after this reply" while the server's model_switch is
//     pending, the new value when it is done, and the server's message when it failed (as text, never markup);
//   - the composer stays enabled and typing works the whole time;
//   - a phone: the menu is a sheet inside the screen with 44 px items. Desktop and 390x844.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const LEVELS = ['low', 'medium', 'high', 'xhigh', 'max'];
    const OPUS = { value: 'claude-opus-5-5', display_name: 'Opus 5.5', efforts: LEVELS };
    const SONNET = { value: 'claude-sonnet-5-5', display_name: 'Sonnet 5.5', efforts: ['low', 'medium', 'high'] };
    const shot = async (name) => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        await ev(`(() => { const R = window.fetch; window.__posts = []; window.__switchAnswer = { status: 202, body: { ok: true } };
            const json = (b, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } });
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = (o.method || 'GET').toUpperCase();
                if (url.pathname === '/api/agents/models') { const type = url.searchParams.get('type');
                    return Promise.resolve(json(type === 'claude' ? { agent_type: type, models: [${JSON.stringify(OPUS)}, ${JSON.stringify(SONNET)}], thinking: ${JSON.stringify(LEVELS)} } : { agent_type: type, models: [{ value: 'gpt-x', display_name: 'GPT X', efforts: ['low', 'high'] }, { value: 'gpt-y', display_name: 'GPT Y' }] })); }
                if (/\\/api\\/sessions\\/live\\/[^/]+\\/model$/.test(url.pathname) && m === 'POST') { window.__posts.push(JSON.parse(o.body)); return Promise.resolve(json(window.__switchAnswer.body, window.__switchAnswer.status)); }
                return R(u, o); };
            for (const [n, type] of [['ch-a', 'claude'], ['ch-codex', 'codex'], ['ch-sleep', 'claude'], ['ch-term', 'terminal']]) window.__m.add(n, n.slice(-1).toUpperCase(), 3, { sleeping: n === 'ch-sleep', agent_type: type });
            T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
        // A feed row for an agent with the fields the server adds (#4969).
        const feed = (id, extra) => ev(`(() => { const a = window.__m.agents[${JSON.stringify(id)}]; T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(a), ...${JSON.stringify(extra)} }] }); return true; })()`);
        // Two chips (#5118): the model chip (model-chip) and the thinking chip (thinking-chip), each with its own menu (<id>-menu).
        const chip = (kind = 'model') => ev(`(() => { const id = ${JSON.stringify(kind + '-chip')}.replace('model-chip', 'model-chip'), c = document.getElementById(id), b = document.getElementById('model-chip-bar'), n = document.getElementById('model-chip-note');
            const r = c.getBoundingClientRect(); return { shown: !b.hidden && r.width > 0, text: document.getElementById(id + '-text').textContent, label: c.getAttribute('aria-label'), state: c.dataset.state, note: n.hidden ? '' : n.textContent, italic: getComputedStyle(document.getElementById(id + '-text')).fontStyle }; })()`);
        const menu = (kind = 'model') => ev(`(() => { const m = document.getElementById(${JSON.stringify(kind + '-chip-menu')}); const r = m.getBoundingClientRect();
            return { open: !m.hidden && r.width > 0, label: m.getAttribute('aria-label'), groups: [...m.querySelectorAll('.model-chip-group')].map(g => ({ name: g.getAttribute('aria-label'), head: g.querySelector('.model-chip-head').textContent.trim(), items: [...g.querySelectorAll('.model-chip-item')].map(i => i.textContent.replace('check', '').trim() + (i.classList.contains('checked') ? '*' : '')) })), empty: (m.querySelector('.model-chip-empty') || {}).textContent || '',
                rect: { l: Math.round(r.left), r: Math.round(r.right), t: Math.round(r.top), b: Math.round(r.bottom) }, minItem: Math.min(...[...m.querySelectorAll('.model-chip-item')].map(i => i.getBoundingClientRect().height)), vw: innerWidth, vh: innerHeight }; })()`);
        const chipId = kind => (kind === 'thinking' ? 'thinking-chip' : 'model-chip');
        const openMenuRaw = async kind => { await ev(`document.getElementById(${JSON.stringify(kind + '-chip')}).click(); true`); await waitFor(`!document.getElementById(${JSON.stringify(kind + '-chip-menu')}).hidden && !!document.querySelector(${JSON.stringify('#' + kind + '-chip-menu .model-chip-item, #' + kind + '-chip-menu .model-chip-empty')})`, 'the ' + kind + ' menu', 5000); };
        const openMenu = async (kind = 'model') => { await ev(`document.getElementById(${JSON.stringify(chipId(kind))}).click(); true`); await waitFor(`!document.getElementById(${JSON.stringify(chipId(kind) + '-menu')}).hidden && document.querySelectorAll(${JSON.stringify('#' + chipId(kind) + '-menu .model-chip-item')}).length > ${kind === 'thinking' ? 2 : 1}`, 'the ' + kind + ' menu', 5000); };
        const pick = sel => { const kind = sel.includes('data-level') ? 'thinking' : 'model'; return ev(`document.querySelector(${JSON.stringify('#' + chipId(kind) + '-menu ' + sel)}).click(); true`); };
        const text = kind => ev(`document.getElementById(${JSON.stringify(chipId(kind) + '-text')}).textContent`);

        // The chip is the first item of the buttons' row: outside the text box (a second or third line never runs under it, and the box keeps its full
        // height), inside the composer's border, level with Send; its note is a small line under that row (#5116). A phone keeps the chip as a row under the input.
        const geometry = () => ev(`(() => { const R = e => { const r = e.getBoundingClientRect(); return { l: r.left, r: r.right, t: r.top, b: r.bottom, h: r.height }; };
            const pane = document.getElementById('command-pane'), ta = document.getElementById('command-input'), chip = document.getElementById('model-chip'), tchip = document.getElementById('thinking-chip'), tb = document.getElementById('command-toolbar');
            const note = document.getElementById('model-chip-note'), send = tb.querySelector('.btn-send');
            const cs = getComputedStyle(pane); const bw = parseFloat(cs.borderLeftWidth) || 0;
            const pr = R(pane); return { pane: { l: pr.l + bw, r: pr.r - bw, t: pr.t + bw, b: pr.b - bw }, ta: R(ta), chip: R(chip), thinking: R(tchip), toolbar: R(tb), send: send && R(send).h ? R(send) : null,
                note: note.textContent ? R(note) : null, taH: ta.getBoundingClientRect().height, scrollH: ta.scrollHeight, clientH: ta.clientHeight, vw: innerWidth }; })()`);
        const checkLayout = async (label, what = '') => {
            // The geometry is read once it has stopped moving (#5343): a font-size or text change reflows over a few frames (the box sizes itself on the input event, the row wraps or not), and a read taken
            // in the middle of it failed 'a card this wide holds the chips and the buttons in one row' on a slow runner (and on every run at a 6x CPU throttle). Two reads a frame apart that agree.
            await ev(`document.fonts.ready.then(() => true)`); // the chips are measured in the fonts the page uses, not a fallback that is still being swapped out
            let g = await geometry();
            for (let i = 0; i < 40; i++) { await new Promise(r => setTimeout(r, 30)); const g2 = await geometry(); if (JSON.stringify(g2) === JSON.stringify(g)) break; g = g2; } // sleep-ok: poll until the layout has settled
            const at = `${label} ${what}: ${JSON.stringify(g)}`; const phone = label !== 'Desktop';
            const shown = [['model chip', g.chip], ['thinking chip', g.thinking.h ? g.thinking : null]].filter(x => x[1]);
            assert.ok(shown.length >= 1, 'the model chip is showing: ' + at);
            for (const [name, c] of shown) {
                assert.ok(c.l >= g.pane.l + 8 && c.r <= g.pane.r - 8 && c.t >= g.pane.t && c.b <= g.pane.b, `the ${name} is inside the composer with room to the border: ` + at);
                assert.ok(c.t >= g.ta.b - 0.5 || c.b <= g.ta.t + 0.5, `the ${name} does not overlap the text box: ` + at);
                if (!phone) assert.ok(c.t >= g.toolbar.t && c.b <= g.toolbar.b, `the ${name} is in the buttons row: ` + at);
            }
            assert.ok(g.scrollH <= g.clientH + 1 || g.taH >= parseFloat(await ev(`getComputedStyle(document.getElementById('command-input')).maxHeight`)) - 1, 'the box shows all its text (it grows, or scrolls at its cap): ' + at);
            if (shown.length === 2) {
                const [m, t] = [g.chip, g.thinking];
                assert.ok(!(m.l < t.r && t.l < m.r && m.t < t.b && t.t < m.b), 'the two chips do not overlap: ' + at);
                // Side by side (same row, model first) unless the card is too narrow and they wrap together.
                if (Math.abs((m.t + m.b) / 2 - (t.t + t.b) / 2) <= 1) assert.ok(m.r <= t.l, 'the thinking chip is right of the model chip: ' + at);
            }
            if (!phone) {
                assert.ok(g.send, 'the desktop toolbar has Send: ' + at);
                // Level with Send and left of it; when the card is too narrow for both (a docked panel at 1024 px) the row wraps and the chips are the row above the buttons.
                const last = shown[shown.length - 1][1], wrapped = shown.every(x => x[1].b <= g.send.t + 0.5);
                if (!wrapped) for (const [name, c] of shown) { assert.ok(Math.abs((c.t + c.b) / 2 - (g.send.t + g.send.b) / 2) <= 1, `the ${name} is vertically centred with Send: ` + at); assert.ok(c.r <= g.send.l, `the ${name} is left of Send: ` + at); }
                // 'This wide' is relative to the text: 440 px holds the chips and the buttons in one row at the normal size, and 200% text needs twice that, so at 200% the row wrapping is the
            // layout working (the chips on their own row above the buttons, which the checks above hold to). Read once the geometry is settled, that is what 200% text always gives (#5343: on a
            // run that read it a frame early the row had not wrapped yet, which is why this passed on some runs and failed on others).
            const wide = (g.pane.r - g.pane.l) >= 440 * (/200% text/.test(what) ? 2 : 1);
                if (wide && !/mode buttons/.test(what)) assert.ok(!wrapped, 'a card this wide holds the chips and the buttons in one row: ' + at);
            }
            if (g.note) assert.ok(g.note.t >= Math.max(...shown.map(x => x[1].b)) - 0.5 && g.note.b <= g.pane.b + 0.5 && (phone || g.note.t >= g.toolbar.b - 6), 'the note is a line under the row, not between the text and the buttons: ' + at);
            return g;
        };

        const run = async (label) => {
            console.log(`${label}: what the agent runs, and what it was asked to`);
            await feed('ch-a-id', { agent_type: 'claude', model: OPUS.value, thinking_actual: 'high', thinking: 'low' });
            await select('ch-a');
            await waitFor(`!document.getElementById('model-chip-bar').hidden`, 'the chip', 5000);
            let c = await chip();
            assert.equal(c.text, 'Claude · Opus 5.5', 'the model chip says the model');
            assert.equal(c.label, 'Model: Claude · Opus 5.5', 'its name holds what it shows');
            assert.equal(c.state, '');
            let th = await chip('thinking');
            assert.equal(th.text, 'High', 'the actual level wins over the requested one: ' + JSON.stringify(th));
            assert.equal(th.label, 'Thinking: High');
            assert.equal(th.state, '');
            await feed('ch-a-id', { agent_type: 'claude', model: OPUS.value, thinking_actual: null, thinking: 'low', model_switch: null });
            await waitFor(`document.getElementById('thinking-chip-text').textContent === 'Low'`, 'the requested level', 4000);
            c = await chip(); th = await chip('thinking');
            assert.equal(th.state, 'requested');
            assert.equal(th.italic, 'italic', 'requested is italic');
            assert.equal(c.state, '', 'the model chip is not marked: the model is the one it runs');
            assert.match(th.note, /started with/);
            for (const [n, why] of [['ch-sleep', 'sleeping'], ['ch-term', 'a terminal']]) {
                await select(n);
                await waitFor(`document.getElementById('model-chip-bar').hidden`, `no chip for ${why}`, 4000);
            }
            console.log('   ✓ actual, requested, hidden for sleeping and terminal');

            console.log(`${label}: the menu`);
            await select('ch-a');
            await waitFor(`!document.getElementById('model-chip-bar').hidden`, 'the chip back', 4000);
            console.log(`${label}: the chip's own row`);
            await shot('model-chip-layout-' + label.split(' ')[0].toLowerCase() + '-after');
            await checkLayout(label);
            // Every state the chip row can be in, with the box typed into and the text bigger: the row stays inside the card and under the text.
            const LONG = 'claude-opus-5-5-a-very-long-custom-model-name-that-keeps-going-and-going-and-going';
            const states = [
                ['long name', { model: LONG, thinking_actual: 'high', model_switch: null }],
                ['pending, after reply', { model: OPUS.value, thinking_actual: 'high', model_switch: { state: 'pending', thinking: 'xhigh', after_reply: true } }],
                ['failed, long message', { model: OPUS.value, thinking_actual: 'high', model_switch: { state: 'failed', message: 'Claude Code did not take the model: ' + 'Unknown model, '.repeat(12) } }],
                ['requested', { model: OPUS.value, thinking_actual: null, thinking: 'low', model_switch: null }],
            ];
            const typeLines = n => ev(`(() => { const i = document.getElementById('command-input'); i.focus(); i.value = ${JSON.stringify(Array.from({ length: n }, (_, k) => 'line ' + (k + 1)).join('\n'))}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
            for (const [what, extra] of states) {
                await feed('ch-a-id', { agent_type: 'claude', thinking: 'low', ...extra });
                await waitFor(`!document.getElementById('model-chip-bar').hidden`, 'the chip in ' + what, 4000);
                for (const lines of [0, 1, 3, 12]) {
                    await typeLines(lines);
                    await new Promise(r => setTimeout(r, 60)); // sleep-ok: the autosize runs on the input event; a frame for the layout to settle
                    await checkLayout(label, `${what}, ${lines} lines`);
                }
            }
            await ev(`document.documentElement.style.fontSize = '200%'; true`);
            await typeLines(3);
            const big = await checkLayout(label, '200% text');
            await shot('model-chip-layout-' + label.split(' ')[0].toLowerCase() + '-200');
            await ev(`document.documentElement.style.fontSize = ''; true`);
            await typeLines(3); // the box sizes itself again at the normal text size
            for (const theme of ['light', 'dark']) {
                await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars && m.clearCustomThemeVars(); m.applyTheme(${JSON.stringify(theme)}); return true; })()`);
                await new Promise(r => setTimeout(r, 400)); // sleep-ok: the colours transition; the screenshot waits for them
                assert.equal(await ev(`document.documentElement.getAttribute('data-theme')`), theme, 'the theme was applied');
                await checkLayout(label, theme + ' theme');
                await shot('model-chip-layout-' + label.split(' ')[0].toLowerCase() + '-' + theme);
            }
            if (label === 'Desktop') { // the terminal view's composer also shows the mode buttons and the macros (chat mode hides them): the chip is still the row's first item
                await ev(`(() => { const st = document.createElement('style'); st.id = 'show-terminal-tools'; st.textContent = '#command-pane.chat-mode .toolbar-group-modes, #command-pane.chat-mode .toolbar-group-macros { display: flex !important; } #command-pane.chat-mode .toolbar-divider { display: block !important; }'; document.head.appendChild(st); return true; })()`);
                const t = await checkLayout(label, 'with the mode buttons and macros showing');
                const modes = await ev(`(() => { const r = document.querySelector('#command-toolbar .toolbar-group-modes').getBoundingClientRect(); return { l: r.left, t: r.top, w: r.width }; })()`);
                assert.ok(modes.w > 0, 'the mode buttons are showing in this case');
                assert.ok(t.chip.r <= modes.l + 0.5 || t.chip.b <= modes.t + 0.5, 'the chip comes first in the row: ' + JSON.stringify({ chip: t.chip, modes }));
                await shot('model-chip-layout-desktop-terminal');
                await ev(`document.getElementById('show-terminal-tools').remove(); true`);
            }
            if (label === 'Desktop') for (const w of [1024, 900, 768]) {
                await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile: false });
                await new Promise(r => setTimeout(r, 150)); // sleep-ok: a resize reflows the card; a few frames before measuring
                await checkLayout(label, `${w} px wide`);
            }
            if (label === 'Desktop') await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
            await ev(`(() => { const i = document.getElementById('command-input'); i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
            await feed('ch-a-id', { agent_type: 'claude', model: OPUS.value, thinking_actual: null, thinking: 'low', model_switch: null }); // back to what the menu stage starts from
            console.log('   ✓ the chip clears the text and stays inside the composer');

            await openMenu();
            let m = await menu();
            assert.equal(m.open, true);
            assert.equal(m.label, 'Model');
            assert.deepEqual(m.groups.map(g => g.name), ['Model'], 'the model menu lists only models');
            assert.deepEqual(m.groups[0].items, ['Opus 5.5*', 'Sonnet 5.5']);
            assert.ok(!/restart/i.test(m.groups[0].head), 'Claude switches live: no "Apply with restart"');
            if (label !== 'Desktop') { assert.ok(m.rect.l >= 0 && m.rect.r <= m.vw && m.rect.b <= m.vh, 'the sheet is inside the screen: ' + JSON.stringify(m.rect)); assert.ok(m.minItem >= 44, 'items are 44 px: ' + m.minItem); }
            await shot('model-chip-menu-' + label.split(' ')[0].toLowerCase());
            await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
            assert.equal((await menu()).open, false, 'Escape closes it');
            assert.equal(await ev(`document.activeElement.id`), 'model-chip', 'Escape gives focus back to its own chip');
            await openMenu('thinking');
            m = await menu('thinking');
            assert.equal(m.label, 'Thinking level');
            assert.deepEqual(m.groups.map(g => g.name), ['Thinking level'], 'the thinking menu lists only levels');
            assert.deepEqual(m.groups[0].items, ['Default', 'Low*', 'Medium', 'High', 'Extra high', 'Max'], 'the levels Opus takes, the requested one marked');
            assert.equal((await menu('model')).open, false, 'only one menu is open at a time');
            if (label !== 'Desktop') { assert.ok(m.rect.l >= 0 && m.rect.r <= m.vw && m.rect.b <= m.vh, 'the sheet is inside the screen: ' + JSON.stringify(m.rect)); assert.ok(m.minItem >= 44, 'items are 44 px: ' + m.minItem); }
            else { const g = await geometry(); assert.ok(m.rect.l >= g.thinking.l - 1 && m.rect.l <= g.thinking.l + 1, 'the desktop menu opens from its own chip: ' + JSON.stringify({ menu: m.rect, chip: g.thinking })); }
            await shot('thinking-chip-menu-' + label.split(' ')[0].toLowerCase());
            await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
            assert.equal(await ev(`document.activeElement.id`), 'thinking-chip', 'Escape gives focus back to its own chip');
            // Opening one menu from the other chip closes the first.
            await openMenu('model'); await openMenu('thinking');
            assert.equal((await menu('model')).open, false, 'the model menu closed when the thinking menu opened');
            await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
            console.log('   ✓ models, levels, one menu at a time, Escape');

            console.log(`${label}: choosing a level switches live, typing stays enabled`);
            await ev(`window.__posts.length = 0; true`);
            await openMenu('thinking');
            await pick('[data-level="xhigh"]');
            await waitFor(`window.__posts.length === 1`, 'the switch request', 4000);
            const body = await ev(`window.__posts[0]`);
            assert.deepEqual(body, { session_id: 'ch-a-id', agent_type: 'claude', thinking: 'xhigh' });
            await waitFor(`/Switching to Extra high/.test(document.getElementById('thinking-chip-text').textContent)`, 'the switching state on the thinking chip', 4000);
            assert.equal(await text('model'), 'Claude · Opus 5.5', 'the model chip keeps saying the model');
            await ev(`(() => { const i = document.getElementById('command-input'); i.focus(); i.value = ''; return true; })()`);
            await ev(`(() => { const i = document.getElementById('command-input'); i.value = 'typed meanwhile'; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
            assert.deepEqual(await ev(`({ disabled: document.getElementById('command-input').disabled, value: document.getElementById('command-input').value })`), { disabled: false, value: 'typed meanwhile' }, 'the composer is never held up');
            await feed('ch-a-id', { agent_type: 'claude', model: OPUS.value, thinking: 'low', model_switch: { state: 'pending', thinking: 'xhigh', after_reply: true } });
            await waitFor(`/Switches after this reply/.test(document.getElementById('model-chip-note').textContent)`, 'the after-reply note', 4000);
            await feed('ch-a-id', { agent_type: 'claude', model: OPUS.value, thinking: 'xhigh', thinking_actual: 'xhigh', model_switch: { state: 'done', thinking: 'xhigh' } });
            await waitFor(`document.getElementById('thinking-chip-text').textContent === 'Extra high'`, 'the new level', 4000);
            assert.equal((await chip('thinking')).state, '');
            console.log('   ✓ request, switching, after reply, done');

            console.log(`${label}: a failure says why, as text`);
            await feed('ch-a-id', { agent_type: 'claude', model: OPUS.value, thinking_actual: 'xhigh', model_switch: { state: 'failed', message: 'Claude Code did not take the model: <img src=x onerror=window.__pwned=1> Unknown model' } });
            await waitFor(`document.getElementById('model-chip').dataset.state === 'failed' && document.getElementById('thinking-chip').dataset.state === 'failed'`, 'the failed state', 4000);
            c = await chip();
            assert.match(c.note, /<img src=x onerror=window.__pwned=1> Unknown model/, 'the CLI\'s words, literally');
            assert.equal(await ev(`document.querySelectorAll('#model-chip-bar img').length + (window.__pwned ? 1 : 0)`), 0, 'nothing in it was parsed as markup');
            await feed('ch-a-id', { agent_type: 'claude', model: OPUS.value, thinking_actual: 'xhigh', model_switch: null });
            console.log('   ✓ failed, literal text');

            console.log(`${label}: another model narrows the levels; a refused request is said`);
            await openMenu('model');
            await pick('[data-model="claude-sonnet-5-5"]');
            await waitFor(`window.__posts.length === 2 && window.__posts[1].model === 'claude-sonnet-5-5'`, 'the model request', 4000);
            await feed('ch-a-id', { agent_type: 'claude', model: SONNET.value, thinking_actual: 'high', model_switch: null });
            await waitFor(`document.getElementById('model-chip-text').textContent === 'Claude · Sonnet 5.5' && document.getElementById('thinking-chip-text').textContent === 'High'`, 'Sonnet', 4000);
            await openMenu('thinking');
            m = await menu('thinking');
            assert.deepEqual(m.groups[0].items, ['Default', 'Low', 'Medium', 'High*'], "Sonnet's levels only");
            await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); window.__switchAnswer = { status: 409, body: { error: 'a switch is already in progress for this agent', code: 'switch_in_progress' } }; true`);
            await openMenu('thinking');
            await pick('[data-level="low"]');
            await waitFor(`!!document.querySelector('.toast') && /already in progress/.test(document.querySelector('.toast').textContent)`, 'the refusal toast', 4000);
            await ev(`window.__switchAnswer = { status: 202, body: { ok: true } }; document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
            await waitFor(`!/Switching/.test(document.getElementById('thinking-chip-text').textContent) && !/Switching/.test(document.getElementById('model-chip-text').textContent)`, 'the chips back to normal after a refusal', 5000);
            // A CLI the server says has no live switch (409 needs_restart): the Restart dialog opens with the choice filled in.
            await ev(`window.__switchAnswer = { status: 409, body: { error: 'cannot switch while it runs', code: 'needs_restart' } }; true`);
            await openMenu('thinking');
            await pick('[data-level="medium"]');
            await waitFor(`!!document.querySelector('#restart-acf .acf-thinking') && document.querySelector('#restart-acf .acf-thinking').value === 'medium'`, 'the Restart dialog with the level filled in', 8000);
            await ev(`window.hideRestartModal && window.hideRestartModal(); window.__switchAnswer = { status: 202, body: { ok: true } }; true`);
            console.log('   ✓ narrowed, refusal, needs_restart');

            console.log(`${label}: a CLI with no live switch applies with a restart`);
            await feed('ch-codex-id', { agent_type: 'codex', model: 'gpt-x', thinking_actual: 'low' });
            await select('ch-codex');
            await waitFor(`!document.getElementById('model-chip-bar').hidden && /gpt.x/i.test(document.getElementById('model-chip-text').textContent)`, 'the codex chip', 5000);
            await ev(`document.getElementById('model-chip').click(); true`);
            await waitFor(`document.querySelectorAll('#model-chip-menu .model-chip-item').length > 0`, 'the codex menu', 5000);
            m = await menu();
            assert.ok(/Apply with restart/.test(m.groups[0].head), 'the menu says so: ' + m.groups[0].head);
            assert.deepEqual(m.groups.map(g => g.name), ['Model'], 'the model menu lists models only');
            await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
            // The row reports a level, so the thinking chip shows; this CLI lists no levels, so its menu says so and offers nothing to apply.
            await openMenuRaw('thinking');
            m = await menu('thinking');
            assert.deepEqual(m.groups, [], 'no levels listed for this CLI');
            assert.match(m.empty, /No thinking levels/);
            await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
            // A CLI row that reports no level at all has no thinking chip.
            await feed('ch-codex-id', { agent_type: 'codex', model: 'gpt-x', thinking_actual: null, thinking: '' });
            await waitFor(`document.getElementById('thinking-chip-wrap').hidden`, 'no thinking chip for a CLI with no level', 4000);
            assert.equal(await ev(`document.getElementById('thinking-chip').getBoundingClientRect().width`), 0);
            await feed('ch-codex-id', { agent_type: 'codex', model: 'gpt-x', thinking_actual: 'low' });
            await ev(`document.getElementById('model-chip').click(); true`);
            await waitFor(`document.querySelectorAll('#model-chip-menu .model-chip-item').length > 0`, 'the codex menu again', 5000);
            await ev(`window.__posts.length = 0; document.querySelector('#model-chip-menu [data-model="gpt-y"]').click(); true`);
            await waitFor(`!!document.querySelector('#restart-acf .acf-model') && document.querySelector('#restart-acf .acf-model').value === 'gpt-y'`, 'the Restart dialog with the model chosen (the agent runs gpt-x)', 8000);
            assert.equal(await ev(`window.__posts.length`), 0, 'no live request for a CLI with none');
            await ev(`window.hideRestartModal && window.hideRestartModal(); true`);
            console.log('   ✓ Apply with restart');
        };

        await run('Desktop');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await select('ch-a');
        await run('Phone 390x844');
        await Emulation.clearDeviceMetricsOverride();
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        console.log('\nPASS model chip');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exitCode = 1; });
