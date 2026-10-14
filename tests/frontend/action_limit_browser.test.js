// A per-action limit is not load (#3435). Coral answers 429 for one action's own limit (too many
// workflow or task runs, a sender's cross-team rate, sign-in attempts) and marks it
// X-Coral-Limit: action. The dashboard then:
//   1. shows the server's own reason on the action (a 6th workflow trigger: "Too many workflow
//      runs…"), never "Coral is busy";
//   2. pauses nothing: the open chat keeps polling and shows new messages during the 30 s the
//      limit asks for;
//   3. the same for a board post refused by a rate limit (the draft stays, the reason is shown);
//   4. while an UNMARKED 429 is still load: it pauses the pollers (unchanged).
// The trigger and the post are answered in the page the way the server answers them once the
// marker ships (Backend's half), so this runs before and after it lands.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const BOARD = 'qa-limit-board';

// Wraps the harness's fetch mock (registered after it): chosen requests get a chosen answer.
const LIMITS = `(() => {
    const lim = window.__lim = { rules: [], hits: [] };
    const inner = window.fetch;
    window.fetch = (url, opts = {}) => {
        let p = '';
        try { const u = new URL(String(url && url.url || url), location.href); p = u.pathname; } catch {}
        const m = String((opts && opts.method) || 'GET').toUpperCase();
        for (const r of lim.rules) {
            if (r.left > 0 && r.method === m && new RegExp(r.re).test(p)) {
                r.left--;
                lim.hits.push(p);
                return Promise.resolve(new Response(JSON.stringify(r.body), { status: r.status, headers: { 'content-type': 'application/json', ...r.headers } }));
            }
        }
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install, select } = await connect();
    const overload = `(await import((window.__coralStatic || '/static/') + 'overload.js'))`;
    const toasts = `[...document.querySelectorAll('.toast .toast-message')].map(t => t.textContent)`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: LIMITS });
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await install();

        // An open chat on a live agent, polling every second.
        await ev(`(async () => { ${overload}._resetOverloadForTests(); window.__m.add('limit-agent', 'L', 6, { sleeping: false }); return true; })()`);
        await select('limit-agent');
        await waitFor(`window.__rendered().length === 6`, 'the chat');

        console.log('1. A 6th workflow trigger: the server\'s reason, no "busy"');
        const REASON = 'Too many workflow runs (5 running); try again in 30 s';
        await ev(`(() => { document.querySelectorAll('.toast').forEach(t => t.remove());
            window.__lim.rules.push({ method: 'POST', re: '^/api/workflows/[^/]+/trigger$', left: 1, status: 429,
                headers: { 'Retry-After': '30', 'X-Coral-Limit': 'action' }, body: { error: ${JSON.stringify(REASON)}, code: 'too_many_runs' } });
            return true; })()`);
        await ev(`(async () => { (await import((window.__coralStatic || '/static/') + 'workflows.js')).triggerWorkflow(7); return true; })()`);
        await waitFor(`${toasts}.some(t => t.includes(${JSON.stringify(REASON)}))`, 'the server\'s reason', 3000);
        const shown = await ev(toasts);
        assert.ok(shown.some(t => t === 'Failed to trigger workflow: ' + REASON), `the reason, not JSON or a status (${JSON.stringify(shown)})`);
        assert.ok(!shown.some(t => /busy/i.test(t)), `never "busy" (${JSON.stringify(shown)})`);
        assert.equal(await ev(`(async () => ${overload}.isPaused())()`), false, 'nothing paused');
        console.log(`   ✓ "${shown.find(t => t.includes(REASON))}", nothing paused`);

        console.log('2. The chat keeps polling during the 30 s the limit asks for');
        await ev(`(() => { const a = window.__m.agents['limit-agent-id']; a.msgs.push({ type: 'assistant', text: 'L6 lorem ipsum, a new reply after the limit' }); return true; })()`);
        await waitFor(`window.__rendered().length === 7`, 'the new message, by the 1 s poll', 4000);
        console.log('   ✓ a new message arrived within the next polls');

        console.log('3. A board post refused by a sender\'s rate limit: the reason, the draft stays');
        const RATE = 'too many messages to "other-team"; retry in 20s';
        await ev(`(async () => { document.querySelectorAll('.toast').forEach(t => t.remove());
            const mb = await import((window.__coralStatic || '/static/') + 'message_board.js');
            mb.selectBoardProject(${JSON.stringify(BOARD)});
            window.__lim.rules.push({ method: 'POST', re: '^/api/board/${BOARD}/messages$', left: 1, status: 429,
                headers: { 'Retry-After': '20', 'X-Coral-Limit': 'action' }, body: { error: ${JSON.stringify(RATE)} } });
            return true; })()`);
        await waitFor(`!!document.getElementById('mb-post-input')`, 'the board composer');
        await ev(`(async () => { document.getElementById('mb-post-input').value = 'hello other team';
            await (await import((window.__coralStatic || '/static/') + 'message_board.js')).postBoardMessage(); return true; })()`);
        await waitFor(`${toasts}.some(t => t.includes(${JSON.stringify(RATE)}))`, 'the rate limit reason', 3000);
        assert.equal(await ev(`document.getElementById('mb-post-input').value`), 'hello other team', 'the draft stays');
        assert.equal(await ev(`(async () => ${overload}.isPaused())()`), false, 'nothing paused');
        console.log('   ✓ reason shown, draft kept, nothing paused');

        console.log('4. An unmarked 429 is still load: it pauses');
        await ev(`(async () => {
            window.__lim.rules.push({ method: 'POST', re: '^/api/workflows/[^/]+/trigger$', left: 1, status: 429, headers: { 'Retry-After': '3' }, body: { error: 'busy' } });
            await fetch('/api/workflows/7/trigger', { method: 'POST', body: '{}' });
            return true; })()`);
        assert.equal(await ev(`(async () => ${overload}.isPaused())()`), true, 'an unmarked 429 pauses, as before');
        await ev(`(async () => { ${overload}._resetOverloadForTests(); return true; })()`);
        console.log('   ✓ paused');

        console.log('\nAll action-limit checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
