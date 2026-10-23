// Stopping a team that works in a git worktree (#6707, from UI Accessibility's #6702 audit): the Kill Team confirmation says what happens to the worktree (removed if it has no unsaved changes,
// otherwise kept), and the stop's answer is shown: removed, kept (and why, and where) or could not be removed (an alert). A team without a worktree says nothing about one. The server's answer shape
// is the kill's `worktree` {state, path, branch, reason} (Backend's #6701). Drives Chrome over CDP through agent_view_harness.js against the isolated dev server; the kill answers are the page's fetch.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const open = `getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`(() => { const inner = window.__innerFetch || (window.__innerFetch = window.fetch); window.__answer = { ok: true, sessions_killed: 1 };
            window.fetch = (url, opts = {}) => { if (/\\/api\\/sessions\\/live\\/[^/]+\\/kill/.test(String(url))) return Promise.resolve(new Response(JSON.stringify(window.__answer), { status: 200, headers: { 'content-type': 'application/json' } })); return inner(url, opts); }; return true; })()`);
        const seed = withWt => ev(`localStorage.clear(); T.state.liveSessions = []; window.__m.agents = {};
            window.__m.add('team-agent', 'K', 3, { sleeping: false, session_id: 't1', board_project: 'team-k' });
            T.state.liveSessions.forEach(s => { s.branch = 'coral-team/team-k'; s.git_state = ${withWt ? '{ is_worktree: true, base_branch: "master" }' : 'null'}; });
            T.render.renderLiveSessions(T.state.liveSessions); true`);
        const ask = async () => { await ev(`T.render.killBoard('team-k'); true`); await waitFor(open, 'the Kill Team confirmation', 8000); return ev(`document.getElementById('confirm-modal-message').textContent`); };
        const cancel = async () => { await ev(`document.querySelector('#confirm-modal .btn:not(#confirm-modal-yes)').click(); true`); await waitFor(`!(${open})`, 'the confirmation to close', 4000); };

        console.log('the confirmation');
        await seed(true);
        const m1 = await ask();
        check('a team on a worktree: the confirmation says the worktree is removed if it has no unsaved changes, otherwise kept, and names its branch', m1.includes('Its worktree (branch coral-team/team-k) is removed if it has no unsaved changes; otherwise it is kept.'), m1);
        check('and that sentence comes before the log note, once', m1.indexOf('worktree') < m1.indexOf('terminal log') && m1.split('worktree').length === 2, m1);
        await cancel();
        await seed(false);
        const m2 = await ask();
        check('a team without a worktree says nothing about one', !/worktree/i.test(m2), m2);
        await cancel();

        console.log('the stop\'s answer');
        const stop = async (answer) => {
            await seed(true);
            await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); document.getElementById('notice-live') && (document.getElementById('notice-live').textContent = ''); document.getElementById('notice-alert') && (document.getElementById('notice-alert').textContent = ''); window.__answer = ${JSON.stringify(answer)}; true`);
            await ask();
            await ev(`document.getElementById('confirm-modal-yes').click(); true`);
            const wantsWords = !!(answer.worktree && answer.worktree.state && answer.worktree.state !== 'none');
            if (wantsWords) await waitFor(`!!(document.getElementById('notice-live').textContent.trim() || document.getElementById('notice-alert').textContent.trim())`, 'the stop to be said', 4000);
            await sleep(900); // sleep-ok: whatever else the toast says (or its delayed alert) lands within this
            // what a screen reader is told: a toast says its words in the polite status or, for an error, the alert region that are in the page (notifications.js)
            return ev(`[...[['notice-live', 'status'], ['notice-alert', 'alert']].map(([id, role]) => ({ text: ((document.getElementById(id) || {}).textContent || '').trim(), role })), ...[...document.querySelectorAll('.toast')].map(t => ({ text: (t.querySelector('.toast-message') || t).textContent.trim(), role: 'visible' }))].filter(x => x.text)`);
        };
        let t = await stop({ ok: true, sessions_killed: 1, worktree: { state: 'removed', path: '/tmp/wt/team-k', branch: 'coral-team/team-k' } });
        check('removed: "The team stopped and its worktree was removed."', t.some(x => x.text === 'The team stopped and its worktree was removed.'), JSON.stringify(t));
        t = await stop({ ok: true, sessions_killed: 1, worktree: { state: 'kept', path: '/tmp/wt/team-k', branch: 'coral-team/team-k', reason: 'it has uncommitted changes' } });
        check('kept: says why and where, as a status', t.some(x => x.text === 'The team stopped. Its worktree was kept: it has uncommitted changes. It is at /tmp/wt/team-k, on branch coral-team/team-k.' && x.role !== 'alert'), JSON.stringify(t));
        t = await stop({ ok: true, sessions_killed: 1, worktree: { state: 'failed', path: '/tmp/wt/team-k', branch: 'coral-team/team-k', reason: 'the worktree is locked' } });
        check('could not be removed: says so with the reason and the path, as an alert', t.some(x => x.text === 'The team stopped, but its worktree could not be removed: the worktree is locked. It is at /tmp/wt/team-k, on branch coral-team/team-k.' && x.role === 'alert'), JSON.stringify(t));
        t = await stop({ ok: true, sessions_killed: 1 });
        check('an answer with no worktree says nothing about one', !t.some(x => /worktree/.test(x.text)), JSON.stringify(t));

        console.log('Kill Session (one agent) and a branch kept');
        const seedOne = withWt => ev(`localStorage.clear(); T.state.liveSessions = []; window.__m.agents = {};
            window.__m.add('solo', 'K', 3, { sleeping: false, session_id: 'o1' });
            T.state.liveSessions.forEach(s => { s.branch = 'coral-team/solo'; s.git_state = ${withWt ? '{ is_worktree: true }' : 'null'}; });
            T.render.renderLiveSessions(T.state.liveSessions); true`);
        const askOne = async () => { await ev(`T.render.killSessionDirect('solo', 'claude', 'o1'); true`); await waitFor(open, 'the Kill Session confirmation', 8000); return ev(`document.getElementById('confirm-modal-message').textContent`); };
        await seedOne(true);
        const o1 = await askOne();
        check('Kill Session of an agent on a worktree says the same sentence, before the log note', o1.includes('Its worktree (branch coral-team/solo) is removed if it has no unsaved changes; otherwise it is kept.') && o1.indexOf('worktree') < o1.indexOf('terminal log'), o1);
        await cancel();
        await seedOne(false);
        check('and an agent that is not on one says nothing about it', !/worktree/i.test(await askOne()));
        await cancel();
        await seedOne(true);
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); document.getElementById('notice-live').textContent = ''; window.__answer = ${JSON.stringify({ ok: true, worktree: { state: 'failed', path: '/tmp/wt/solo', branch: 'coral-team/solo', reason: 'git would not remove it' } })}; true`);
        await askOne(); await ev(`document.getElementById('confirm-modal-yes').click(); true`);
        await waitFor(`document.getElementById('notice-alert').textContent.includes('could not be removed')`, 'the single kill\'s worktree answer', 4000);
        check('a single kill says it too (an alert when the worktree could not be removed)', (await ev(`document.getElementById('notice-alert').textContent`)).includes('The agent was killed, but its worktree could not be removed: git would not remove it. It is at /tmp/wt/solo, on branch coral-team/solo.'));
        t = await stop({ ok: true, sessions_killed: 1, worktree: { state: 'removed', path: '/tmp/wt/team-k', branch: 'coral-team/team-k', reason: 'its branch is kept: it has commits that are not merged' } });
        check('removed with a branch kept: says the branch is kept and why', t.some(x => x.text === 'The team stopped and its worktree was removed. Its branch is kept: it has commits that are not merged.'), JSON.stringify(t));
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} worktree stop checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
