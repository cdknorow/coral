// The agent list's line under an agent's name and its three Display options (#5133, #5742; user feedback: show the effort level): "Claude · Opus 4.1 · High", built from Show provider,
// Show model and Show thinking level, each on unless turned off, each kept in this browser. The thinking level is the one the chat bar's chip shows (the level the newest turn ran at,
// else the one the agent was started with); an agent with none shows none, and a row with no model and no level has no line unless the provider is all that is asked for. The hover card has its own Thinking row. Turning an option off is told in the summary line while the panel is
// shut, and Clear turns it back on. The old single "Show agent type" choice carries over. The sidebar's panel and the phone's, in both themes; the line stays one line at 390 px.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const KEYS = { provider: 'coral-show-provider', model: 'coral-show-model', thinking: 'coral-show-thinking' };
const LEGACY = 'coral-show-agent-type';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    // CORAL_SCREENSHOT_DIR: a picture of each view with the three options on and the Display panel open (for review).
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        // three agents: one ran at High (the transcript's level), one was started at Medium and has not confirmed, one has no level on record
        const scene = () => ev(`(async () => {
            T.state.liveSessions = []; window.__m.agents = {};
            window.__m.add('wt-one', 'A', 3, { sleeping: false }); window.__m.add('wt-two', 'B', 3, { sleeping: false }); window.__m.add('wt-three', 'C', 3, { sleeping: false });
            T.state.liveSessions.forEach(s => { s.model = 'claude-opus-4-1'; });
            T.state.liveSessions[0].thinking_actual = 'high'; T.state.liveSessions[0].thinking = 'low';
            T.state.liveSessions[1].thinking = 'medium';
            T.render.renderLiveSessions(T.state.liveSessions); return T.state.liveSessions.length; })()`);
        const load = async view => {
            await Emulation.setDeviceMetricsOverride({ width: view.width, height: view.height, deviceScaleFactor: 1, mobile: view.mobile });
            await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: view.theme }] });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await installAudit(ev); await setTheme(ev, view.theme);
        };
        const list = view => view.mobile ? '#mobile-agent-list' : '.sidebar';
        const state = view => ev(`(() => {
            const root = document.querySelector(${JSON.stringify(list(view))});
            const vis = e => !!e && !e.hidden && e.getClientRects().length > 0;
            const lines = [...root.querySelectorAll('.session-model')].filter(vis);
            const box = k => root.querySelector('[data-display-input="' + k + '"]');
            const sum = root.querySelector('[data-agent-list-summary]');
            const btn = root.querySelector('[data-display-button]');
            const rects = lines.map(l => l.getBoundingClientRect());
            return { texts: lines.map(l => l.textContent.trim()), checked: ['provider', 'model', 'thinking'].map(k => box(k) ? box(k).checked : null), shown: ['provider', 'model', 'thinking'].map(k => !!box(k) && vis(box(k).closest('label'))),
                legacyBox: !!root.querySelector('[data-display-input="agenttype"]'), summary: sum && !sum.hidden ? sum.textContent.trim() : '', buttonName: btn ? btn.getAttribute('aria-label') : '', overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
                oneLine: rects.every(r => r.height < 20), inside: rects.every(r => r.right <= innerWidth + 1) };
        })()`);
        const stored = () => ev(`({ provider: localStorage.getItem(${JSON.stringify(KEYS.provider)}), model: localStorage.getItem(${JSON.stringify(KEYS.model)}), thinking: localStorage.getItem(${JSON.stringify(KEYS.thinking)}) })`);
        const openPanel = view => ev(`(() => { const b = document.querySelector(${JSON.stringify(list(view) + ' [data-display-button]')}); if (b.getAttribute('aria-expanded') !== 'true') b.click(); return true; })()`);
        const shutPanel = view => ev(`(() => { const b = document.querySelector(${JSON.stringify(list(view) + ' [data-display-button]')}); if (b.getAttribute('aria-expanded') === 'true') b.click(); return true; })()`);
        const tick = (view, k) => ev(`(() => { document.querySelector(${JSON.stringify(list(view))} + ' [data-display-input="${k}"]').click(); return true; })()`);
        const clear = () => ev(`(() => { for (const k of ${JSON.stringify(Object.values(KEYS).concat([LEGACY]))}) localStorage.removeItem(k); return true; })()`);
        const want = (view, texts, why) => waitFor(`JSON.stringify([...document.querySelectorAll(${JSON.stringify(list(view) + ' .session-model')})].map(l => l.textContent.trim())) === ${JSON.stringify(JSON.stringify(texts))}`, `${view.name}: ${why}`, 4000);

        for (const view of [
            { name: 'sidebar, dark', width: 1280, height: 900, mobile: false, theme: 'dark' },
            { name: 'sidebar, light', width: 1280, height: 900, mobile: false, theme: 'light' },
            { name: 'phone 390, dark', width: 390, height: 844, mobile: true, theme: 'dark' },
            { name: 'phone 390, light', width: 390, height: 844, mobile: true, theme: 'light' },
        ]) {
            console.log(`${view.name}: on as it starts, each option turns its part off, kept over a reload, Clear turns them back on`);
            await load(view);
            await clear();
            assert.equal(await scene(), 3);
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' li.session-group-item')}).length >= 3`, `${view.name}: the three agents`, 4000);
            await want(view, ['Claude · Opus 4.1 · High', 'Claude · Opus 4.1 · Medium', 'Claude · Opus 4.1'], 'provider, model and the level on record (none for the third)');
            let s = await state(view);
            assert.ok(s.oneLine && s.inside && s.overflow === 0, `${view.name}: the line is one line and inside the screen: ${JSON.stringify(s)}`);
            assert.equal(s.summary, '', 'nothing in the summary line while all three are on');
            await openPanel(view);
            s = await state(view);
            assert.deepEqual(s.shown, [true, true, true], 'Show provider, Show model and Show thinking level are in the Display panel');
            assert.deepEqual(s.checked, [true, true, true], 'and ticked');
            assert.equal(s.legacyBox, false, 'the single "Show agent type" box is gone');
            await shot(`agent-line-${view.name.replace(/[ ,]+/g, '-')}`);

            await tick(view, 'thinking');
            await want(view, ['Claude · Opus 4.1', 'Claude · Opus 4.1', 'Claude · Opus 4.1'], 'without the level');
            await tick(view, 'model');
            await want(view, ['Claude', 'Claude', 'Claude'], 'provider only');
            await tick(view, 'thinking');
            await want(view, ['Claude · High', 'Claude · Medium'], 'provider and level (an agent with neither a model nor a level has no line: the provider alone is not a line)');
            await tick(view, 'provider');
            await want(view, ['High', 'Medium'], 'the level alone (an agent with no level has no line)');
            assert.deepEqual(await stored(), { provider: 'false', model: 'false', thinking: 'true' });
            await tick(view, 'thinking');
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' .session-model')}).length === 0`, `${view.name}: all three off: no line`, 4000);
            await shutPanel(view);
            s = await state(view);
            assert.equal(s.summary, '', `a display preference is not listed over the agent list: no summary line (${s.summary}) (#6232)`);
            assert.equal(s.buttonName, 'Display options', 'and the Display button does not count them');

            // kept in this browser: a reload keeps them off; they are turned back on in the panel (there is no Clear for them)
            await Page.reload(); await Page.loadEventFired(); await install();
            await installAudit(ev); await setTheme(ev, view.theme);
            await scene();
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' li.session-group-item')}).length >= 3`, 'the agents after a reload', 4000);
            assert.equal((await state(view)).texts.length, 0, 'still no line after a reload');
            await openPanel(view);
            for (const k of ['provider', 'model', 'thinking']) await tick(view, k);
            await want(view, ['Claude · Opus 4.1 · High', 'Claude · Opus 4.1 · Medium', 'Claude · Opus 4.1'], 'ticking the three in the panel turns them back on');
            assert.equal((await state(view)).summary, '');
            const audit = await auditRegion(ev, [list(view) + ' [data-display-panel]', list(view) + ' .session-model']);
            assert.deepEqual(audit.fails, [], `${view.name}: contrast of the three options and the line: ` + JSON.stringify(audit.fails));
            assert.ok(audit.checked >= 4, `the audit looked at the options and the lines (${audit.checked})`);
        }

        console.log('the old single switch carries over; the hover card has a Thinking row');
        const view = { name: 'sidebar, dark', width: 1280, height: 900, mobile: false, theme: 'dark' };
        await load(view);
        await ev(`localStorage.setItem(${JSON.stringify(LEGACY)}, 'false'); for (const k of ${JSON.stringify(Object.values(KEYS))}) localStorage.removeItem(k); true`);
        await scene();
        await waitFor(`document.querySelectorAll('.sidebar li.session-group-item').length >= 3`, 'the agents', 4000);
        assert.equal((await state(view)).texts.length, 0, 'a viewer who had turned Show agent type off has all three off');
        await ev(`localStorage.setItem(${JSON.stringify(LEGACY)}, 'true'); true`);
        await scene();
        await want(view, ['Claude · Opus 4.1 · High', 'Claude · Opus 4.1 · Medium', 'Claude · Opus 4.1'], 'one who had turned it on has all three on');
        const tip = await ev(`(async () => { const m = await import(${S} + 'session_state.js'); const t = T.state.liveSessions; const row = x => (/Thinking<\\/td><td class="tt-value">([^<]*)</.exec(m.buildSessionTooltip(x)) || [])[1] || null; return [row(t[0]), row(t[1]), row(t[2])]; })()`);
        assert.deepEqual(tip, ['High', 'Medium (as started)', null], 'the hover card: the level the newest turn ran at, the one it was started with (as started), and no row for none');
        console.log('agent_list_agent_line: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        await client.close().catch(() => {});
        process.exit(1);
    }
})();
