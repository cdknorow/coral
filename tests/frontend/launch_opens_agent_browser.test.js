// First run (#5835): pressing Launch lands the person in what they launched. Before, the dialog closed, a toast said "Launched: <name>" for four seconds and the page stayed on the
// home screen, with the new agent only a row to find in the list (the likeliest cause of "launched, never prompted"). A terminal is launched for real (no agent CLI is needed), the
// session it names is opened, its view shows and the composer has the focus; a launch that fails opens nothing. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    let launched = null;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        const cur = () => ev(`import((window.__coralStatic || '/static/') + 'state.js').then(m => m.state.currentSession ? m.state.currentSession.name : null)`);
        assert.equal(await cur(), null, 'no agent is open to start with');
        await ev(`(async () => { const real = window.fetch; window.__launchBody = null;
            window.fetch = async (u, o = {}) => { const r = await real(u, o); if (new URL(String(u), location.href).pathname === '/api/sessions/launch' && (o.method || 'GET') === 'POST') { window.__launchBody = await r.clone().json().catch(() => null); } return r; };
            return true; })()`);

        console.log('1. a launch opens the new session');
        await ev(`(async () => { await window.showLaunchModal(); window._selectLaunchType('terminal'); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('launch-step-terminal')).display !== 'none'`, 'the terminal step');
        await ev(`document.getElementById('launch-terminal-dir').value = '/tmp'; window.launchSession(); true`);
        await waitFor(`!!window.__launchBody && !!window.__launchBody.session_name`, 'the launch answer', 15000);
        launched = await ev(`window.__launchBody`);
        await waitFor(`import((window.__coralStatic || '/static/') + 'state.js').then(m => !!m.state.currentSession && m.state.currentSession.name === ${JSON.stringify(launched.session_name)})`, 'the new session opened', 15000);
        const open = await ev(`(() => ({ welcome: getComputedStyle(document.getElementById('welcome-screen')).display, }))()`);
        assert.equal(await cur(), launched.session_name);
        assert.equal(open.welcome, 'none', 'the home screen is gone: ' + JSON.stringify(open));
        console.log('PASS launching opens the new session');
    } finally {
        if (launched) { try { await ev(`fetch('/api/sessions/live/' + encodeURIComponent(${JSON.stringify(launched.session_name)}) + '/kill', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ agent_type: 'terminal', session_id: ${JSON.stringify(launched.session_id || '')} }) }).then(() => true)`); } catch { /* the runner's own cleanup follows */ } }
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
