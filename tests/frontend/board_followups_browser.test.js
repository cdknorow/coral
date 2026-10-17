// Board follow-ups from UX's #4492 review (#4502): a long unbroken word or sender name wraps on a phone at 390 px and 200% text instead of clipping; and while
// you read higher up a small "N new messages, jump to latest" pill shows at the bottom edge when posts arrive: keyboard reachable, announced
// politely, gone at the bottom. The board's messages are served by the test (CDP Fetch) from a model, as board_window_browser does.
'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const B = 'followups-4502';
const AGENTS = ['Lead Developer', 'QA Engineer', 'Orchestrator', 'Backend Dev'];
const LONG = 'x'.repeat(10) + 'LONGUNBROKENTOKEN'.repeat(9) + 'END';
const KINDS = [`bold **${'B'.repeat(80)}END1** after`, `code \`${'C'.repeat(80)}END2\` after`, `link https://example.com/${'p'.repeat(80)}END3 after`, `path /usr/local/${'q'.repeat(80)}END4/file`, `a fenced block\n\n\`\`\`\n${'F'.repeat(120)}END5\n\`\`\``];
const model = [];
let nextId = 1;
function add(n, { content, who: forced } = {}) {
    for (let i = 0; i < n; i++) {
        const id = nextId++;
        const who = forced || AGENTS[Math.floor(id / 3) % AGENTS.length];
        model.push({ id, subscriber_id: who.toLowerCase().replace(/ /g, '-'), job_title: who, content: content || `message ${id} of the model`, created_at: new Date(Date.UTC(2026, 9, 17, 8, 0, 0) + id * 1000).toISOString() });
    }
}
function page(q) {
    const limit = Number(q.get('limit')) || 50;
    let rows;
    if (q.has('after_id')) rows = model.filter(m => m.id > Number(q.get('after_id'))).slice(0, limit);
    else if (q.has('before_id')) rows = model.filter(m => m.id < Number(q.get('before_id'))).slice(-limit);
    else rows = model.slice(-limit);
    const before = rows.length ? model.filter(m => m.id < rows[0].id).length : 0;
    return { messages: rows, total: model.length, before_count: before };
}

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Fetch, Input } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 600));
        return r.result.value;
    };
    const { waitFor, waitStable, coralReady } = require('./helpers/wait').bind(ev);
    const { framesJs } = require('./helpers/wait');
    const C = `document.getElementById('mb-messages')`;
    const shot = async (name) => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };
    const pollUntil = async (expr, what) => {
        for (let i = 0; i < 40; i++) {
            await ev(`window.__coral.pollBoardNow()`);
            if (await ev(expr)) return;
            await ev(`new Promise(r => setTimeout(() => r(true), 50))`); // sleep-ok: between poll attempts (the app's own poll may be in flight)
        }
        throw new Error(`poll: ${what}`);
    };
    const key = async (k, code, vk) => { for (const type of ['keyDown', 'keyUp']) await Input.dispatchKeyEvent({ type, key: k, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk, ...(type === 'keyDown' && k === 'Enter' ? { text: '\r' } : {}) }); };

    try {
        add(400);
        add(1, { content: LONG });
        for (const k of KINDS) add(1, { content: k });
        add(1, { content: 'left aligned ' + 'L'.repeat(90) + 'END7', who: 'Orchestrator' });
        add(1, { content: 'by an unusual sender', who: 'A Sender With A Rather Long Unbroken-Name' + 'N'.repeat(60) });
        { const id = nextId++; model.push({ id, subscriber_id: 'Coral Task Queue', is_system: true, job_title: 'Coral Task Queue', content: `[Task #9 claimed by QA Engineer] ${'T'.repeat(90)}END6`, created_at: new Date(Date.UTC(2026, 9, 17, 8, 0, 0) + id * 1000).toISOString() }); }
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Fetch.enable({ patterns: [{ urlPattern: `*/api/board/${B}/messages/all*` }] });
        Fetch.requestPaused(({ requestId, request }) => {
            const body = Buffer.from(JSON.stringify(page(new URL(request.url).searchParams))).toString('base64');
            Fetch.fulfillRequest({ requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body }).catch(() => {});
        });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: `${BASE}/board/${B}` });
        await loaded;
        await coralReady();
        await waitFor(`!!${C}.querySelector('.mb-load-earlier button')`, 'the Load earlier button');
        await waitFor(`(() => { const c = ${C}; return c.scrollHeight > c.clientHeight * 2 && c.scrollHeight - c.scrollTop - c.clientHeight < 50; })()`, 'a scrolled board, at the bottom');

        await waitStable(`${C}.children.length + ':' + ${C}.scrollHeight`, 'the board to stop re-rendering after its start', 500);
        console.log('1. A long unbroken word wraps on a phone at 390 px and 200% text');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await ev(`${C}.scrollTop = ${C}.scrollHeight; ${framesJs(2)}`);
        // At 100% text the board fits the phone: no row, name or word pushes it past the viewport.
        const fit = await ev(`(() => { const vw = document.documentElement.clientWidth; return { vw, board: Math.round(document.getElementById('mb-board').getBoundingClientRect().right), messages: Math.round(${C}.getBoundingClientRect().right), page: document.documentElement.scrollWidth, cOverflow: ${C}.scrollWidth - ${C}.clientWidth }; })()`);
        assert.ok(fit.board <= fit.vw && fit.messages <= fit.vw && fit.page <= fit.vw && fit.cOverflow <= 1, 'the board fits a 390 px phone at 100% text: ' + JSON.stringify(fit));
        await shot('board-long-word-phone-100');
        await ev(`document.documentElement.style.fontSize = '32px'; true`);
        await ev(`${C}.scrollTop = ${C}.scrollHeight; ${framesJs(2)}`);
        const w = await ev(`(() => { const c = ${C}; const row = [...c.querySelectorAll('.mb-message')].find(r => r.textContent.includes('END'));
            const body = row.querySelector('.mb-message-body'); const cr = c.getBoundingClientRect(), br = body.getBoundingClientRect();
            return { rowLeft: Math.round(row.getBoundingClientRect().left), rowRight: Math.round(row.getBoundingClientRect().right), bodyLeft: Math.round(br.left), bodyRight: Math.round(br.right), cLeft: Math.round(cr.left), cRight: Math.round(cr.right),
                cOverflow: c.scrollWidth - c.clientWidth, bodyOverflow: body.scrollWidth - body.clientWidth, rowOverflow: row.scrollWidth - row.clientWidth, wrap: getComputedStyle(body).overflowWrap }; })()`);
        console.log('   ', JSON.stringify(w));
        await shot('board-long-word-phone-200');
        assert.ok(w.bodyRight <= w.cRight + 1 && w.bodyLeft >= w.cLeft - 1, 'the message body is inside the board: ' + JSON.stringify(w));
        assert.ok(w.rowRight <= w.cRight + 1, 'the row is inside the board: ' + JSON.stringify(w));
        assert.ok(w.cOverflow <= 1 && w.bodyOverflow <= 1 && w.rowOverflow <= 1, 'nothing scrolls sideways or is clipped: ' + JSON.stringify(w));
        // the same for a bold run, inline code, a link, a path, and a fenced block (which keeps its own horizontal scroll, never the page's)
        const kinds = await ev(`(() => { const c = ${C}; const cr = c.getBoundingClientRect(); return ['END1', 'END2', 'END3', 'END4', 'END5', 'END7'].map(t => { const row = [...c.querySelectorAll('.mb-message')].find(r => r.textContent.includes(t));
            const body = row.querySelector('.mb-message-body'); const br = body.getBoundingClientRect(); const pre = body.querySelector('pre');
            return { t, bodyRight: Math.round(br.right), cRight: Math.round(cr.right), bodyOverflow: body.scrollWidth - body.clientWidth, preScrolls: !!pre && pre.scrollWidth > pre.clientWidth, preInside: !pre || pre.getBoundingClientRect().right <= cr.right + 1 }; }); })()`);
        const q = await ev(`(() => { const c = ${C}; const cr = c.getBoundingClientRect(); const row = [...c.querySelectorAll('.mb-queue-run')].pop(); if (!row) return null; const rr = row.getBoundingClientRect();
            return { right: Math.round(rr.right), cRight: Math.round(cr.right), overflow: row.scrollWidth - row.clientWidth, line: (row.querySelector('.mb-queue-line') || {}).scrollWidth - (row.querySelector('.mb-queue-line') || {}).clientWidth }; })()`);
        console.log('    queue', JSON.stringify(q));
        console.log('   ', JSON.stringify(kinds));
        for (const k of kinds) assert.ok(k.bodyRight <= k.cRight + 1 && k.bodyOverflow <= 1 && k.preInside, 'no clipping for ' + JSON.stringify(k));
        console.log('   ✓');
        await ev(`document.documentElement.style.fontSize = ''; true`);
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });

        const pill = () => ev(`(() => { const r = document.querySelector('.mb-new-pill'); const b = r && r.querySelector('button'); const live = document.querySelector('.mb-new-pill-status, .mb-new-pill[aria-live]');
            const cr = ${C}.getBoundingClientRect(), br = b ? b.getBoundingClientRect() : null;
            return { shown: !!b && getComputedStyle(b).display !== 'none' && br.width > 0, text: b ? b.textContent.trim().replace(/\\s+/g, ' ') : '', live: live ? (live.getAttribute('aria-live') || (r.getAttribute('aria-live'))) : '', liveText: live ? live.textContent.trim() : '',
                insideBoard: !!br && br.bottom <= cr.bottom + 1 && br.bottom > cr.bottom - 80 && br.left >= cr.left && br.right <= cr.right, tab: !!b && b.tabIndex >= 0 }; })()`);
        console.log('2. Reading higher up: a "N new messages, jump to latest" pill; at the bottom there is none');
        await ev(`${C}.scrollTop = ${C}.scrollHeight; ${framesJs(2)}`);
        add(1);
        await pollUntil(`${C}.querySelector('[data-message-id="${nextId - 1}"]')`, 'a message at the bottom');
        assert.equal((await pill()).shown, false, 'no pill while pinned at the bottom');
        assert.equal(await ev(`getComputedStyle(document.querySelector('.mb-new-pill')).display !== 'none' && !document.querySelector('.mb-new-pill').hidden`), true, 'the live region is displayed even with no pill');
        await ev(`${C}.scrollTop = Math.round(${C}.scrollHeight / 2); ${framesJs(2)}`);
        add(3);
        await pollUntil(`${C}.querySelector('[data-message-id="${nextId - 1}"]')`, 'three new messages');
        let p = await pill();
        console.log('   ', JSON.stringify(p));
        await shot('board-new-pill');
        assert.ok(p.shown, 'the pill is shown while reading higher up');
        assert.equal(p.text, '3 new messages, jump to latest');
        assert.equal(p.live, 'polite', 'announced politely'); assert.match(p.liveText, /3 new messages/);
        // The live region itself stays displayed (only its text changes), and white text on the pill has the contrast of a button (4.5:1).
        const region = await ev(`(() => { const r = document.querySelector('.mb-new-pill'); const b = r.querySelector('button'); const lum = c => { const [R, G, B] = c.match(/[\\d.]+/g).slice(0, 3).map(Number).map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }); return 0.2126 * R + 0.7152 * G + 0.0722 * B; };
            const cs = getComputedStyle(b); const l1 = lum(cs.color), l2 = lum(cs.backgroundColor); return { regionDisplay: getComputedStyle(r).display, hiddenAttr: r.hidden, ratio: (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05) }; })()`);
        assert.ok(region.regionDisplay !== 'none' && !region.hiddenAttr, 'the live region stays displayed: ' + JSON.stringify(region));
        assert.ok(region.ratio >= 4.5, 'the pill text has 4.5:1 contrast: ' + region.ratio.toFixed(2));
        assert.ok(p.insideBoard, 'at the bottom edge of the board: ' + JSON.stringify(p)); assert.ok(p.tab, 'keyboard reachable');
        add(1);
        await pollUntil(`${C}.querySelector('[data-message-id="${nextId - 1}"]')`, 'one more');
        assert.equal((await pill()).text, '4 new messages, jump to latest', 'the count grows');
        // the keyboard: Tab reaches it, Enter jumps to the latest and the pill goes
        await ev(`document.querySelector('.mb-new-pill button').focus(); true`);
        await key('Enter', 'Enter', 13);
        await waitFor(`${C}.scrollHeight - ${C}.scrollTop - ${C}.clientHeight < 50`, 'the board at the bottom');
        p = await pill();
        assert.equal(p.shown, false, 'gone at the bottom');
        // the singular, and gone by scrolling to the bottom by hand
        await ev(`${C}.scrollTop = Math.round(${C}.scrollHeight / 2); ${framesJs(2)}`);
        add(1);
        await pollUntil(`${C}.querySelector('[data-message-id="${nextId - 1}"]')`, 'a single message');
        assert.equal((await pill()).text, '1 new message, jump to latest');
        await ev(`${C}.scrollTop = ${C}.scrollHeight; ${framesJs(2)}`);
        await waitFor(`document.querySelector('.mb-new-pill button').getBoundingClientRect().width === 0`, 'the pill to go when scrolled to the bottom');
        console.log('   ✓');

        console.log('board_followups: ok');
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        try { await client.close(); } catch { /* closing */ }
        process.exit(1);
    }
})();
