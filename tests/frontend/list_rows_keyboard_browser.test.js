// The rows that open something are reachable and openable by keyboard (#6976, UI Accessibility's #6962): a Workflows card, a Job row and a Chat row were plain div and li elements with a click action, so
// Tab never landed on one and a screen reader could not open them. Each is a button for the keyboard (role=button, one Tab stop, Enter or Space opens it, a visible ring); a run row in a table keeps its table
// semantics and holds a real button in its first cell. Drives Chrome over CDP through agent_view_harness.js; the scheduler's and the workflows' answers are stubbed.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the detail draws after the key
    const key = async (k, vk, text) => { const b = { key: k, code: k === ' ' ? 'Space' : k, windowsVirtualKeyCode: vk }; await Input.dispatchKeyEvent({ type: 'keyDown', ...b, ...(text ? { text } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...b }); await sleep(300); }; // sleep-ok: the handler runs
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Emulation.setFocusEmulationEnabled({ enabled: true });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const real = window.fetch; const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
            const base = { description: '', timezone: 'UTC', agent_type: 'claude', repo_path: '/work/repo', base_branch: 'main', prompt: 'Tidy.', enabled: 1, max_duration_s: 3600, cleanup_worktree: 0, flags: '', job_type: 'prompt', cron_expr: '0 2 * * *', next_fire_at: new Date(Date.now() + 3600000).toISOString(), consecutive_failures: 0, last_run: null };
            window.__jobs = [{ ...base, id: 1, name: 'nightly', job_type: 'workflow', workflow_id: 1, workflow_runs: [{ id: 11, status: 'completed', exit_reason: 'agent_done', started_at: new Date(Date.now() - 7200000).toISOString(), finished_at: new Date(Date.now() - 3600000).toISOString() }] }, { ...base, id: 2, name: 'weekly' }];
            window.__runs = { 1: [{ id: 11, status: 'completed', exit_reason: 'agent_done', started_at: new Date(Date.now() - 7200000).toISOString(), finished_at: new Date(Date.now() - 3600000).toISOString() }] };
            window.fetch = (u, o) => { const url = String(u), m = (o && o.method) || 'GET';
                if (m === 'GET' && /\\/api\\/scheduled\\/jobs$/.test(url)) return json({ jobs: window.__jobs });
                const r = url.match(/\\/api\\/scheduled\\/jobs\\/(\\d+)\\/runs/); if (r) return json({ runs: window.__runs[r[1]] || [] });
                if (m === 'GET' && /\\/api\\/workflows$/.test(url)) return json({ workflows: [{ id: 1, name: 'build', description: 'Build it', enabled: 1, steps: [{ name: 'a' }], last_run: null }, { id: 2, name: 'release', description: '', enabled: 1, steps: [], last_run: null }] });
                return real(u, o); };
            return true; })()`);
        // Jobs: a row is a button, one Tab stop, Enter opens it
        await ev(`import(${S} + 'scheduler.js').then(m => { window.__sched = m; return true; })`);
        await ev(`window.switchNavTab('jobs'); window.__sched.refreshJobs().then(() => true)`);
        await waitFor(`document.querySelectorAll('#scheduled-jobs-list .sched-row').length === 2`, 'two job rows', 8000);
        const row = await ev(`(() => { const r = document.querySelector('#scheduled-jobs-list .sched-row'); return { role: r.getAttribute('role'), tab: r.tabIndex, keyClick: r.hasAttribute('data-key-click') }; })()`);
        assert.deepEqual(row, { role: 'button', tab: 0, keyClick: true }, 'a job row is a button with one Tab stop: ' + JSON.stringify(row));
        await ev(`document.querySelector('#scheduled-jobs-list .sched-row').focus(); true`);
        await key('Enter', 13, '\r');
        await waitFor(`!!document.querySelector('#scheduler-detail .sched-runs-section') && /nightly/.test(document.getElementById('scheduler-detail').textContent)`, 'the job opened by Enter', 6000);
        assert.equal(await ev(`document.querySelector('#scheduled-jobs-list .sched-row[aria-current="true"] .sched-row-name').textContent`), 'nightly', 'the open job is the current one');
        // its Runs table: the first cell holds a real button; Enter on it opens the run (the table keeps its rows)
        await waitFor(`!!document.querySelector('.sched-runs-table tbody tr button.row-open-btn')`, 'the run row button', 6000);
        const btn = await ev(`(() => { const b = document.querySelector('.sched-runs-table tbody tr button.row-open-btn'); return b ? { tag: b.tagName, name: b.textContent.trim() } : null; })()`);
        assert.ok(btn && btn.tag === 'BUTTON' && btn.name, 'a run row has a real button in its first cell: ' + JSON.stringify(btn));
        assert.equal(await ev(`document.querySelector('.sched-runs-table tbody tr').getAttribute('role')`), null, 'the row keeps its table semantics (no button role on the tr)');
        // Workflows: a card is a button too
        await ev(`window.switchNavTab('workflows'); true`);
        await waitFor(`document.querySelectorAll('.wf-card').length === 2`, 'two workflow cards', 8000);
        const card = await ev(`(() => { const c = document.querySelector('.wf-card'); return { role: c.getAttribute('role'), tab: c.tabIndex }; })()`);
        assert.deepEqual(card, { role: 'button', tab: 0 }, 'a workflow card is a button with one Tab stop');
        await ev(`document.querySelector('.wf-card').focus(); true`);
        await key(' ', 32, ' ');
        await waitFor(`!!document.querySelector('.wf-detail, #workflow-detail, .wf-runs-table, .wf-steps') || /build/.test((document.getElementById('workflow-detail') || document.body).textContent)`, 'the workflow opened by Space', 6000);
        console.log('PASS list rows keyboard');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
