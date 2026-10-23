// The Workflow page (#6515 finding 3, #6551): its name once, the last failure in words, steps numbered from 1 with their command, runs as When / Result / Took / Started by. 1280 and 390 px, stubbed API with #6530's fields.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const now = Date.now();
    const iso = (ms) => new Date(now - ms).toISOString();
    const WF = { id: 1, name: 'nightly-build', description: 'Build and test', enabled: true, step_count: 2, max_duration_s: 3600, repo_path: '/work/app', created_at: iso(86400000 * 3),
        steps: [{ name: 'Install', type: 'shell', command: 'npm ci' }, { name: 'Test', type: 'shell', command: 'npm test -- --ci' }], last_run: { status: 'failed', started_at: iso(3600000) } };
    const RUNS = [
        { id: 2, workflow_id: 1, status: 'failed', trigger_type: 'schedule', started_at: iso(3700000), finished_at: iso(3600000), duration_seconds: 100, failed_step: { number: 2, name: 'Test' }, failure_reason: 'exit code 3' },
        { id: 1, workflow_id: 1, status: 'completed', trigger_type: 'manual', started_at: iso(90000000), finished_at: iso(89990000), duration_seconds: 10 }];
    const STUB = `(() => { const real = window.fetch; const J = (b) => Promise.resolve(new Response(JSON.stringify(b), { status: 200, headers: { 'Content-Type': 'application/json' } }));
        window.fetch = (u, o) => { const s = String(u); const get = !(o && o.method && o.method !== 'GET');
            if (get && /\\/api\\/workflows\\/1\\/runs/.test(s)) return J({ runs: ${JSON.stringify(RUNS)} });
            if (get && /\\/api\\/workflows\\/1$/.test(s)) return J(${JSON.stringify(WF)});
            if (get && /\\/api\\/workflows$/.test(s)) return J({ workflows: [${JSON.stringify(WF)}] });
            return real(u, o); }; })();`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            await ev(`window.switchNavTab('workflows'); true`);
            await waitFor(`document.querySelectorAll('#wf-list .wf-card').length === 1`, 'the workflow card', 8000).catch(async e => { throw new Error(e.message + ' at ' + w + ': ' + await ev(`JSON.stringify([document.getElementById('workflows-view')?.style.display, document.getElementById('workflows-view')?.innerText.slice(0, 200), location.hash])`)); });
            await ev(`document.querySelector('#wf-list .wf-card').click(); true`);
            await waitFor(`document.querySelectorAll('#wf-detail .wf-runs-table tbody tr').length === 2`, 'the runs', 8000);
            const at = `at ${w}px: `;
            const page = await ev(`(() => { const t = document.getElementById('workflows-view').innerText; return { text: t, count: t.split('nightly-build').length - 1, badge: getComputedStyle(document.getElementById('wf-count-badge')).display,
                steps: [...document.querySelectorAll('.wf-step-item')].map(e => [e.querySelector('.wf-step-index').textContent, e.querySelector('.wf-step-what')?.textContent]),
                head: [...document.querySelectorAll('.wf-runs-table th')].map(e => e.textContent), rows: [...document.querySelectorAll('.wf-runs-table tbody tr')].map(r => r.innerText.replace(/\\s+/g, ' ').trim()),
                last: document.querySelector('.wf-last-failed')?.innerText.replace(/\\s+/g, ' ').trim(), wide: document.documentElement.scrollWidth > innerWidth }; })()`);
            assert.equal(page.count, 1, at + 'the name once: ' + page.text);
            assert.equal(page.badge, 'none', at + 'no count badge on a workflow page');
            assert.deepEqual(page.steps, [['1', 'npm ci'], ['2', 'npm test -- --ci']], at + 'steps from 1 with their commands');
            assert.deepEqual(page.head, ['When', 'Result', 'Took', 'Started by'], at);
            assert.match(page.rows[0], /Failed: step 2 \(Test\).*exit code 3.*1m 40s/, at + page.rows[0]);
            assert.match(page.rows[1], /Passed.*10s?.*manual/, at + page.rows[1]);
            assert.match(page.last, /The last run failed.*step 2 \(Test\).*exit code 3\. Open the run/, at + page.last);
            assert.equal(page.wide, false, at + 'no sideways scroll');
            if (process.env.SHOTS) { const s = await Page.captureScreenshot({ format: 'png' }); require('fs').writeFileSync(`${process.env.SHOTS}/workflow-page-${w}.png`, Buffer.from(s.data, 'base64')); }
        }
        console.log('PASS workflow page: name once, steps from 1 with commands, runs in words');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
