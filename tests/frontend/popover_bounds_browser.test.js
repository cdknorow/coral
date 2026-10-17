// At 200 percent text the display menu, the server list, a server's menu and the Settings modal stay inside the screen (#4247, from QA's
// phone and large-text pass #4231): on a 390 px phone and in the desktop sidebar (a popover never leaves the sidebar), dark and light,
// 100 and 200 percent. A server header keeps its name beside the Update required marker (the marker gives way first), and the
// Settings modal body does not scroll sideways, its buttons and selects whole.
//
// Drives Chrome over CDP through agent_view_harness.js against an isolated dev server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const NOW = new Date().toISOString();
const SERVERS = [
    { id: 'm1', label: 'studio-mac.local', status: 'online', last_seen: NOW },
    { id: 'm2', label: 'workstation.lan', status: 'online', last_seen: NOW, update: 'required', latest_coral: '1.5.0', coral_version: '1.3.9' },
    { id: 'm3', label: 'Old Laptop', status: 'unreachable', last_seen: '2026-10-01T00:00:00Z', last_error: 'dial tcp: timeout' },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const scene = () => ev(`(async () => { T.state.hub = true; T.state.cloud = true; T.state.servers = ${JSON.stringify(SERVERS)};
        T.state.liveSessions = []; window.__m.agents = {};
        ${JSON.stringify(SERVERS.map(s => s.id))}.forEach(id => { window.__m.add('team-' + id, 'T', 3, { sleeping: false, server: id, board_project: 'team-' + id }); });
        localStorage.setItem('coral-group-by-server', 'true'); T.render.renderLiveSessions(T.state.liveSessions);
        const mob = await import((window.__coralStatic || '/static/') + 'mobile.js'); mob.syncMobileAgentList(); return true; })()`);
    // The open popover's box and the box it must stay inside (the sidebar on the desktop, the screen on a phone).
    const box = (root, openSel, panelSel) => ev(`(() => {
        const d = document.querySelector(${JSON.stringify(root + ' ' + openSel)}); if (!d) return null; d.open = true;
        const p = d.querySelector(${JSON.stringify(panelSel)}); const r = p.getBoundingClientRect();
        const bound = document.querySelector('.sidebar') && ${JSON.stringify(root)}.startsWith('#live') ? document.querySelector('.sidebar').getBoundingClientRect() : { left: 0, right: innerWidth };
        const out = { left: Math.round(r.left), right: Math.round(r.right), bl: Math.round(bound.left), br: Math.round(bound.right), w: Math.round(r.width) };
        d.open = false; return out; })()`);
    const inside = (b, what) => assert.ok(b && b.w > 0 && b.left >= b.bl - 1 && b.right <= b.br + 1, `${what} stays inside (${JSON.stringify(b)})`);

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const theme of ['dark', 'light']) for (const scale of [100, 200]) for (const phone of [true, false]) {
            const name = `${phone ? 'phone 390' : 'desktop sidebar'}, ${theme}, ${scale}%`;
            console.log(name);
            if (phone) {
                await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
                await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
            } else {
                await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
                await Emulation.setTouchEmulationEnabled({ enabled: false });
            }
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme('${theme}');
                const st = document.createElement('style'); st.textContent = 'html{font-size:${scale}% !important}'; document.head.appendChild(st); return true; })()`);
            const bg = await ev(`getComputedStyle(document.body).backgroundColor`);
            const lum = bg.match(/\d+/g).slice(0, 3).reduce((a, b) => a + Number(b), 0) / 3;
            assert.equal(theme === 'light', lum > 128, `the ${theme} theme is really on (${bg})`);
            await scene();
            const root = phone ? '#mobile-agent-list' : '#live-sessions-list';
            const header = phone ? '#mobile-agent-list' : '.sidebar-header';
            await waitFor(`!!document.querySelector('${root} li.server-group-header')`, 'the server headers', 4000);

            // The popovers: the display menu, a server's menu (online with an update, and offline).
            inside(await box(header, '.list-display-menu', '.list-display-panel'), 'the display menu');
            for (const id of ['m1', 'm2', 'm3']) inside(await box(root, `li.server-group-header[data-server-group="${id}"] details`, '.server-switcher-panel'), `the menu of server ${id}`);

            // The server that needs an update keeps its name: the marker gives way first. (A name that does not fit even alone, in a 320 px
            // sidebar at 200 percent, is cut with or without the marker; the check is for the name the marker takes the room from.)
            const head = await ev(`(() => { const h = document.querySelector('${root} li.server-group-header[data-server-group="m2"]'); const n = h.querySelector('.server-group-name'), mk = h.querySelector('.server-update-marker');
                const cut = () => n.scrollWidth > n.clientWidth + 1; const withMarker = cut(); const shown = mk.getBoundingClientRect().width > 0;
                mk.style.display = 'none'; const alone = cut(); mk.style.display = '';
                return { text: n.textContent, withMarker, alone, shown }; })()`);
            assert.ok(head.shown, 'the Update marker is shown');
            assert.ok(head.alone || !head.withMarker, `the name "${head.text}" is whole beside the Update marker (it fits alone)`);

            // Settings (the whole modal, with the Sign out buttons in it) does not scroll sideways.
            if (phone) {
                await ev(`(async () => { const S = window.__coralStatic || '/static/'; const Real = window.fetch;
                    window.fetch = async (u, o = {}) => { const url = String(u);
                        if (url.startsWith('/api/system/auth-status')) return new Response(JSON.stringify({ authenticated: true, method: 'session', password_set: true, local: true }), { status: 200 });
                        if (url.startsWith('/api/auth/sessions') && !o.method) return new Response(JSON.stringify({ sessions: [{ id: 'aaaaaaaaaaaa', label: 'Chrome on macOS', method: 'password', last_seen: new Date().toISOString(), expires_at: new Date(Date.now() + 86400000).toISOString(), persistent: true, current: true }, { id: 'bbbbbbbbbbbb', label: 'A phone with a rather long browser label', method: 'password', last_seen: new Date().toISOString(), expires_at: new Date(Date.now() + 86400000).toISOString(), persistent: true, current: false }], recent: [] }), { status: 200 });
                        return Real(u, o); };
                    await window.showSettingsModal(); const sec = await import(S + 'security_settings.js'); await sec.loadSecurityRow(); return true; })()`);
                await waitFor(`!!document.getElementById('sec-signout')`, 'the Sign out button', 4000);
                const modal = await ev(`(() => { const body = document.querySelector('.settings-modal-body'); const br = body.getBoundingClientRect();
                    const wide = [...body.querySelectorAll('button, select, input:not([type=checkbox]):not([type=radio]), textarea')].filter(e => e.getBoundingClientRect().width > 0)
                        .filter(e => e.getBoundingClientRect().right > br.right + 1 || e.getBoundingClientRect().left < br.left - 1).map(e => (e.id || e.className.toString() || e.tagName).slice(0, 40));
                    return { sw: body.scrollWidth, cw: body.clientWidth, wide, doc: document.documentElement.scrollWidth - innerWidth }; })()`);
                assert.ok(modal.sw <= modal.cw + 1, `the Settings body does not scroll sideways (${modal.sw} in ${modal.cw})`);
                assert.deepEqual(modal.wide, [], 'no Settings control is wider than the modal');
                assert.equal(modal.doc, 0, 'the page does not scroll sideways');
                await ev(`window.hideSettingsModal && window.hideSettingsModal(); true`);
            }
        }
        console.log('\nAll popover and Settings bounds checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
