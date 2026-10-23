// The agent list's "Show branches" Display option (#5117; operator: "on some computers the branch is showing up in the agent tab with no way for the user to hide it. It is
// quite noisy."). Off as it starts: an agent on its own worktree has no branch line under its name, and nothing at the summary line. Ticking it in the Display panel brings
// the line back (today's markup), tells it in the summary line while the panel is shut, and is kept in this browser: it is still on after a reload. Clear on the summary
// line turns it off again. The branch stays in the row's tooltip and the agent's header whichever way it is set. The sidebar's panel and the phone's, in both themes.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const KEY = 'coral-show-branches';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const scene = () => ev(`(async () => {
            T.state.liveSessions = []; window.__m.agents = {};
            window.__m.add('wt-one', 'A', 3, { sleeping: false }); window.__m.add('wt-two', 'B', 3, { sleeping: false });
            T.state.liveSessions.forEach(s => { s.branch = 'fix/login'; s.git_state = { is_worktree: true, base_branch: 'main' }; });
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
            const lines = [...root.querySelectorAll('.session-worktree-line')].filter(vis);
            const box = root.querySelector('[data-display-input="branches"]');
            const sum = root.querySelector('[data-agent-list-summary]');
            const btn = root.querySelector('[data-display-button]');
            const bar = root.querySelector('.list-display-option [data-display-input="branches"]');
            const label = bar && bar.closest('label').getBoundingClientRect();
            return { lines: lines.length, lineText: lines[0] ? lines[0].textContent.trim() : '', checked: box ? box.checked : null, boxShown: !!box && vis(box.closest('label')),
                summary: sum && !sum.hidden ? sum.textContent.trim() : '', buttonName: btn ? btn.getAttribute('aria-label') : '', open: !!btn && btn.getAttribute('aria-expanded') === 'true',
                stored: localStorage.getItem(${JSON.stringify(KEY)}), labelFits: label ? label.left >= 0 && label.right <= innerWidth : null, overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth };
        })()`);
        const openPanel = view => ev(`(() => { const b = document.querySelector(${JSON.stringify(list(view) + ' [data-display-button]')}); if (b.getAttribute('aria-expanded') !== 'true') b.click(); return true; })()`);
        const shutPanel = view => ev(`(() => { const b = document.querySelector(${JSON.stringify(list(view) + ' [data-display-button]')}); if (b.getAttribute('aria-expanded') === 'true') b.click(); return true; })()`);
        const tick = view => ev(`(() => { document.querySelector(${JSON.stringify(list(view) + ' [data-display-input="branches"]')}).click(); return true; })()`);

        for (const view of [
            { name: 'sidebar, dark', width: 1280, height: 900, mobile: false, theme: 'dark' },
            { name: 'sidebar, light', width: 1280, height: 900, mobile: false, theme: 'light' },
            { name: 'phone 390, dark', width: 390, height: 844, mobile: true, theme: 'dark' },
            { name: 'phone 390, light', width: 390, height: 844, mobile: true, theme: 'light' },
        ]) {
            console.log(`${view.name}: off as it starts, ticked on, kept over a reload, unticked turns it off`);
            await load(view);
            await ev(`localStorage.removeItem(${JSON.stringify(KEY)}); true`);
            assert.equal(await scene(), 2);
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' li.session-group-item')}).length >= 2`, `${view.name}: the two agents`, 4000);
            let s = await state(view);
            assert.equal(s.lines, 0, `${view.name}: no worktree line under an agent while Show branches is off`);
            assert.equal(s.summary.includes('Branches shown'), false, 'and nothing in the summary line');
            await openPanel(view);
            s = await state(view);
            assert.equal(s.boxShown, true, 'Show branches is in the Display panel');
            assert.equal(s.checked, false, 'and unticked');
            assert.ok(s.labelFits && s.overflow === 0, `${view.name}: the option fits the screen (no horizontal overflow)`);
            // the branch is still in the row's tooltip
            const tip = await ev(`(async () => { const m = await import(${S} + 'session_state.js'); return m.buildSessionTooltip(T.state.liveSessions[0]); })()`);
            assert.ok(tip.includes('fix/login'), 'the branch stays in the row tooltip');

            await tick(view);
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' .session-worktree-line')}).length === 2`, `${view.name}: the branch lines come back`, 4000);
            s = await state(view);
            assert.equal(s.stored, 'true'); assert.equal(s.checked, true);
            assert.ok(s.lineText.includes('fix/login'), `the line names the branch (${s.lineText})`);
            await shutPanel(view);
            s = await state(view);
            assert.equal(s.summary, '', `a display preference is not listed over the agent list (${s.summary}) (#6232)`);
            assert.equal(s.buttonName, 'Display options', 'and the Display button does not count it');

            // kept in this browser: a reload keeps it
            await Page.reload(); await Page.loadEventFired(); await install();
            await installAudit(ev); await setTheme(ev, view.theme);
            await scene();
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' .session-worktree-line')}).length === 2`, `${view.name}: still on after a reload`, 4000);
            await openPanel(view);
            assert.equal((await state(view)).checked, true, 'its box reads ticked after a reload');
            // both themes: the option's label and the branch lines it brings back are readable (4.5:1) with the panel open
            const audit = await auditRegion(ev, [list(view) + ' [data-display-panel]', list(view) + ' .session-worktree-line']);
            assert.deepEqual(audit.fails, [], `${view.name}: contrast of the Show branches option and the branch line: ` + JSON.stringify(audit.fails));
            assert.ok(audit.checked >= 3, `the audit looked at the option and the lines (${audit.checked})`);

            // unticking turns it off again, and Clear does the same from the summary line
            await tick(view);
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' .session-worktree-line')}).length === 0`, `${view.name}: unticked, the lines go`, 4000);
            assert.equal((await state(view)).stored, 'false');
            await tick(view);
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' .session-worktree-line')}).length === 2`, `${view.name}: ticked again`, 4000);
            await tick(view); // unticked in the panel: there is no Clear for a display preference (#6232)
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' .session-worktree-line')}).length === 0`, `${view.name}: unticking turns it off`, 4000);
            s = await state(view);
            assert.equal(s.summary.includes('Branches shown'), false); assert.equal(s.stored, 'false');
        }
        console.log('agent_list_branches: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        await client.close().catch(() => {});
        process.exit(1);
    }
})();
