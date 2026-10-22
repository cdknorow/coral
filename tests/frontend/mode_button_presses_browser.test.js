// The Mode button counts Shift+Tab presses ONLY from the mode line on the screen, never from the mode the agent reported (live_permission_mode: its own report, which could be wrong or forged) -
// Security's #5979. A session reporting "default" with "plan mode" on the screen counts from Plan; with no mode line on the screen it sends exactly ONE press and says to check the mode line.
// The keys go through the page's own sendModeToggle; the route that takes them is stubbed and records what it is sent. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const STUB = `(() => { const R = window.fetch; window.__keys = [];
    window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin);
        if (/\\/api\\/sessions\\/live\\/[^/]+\\/keys$/.test(url.pathname) && (o.method || 'GET').toUpperCase() === 'POST') { window.__keys.push(JSON.parse(o.body).keys); return Promise.resolve(new Response('{"ok":true}', { status: 200, headers: { 'content-type': 'application/json' } })); }
        return R(u, o); };
    window.__m.add('mp-claude', 'C', 3, { sleeping: false, agent_type: 'claude' }); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install, select } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.clear(); true`);
        await ev(STUB);
        await select('mp-claude');
        const situation = (footer, reported) => ev(`(() => { const el = document.getElementById('pane-capture'); el.dataset.captureState = 'ok'; el.textContent = ${JSON.stringify(footer)}; T.state.currentSession.live_permission_mode = ${JSON.stringify(reported)}; window.__keys.length = 0; return true; })()`);
        const press = async (target) => { await ev(`window.__coralTest.sendModeToggle(${JSON.stringify(target)}); true`); await new Promise(r => setTimeout(r, 300)); return ev(`window.__keys.slice()`); }; // sleep-ok: the call is async (a socket is tried first, then the route)

        // the agent says Default, the screen says Plan: counted from PLAN (two presses to Accept edits), not from the report (one)
        await situation('❯ \n  ⏸ plan mode on (shift+tab to cycle)', 'default');
        assert.deepEqual(await press('acceptEdits'), [['BTab', 'BTab']], 'counted from the screen (Plan -> Default -> Accept edits: two presses), not from the reported Default (one)');
        // the screen says Default, the agent says Plan: counted from Default (one press), not from the report (two)
        await situation('❯ \n  ⏸ manual mode on (shift+tab to cycle)', 'plan');
        assert.deepEqual(await press('acceptEdits'), [['BTab']], 'counted from the screen, not from a reported Plan');
        // no mode line on the screen: exactly one press, whatever the agent reports; and it says so
        for (const reported of ['default', 'plan', 'auto', 'acceptEdits']) {
            await situation('> a screen with no mode line at all', reported);
            assert.deepEqual(await press('plan'), [['BTab']], `no mode line (reported ${reported}): exactly one press`);
        }
        const toast = await ev(`[...document.querySelectorAll('.toast, [role=status], #toast-container *')].map(e => e.textContent).join(' | ')`);
        assert.match(toast, /check the mode line/i, 'it says to check the mode line: ' + toast);
        // nothing captured at all: the same
        await situation('', 'default');
        assert.deepEqual(await press('plan'), [['BTab']], 'nothing captured: one press');
        console.log('PASS the Mode button counts presses from the screen, never from the agent\'s report');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
