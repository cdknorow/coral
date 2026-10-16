// #3832: the History tab shows an agent's events with the same icons, titles and filters as the live Activity view. It had an
// older copy of them (no auto-accept icon, title or filter), so a finished agent's auto-accept events showed as plain pulses and could
// not be filtered. Both views now read one module (event_icons.js).
// The history events route is answered in the page. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const EVENTS = [
    { id: 1, event_type: 'tool_use', tool_name: 'Read', summary: 'Read main.go', created_at: '2026-10-16T08:00:01Z' },
    { id: 2, event_type: 'auto_accept', summary: 'Approved: go test ./...', created_at: '2026-10-16T08:00:02Z' },
    { id: 3, event_type: 'auto_accept_paused', summary: 'Paused: the agent is stuck', created_at: '2026-10-16T08:00:03Z' },
    { id: 4, event_type: 'goal', summary: 'Ship the fix', created_at: '2026-10-16T08:00:04Z' },
];
const STUB = `(() => {
    const inner = window.fetch;
    window.fetch = (url, opts) => {
        const u = new URL(String(url && url.url || url), location.origin);
        if (/\\/api\\/sessions\\/history\\/qa-hist\\/events$/.test(u.pathname)) return Promise.resolve(new Response(JSON.stringify(${JSON.stringify(EVENTS)}), { status: 200, headers: { 'content-type': 'application/json' } }));
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const MOD = `(window.__coralStatic || '/static/')`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`import(${MOD} + 'history_tabs.js').then(m => m.loadHistoryEvents('qa-hist')).then(() => true)`);
        await waitFor(`document.querySelectorAll('#history-events-list .event-item').length === 4`, 'the four history events', 5000);

        console.log('1. Auto-accept events show their own icon, title and class');
        const rows = await ev(`[...document.querySelectorAll('#history-events-list .event-item')].map(r => ({ icon: r.querySelector('.event-icon').className.replace('event-icon ', ''), tip: r.getAttribute('data-tooltip') }))`);
        assert.deepEqual(rows.map(r => r.icon), ['tool-read', 'tool-auto-accept', 'tool-auto-accept-paused', 'tool-goal'], 'each event has its own icon class (not the generic pulse)');
        assert.match(rows[1].tip, /^Auto-approved: Approved: go test/, 'the title names it');
        assert.match(rows[2].tip, /^Auto-accept paused: Paused/, 'and the paused one');
        console.log('   ✓ tool-auto-accept / tool-auto-accept-paused with their titles');

        console.log('2. The filter list has an Auto-accept group that counts and hides them');
        await ev(`document.querySelector('#history-event-filters .filter-dropdown-btn').click(); true`);
        await waitFor(`!!document.querySelector('#filter-dropdown .filter-dropdown-item')`, 'the filter list', 4000);
        const labels = await ev(`[...document.querySelectorAll('#filter-dropdown .filter-dropdown-item .filter-item-label')].map(n => n.textContent.trim())`);
        assert.ok(labels.includes('Auto-accept'), `the list has an Auto-accept group (${JSON.stringify(labels)})`);
        await ev(`document.querySelector('#filter-dropdown .filter-dropdown-item[data-key=autoaccept]').click(); true`);
        await waitFor(`document.querySelectorAll('#history-events-list .event-item').length === 2`, 'the auto-accept events to be hidden', 4000);
        assert.deepEqual(await ev(`[...document.querySelectorAll('#history-events-list .event-icon')].map(r => r.className.replace('event-icon ', ''))`), ['tool-read', 'tool-goal'], 'only the other events remain');
        // The list redraws with each group's count: both auto-accept events are counted.
        await waitFor(`!!document.querySelector('#filter-dropdown .filter-dropdown-item[data-key=autoaccept] .filter-item-count')`, 'the counts', 4000);
        assert.equal(await ev(`document.querySelector('#filter-dropdown .filter-dropdown-item[data-key=autoaccept] .filter-item-count').textContent.trim()`), '2', 'the Auto-accept group counts both events');
        console.log('   ✓ an Auto-accept group; unticking it hides both, and it counts 2');

        console.log('\nAll history event icon checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
