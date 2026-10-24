// Hovering the agents of an unreachable server must not move the pinned "+ New Agent" button (#6340, coral-hub's bug): their rows are dimmed with `filter`, and a filter makes the row the containing block of
// its position:fixed hover tooltip, so the tooltip was laid out inside the row, grew the list's scroll height by 300 to 400 px, and the sticky footer jumped 16 px on every hover and back (and the tooltip
// opened far from the pointer). A team whose agents are all stale, at 1280 px, hovering the team card, each row and its parts: the button's top and the list's scroll height stay what they were, and the
// tooltip is a viewport-fixed box beside the row (not inside it). Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const { Input } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 700, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { for (let i = 1; i <= 3; i++) window.__m.add('rb' + i, 'R' + i, 3, { sleeping: false, agent_type: 'claude', session_id: 'rb-' + i, server: 'box-b', board_project: 'team-x' });
            const ws = await import((window.__coralStatic || '/static/') + 'websocket.js'); ws.handleCoralMessage({ type: 'coral_diff', changed: [], servers: [{ id: 'box-b', label: 'box-b.lan', status: 'unreachable' }] });
            T.state.liveSessions.forEach(s => { s.stale = true; }); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
        await waitFor(`document.querySelectorAll('#live-sessions-list .session-group-item.session-stale').length === 3`, 'three stale agents in one team', 8000);
        const pos = () => ev(`(() => { const b = document.querySelector('.sidebar-new-btn-full'); const l = document.getElementById('live-sessions-list'); return { top: Math.round(b.getBoundingClientRect().top * 10) / 10, scroll: l.scrollHeight, client: l.clientHeight }; })()`);
        const base = await pos();
        const spots = await ev(`[...document.querySelectorAll('#live-sessions-list .session-board-card, #live-sessions-list .session-group-item, #live-sessions-list .session-label-name')].filter(e => e.getClientRects().length).map(e => { const r = e.getBoundingClientRect(); return [e.className.split(' ')[0], Math.round(r.left + r.width / 2), Math.round(r.top + r.height / 2)]; })`);
        check('the team card and its three stale rows are on screen to hover', spots.length >= 4, String(spots.length));
        const moved = [], tips = [];
        for (const [name, x, y] of spots) {
            for (let i = 0; i < 3; i++) {
                await Input.dispatchMouseEvent({ type: 'mouseMoved', x, y }); await sleep(200); // sleep-ok: the tooltip's own show delay
                const p = await pos(); if (p.top !== base.top || p.scroll !== base.scroll) moved.push([name, x, y, p]);
                const t = await ev(`(() => { const t = [...document.querySelectorAll('.session-tooltip')].find(e => getComputedStyle(e).display !== 'none'); if (!t) return null; const r = t.getBoundingClientRect(); const li = t.closest('li'); const lr = li && li.getBoundingClientRect(); return { top: r.top, left: r.left, h: r.height, rowTop: lr ? lr.top : null, inLi: !!li }; })()`);
                if (t) tips.push(t);
                await Input.dispatchMouseEvent({ type: 'mouseMoved', x: 900, y: 300 }); await sleep(120); // sleep-ok: the pointer leaves
                const q = await pos(); if (q.top !== base.top || q.scroll !== base.scroll) moved.push([name, 'after leaving', q]);
            }
        }
        check('hovering the team card, the rows and their parts never moves the + New Agent button or grows the list', moved.length === 0, JSON.stringify({ base, moved: moved.slice(0, 3) }));
        check('and the tooltip shows beside the hovered row, not laid out inside it (its top is within the viewport near the row)', tips.length > 0 && tips.every(t => t.top <= t.rowTop + 120 && t.top + t.h >= t.rowTop - 120 && t.top >= 0), JSON.stringify(tips.slice(0, 2)));
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} stale hover checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
