// An open board reflows on a phone (#4505, WCAG 1.4.10): at 390 px with 100% and 200% text (the root font doubled, with a
// coarse pointer; and as the #4502 suite does it, at device scale 2 with a 32 px root), no message row, sender name or
// message text ends past the right edge of the screen, and nothing in the board view scrolls sideways. The board has more
// than a page of messages, so "Load earlier" is shown: at 200% that nowrap button was wider than the screen, and the feed
// (centred with auto margins, so not stretched) took its width; every row was laid out in that box and clipped. Also long
// sender names with spaces, ordinary messages, a long unbroken token, a link and a fenced block (which scrolls on its own).
// A long unbroken sender name is #4502's case (board_followups_browser). On a desktop the feed is as before: the column's
// width at 1400 px, and at most 840 px and centred at 1920. On a failure it prints the right edge of each box from the
// last row up to the body.
'use strict';

const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const B = 'reflow-4505';
const LONG = 'Senior Platform Reliability Developer With A Long Title';

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 600));
        return r.result.value;
    };
    const { waitFor, waitStable, coralReady } = require('./helpers/wait').bind(ev);
    const post = (as, content) => ev(`fetch('/api/board/${B}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ subscriber_id: ${JSON.stringify(as.toLowerCase().slice(0, 40))}, as: ${JSON.stringify(as)}, content: ${JSON.stringify(content)} }) }).then(r => r.status)`);
    // Each message row and what is in it, against the screen and the feed; and every box from a row up whose right edge passes the screen.
    const measure = `(() => {
        const vw = document.documentElement.clientWidth, feed = document.getElementById('mb-messages');
        const fr = feed.getBoundingClientRect();
        const rows = [...feed.querySelectorAll(':scope > .mb-message')];
        const over = [];
        for (const row of rows) for (const el of [row, ...row.querySelectorAll('.mb-agent-name, .mb-message-body, .mb-message-body *, .mb-message-time, .mb-delete-msg-btn')].filter(e => !e.closest('pre'))) {
            const r = el.getBoundingClientRect();
            if (r.width && r.right > vw + 0.5) over.push((el.className || el.tagName) + ' right ' + Math.round(r.right));
        }
        const chain = [];
        for (let el = rows[rows.length - 1]; el && el !== document.documentElement; el = el.parentElement) {
            const r = el.getBoundingClientRect(), cs = getComputedStyle(el);
            chain.push((el.id ? '#' + el.id : el.tagName.toLowerCase() + '.' + String(el.className).split(' ')[0]) + ' w' + Math.round(r.width) + ' right ' + Math.round(r.right)
                + ' ' + cs.display + (cs.minWidth !== 'auto' && cs.minWidth !== '0px' ? ' min-w ' + cs.minWidth : '') + (cs.overflowX !== 'visible' ? ' ox ' + cs.overflowX : ''));
        }
        const view = document.getElementById('messageboard-view');
        const sideways = [document.documentElement, document.body, view, ...view.querySelectorAll('*')].filter(e => e.scrollWidth > e.clientWidth + 1 && /auto|scroll/.test(getComputedStyle(e).overflowX) && e.clientWidth > 0 && !e.closest('pre, code, .mb-message-body table'))
            .map(e => (e.id || e.className || e.tagName) + ' ' + e.scrollWidth + '>' + e.clientWidth);
        const earlier = feed.querySelector('.mb-load-earlier button');
        if (earlier && earlier.getBoundingClientRect().right > fr.right + 0.5) over.push('Load earlier right ' + Math.round(earlier.getBoundingClientRect().right));
        const col = document.getElementById('mb-board').getBoundingClientRect();
        return { vw, rows: rows.length, feedRight: Math.round(fr.right), feedScroll: feed.scrollWidth - feed.clientWidth, feedWidth: Math.round(fr.width),
            feedMargins: [Math.round(fr.left - col.left), Math.round(col.right - fr.right)], colWidth: Math.round(col.width), hasEarlier: !!earlier,
            docOver: document.documentElement.scrollWidth - vw, over: over.slice(0, 8), nOver: over.length, sideways, chain,
            widest: Math.max(...rows.map(r => r.getBoundingClientRect().right)) - fr.right };
    })()`;

    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        let loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' }); await loaded; await coralReady();
        for (let i = 1; i <= 60; i++) await post('agent-' + (i % 5), 'filler message ' + i); // more than a page: "Load earlier" is shown
        await post(LONG, 'A message from a sender with a long job title.');
        for (let i = 1; i <= 6; i++) await post(i % 2 ? 'QA Engineer' : 'Orchestrator', `heartbeat #${i} from 5b3c9e7a: everything is fine, the build is green and the queue is empty`);
        await post('Backend Dev', 'a long unbroken token: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef and then more words after it');
        for (let i = 1; i <= 4; i++) await post('agent-' + i, 'heartbeat #12 from 5b3c9e7a-a75a-06bb-639c-a09c8921bc86 at 2026-10-17T22:00:00Z');
        await post('Lead Developer', 'a link https://example.com/a/very/long/path/that/goes/on/and/on and \x60inline code\x60\n\n\x60\x60\x60\nconst aVeryLongLineOfCode = someFunction(withArguments, andMore, andMoreStill, untilItIsWide, andWider);\n\x60\x60\x60');

        const views = [
            { name: 'phone 390, 100%', width: 390, height: 844, mobile: true, font: '' },
            { name: 'phone 390, 200%', width: 390, height: 844, mobile: true, font: '200%' },
            { name: 'phone 390, device scale 2, 32px root', width: 390, height: 844, mobile: true, font: '32px', dsf: 2, noTouch: true },
            { name: 'desktop 1400', width: 1400, height: 900, mobile: false, font: '' },
            { name: 'desktop 1920', width: 1920, height: 1080, mobile: false, font: '' },
        ];
        for (const v of views) {
            await Emulation.setDeviceMetricsOverride({ width: v.width, height: v.height, deviceScaleFactor: v.dsf || 1, mobile: v.mobile });
            const touch = v.mobile && !v.noTouch;
            await Emulation.setTouchEmulationEnabled({ enabled: touch, maxTouchPoints: touch ? 5 : 1 });
            await Emulation.setEmulatedMedia({ features: touch ? [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] : [{ name: 'pointer', value: 'fine' }, { name: 'hover', value: 'hover' }] });
            loaded = Page.loadEventFired();
            await Page.navigate({ url: `${BASE}/board/${B}` }); await loaded; await coralReady();
            await ev(`document.documentElement.style.fontSize = ${JSON.stringify(v.font)}; true`);
            await waitFor(`document.querySelectorAll('#mb-messages > .mb-message').length >= 40`, `${v.name}: the board's messages`);
            await waitStable(`document.getElementById('mb-messages').scrollHeight + ':' + document.documentElement.clientWidth`, `${v.name}: the layout`, 300);
            const m = await ev(measure);
            const why = `\n  over: ${JSON.stringify(m.over)}\n  sideways: ${JSON.stringify(m.sideways)}\n  from the last row up:\n    ${m.chain.join('\n    ')}`;
            assert.equal(m.nOver, 0, `${v.name}: nothing in a message row ends past the screen (${m.vw} px)` + why);
            assert.deepEqual(m.sideways, [], `${v.name}: nothing in the board view scrolls sideways` + why);
            assert.ok(m.docOver <= 0, `${v.name}: the page is not wider than the screen (${m.docOver})` + why);
            assert.ok(m.feedRight <= m.vw && m.widest <= 0.5, `${v.name}: every row fits the feed, and the feed the screen (feed right ${m.feedRight}, widest row ${m.widest} past it)` + why);
            assert.ok(m.hasEarlier, `${v.name}: the board shows "Load earlier"`);
            if (v.font === '200%') {
                // Whatever is wide and unbreakable in the feed (here a 600 px nowrap line), the feed keeps its column's width and the
                // rows still fit: the feed scrolls it instead of growing (the auto margins no longer size the feed by its content).
                const g = await ev(`(() => { const feed = document.getElementById('mb-messages'); const w = document.createElement('div');
                    w.id = 'qa-wide'; w.style.cssText = 'white-space:nowrap;width:600px'; w.textContent = 'x'.repeat(80); feed.prepend(w);
                    const fr = feed.getBoundingClientRect(), col = document.getElementById('mb-board').getBoundingClientRect();
                    const rowRight = Math.max(...[...feed.querySelectorAll(':scope > .mb-message')].map(r => r.getBoundingClientRect().right));
                    w.remove(); return { feed: Math.round(fr.width), col: Math.round(col.width), rowRight: Math.round(rowRight), vw: document.documentElement.clientWidth }; })()`);
                assert.ok(g.feed <= g.col && g.rowRight <= g.vw, `${v.name}: wide content does not widen the feed (${JSON.stringify(g)})`);
            }
            if (v.width === 1400) assert.equal(m.feedWidth, m.colWidth, `${v.name}: the feed fills its column as before`);
            if (v.width === 1920) {
                assert.equal(m.feedWidth, 840, `${v.name}: the feed stays at its 840 px maximum`);
                assert.ok(Math.abs(m.feedMargins[0] - m.feedMargins[1]) <= 1, `${v.name}: and centred (${m.feedMargins})`);
            }
            console.log(`${v.name}: ${m.rows} rows fit (feed right ${m.feedRight} of ${m.vw}, ${m.feedWidth} wide)`);
        }
        await ev(`document.documentElement.style.fontSize = ''; fetch('/api/board/${B}', { method: 'DELETE' }).then(r => r.status)`);
        console.log('board_reflow: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        try { await ev(`fetch('/api/board/${B}', { method: 'DELETE' }).then(r => r.status)`); } catch { /* best effort */ }
        try { await client.close(); } catch { /* closing */ }
        process.exit(1);
    }
})();
