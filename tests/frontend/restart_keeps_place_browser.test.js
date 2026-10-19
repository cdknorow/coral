// Restarting an agent keeps its place in the agent list (#4943). A restart gives the agent a new session id. The server serves its list in
// the order the agents' places were taken (list_at, which the new session inherits) and moves the old id's place to the new one in the
// saved order (session_order, the order within a group), but a feed diff added the new session at the END of the page's list and the
// page read the saved order once, so the restarted agent dropped to the bottom. The page now places a new session by its list_at
// (websocket.js) and reads the saved order again when a session it has not seen arrives (list_order.js followServerOrder).
//
// Agents of one folder (the same name) share a group and are ordered by the saved order within it; agents of different folders are
// groups, ordered as the list gives them. Both, on desktop and a 390x844 phone: the middle, first and last agent restarted, an order
// the user saved (the page's copy is stale after the restart), an order never saved, and a repeated restart.
//
// The server's side is mocked here: the restart route swaps the agent for a new id that keeps the old one's list_at and place in the saved
// order, as store.ReplaceLiveSession does (the Go tests cover the real thing).
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();

    // The list as a reader sees it: the rows' session ids, top to bottom.
    const shown = () => ev(`window.__shown()`);
    const restart = () => ev(`(() => { window.confirmRestart(); return true; })()`);

    const MOCK = `(() => {
        window.__srv = { order: null, restarts: 0, settingsReads: 0, seq: 0, last: null };
        window.__shown = () => [...document.querySelectorAll('#live-sessions-list .session-group-item, #mobile-session-list .session-group-item')].map(e => e.dataset.sessionId);
        const m0 = window.__m;
        const entry0 = m0.entry.bind(m0);
        m0.entry = a => ({ ...entry0(a), list_at: a.__at }); // every row says when its place in the list was taken (#4943)
        const prev = window.fetch;
        const json = (b, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } });
        window.fetch = (url, opts = {}) => {
            const u = String(url);
            const method = (opts.method || 'GET').toUpperCase();
            const rm = u.match(/\\/api\\/sessions\\/live\\/([^/?]+)\\/restart$/);
            if (rm && method === 'POST') {
                window.__srv.restarts++;
                const m = window.__m;
                const old = m.agents[JSON.parse(opts.body).session_id];
                const fresh = 'kp-new' + window.__srv.restarts;
                window.__srv.last = fresh;
                // The server: the new session takes the old one's place in the saved order and keeps its list_at.
                if (window.__srv.order) window.__srv.order = window.__srv.order.map(id => id === old.session_id ? fresh : id);
                delete m.agents[old.session_id];
                const qa = window.__qaState; window.__qaState = null; // m.add would put the row into the page's list itself; the feed's diff does that here
                m.add(old.name, old.prefix, 0, { sleeping: false, session_id: fresh }).__at = old.__at;
                window.__qaState = qa;
                // The list endpoint serves m.agents in insertion order: put it in the server's order.
                const served = Object.values(m.agents).sort((x, y) => x.__at < y.__at ? -1 : x.__at > y.__at ? 1 : 0);
                for (const id of Object.keys(m.agents)) delete m.agents[id];
                for (const a of served) m.agents[a.session_id] = a;
                // The feed's diff: the old row gone, the new one added.
                T.ws.handleCoralMessage({ type: 'coral_diff', changed: [m.entry(m.agents[fresh])], removed: [old.session_id] });
                return Promise.resolve(json({ ok: true, session_id: fresh, session_name: old.name, agent_name: old.name }));
            }
            if (/\\/api\\/sessions\\/live\\/[^/?]+\\?session_id=/.test(u)) return Promise.resolve(json({ ok: true }));
            if (u === '/api/settings' && method === 'GET') {
                window.__srv.settingsReads++;
                const s = {};
                if (window.__srv.order) s.session_order = JSON.stringify(window.__srv.order);
                return Promise.resolve(json({ settings: s }));
            }
            return prev(url, opts);
        };
    })()`;

    // agents: [name, id] in creation order; savedOrder: the order the user saved (null: never moved).
    const seed = (agents, savedOrder) => ev(`(() => {
        const m = window.__m;
        for (const id of Object.keys(m.agents)) delete m.agents[id];
        window.__srv.seq = 0;
        for (const [name, id] of ${JSON.stringify(agents)}) m.add(name, id.slice(-1).toUpperCase(), 3, { sleeping: false, session_id: id }).__at = '2026-10-19T00:00:0' + (++window.__srv.seq) + 'Z';
        window.__srv.order = ${JSON.stringify(savedOrder)};
        T.state.settings.session_order = ${JSON.stringify(savedOrder)} ? JSON.stringify(${JSON.stringify(savedOrder)}) : '';
        T.state.liveSessions = Object.values(m.agents).map(a => m.entry(a));
        T.render.renderLiveSessions(T.state.liveSessions);
    })()`);

    // The list is in this order within 4 s (the page reads the list and the saved order again when the new session appears).
    const expectOrder = (ids, what) => waitFor(`JSON.stringify(window.__shown()) === ${JSON.stringify(JSON.stringify(ids))}`, what, 4000).catch(async err => {
        err.message += ' (shown ' + JSON.stringify(await ev('window.__shown()')) + ', wanted ' + JSON.stringify(ids) + ', saved ' + JSON.stringify(await ev('T.state.settings.session_order')) + ', server ' + JSON.stringify(await ev('window.__srv')) + ')';
        throw err;
    });
    const restartAndWait = async (name, id) => {
        const before = await ev(`window.__srv.restarts`);
        await ev(`window.selectLiveSession(${JSON.stringify(name)}, 'claude', ${JSON.stringify(id)}, 'local')`);
        await restart();
        await waitFor(`window.__srv.restarts > ${before}`, `${id}'s restart to be asked`, 4000);
        return ev(`window.__srv.last`);
    };

    const run = async (label) => {
        for (const [group, names] of [['one group', ['kp', 'kp', 'kp']], ['a group each', ['kp-a', 'kp-b', 'kp-c']]]) {
            const agents = ['kp-a-id', 'kp-b-id', 'kp-c-id'].map((id, i) => [names[i], id]);
            const nameOf = id => agents.find(a => a[1] === id)[0];

            console.log(`${label}, ${group}: an agent never moved keeps its place when it is restarted`);
            await seed(agents, null);
            assert.deepEqual(await shown(), ['kp-a-id', 'kp-b-id', 'kp-c-id']);
            let fresh = await restartAndWait(nameOf('kp-b-id'), 'kp-b-id');
            await expectOrder(['kp-a-id', fresh, 'kp-c-id'], 'the restarted middle agent to stay in the middle');
            console.log('   ✓ a b c, restart b: a b c');

            if (group === 'one group') {
                console.log(`${label}, ${group}: an agent moved by the user keeps its place, though the page's copy of the order is stale`);
                await seed(agents, ['kp-c-id', 'kp-a-id', 'kp-b-id']);
                assert.deepEqual(await shown(), ['kp-c-id', 'kp-a-id', 'kp-b-id']);
                const reads = await ev(`window.__srv.settingsReads`);
                fresh = await restartAndWait(nameOf('kp-a-id'), 'kp-a-id');
                await expectOrder(['kp-c-id', fresh, 'kp-b-id'], 'the restarted agent to stay second');
                assert.ok((await ev(`window.__srv.settingsReads`)) > reads, 'the page read the saved order again when the new session appeared');
                console.log('   ✓ c a b, restart a: c a b');
            }

            console.log(`${label}, ${group}: the first and the last agent stay first and last, and a repeated restart keeps the place`);
            await seed(agents, null);
            const first = await restartAndWait(nameOf('kp-a-id'), 'kp-a-id');
            await expectOrder([first, 'kp-b-id', 'kp-c-id'], 'the restarted first agent to stay first');
            const last = await restartAndWait(nameOf('kp-c-id'), 'kp-c-id');
            await expectOrder([first, 'kp-b-id', last], 'the restarted last agent to stay last');
            const again = await restartAndWait(nameOf('kp-a-id'), first);
            await expectOrder([again, 'kp-b-id', last], 'a second restart of the first agent to keep it first');
            console.log('   ✓ first, last and repeated restarts');
        }
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat')`);
        await ev(MOCK);
        await run('Desktop');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await sleep(300); // sleep-ok: the phone layout settles
        await run('Phone 390x844');
        await Emulation.clearDeviceMetricsOverride();
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        console.log('\nAll restart-keeps-place checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
