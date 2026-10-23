// The saved messages' buttons for a person using a screen reader (#6541): every saved item has "Read the whole message", "Done" and "Remove", and four items read as the same three names four times,
// so a person moving through them by button cannot tell which message a press would act on. Each button now says which message it is for (the source and the start of its text), and the name still
// contains the words on the button (a person speaking "Done" to a voice control is understood). Desktop and a 390 px phone. Drives Chrome over CDP through agent_view_harness.js with the Inbox's
// stand-in routes (helpers/inbox_world.js).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { INBOX_WORLD } = require('./helpers/inbox_world');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, tag] of [[1280, 800, false, 'desktop'], [390, 844, true, 'phone']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(INBOX_WORLD);
            await ev(`localStorage.setItem('coral.inbox.tab', 'saved'); window.switchNavTab('hub-inbox'); true`);
            await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 4`, `${tag}: four saved items`, 10000);
            const names = () => ev(`[...document.querySelectorAll('#saved-list .saved-item')].map(li => [...li.querySelectorAll('button')].map(b => ({ name: (b.getAttribute('aria-label') || b.textContent).replace(/\\s+/g, ' ').trim(), visible: [...b.childNodes].filter(n => !(n.classList && n.classList.contains('material-icons'))).map(n => n.textContent).join('').replace(/\\s+/g, ' ').trim() })))`);
            const check = (rows, when) => {
                const all = rows.flat().map(b => b.name);
                assert.deepEqual([...new Set(all.filter((n, i) => all.indexOf(n) !== i))], [], `${tag}, ${when}: no two buttons in the list share a name`);
                for (const b of rows.flat()) assert.ok(b.name.toLowerCase().includes(b.visible.toLowerCase()), `${tag}, ${when}: "${b.name}" contains its visible words "${b.visible}"`);
            };
            check(await names(), 'closed');
            // a message opened: the toggle's words change ("Show less") and its name follows
            await ev(`document.querySelector('#saved-list .ibx-saved-toggle').click(); true`);
            await waitFor(`document.querySelector('#saved-list .ibx-saved-toggle').getAttribute('aria-expanded') === 'true'`, `${tag}: the message opens`, 5000);
            check(await names(), 'one message open');
            // two items from one source that start alike are named apart, and a cut at 40 characters never leaves half an emoji
            // fixed times, so the list's own order (newest saved first) does not depend on the clock
            await ev(`(() => { const real = window.fetch; const mk = (id, snippet) => ({ id, kind: 'agent', ref: 'sess-same', key: String(id).padStart(20, '0'), title: 'Same Agent', snippet, agent_type: 'claude', saved_at: '2026-10-23T10:0' + (9 - (id - 20)) + ':00Z', said_at: '2026-10-23T09:00:00Z' });
                const items = [mk(21, 'The same first words'), mk(22, 'The same first words'), mk(23, 'x'.repeat(39) + '\u{1F600} and then more words after it')];
                window.fetch = (u, o = {}) => new URL(String(u), location.origin).pathname === '/api/saved' && (o.method || 'GET').toUpperCase() === 'GET' ? Promise.resolve(new Response(JSON.stringify({ items, open: 3, done: 0 }), { status: 200, headers: { 'content-type': 'application/json' } })) : real(u, o); return true; })()`);
            await ev(`document.getElementById('inbox-tab-needs').click(); true`); await ev(`document.getElementById('inbox-tab-saved').click(); true`); // the list is read again when the tab is shown
            await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 3`, `${tag}: the three items`, 10000);
            check(await names(), 'two that start alike');
            const lone = await ev(`[...document.querySelectorAll('#saved-list .saved-remove')].map(b => b.getAttribute('aria-label'))`);
            // told apart by what the names say, not by where the list put them
            const pair = lone.filter(n => /The same first words/.test(n)), single = lone.filter(n => !/The same first words/.test(n));
            assert.deepEqual(pair.map(n => n.slice(-8)).sort(), ['(1 of 2)', '(2 of 2)'], `${tag}: the pair is told apart (${JSON.stringify(lone)})`);
            assert.equal(single.length, 1, `${tag}: and the other item stands alone (${JSON.stringify(lone)})`);
            assert.ok(!/\(\d of/.test(single[0]), `${tag}: an item that starts alone has no number (${single[0]})`);
            assert.ok(!/[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/.test(single[0]) && single[0].includes('\u{1F600}'), `${tag}: the cut keeps the whole emoji (${JSON.stringify(single[0])})`);
            console.log(`   ✓ ${tag}`);
        }
        console.log('PASS Saved messages: each button names the message it acts on and keeps its visible words');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
