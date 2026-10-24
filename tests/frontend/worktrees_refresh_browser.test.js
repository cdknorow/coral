// Settings > Storage and cleanup is open while a Kill Team is still finishing (#7055, Codex Test's #7051): the kept worktree was read as "In use"; when the stop's answer says it kept the worktree
// (the notice fires then) the list is read again and the entry shows what keeps it now. A removal, or Settings closed, reads nothing. The server's answers are the page's fetch. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const real = window.fetch; window.__reads = 0;
            window.__wt = [{ id: 'w3', kind: 'team', name: 'crew-2', path: '/data/worktrees/crew-2', repo_path: '/work/other', branch: 'coral-team/crew-2', dirty: false, unmerged: false, in_use: true }];
            window.fetch = (u, o) => { const p = new URL(String(u), location.origin).pathname;
                if (p === '/api/worktrees' && (!o || !o.method || o.method === 'GET')) { window.__reads++; return Promise.resolve(new Response(JSON.stringify({ worktrees: window.__wt }), { status: 200, headers: { 'Content-Type': 'application/json' } })); }
                return real(u, o); };
            return true; })()`);
        const badges = () => ev(`[...document.querySelectorAll('#wt-list .wt-item .wt-badge')].map(b => b.textContent)`);
        const notice = w => ev(`(async () => { (await import(${S} + 'worktree_notice.js')).showWorktreeNotice(${JSON.stringify(w)}, 'team'); return true; })()`);
        await ev(`window.showSettingsModal('cleanup'); true`);
        await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 1`, 'the kept worktree', 8000);
        assert.deepEqual(await badges(), ['In use'], 'read while the stop was still finishing');
        const before = await ev(`window.__reads`);
        // the stop finishes and kept it: it no longer holds an agent, it has unsaved changes
        await ev(`window.__wt = [{ ...window.__wt[0], in_use: false, dirty: true }]; true`);
        await notice({ state: 'kept', path: '/data/worktrees/crew-2', branch: 'coral-team/crew-2', reason: 'it has uncommitted changes' });
        await waitFor(`!document.querySelector('#wt-list .wt-item .wt-badge')?.textContent.includes('In use')`, 'the entry to stop saying In use', 8000);
        assert.ok((await ev(`window.__reads`)) > before, 'the list was read again');
        assert.ok(!(await badges()).includes('In use') && (await badges()).length === 1, 'and shows what keeps it now: ' + JSON.stringify(await badges()));
        // a removal reads nothing
        const reads = await ev(`window.__reads`);
        await notice({ state: 'removed', path: '/data/worktrees/crew-2', branch: 'coral-team/crew-2' });
        await ev(`new Promise(r => setTimeout(r, 300))`); // sleep-ok: proving nothing happens
        assert.equal(await ev(`window.__reads`), reads, 'a removed worktree does not re-read the list');
        // Settings closed (no list on screen): nothing is read
        await ev(`document.getElementById('wt-list').remove(); true`);
        await notice({ state: 'kept', path: '/data/worktrees/crew-2', branch: 'coral-team/crew-2', reason: 'it has uncommitted changes' });
        await ev(`new Promise(r => setTimeout(r, 300))`); // sleep-ok: proving nothing happens
        assert.equal(await ev(`window.__reads`), reads, 'with no list on screen nothing is read');
        console.log('PASS Settings kept-worktrees list refreshes when a stop keeps a worktree');
    } finally { await client.close(); }
})().catch(err => { console.error(err); process.exit(1); });
