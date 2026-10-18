// Agent list at scale (#4624, UX #4612 items 1 and 3): (1) a long agent name keeps the end that tells agents of one team apart: its head shrinks with an ellipsis, its last 7 characters do
// not, on the desktop sidebar; the full name stays in the text, the hover title and the row's accessible name; on the phone the name wraps, so its end is there. (3) The phone's Agents
// page has a Search agents button (a 44 px box under the list header's controls, the same search): typing filters the rows, Escape clears it, and a widened screen's sidebar box takes the text over.
// Drives Chrome over CDP through agent_view_harness.js against an isolated dev server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: layout settles over frames after a viewport change; there is no completion event
    const NAMES = ['release-engineering-coordinator-4-9', 'release-engineering-coordinator-4-7', 'backend-platform-reliability-engineer-4-1', 'backend-platform-reliability-engineer-4-2', 'qa', 'frontend-accessibility-reviewer-4-3'];
    const open = async (w, h, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
        await Emulation.setTouchEmulationEnabled({ enabled: mobile });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => { const st = window.__coralStatic || '/static/'; const ws = await import(st + 'websocket.js'); const render = await import(st + 'render.js'); const { state } = await import(st + 'state.js');
            if (state.coralWs) { state.coralWs.onclose = null; state.coralWs.onmessage = null; try { state.coralWs.close(); } catch {} }
            ws.handleCoralMessage({ type: 'coral_update', servers: [{ id: 'local', label: 'here', status: 'online' }], sessions: ${JSON.stringify(NAMES)}.map((n, i) => ({ name: 'p-' + i, session_id: 'p-' + i, agent_type: 'claude', server: 'local', status: 'idle', summary: 'Task ' + i, display_name: n, working_directory: '/repo/a', branch: 'main', board_project: 'team-a' })) });
            render.flushLiveSessionsRender(); return true; })()`);
        await waitFor(`document.querySelectorAll('.session-group-item[data-session-id]').length >= ${NAMES.length}`, 'the rows', 8000);
        await sleep(400); // sleep-ok: layout and the filter redraw settle over frames; there is no completion event
    };
    const ROWS = `[...document.querySelectorAll('.session-group-item[data-session-id]')].filter(r => r.getClientRects().length)`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });

        console.log('1. Desktop: the end of a long name stays visible');
        await open(1280, 800, false);
        const rows = await ev(`${ROWS}.map(r => { const l = r.querySelector('.session-label'), name = r.querySelector('.session-label-name'), tail = r.querySelector('.sln-tail'), head = r.querySelector('.sln-head'); const lr = l.getBoundingClientRect(), tr = tail ? tail.getBoundingClientRect() : null;
            return { name: name.textContent, title: l.title, aria: r.getAttribute('aria-label'), split: l.classList.contains('session-label-split'), tail: tail ? tail.textContent : null, tailVisible: tr ? tr.width > 0 && tr.right <= lr.right + 1 && tr.left >= lr.left - 1 : null, headCut: head ? head.scrollWidth > head.clientWidth : null }; })`);
        assert.equal(rows.length, NAMES.length);
        for (const r of rows) {
            assert.ok(NAMES.includes(r.name), 'the text is the whole name: ' + r.name);
            assert.equal(r.title, r.name, 'the hover shows the full name'); assert.ok(r.aria.startsWith(r.name + ','), 'the accessible name carries the full name: ' + r.aria);
            if (r.name.length > 16) assert.ok(r.split && r.tail === r.name.slice(-7) && r.tailVisible, 'its last 7 characters are in view: ' + JSON.stringify(r));
            else assert.ok(!r.split, 'a short name is left as it is');
        }
        assert.ok(rows.some(r => r.headCut), 'the sidebar is narrow enough that a head is cut with an ellipsis (the case under test): ' + JSON.stringify(rows.map(r => r.headCut)));
        const tails = rows.filter(r => r.split).map(r => r.tail); assert.equal(new Set(tails).size, tails.length, 'agents of one team with a shared prefix show different ends: ' + tails.join(','));

        await shot('long-names-desktop');
        console.log('2. Phone: the name wraps, so its end is there; the Agents page has a Search agents box');
        await open(390, 844, true);
        const ph = await ev(`${ROWS}.map(r => { const l = r.querySelector('.session-label'), t = r.querySelector('.sln-tail'); const lr = l.getBoundingClientRect(), tr = t ? t.getBoundingClientRect() : null; return { display: getComputedStyle(l).display, tailIn: tr ? tr.width > 0 && tr.right <= lr.right + 1 : null, sideways: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })`);
        assert.ok(ph.length === NAMES.length && ph.every(p => p.sideways <= 0 && (p.tailIn === null || p.tailIn)), JSON.stringify(ph));
        const box = await ev(`(() => { const i = document.querySelector('#mobile-search-row input'); const r = i && i.getBoundingClientRect(); return i ? { h: Math.round(r.height), w: Math.round(r.width), label: i.getAttribute('aria-label'), type: i.type, inside: r.left >= 0 && r.right <= innerWidth, shown: r.width > 0 } : null; })()`);
        assert.ok(box && box.shown && box.h >= 44 && box.label === 'Search agents' && box.type === 'search' && box.inside, 'the Agents page has a visible Search agents box that is a 44 px target: ' + JSON.stringify(box));

        console.log('3. The phone search filters the list, Escape clears it');
        await ev(`document.querySelector('#mobile-search-row input').focus(); true`);
        const typed = async v => { await ev(`(() => { const i = document.querySelector('#mobile-search-row input'); i.value = ${JSON.stringify(v)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`); await sleep(300); }; // sleep-ok: layout and the filter redraw settle over frames; there is no completion event
        await shot('phone-search-open');
        await typed('4-9');
        assert.equal(await ev(`${ROWS}.length`), 1, 'one agent matches 4-9'); assert.equal(await ev(`${ROWS}[0].querySelector('.session-label-name').textContent`), 'release-engineering-coordinator-4-9');
        await typed('release');
        assert.equal(await ev(`${ROWS}.length`), 2, 'two agents match release');
        await ev(`document.querySelector('#mobile-search-row input').dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`); await sleep(300); // sleep-ok: layout and the filter redraw settle over frames; there is no completion event
        assert.equal(await ev(`document.querySelector('#mobile-search-row input').value`), '', 'Escape clears the box'); assert.equal(await ev(`${ROWS}.length`), NAMES.length, 'and the filter');
        await typed('qa'); assert.equal(await ev(`${ROWS}.length`), 1);
        // On a wide screen the sidebar's own box takes the text over.
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await ev(`window.dispatchEvent(new Event('resize')); true`); await sleep(600); // sleep-ok: layout and the filter redraw settle over frames; there is no completion event
        assert.equal(await ev(`document.getElementById('agent-search-input').value`), 'qa', 'the phone\'s search is in the sidebar\'s box when the screen widens');
        console.log('\nPASS long names and the phone search');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
