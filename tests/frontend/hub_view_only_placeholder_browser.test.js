// "View only through Coral Hub" survives the render (#5856, coral-hub's report). hub_access.js applyViewOnly disables the composer and gives it that text; the session render (sessions.js, on
// every select) and every live update (websocket.js) wrote the agent's normal placeholder over it, so when the access answer came BEFORE the render the box stayed disabled without saying why.
// Both orders, and the live update: the view-only text stays while the state holds, the normal text of the agent that is open is what comes back when it ends, a normal session has the normal
// placeholder, and a sleeping agent keeps its sleeping words the same way. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const VIEW = 'View only through Coral Hub';

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`(async () => { window.__m.add('ro-a', 'Alpha', 2, { sleeping: false, session_id: 'ro-a-id' }); window.__m.add('ro-b', 'Beta', 2, { sleeping: false, session_id: 'ro-b-id' });
            window.T.render.renderLiveSessions(window.T.state.liveSessions); window.__ha = await import((window.__coralStatic || '/static/') + 'hub_access.js');
            window.__cp = await import((window.__coralStatic || '/static/') + 'composer_placeholder.js'); return true; })()`);
        const box = () => ev(`(() => { const i = document.getElementById('command-input'); return { disabled: i.disabled, placeholder: i.placeholder }; })()`);
        const select = async (name, sid) => { await ev(`window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(sid)}, 'local')`); await ev(`window.__coralReady.agentSelected(${JSON.stringify(sid)}).then(() => true)`); };
        const readOnly = on => ev(`window.__ha.setHubReadOnly(window.T.state.currentSession, ${on ? `{ hub: 'view', effective: 'view', limitedBy: 'hub', label: 'box' }` : 'null'}); true`);

        console.log('1. a normal session has the normal placeholder');
        await select('ro-a', 'ro-a-id');
        const normalA = await box();
        assert.equal(normalA.disabled, false); assert.notEqual(normalA.placeholder, VIEW); assert.ok(/Alpha|ro-a/i.test(normalA.placeholder), 'it names the agent: ' + normalA.placeholder);

        console.log('2. render first, then the access answer');
        await readOnly(true);
        assert.deepEqual(await box(), { disabled: true, placeholder: VIEW });

        assert.equal(await ev(`document.getElementById('command-input').getAttribute('aria-label')`), VIEW, 'the box is named for why it is off, not only by a placeholder');
        console.log('3. the access answer first, then a live update (the bug): the words stay');
        await ev(`window._coralHandleWsMessage({ type: 'coral_update', sessions: [{ name: 'ro-a', session_id: 'ro-a-id', agent_type: 'claude', display_name: 'Alpha renamed', working_directory: '/x', status: 'working' }, { name: 'ro-b', session_id: 'ro-b-id', agent_type: 'claude', working_directory: '/y', status: 'working' }] }); true`);
        assert.equal(await ev(`window.T.state.currentSession.display_name`), 'Alpha renamed', 'the update was applied (it renames the agent)');
        assert.deepEqual(await box(), { disabled: true, placeholder: VIEW }, 'the live update that renames the agent does not take the text away');
        await ev(`window.__cp.setCommandPlaceholder(document.getElementById('command-input'), 'a live update wrote this'); true`);
        assert.deepEqual(await box(), { disabled: true, placeholder: VIEW }, 'nor does a direct write');

        console.log('4. a different agent rendered while view only: the words stay, and its own text is what comes back');
        await ev(`window.__ha.setHubReadOnly(window.T.state.currentSession, null); true`); // lift on A, then B opens view only
        await select('ro-b', 'ro-b-id'); await readOnly(true);
        assert.deepEqual(await box(), { disabled: true, placeholder: VIEW });
        await readOnly(false);
        const backB = await box();
        assert.equal(await ev(`document.getElementById('command-input').getAttribute('aria-label')`), 'Enter command or paste image', 'its own name is back');
        assert.equal(backB.disabled, false); assert.ok(/Beta|ro-b/i.test(backB.placeholder) && !/Alpha|ro-a/i.test(backB.placeholder), `Beta's own placeholder is back: ${backB.placeholder}`);

        console.log('5. what a live update wrote while view only is what comes back');
        await readOnly(true);
        await ev(`window.__cp.setCommandPlaceholder(document.getElementById('command-input'), 'newest normal text'); true`);
        await readOnly(false);
        assert.deepEqual(await box(), { disabled: false, placeholder: 'newest normal text' });

        console.log('6. the helper writes normally when no state holds');
        await ev(`window.__cp.setCommandPlaceholder(document.getElementById('command-input'), 'plain write'); true`);
        assert.equal((await box()).placeholder, 'plain write');
        console.log('7. a sleeping agent keeps its sleeping words the same way: a write while asleep is what comes back on wake');
        await readOnly(false);
        const sv = `window.__sv`;
        await ev(`(async () => { window.__sv = await import((window.__coralStatic || '/static/') + 'sleeping_view.js'); window.__cp.setCommandPlaceholder(document.getElementById('command-input'), 'awake text'); window.__sv.applySleepingView(true); return true; })()`);
        const asleep = await box();
        assert.equal(asleep.disabled, true); assert.match(asleep.placeholder, /sleeping/i);
        await ev(`window.__cp.setCommandPlaceholder(document.getElementById('command-input'), 'text written while asleep'); true`);
        assert.deepEqual(await box(), asleep, 'a live update while asleep does not take the sleeping words away');
        await ev(`${sv}.applySleepingView(false); true`);
        assert.deepEqual(await box(), { disabled: false, placeholder: 'text written while asleep' }, 'what was written while asleep is what comes back');

        console.log('PASS the view-only text survives the render and the live update');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
