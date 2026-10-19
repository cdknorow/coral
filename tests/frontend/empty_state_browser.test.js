// emptyState (#5162), driven directly in the page: it leaves exactly the markup the hand-written placeholders did (a div with the class and the text, or an li), replaces whatever the
// container held, and treats the text as text, never as markup. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const out = await ev(`(async () => {
            const { emptyState } = await import((window.__coralStatic || '/static/') + 'dom_write.js');
            const same = (build, old) => { const a = document.createElement('div'); const b = document.createElement('div'); build(a); b.innerHTML = old; return a.innerHTML === b.innerHTML; };
            const r = {};
            r.cost = same(c => emptyState(c, 'No usage data yet', 'cost-empty'), '<div class="cost-empty">No usage data yet</div>');
            r.board = same(c => emptyState(c, 'No messages yet'), '<div class="empty-state">No messages yet</div>');
            const ul = document.createElement('ul'); ul.innerHTML = '<li>old one</li><li>old two</li>'; emptyState(ul, 'No projects', 'empty-state', 'li'); r.li = ul.innerHTML; r.liKids = ul.children.length;
            const c = document.createElement('div'); emptyState(c, '<img src=x onerror=alert(1)> & more', 'cost-empty'); r.text = [c.textContent, c.querySelectorAll('img').length, c.innerHTML];
            return r; })()`);
        assert.equal(out.cost, true); assert.equal(out.board, true);
        assert.equal(out.li, '<li class="empty-state">No projects</li>'); assert.equal(out.liKids, 1);
        assert.deepEqual(out.text, ['<img src=x onerror=alert(1)> & more', 0, '<div class="cost-empty">&lt;img src=x onerror=alert(1)&gt; &amp; more</div>']);
        console.log('   ✓ the same markup as the hand-written placeholders, the old content replaced, the text kept as text');
        console.log('empty_state: ok');
        await client.close(); process.exit(0);
    } catch (e) { console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})();
