// The Restart and New Agent dialogs while the agent CLI is slow or does not answer (#5105, QA's #5098 observation): the CLI check ("Checking claude CLI…") and the model list are
// requests that may never come back (a stuck CLI is exactly the agent you want to restart), and neither may hold the dialog's buttons. Restart goes out with the agent's own model
// and leaves its level alone; when the list arrives it fills in the Thinking select and the model's name. And the form says so when the agent last ran at a level other than the stored
// one (a change made inside the CLI): the form keeps the stored level (by design, a restart is not asked to change it), with a note under Thinking.
// Stubbed /api/agents/models and /api/system/cli-check held until the suite releases them; run against an isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: shows that a held request stays held (no completion event for "nothing happened")
    const LEVELS = ['low', 'medium', 'high', 'xhigh', 'max'];
    const OPUS = { value: 'opus-x', display_name: 'Opus X', efforts: LEVELS };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { for (const k of Object.keys(localStorage)) if (k.startsWith('coral-models:v1:')) localStorage.removeItem(k); return true; })()`);
        await ev(`(() => { const R = window.fetch; window.__restarts = []; window.__held = [];
            const json = (o, status = 200) => new Response(JSON.stringify(o), { status, headers: { 'content-type': 'application/json' } });
            window.__release = () => { for (const h of window.__held.splice(0)) h(); };
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = (o.method || 'GET').toUpperCase();
                if (url.pathname === '/api/agents/models') return new Promise(r => window.__held.push(() => r(json({ agent_type: 'claude', fetched_at: '2026-10-19T00:00:00Z', refreshing: false, source: 'account', models: [${JSON.stringify(OPUS)}], thinking: ${JSON.stringify(LEVELS)} }))));
                if (url.pathname === '/api/system/cli-check') return new Promise(r => window.__held.push(() => r(json({ found: true, agent_type: 'claude', status: 'available' }))));
                if (/\\/api\\/sessions\\/live\\/[^/]+\\/restart$/.test(url.pathname) && m === 'POST') { window.__restarts.push(JSON.parse(o.body)); return Promise.resolve(json({ ok: true, session_id: 'th-a-new', session_name: 'th-a', agent_name: 'th-a' })); }
                if (/\\/api\\/sessions\\/live\\/[^/?]+\\?session_id=/.test(url.pathname + url.search)) return Promise.resolve(json({ ok: true }));
                return R(u, o); }; return true; })()`);
        await ev(`window.__m.add('th-a', 'A', 3, { sleeping: false }); T.render.renderLiveSessions(T.state.liveSessions); true`);
        const row = extra => ev(`(() => { const a = window.__m.agents['th-a-id']; T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(a), agent_type: 'claude', ...${JSON.stringify(extra)} }] }); return true; })()`);
        const openRestart = async () => {
            await ev(`window.__restarts.length = 0; window.__held.length = 0; (async () => { await window.restartSession(); return true; })()`);
            await waitFor(`document.getElementById('restart-modal').style.display === 'flex' && !!document.querySelector('#restart-acf .acf-model')`, 'the restart form', 5000);
        };
        const state = () => ev(`(() => { const b = document.querySelector('#restart-modal .btn-warning'); const t = document.querySelector('#restart-acf .acf-thinking'), w = document.querySelector('#restart-acf .acf-thinking-wrap');
            return { disabled: b.disabled, cli: document.querySelector('#restart-acf .acf-cli-warning').textContent, model: document.querySelector('#restart-acf .acf-model').value,
                thinkingShown: !w.hidden, thinking: t.value, note: document.querySelector('#restart-acf .acf-thinking-ran').textContent, held: window.__held.length }; })()`);
        const clickRestart = async () => { await ev(`document.querySelector('#restart-modal .btn-warning').click(); true`); await waitFor(`window.__restarts.length === 1`, 'the restart request', 5000); return ev(`window.__restarts[0]`); };

        console.log('1. Neither the CLI check nor the model list has answered: Restart is not held, and goes out with the agent\'s own model');
        await row({ model: 'opus-x', thinking: 'high', thinking_actual: 'high' });
        await select('th-a');
        await openRestart();
        await waitFor(`window.__held.length >= 2`, 'both requests held', 5000);
        await sleep(400);   // sleep-ok: a held request staying held has no completion event; this is the time the form is left to react to nothing
        let s = await state();
        assert.match(s.cli, /Checking claude CLI/, 'the check is still pending: ' + s.cli);
        assert.equal(s.thinkingShown, false, 'no levels until the list arrives');
        assert.equal(s.disabled, false, 'the Restart button is not disabled while they are pending');
        assert.equal(s.model, 'opus-x', 'the model field holds the agent\'s own model');
        let body = await clickRestart();
        assert.equal(body.model, 'opus-x', 'the restart keeps the model: ' + JSON.stringify(body));
        assert.equal('thinking' in body, false, 'and sends no level (the agent keeps its own): ' + JSON.stringify(body));
        console.log('   ✓');

        console.log('2. New Agent: Launch is not held by the pending check either (before the check has ever answered: it is remembered for 30 s)');
        await ev(`window.__held.length = 0; window.showLaunchModal(); (async () => { await window._selectLaunchType('agent'); return true; })()`);
        await waitFor(`document.getElementById('launch-step-agent').style.display !== 'none' && !!document.querySelector('#launch-step-agent .acf-cli-warning')`, 'the New Agent form', 5000);
        await waitFor(`window.__held.length >= 1`, 'the check held', 5000);
        const launch = await ev(`(() => { const b = document.querySelector('#launch-step-agent [data-on-click="m-launch-session"]'); return { disabled: b.disabled, cli: document.querySelector('#launch-step-agent .acf-cli-warning').textContent }; })()`);
        assert.match(launch.cli, /Checking/); assert.equal(launch.disabled, false, 'Launch is not disabled while the check is pending');
        console.log('   ✓');
        console.log('3. When the answers arrive, the list fills in and the stored level is shown');
        await openRestart();
        await waitFor(`window.__held.length >= 2`, 'both requests held', 5000);
        await ev(`window.__release(); true`);
        await waitFor(`document.querySelector('#restart-acf .acf-thinking').options.length > 1`, 'the levels', 5000);
        await waitFor(`document.querySelector('#restart-acf .acf-cli-warning').style.display === 'none'`, 'the check to end', 5000);
        s = await state();
        assert.equal(s.thinking, 'high'); assert.equal(s.model, 'opus-x');
        assert.equal(s.note, '', 'the agent runs at the stored level: no note');
        body = await clickRestart();
        assert.equal(body.thinking, 'high', 'the stored level is kept: ' + JSON.stringify(body));
        console.log('   ✓');

        console.log('4. The agent last ran at another level: the form keeps the stored one and says so');
        const noteFor = async extra => { await row(extra); await openRestart(); await ev(`window.__release(); true`); await waitFor(`document.querySelector('#restart-acf .acf-thinking').options.length > 1`, 'the levels', 5000); return state(); };
        s = await noteFor({ model: 'opus-x', thinking: 'high', thinking_actual: 'max' });
        assert.equal(s.thinking, 'high', 'the form keeps the stored level');
        assert.equal(s.note, 'The agent last ran at Max (changed inside the CLI).');
        if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/restart-ran-note.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
        body = await clickRestart();
        assert.equal(body.thinking, 'high', 'and a restart keeps it too: ' + JSON.stringify(body));
        s = await noteFor({ model: 'opus-x', thinking: '', thinking_actual: 'max' });
        assert.equal(s.thinking, '', 'no stored level: Default');
        assert.equal(s.note, 'The agent last ran at Max.', 'no stored level: it does not claim a change');
        s = await noteFor({ model: 'opus-x', thinking: 'max', thinking_actual: 'max' });
        assert.equal(s.note, '', 'the same level: no note');
        s = await noteFor({ model: 'opus-x', thinking: 'high', thinking_actual: '' });
        assert.equal(s.note, '', 'the transcript shows none yet: no note');
        console.log('   ✓');

        console.log('\nPASS restart slow CLI');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
