// "+ New Agent" stays in view with many agents (#6159, coral-hub's ask): with 25 agents the sidebar scrolls, and the button used to sit at the end of the scrolled content, below the fold. It is pinned to the
// bottom of the sidebar now and the last agent row can still be scrolled clear of it. At 1280 px (the sidebar) and 390 px (the header's New button), in both themes, with the dashboard's mocked agents
// (agent_view_harness.js) drawn into the real list; against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { themeJs } = require('./helpers/theme');
const BASE = require('./test_base').BASE;

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`window.switchNavTab('agents'); true`);
            const n = await ev(`(async () => { T.state.liveSessions = []; window.__m.agents = {};
                for (let i = 1; i <= 25; i++) window.__m.add('agent-' + String(i).padStart(2, '0'), 'A', 3, { sleeping: false });
                T.render.renderLiveSessions(T.state.liveSessions); return T.state.liveSessions.length; })()`);
            check(`${tag}: 25 agents are drawn`, n === 25, String(n));
            const rows = mobile ? '#mobile-agent-list li' : '[data-section="live-sessions"] .session-list > li';
            await waitFor(`document.querySelectorAll(${JSON.stringify(rows)}).length >= 25`, `${tag}: 25 rows in the list`, 8000);
            for (const theme of ['light', 'dark']) {
                await ev(`${themeJs(theme)}; true`);
                await ev(`(() => { const s = document.querySelector('.sidebar'); if (s) s.scrollTop = 0; window.scrollTo(0, 0); return true; })()`);
                const m = await ev(`(() => { const b = [...document.querySelectorAll('[data-on-click="launch-show"]')].filter(e => e.getClientRects().length); const r = b[0] && b[0].getBoundingClientRect();
                    return { n: b.length, text: b[0] && b[0].textContent.trim(), top: r && Math.round(r.top), bottom: r && Math.round(r.bottom), left: r && Math.round(r.left), right: r && Math.round(r.right), vh: innerHeight, vw: innerWidth,
                        hit: !!r && (t => !!t && (t === b[0] || b[0].contains(t)))(document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)) }; })()`);
                check(`${tag} ${theme}: "+ New Agent" is in the viewport at the top of the list, not scrolled to, and nothing covers it`, m.n >= 1 && m.top >= 0 && m.bottom <= m.vh && m.left >= 0 && m.right <= m.vw && m.hit, JSON.stringify(m));
                if (!mobile) {
                    // scrolled to the end, the last agent row is whole and clear of the button
                    const e = await ev(`(() => { const s = document.querySelector('.sidebar'); s.scrollTop = s.scrollHeight; const rows = [...document.querySelectorAll('[data-section="live-sessions"] .session-list > li')], last = rows[rows.length - 1].getBoundingClientRect(), f = document.querySelector('.sidebar-footer').getBoundingClientRect(), b = document.querySelector('.sidebar-footer [data-on-click="launch-show"]').getBoundingClientRect();
                        const t = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2);
                        return { lastBottom: Math.round(last.bottom), footerTop: Math.round(f.top), btnTop: Math.round(b.top), btnBottom: Math.round(b.bottom), vh: innerHeight, btnHit: !!t && !!t.closest('[data-on-click="launch-show"]'), canScroll: s.scrollHeight > s.clientHeight }; })()`);
                    check(`${tag} ${theme}: with the sidebar scrolled to the end the last agent is clear of the button and the button is still in view`, e.canScroll && e.lastBottom <= e.footerTop + 1 && e.btnTop >= 0 && e.btnBottom <= e.vh && e.btnHit, JSON.stringify(e));
                }
            }
        }
    } finally { /* the harness closes the page */ }
    const failed = results.filter(r => !r).length;
    console.log(`sidebar_new_agent_pinned: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
