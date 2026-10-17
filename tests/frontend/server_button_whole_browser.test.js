// The header's server button always shows its dot, count and Update marker whole (operator, #4062): at the sidebar's default width it was
// cut to "dot, clipped count" beside the Update marker because the search box kept 7em and the button shrank. Now the button never shrinks
// (min-width: max-content), the search box gives up width down to 6em, and on a very narrow sidebar the header wraps instead of clipping the
// button. Checked on desktop (default width 320px, and narrowed) and on a phone (390 and 340px). Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const NOW = new Date().toISOString();
const SERVERS = [
    { id: 'm1', label: 'studio-mac.local', status: 'online', version: '1.4.6', last_seen: NOW },
    { id: 'm2', label: 'workstation.lan', status: 'online', version: '1.4.6', last_seen: NOW, update: 'required', latest_coral: '1.5.0', coral_version: '1.3.9' },
    { id: 'm3', label: 'Old Laptop', status: 'unreachable', version: '1.3.9', last_seen: '2026-10-01T00:00:00Z' },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a layout settles over frames; there is no completion event
    const SW = `[...document.querySelectorAll('details[data-server-switcher]')].find(x => x.getClientRects().length)`;
    // Whole: no child of the button runs past its right edge, no text in it is cut, and the button is not narrower than its content.
    const whole = () => ev(`(() => { const sw = ${SW}; const btn = sw.querySelector('summary'); const b = btn.getBoundingClientRect();
        const bad = [];
        for (const c of btn.querySelectorAll('*')) { const r = c.getBoundingClientRect(); if (r.width && r.right > b.right + 0.5) bad.push(c.className + ' runs past'); if (c.scrollWidth > c.clientWidth + 1 && getComputedStyle(c).display !== 'inline' && !c.classList.contains('server-switcher-label')) bad.push(c.className + ' cut ' + c.scrollWidth + '>' + c.clientWidth); }
        const header = btn.closest('.sidebar-header, .mobile-list-header'); const hr = header.getBoundingClientRect();
        const count = btn.querySelector('.server-switcher-count'); const dot = btn.querySelector('.server-dot'); const marker = btn.querySelector('.server-update-marker'); const caret = btn.querySelector('.server-switcher-caret');
        const whole = [dot, count, marker, caret].every(e => e && e.getBoundingClientRect().width > 0 && e.getBoundingClientRect().right <= b.right + 0.5 && e.getBoundingClientRect().left >= b.left - 0.5);
        const searchEl = document.querySelector('.sidebar-header .sidebar-search-input'); const searchOk = !searchEl || !searchEl.getClientRects().length || searchEl.scrollWidth <= searchEl.clientWidth + 1; const search = searchEl && searchEl.getClientRects().length ? Math.round(searchEl.getBoundingClientRect().width) : null;
        return { hasWord: !!btn.querySelector('.server-switcher-label'), title: btn.getAttribute('title'), ariaOk: /3 servers/.test(btn.getAttribute('aria-label') || ''), searchOk, search, whole, countText: count && count.textContent, bad, btnScroll: [btn.scrollWidth, btn.clientWidth], inside: b.left >= hr.left - 0.5 && b.right <= hr.right + 0.5, marker: !!btn.querySelector('.server-update-marker'), w: Math.round(b.width), text: btn.innerText.replace(/\\s+/g, ' ').trim() }; })()`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [label, w, h, mobile, sidebarW] of [['desktop, default sidebar', 1280, 800, false, null], ['desktop, sidebar 240px', 1280, 800, false, 240], ['phone 390px', 390, 844, true, null], ['phone 340px', 340, 800, true, null]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`T.state.hub = true; T.state.cloud = true; T.state.servers = ${JSON.stringify(SERVERS)}; T.state.liveSessions = []; window.__m.agents = {};
                ${JSON.stringify(SERVERS.map(s => s.id))}.forEach(id => window.__m.add('a-' + id, 'S', 3, { sleeping: false, server: id })); T.render.renderLiveSessions(T.state.liveSessions); true`);
            await waitFor(`!!(${SW})`, 'the server button', 8000);
            if (sidebarW) await ev(`document.querySelector('.sidebar').style.width = '${sidebarW}px'; true`);
            await sleep(500);
            const m = await whole();
            assert.deepEqual(m.bad, [], `${label}: nothing in the button is cut or runs past it: ${JSON.stringify(m)}`);
            assert.ok(m.btnScroll[0] <= m.btnScroll[1] + 1, `${label}: the button's content fits it: ${JSON.stringify(m)}`);
            assert.ok(m.inside, `${label}: the button is inside the header: ${JSON.stringify(m)}`);
            assert.ok(m.marker, `${label}: the Update marker is there`);
            assert.ok(!m.hasWord, `${label}: no word after the count: ${JSON.stringify(m)}`);
            assert.equal(m.title, '3 servers, 1 not online', `${label}: 'N servers' is the button's title`);
            assert.ok(m.ariaOk, `${label}: and in its accessible name`);
            assert.ok(m.searchOk, `${label}: the search box keeps its placeholder whole (${m.search})`);
            assert.ok(m.whole && m.countText === '3', `${label}: the dot, the count (3), the Update marker and the caret are whole inside the button: ${JSON.stringify(m)}`);
            console.log(`   ✓ ${label}: the server button is whole (${m.w}px: ${m.text})`);
        }
        console.log('\nPASS the server button is never clipped');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
