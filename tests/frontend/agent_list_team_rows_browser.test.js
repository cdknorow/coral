// #5237 (operator): "Show branches" and "Show agent type" (now Show provider, Show model and Show thinking level, #5742: this suite starts with the three off and ticks Show model) were ticked and the agents of a team group showed neither. Team rows are the compact rows, and the agent type
// line was left out of every compact row; the branch line was drawn only for an agent on its own worktree, so the agents of a shared checkout never had one. With either
// option on, every row that has the data shows its line, in a team group too, toggled live; a row with no branch or model on record shows no line, and the Display panel says
// how many agents have none. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BRANCHES = 'coral-show-branches', TYPES = ['coral-show-provider', 'coral-show-model', 'coral-show-thinking'];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    // CORAL_SCREENSHOT_DIR: a picture of each view with both options ticked (for review).
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        // A team of four on one board: an agent on its own worktree, one on the shared checkout (a branch, no worktree), one with a model but no git folder, one with nothing on record.
        const scene = () => ev(`(async () => {
            T.state.liveSessions = []; window.__m.agents = {};
            for (const n of ['wt-agent', 'shared-agent', 'nogit-agent', 'bare-agent']) window.__m.add(n, n.slice(0, 1).toUpperCase(), 3, { sleeping: false, board_project: 'team-x' });
            const by = n => T.state.liveSessions.find(s => s.name === n);
            Object.assign(by('wt-agent'), { branch: 'fix/login', git_state: { is_worktree: true, base_branch: 'main' }, model: 'claude-opus-4-1' });
            Object.assign(by('shared-agent'), { branch: 'main', model: 'claude-sonnet-4-5' });
            Object.assign(by('nogit-agent'), { model: 'claude-opus-4-1' });
            T.render.renderLiveSessions(T.state.liveSessions); return T.state.liveSessions.length; })()`);
        const load = async view => {
            await Emulation.setDeviceMetricsOverride({ width: view.width, height: view.height, deviceScaleFactor: 1, mobile: view.mobile });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        };
        const list = view => view.mobile ? '#mobile-agent-list' : '.sidebar';
        const rows = view => ev(`(() => {
            const root = document.querySelector(${JSON.stringify(list(view))});
            const vis = e => !!e && !e.hidden && e.getClientRects().length > 0;
            const out = {};
            for (const li of root.querySelectorAll('.board-card-agents li.session-group-item')) {
                const name = (li.dataset.name || li.querySelector('.session-label')?.textContent || '').trim();
                const m = li.querySelector('.session-model'), b = li.querySelector('.session-worktree-line');
                out[name] = { model: m && vis(m) ? m.textContent.trim() : '', branch: b && vis(b) ? b.textContent.trim() : '', wtIcon: !!(b && b.querySelector('svg')) };
            }
            const hint = root.querySelector('[data-display-hint]');
            return { rows: out, hint: hint && !hint.hidden ? hint.textContent.trim() : '', overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth };
        })()`);
        const openPanel = view => ev(`(() => { const b = document.querySelector(${JSON.stringify(list(view) + ' [data-display-button]')}); if (b.getAttribute('aria-expanded') !== 'true') b.click(); return true; })()`);
        const tick = (view, what) => ev(`(() => { document.querySelector(${JSON.stringify(list(view) + ' [data-display-input="' + what + '"]')}).click(); return true; })()`);

        for (const view of [
            { name: 'sidebar', width: 1280, height: 900, mobile: false },
            { name: 'phone 390', width: 390, height: 844, mobile: true },
        ]) {
            console.log(`${view.name}: a team group, both options, toggled live`);
            await load(view);
            await ev(`localStorage.removeItem(${JSON.stringify(BRANCHES)}); for (const k of ${JSON.stringify(TYPES)}) localStorage.setItem(k, 'false'); true`); // the agent line's options are on unless turned off: start with them off
            assert.equal(await scene(), 4);
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' .board-card-agents li.session-group-item')}).length === 4`, `${view.name}: the four team rows`, 4000);
            let s = await rows(view);
            assert.deepEqual(Object.values(s.rows).map(r => r.model + r.branch), ['', '', '', ''], `${view.name}: both options off: no second lines`);

            await openPanel(view);
            await tick(view, 'model');
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' .board-card-agents .session-model')}).length === 3`, `${view.name}: Show model brings the line to the team rows that have a model`, 4000);
            s = await rows(view);
            assert.match(s.rows['wt-agent'].model, /Opus/, JSON.stringify(s.rows));
            assert.match(s.rows['shared-agent'].model, /Sonnet/);
            assert.match(s.rows['nogit-agent'].model, /Opus/);
            assert.equal(s.rows['bare-agent'].model, '', 'an agent with no model on record shows no line');

            await tick(view, 'branches');
            await new Promise(r => setTimeout(r, 300)); // sleep-ok: a settled frame for the picture
            await shot(`team_rows_${view.mobile ? 'phone' : 'sidebar'}`);
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' .board-card-agents .session-worktree-line')}).length === 2`, `${view.name}: Show branches brings the line to every team row with a branch`, 4000);
            s = await rows(view);
            assert.match(s.rows['wt-agent'].branch, /fix\/login/);
            assert.equal(s.rows['wt-agent'].wtIcon, true, 'the worktree keeps its icon');
            assert.match(s.rows['shared-agent'].branch, /main/, 'an agent on the shared checkout shows its branch too');
            assert.equal(s.rows['nogit-agent'].branch, '');
            assert.equal(s.rows['bare-agent'].branch, '');
            assert.match(s.hint, /1 agent has no model/i, `the panel says how many agents have no model: ${s.hint}`);
            assert.match(s.hint, /2 agents have no branch/i, `and how many have no branch: ${s.hint}`);
            assert.equal(s.overflow, 0, 'nothing scrolls sideways');

            await tick(view, 'model');
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' .board-card-agents .session-model')}).length === 0`, `${view.name}: untick: the model lines go at once`, 4000);
            await tick(view, 'branches');
            await waitFor(`document.querySelectorAll(${JSON.stringify(list(view) + ' .board-card-agents .session-worktree-line')}).length === 0`, `${view.name}: untick: the branch lines go at once`, 4000);
            s = await rows(view);
            assert.equal(s.hint, '', 'no hint with both options off');
        }
        console.log('agent_list_team_rows: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        await client.close().catch(() => {});
        process.exit(1);
    }
})();
