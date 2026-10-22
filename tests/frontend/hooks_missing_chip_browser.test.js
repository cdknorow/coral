// #6089: a running agent whose hook programs are gone (the app it started from was replaced) is flagged once, on its row, with the fix in the chip's tooltip. The server lists such agents in
// /api/system/hooks-status ("missing"); the row shows "Hooks can't run" for exactly those, and the chip goes when the list no longer has them (the agent was restarted).
//
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await install();

        await ev(`(async () => {
            window.__m.add('lost-a', 'L', 3, { sleeping: false });
            window.__m.add('fine-b', 'F', 3, { sleeping: false });
            window.__m.add('lost-c', 'C', 3, { sleeping: false });
            window.T.render.renderLiveSessions(window.T.state.liveSessions);
            return true;
        })()`);
        const chips = () => ev(`(() => { const out = {}; for (const id of ['lost-a-id', 'fine-b-id', 'lost-c-id']) { const r = document.querySelector('.session-group-item[data-session-id="' + id + '"]');
            const all = r ? r.querySelectorAll('.hooks-missing-chip') : []; out[id] = { n: all.length, title: all[0] ? all[0].title : '', text: all[0] ? all[0].textContent : '' }; } return out; })()`);
        const rowsShown = await ev(`document.querySelectorAll('.session-group-item').length`);
        assert.equal(rowsShown, 3, 'the three awake agents are on screen');
        assert.deepEqual(Object.values(await chips()).map(c => c.n), [0, 0, 0], 'no chip until the server lists an agent');

        const setMissing = ids => ev(`(() => { window.T.state.hooksMissing = new Set(${JSON.stringify(ids)}); window.T.render.renderLiveSessions(window.T.state.liveSessions); return true; })()`);
        await setMissing(['lost-a-id', 'lost-c-id']);
        const c = await chips();
        assert.equal(c['lost-a-id'].n, 1, 'one chip on a listed agent (once, not per message)');
        assert.equal(c['lost-c-id'].n, 1);
        assert.equal(c['fine-b-id'].n, 0, 'none on an agent whose hooks are fine');
        assert.equal(c['lost-a-id'].text, "Hooks can't run");
        assert.match(c['lost-a-id'].title, /can't reach this agent's hooks/);
        assert.match(c['lost-a-id'].title, /Reopen Coral or restart the agent/);

        // the same list again changes nothing (no second chip); a shorter list removes the restarted agent's chip
        await setMissing(['lost-a-id', 'lost-c-id']);
        assert.equal((await chips())['lost-a-id'].n, 1, 'still one chip');
        await setMissing(['lost-c-id']);
        const after = await chips();
        assert.equal(after['lost-a-id'].n, 0, 'the chip goes when the agent is restarted and no longer listed');
        assert.equal(after['lost-c-id'].n, 1);
        console.log('PASS an agent without hooks is flagged once on its row, and only while it is listed');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
