// A feed diff that only ages agents (staleness_seconds) must not rebuild the whole sidebar every time (P6 of the T137
// profile: 12-28 ms per render at 50 agents, once a second). A real change always renders, and the sidebar is
// re-rendered at least every 10 s so the "Ns ago" text stays honest.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const evaluate = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await evaluate(`new Promise(r => { const w = () => typeof window.selectLiveSession === 'function' ? r() : setTimeout(w, 50); w(); })`);
        const r = await evaluate(`(async () => {
            const m = await import((window.__coralStatic||'/static/')+'websocket.js');
            const { state } = await import((window.__coralStatic||'/static/')+'state.js');
            // Silence the page's own feed and let its start-up renders finish, so only the messages below move the sidebar.
            if (state.coralWs) { state.coralWs.onmessage = null; state.coralWs.onclose = null; state.coralWs.close(); }
            await new Promise(r => setTimeout(r, 2500)); // sleep-ok: the start-up renders have no completion event once the page's own feed is silenced; a window with nothing left to arrive
            let renders = 0;
            // Renders are counted by the page's own event, not by DOM changes: a row's tooltip is built on hover now (#4497), so a render that has nothing new to show changes no node.
            window.addEventListener('coral:live-list-rendered', () => { renders++; });
            const tick = () => new Promise(r => setTimeout(r, 60));
            const row = (extra = {}) => ({ name: 'drift-a', session_id: 'drift-sid-a', agent_type: 'terminal', server: 'local', display_name: 'drift-a', working: false, staleness_seconds: 10, ...extra });
            const realNow = Date.now; let clock = realNow(); Date.now = () => clock;
            const count = async fn => { const before = renders; fn(); await tick(); return renders - before; };
            const out = {};
            state.liveSessions = [];
            out.first = await count(() => m.handleCoralMessage({ type: 'coral_update', sessions: [row()] }));        // full update: renders
            out.driftSkipped = await count(() => m.handleCoralMessage({ type: 'coral_diff', changed: [row({ staleness_seconds: 11 })] }));
            out.driftSkipped2 = await count(() => m.handleCoralMessage({ type: 'coral_diff', changed: [row({ staleness_seconds: 12 })] }));
            out.stalenessStillStored = state.liveSessions.find(s => s.session_id === 'drift-sid-a').staleness_seconds;
            out.realChange = await count(() => m.handleCoralMessage({ type: 'coral_diff', changed: [row({ working: true, staleness_seconds: 1 })] }));
            out.afterRealChange = await count(() => m.handleCoralMessage({ type: 'coral_diff', changed: [row({ working: true, staleness_seconds: 2 })] }));
            clock += 10500;                                                                                          // the age text is now stale enough
            out.driftAfter10s = await count(() => m.handleCoralMessage({ type: 'coral_diff', changed: [row({ working: true, staleness_seconds: 13 })] }));
            out.removal = await count(() => m.handleCoralMessage({ type: 'coral_diff', removed_remote: [{ server: 'local', key: 'drift-sid-a' }], changed: [] }));
            Date.now = realNow; state.liveSessions = [];
            return out;
        })()`);
        assert.ok(r.first > 0, 'the first full update renders the sidebar');
        assert.equal(r.driftSkipped, 0, 'an age-only diff does not rebuild the sidebar');
        assert.equal(r.driftSkipped2, 0, 'nor the next one');
        assert.equal(r.stalenessStillStored, 12, 'the new age is still stored for everything that reads it');
        assert.ok(r.realChange > 0, 'a status change renders');
        assert.equal(r.afterRealChange, 0, 'and age-only diffs after it skip again');
        assert.ok(r.driftAfter10s > 0, 'after 10 s even an age-only diff re-renders (the "Ns ago" text)');
        assert.ok(r.removal > 0, 'a removal renders');
        console.log('PASS age-only feed diffs skip the sidebar render; real changes, removals and the 10 s bound render', JSON.stringify(r));
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
