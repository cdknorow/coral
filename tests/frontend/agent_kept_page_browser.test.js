// #3726 (the client side of the #3714 audit's items 9 and 6):
//  - Switching back to an agent seen in the last few minutes shows its kept chat page at once and fetches only
//    what came after (a poll from the page's end), not the first page again; messages that arrived meanwhile are
//    appended; a transcript that got shorter, or a kept page older than its time limit, loads from the start.
//  - Board /status is not called more than once per team however fast the agents are switched: the sidebar's
//    refresh is throttled per team (team_availability.js), which this pins.
// The agents' /chat is answered by the harness's in-page mock; /status goes to the isolated server and is only counted.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const PAGE = 30; // live_chat.js FIRST_PAGE: the size of the first page the dashboard asks for

(async () => {
    const { client, Page, Runtime, ev, waitFor, PRELOAD, install, select, reqs, clearReqs, firstPage, settled } = await connect();
    const { waitStable } = require('./helpers/wait').bind(ev);
    const ids = () => ev('window.__rendered()');
    const chat = async () => (await reqs()).filter(r => r.kind === 'chat');
    // No new mocked request for 300 ms: the page's own follow-up requests have had their chance.
    const quiet = async () => { await waitStable(`window.__m.requests.length`, 'the mocked requests to stop', 300).catch(() => {}); await settled(); };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat')`);

        console.log('1. A switch back shows the kept page at once and fetches only what came after');
        await ev(`window.__m.add('qa-kept-a', 'A', 250, { sleeping: true }); window.__m.add('qa-kept-b', 'B', 250, { sleeping: true })`);
        await clearReqs();
        await select('qa-kept-a');
        const firstA = await firstPage();
        assert.equal(firstA.length, PAGE, `the first visit loads a first page of ${PAGE}`);
        await quiet();
        await select('qa-kept-b');
        await firstPage();
        await quiet();
        // While A is away, three messages arrive.
        await ev(`(() => { const a = window.__m.agents['qa-kept-a-id']; for (let i = 250; i < 253; i++) a.msgs.push({ type: 'assistant', text: 'A' + i + ' lorem ipsum while you were away' }); })()`);
        await clearReqs();
        await select('qa-kept-a');
        // Before any reply: the kept page is already on screen.
        assert.deepEqual((await ids()).slice(0, PAGE), firstA, 'the kept page is shown at once, before the server has answered');
        await waitFor(`window.__rendered().length === ${PAGE + 3}`, 'the three new messages');
        await quiet();
        const back = await chat();
        assert.equal(back.filter(r => r.first || r.older).length, 0, `no first page and no older page on the way back (got ${JSON.stringify(back.map(r => ({ after: r.after, first: r.first })))})`);
        assert.ok(back.some(r => r.after === 250), 'the poll asked for what came after message 250');
        assert.deepEqual((await ids()).slice(-3), ['A250', 'A251', 'A252'], 'the new messages follow the kept page');
        console.log('   ✓ kept page shown before the answer, one after=250 poll, no first page, the 3 new messages appended');

        console.log('2. A transcript that got shorter loads again from its start');
        await select('qa-kept-b');
        await waitFor(`window.__rendered().length === ${PAGE}`, 'B shown');
        await quiet();
        await ev(`window.__m.agents['qa-kept-b-id'].msgs.length = 200`); // B was cut from 250 to 200 messages while away
        await select('qa-kept-a');
        await waitFor(`window.__rendered().length === ${PAGE + 3}`, 'A shown again');
        await quiet();
        await clearReqs();
        await select('qa-kept-b');
        await waitFor(`window.__m.requests.some(r => r.first && r.settled)`, 'a first page for the shortened transcript', 5000);
        await quiet();
        const nowB = await ids();
        assert.equal(nowB.length, PAGE, 'B is shown again from its start');
        assert.equal(nowB[nowB.length - 1], 'B199', `the newest message is B199 (got ${nowB[nowB.length - 1]})`);
        console.log('   ✓ a shorter transcript: the kept page is dropped and the first page is loaded again');

        console.log('3. A kept page older than its time limit is not used');
        await select('qa-kept-a');
        await waitFor(`window.__rendered().length === ${PAGE + 3}`, 'A shown');
        await quiet();
        await select('qa-kept-b');
        await waitFor(`window.__rendered().length === ${PAGE}`, 'B shown');
        await quiet();
        // Six minutes pass (the limit is five).
        await ev(`window.__realNow = Date.now.bind(Date); Date.now = () => window.__realNow() + 6 * 60 * 1000; true`);
        await clearReqs();
        await select('qa-kept-a');
        await waitFor(`window.__m.requests.some(r => r.first && r.settled)`, 'a first page after the limit', 5000);
        await ev(`Date.now = window.__realNow; true`);
        await quiet();
        const afterLimit = await ids();
        assert.equal(afterLimit.length, PAGE, `a fresh first page: the newest ${PAGE} of the 253 messages`);
        assert.equal(afterLimit[afterLimit.length - 1], 'A252', 'ending at the newest message');
        console.log('   ✓ after the time limit the first page is fetched');

        console.log('4. Board /status: at most one call per team however fast the agents are switched');
        await ev(`window.__stat = []; const inner = window.fetch; window.fetch = (u, o) => { if (/\\/api\\/board\\/[^/]+\\/status/.test(String(u))) window.__stat.push(String(u).replace(location.origin, '')); return inner(u, o); }; true`);
        await ev(`window.__m.add('qa-team-1', 'P', 20, { sleeping: true, board_project: 'qa-board-one' }); window.__m.add('qa-team-2', 'Q', 20, { sleeping: true, board_project: 'qa-board-two' })`);
        await ev(`T.render.renderLiveSessions(T.state.liveSessions); true`);
        await waitFor(`window.__stat.length >= 2`, 'the first status calls (one per team)', 5000);
        await ev(`window.__stat.length = 0; true`);
        for (let i = 0; i < 8; i++) { await select(i % 2 ? 'qa-team-1' : 'qa-team-2'); await ev(`T.render.renderLiveSessions(T.state.liveSessions); true`); }
        await quiet();
        const stat = await ev('window.__stat');
        assert.ok(stat.length <= 2, `eight quick switches and re-renders made at most one status call per team inside the throttle (got ${stat.length}: ${JSON.stringify(stat)})`);
        console.log(`   ✓ 8 quick switches and re-renders: ${stat.length} status calls (the throttle is 5 s per team)`);

        console.log('5. A change of Coral Hub account, and sign-out, forget the kept pages');
        const MOD = `(window.__coralStatic || '/static/') + 'local_store.js'`;
        for (const [how, forget] of [['an account change (setStoreOwner)', `import(${MOD}).then(m => { m.setStoreOwner('0123456789abcdef'); return true; })`],
            ['sign-out (forgetAccountMemory, which cloud.js runs next to purgeStored)', `import(${MOD}).then(m => { m.forgetAccountMemory(); return true; })`]]) {
            await select('qa-kept-a');
            await waitFor(`window.__rendered().length >= ${PAGE}`, 'A shown');
            await quiet();
            await select('qa-kept-b');
            await waitFor(`window.__rendered().length >= ${PAGE}`, 'B shown');
            await quiet();
            await ev(forget);
            await clearReqs();
            await select('qa-kept-a');
            await waitFor(`window.__m.requests.some(r => r.first && r.settled)`, `a first page after ${how}`, 5000);
            await quiet();
            assert.ok((await chat()).some(r => r.first), `after ${how}: the first page is fetched, not shown from a kept page`);
        }
        console.log('   ✓ after an account change and after sign-out, the first page is fetched again');

        console.log('\nAll kept-page checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
