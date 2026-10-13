// Phone side panel (#3096, after #3073): the top bar's height depends on the width (touch-sized controls
// wrap), so the side-panel overlay must start below the bar as measured, and the panel's tabs (Files,
// Browse, Artifacts, …) must not be covered. Checked with touch at 375, 393, 412 and iPad 768 (where the
// panel is not an overlay), and with a fine pointer at 390. Also: the Cloud Hub server list never shows
// a literal 'null' (replaceChildren turns a null child into that text).
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();

    const measure = () => ev(`(() => {
        const bar = document.querySelector('header.top-bar');
        const panel = document.getElementById('agentic-state');
        const barRect = bar.getBoundingClientRect(), pr = panel.getBoundingClientRect();
        const tabs = [...panel.querySelectorAll('.agentic-tab')].filter(t => t.getBoundingClientRect().width > 0 && getComputedStyle(t).display !== 'none');
        // A tab can be scrolled out of the horizontal tab strip; test the part of it that is on screen.
        const strip = panel.querySelector('.agentic-state-tabs') || panel;
        const visiblePart = t => {
            const r = t.getBoundingClientRect(), sr = strip.getBoundingClientRect();
            const left = Math.max(r.left, sr.left, 0), right = Math.min(r.right, sr.right, innerWidth);
            const top = Math.max(r.top, 0), bottom = Math.min(r.bottom, innerHeight);
            return right - left >= 8 && bottom - top >= 8 ? { x: (left + right) / 2, y: (top + bottom) / 2 } : null;
        };
        const covered = tabs.map(t => ({ t, p: visiblePart(t) })).filter(({ p }) => p).filter(({ t, p }) => {
            const hit = document.elementFromPoint(p.x, p.y);
            return !(hit && (hit === t || t.contains(hit)));
        }).map(({ t, p }) => { const h = document.elementFromPoint(p.x, p.y); return { id: t.id, hit: h ? (h.id || h.className || h.tagName) : null }; });
        return { overlay: panel.classList.contains('mobile-panel-overlay'), barBottom: Math.round(barRect.bottom), barH: Math.round(barRect.height), panelTop: Math.round(pr.top), tabs: tabs.length, covered };
    })()`);

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [width, touch] of [[375, true], [393, true], [412, true], [768, true], [390, false]]) {
            await Emulation.setDeviceMetricsOverride({ width, height: width >= 768 ? 1024 : 844, deviceScaleFactor: 2, mobile: true });
            await Emulation.setTouchEmulationEnabled({ enabled: touch, maxTouchPoints: touch ? 5 : 1 });
            await Emulation.setEmitTouchEventsForMouse({ enabled: touch, configuration: 'mobile' });
            await Page.navigate({ url: BASE });
            await Page.loadEventFired();
            await install();
            assert.equal(await ev(`matchMedia('(pointer: coarse)').matches`), touch, 'pointer emulated');
            await ev(`(async () => { localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('phone-agent', 'P', 3, { sleeping: false }); await window.selectLiveSession('phone-agent', 'claude', 'phone-agent-id', 'local'); return true; })()`);
            // Open the side panel the way a user does: the panel toggle in the top bar.
            await ev(`(async () => { const b = document.getElementById('panel-toggle-btn'); if (b && b.getBoundingClientRect().width > 0) b.click(); else (await import((window.__coralStatic || '/static/') + 'sidebar.js')).toggleAgenticPanel(true); return true; })()`);
            if (width <= 767) await waitFor(`document.getElementById('agentic-state').classList.contains('mobile-panel-overlay')`, 'the panel overlay', 3000);
            else await ev(`(async () => { const p = document.getElementById('agentic-state'); if (p.classList.contains('collapsed')) (await import((window.__coralStatic || '/static/') + 'sidebar.js')).toggleAgenticPanel(); return true; })()`);
            await sleep(300);
            const m = await measure();
            const label = `${width}px ${touch ? 'touch' : 'fine'}`;
            assert.ok(m.tabs >= 3, `${label}: panel tabs visible (${m.tabs})`);
            assert.ok(m.panelTop >= m.barBottom - 1, `${label}: the panel starts below the top bar (panel top ${m.panelTop}, bar bottom ${m.barBottom}, bar ${m.barH}px)`);
            assert.deepEqual(m.covered, [], `${label}: no panel tab is covered`);
            console.log(`   ✓ ${label}: bar ${m.barH}px, panel top ${m.panelTop} ≥ bar bottom ${m.barBottom}; ${m.tabs} tabs uncovered`);
            if (width === 393) {
                // The header's height can change after load (a control appears, the title wraps): the panel follows.
                await ev(`(() => { const b = document.querySelector('header.top-bar'); b.dataset.qaH = b.style.height; b.style.setProperty('height', (b.getBoundingClientRect().height + 30) + 'px', 'important'); return true; })()`);
                await sleep(300);
                const g = await measure();
                assert.ok(g.barBottom > m.barBottom, `the header grew (${m.barBottom} → ${g.barBottom})`);
                assert.ok(g.panelTop >= g.barBottom - 1, `${label}: after the header grew, the panel still starts below it (panel ${g.panelTop}, bar ${g.barBottom})`);
                assert.deepEqual(g.covered, [], `${label}: no tab covered after the header grew`);
                await ev(`(() => { const b = document.querySelector('header.top-bar'); b.style.removeProperty('height'); if (b.dataset.qaH) b.style.height = b.dataset.qaH; return true; })()`);
                await sleep(300);
                const r = await measure();
                assert.ok(r.panelTop >= r.barBottom - 1 && r.panelTop <= r.barBottom + 1, `${label}: and follows it back when it shrinks (panel ${r.panelTop}, bar ${r.barBottom})`);
                console.log(`   ✓ ${label}: header grew to ${g.barBottom} → panel ${g.panelTop}; shrank back → panel ${r.panelTop}`);
            }
        }
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Emulation.clearDeviceMetricsOverride();

        console.log('Cloud Hub server list: no literal "null"');
        for (const mode of ['ok', 'error']) {
            const nulls = await ev(`(async () => {
                const real = window.fetch;
                window.fetch = (u, o) => new URL(String(u), location.origin).pathname === '/api/servers'
                    ? Promise.resolve(${JSON.stringify(mode)} === 'ok'
                        ? new Response(JSON.stringify({ servers: [{ id: 'm1', label: 'Box One', online: true, access_mode: 'view', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z' }] }), { status: 200, headers: { 'content-type': 'application/json' } })
                        : new Response('{}', { status: 503 }))
                    : real(u, o);
                const host = document.getElementById('servers-tab-body') || document.body.appendChild(document.createElement('div'));
                const cs = await import((window.__coralStatic || '/static/') + 'cloud_servers.js');
                await cs.renderCloudServersInto(host);
                cs.hideCloudServers();
                window.fetch = real;
                const w = document.createTreeWalker(host, NodeFilter.SHOW_TEXT);
                const bad = []; for (let n = w.nextNode(); n; n = w.nextNode()) if (n.nodeValue.trim() === 'null' || /\bnull\b/.test(n.nodeValue)) bad.push(n.nodeValue.trim().slice(0, 40));
                return { bad, text: host.textContent.slice(0, 80) };
            })()`);
            assert.deepEqual(nulls.bad, [], `${mode}: no 'null' text in the server list (${JSON.stringify(nulls)})`);
            console.log(`   ✓ ${mode}: no 'null' text`);
        }
        console.log('\nAll phone panel overlay checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
