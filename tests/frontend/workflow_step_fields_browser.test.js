// #4423: saving a workflow in the dashboard keeps every step field the editor does not show (timeout_s, agent, continue_on_failure,
// hooks, interactive, output_artifact, and one it does not know), against stubbed APIs:
//   - opened and saved with no change, the steps go back exactly as they were loaded;
//   - a prompt edited in the editor changes only the prompt;
//   - a step switched from shell to agent keeps its other fields and drops the shell command.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const STEPS = [
    { name: 'build', type: 'shell', command: 'make build', timeout_s: 900, continue_on_failure: true, hooks: { on_failure: [{ type: 'notify' }] }, future_field: { kept: 1 } },
    { name: 'review', type: 'agent', prompt: 'Review the diff', agent: { agent_type: 'claude', model: 'opus' }, interactive: true, output_artifact: 'review.md', timeout_s: 1800 },
];
const WF = { id: 41, name: 'Nightly', description: 'd', repo_path: '/tmp/repo', max_duration_s: 3600, steps: STEPS };

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; window.__puts = [];
            const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u && u.url || u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
                if (p === '/api/workflows/41' && m === 'GET') return json(${JSON.stringify(WF)});
                if (p === '/api/workflows/41' && m === 'PUT') { window.__puts.push(JSON.parse(o.body)); return json({ ok: true }); }
                if (p === '/api/workflows' && m === 'GET') return json({ workflows: [] });
                return R(u, o); };
            return true; })()`);
        const MOD = `import((window.__coralStatic || '/static/') + 'workflows.js')`;
        const saveOnce = async edit => {
            await ev(`${MOD}.then(m => m.editWorkflow(41)).then(() => true)`);
            await waitFor(`document.querySelectorAll('#wf-steps-editor .wf-step-editor-item').length === 2`, 'the editor with both steps', 8000);
            if (edit) await ev(edit);
            await ev(`window.__puts.length = 0; ${MOD}.then(m => m.saveWorkflow()).then(() => true)`);
            await waitFor(`window.__puts.length === 1`, 'the save', 8000);
            return ev(`window.__puts[0]`);
        };
        const item = i => `document.querySelectorAll('#wf-steps-editor .wf-step-editor-item')[${i}]`;

        console.log('1. Saved with no change: every step field comes back as it was loaded');
        let body = await saveOnce();
        assert.deepEqual(body.steps, STEPS);

        console.log('2. An edited prompt changes only the prompt');
        body = await saveOnce(`${item(1)}.querySelector('.wf-step-content').value = 'Review it again'; true`);
        assert.deepEqual(body.steps[1], { ...STEPS[1], prompt: 'Review it again' });
        assert.deepEqual(body.steps[0], STEPS[0]);

        console.log('3. A step switched from shell to agent keeps its other fields and drops the command');
        body = await saveOnce(`(() => { const it = ${item(0)}; it.querySelector('.wf-step-type-select').value = 'agent'; it.querySelector('.wf-step-content').value = 'Build it'; return true; })()`);
        const { command, ...rest } = STEPS[0];
        assert.equal(command, 'make build');
        assert.deepEqual(body.steps[0], { ...rest, type: 'agent', prompt: 'Build it' });
        console.log('PASS workflow editor keeps step fields');
    } finally {
        try { await client.close(); } catch (_) {}
    }
})().catch(error => { console.error(error); process.exit(1); });
