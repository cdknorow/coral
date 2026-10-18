// The shared list bar's two small promises (#4778, Frontend's review of #4744): a panel may give its own plural, and a bar that is replaced lets go of everything it put on the page
// (dispose()). Built directly in a page, away from any panel: the count says "1 octopus", "2 octopuses" (the spec's plural) and "5 cats" (none given: the noun and an s), "N+" for a cut list
// and "k of N+" for a filtered cut one; dispose() removes the document click listener it added (every addEventListener has its removeEventListener), the node, and a search still waiting
// (typed, then disposed: onChange is never called). Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1200, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { const base = window.__coralStatic || '/static/'; window.__LB = await import(base + 'list_bar.js'); window.__F = await import(base + 'task_filters.js'); return true; })()`);
        const spec = extra => `({ idp: 'zz', noun: 'cat', label: 'x', searchLabel: 'x', placeholder: 'x', sorts: null, fields: [], model: { EMPTY: { q: '' }, clean: f => ({ q: String((f && f.q) || '') }), chips: f => (f.q ? [{ key: 'q', label: f.q }] : []), isFiltered: f => !!f.q, without: (f, k) => ({ ...f, [k]: '' }) }, ...${extra} })`;
        const count = (extra, f, counts) => ev(`(() => { const bar = window.__LB.buildListBar(${spec(extra)}, () => {}); document.body.append(bar.node); bar.set(${JSON.stringify(f)}, ${JSON.stringify(counts)}); const t = bar.node.querySelector('.list-bar-count').textContent; bar.dispose(); return t; })()`);

        console.log('1. The count: the spec\'s plural, or the noun and an s, and N+ for a cut list');
        assert.equal(await count('{ noun: \'octopus\', plural: \'octopuses\' }', { q: '' }, { matched: 1, all: 1 }), '1 octopus');
        assert.equal(await count('{ noun: \'octopus\', plural: \'octopuses\' }', { q: '' }, { matched: 2, all: 2 }), '2 octopuses');
        assert.equal(await count('{}', { q: '' }, { matched: 5, all: 5 }), '5 cats');
        assert.equal(await count('{}', { q: '' }, { matched: 1, all: 1 }), '1 cat');
        assert.equal(await count('{}', { q: '' }, { matched: 0, all: 0 }), '0 cats');
        assert.equal(await count('{ noun: \'octopus\', plural: \'octopuses\' }', { q: '' }, { matched: 1, all: 1, truncated: true }), '1+ octopuses', 'a cut list is never singular');
        assert.equal(await count('{}', { q: '' }, { matched: 500, all: 500, truncated: true }), '500+ cats');
        assert.equal(await count('{}', { q: 'x' }, { matched: 3, all: 500, truncated: true }), '3 of 500+');
        assert.equal(await count('{}', { q: 'x' }, { matched: 3, all: 40 }), '3 of 40');
        assert.equal(await count('{}', { q: '' }, null), '', 'no counts, no text');
        assert.equal(await count('{}', { q: '' }, { text: 'the panel\'s own words', matched: 1, all: 1 }) !== '', true);

        console.log('2. dispose(): the page is as before');
        const r = await ev(`(async () => {
            const added = [], removed = [], realAdd = document.addEventListener.bind(document), realRemove = document.removeEventListener.bind(document);
            document.addEventListener = (t, fn, o) => { added.push([t, fn]); return realAdd(t, fn, o); };
            document.removeEventListener = (t, fn, o) => { removed.push([t, fn]); return realRemove(t, fn, o); };
            const calls = [];
            const bar = window.__LB.buildListBar(${spec('{}')}, f => calls.push(f));
            document.body.append(bar.node);
            const q = bar.node.querySelector('.list-bar-search'); q.value = 'abc'; q.dispatchEvent(new Event('input', { bubbles: true })); // a search waiting its 300 ms
            bar.dispose();
            await new Promise(r => setTimeout(r, 450)); // sleep-ok: the search would be sent 300 ms after the last key; there is no event for it
            const clicks = added.filter(([t]) => t === 'click'), gone = removed.filter(([t]) => t === 'click');
            document.addEventListener = realAdd; document.removeEventListener = realRemove;
            return { clicksAdded: clicks.length, clicksRemoved: gone.length, same: clicks.every(([, fn]) => gone.some(([, g]) => g === fn)), connected: bar.node.isConnected, calls: calls.length };
        })()`);
        assert.equal(r.clicksAdded >= 1, true, 'the bar listens for clicks outside its menu');
        assert.equal(r.same, true, 'every click listener it added is removed again');
        assert.equal(r.connected, false, 'its node is gone from the page');
        assert.equal(r.calls, 0, 'a search that was waiting is never sent');
        // A second dispose, and a closed menu after one, change nothing and throw nothing.
        await ev(`(() => { const bar = window.__LB.buildListBar(${spec('{}')}, () => {}); document.body.append(bar.node); bar.dispose(); bar.dispose(); return true; })()`);
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
