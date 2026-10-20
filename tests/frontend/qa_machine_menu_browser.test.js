// QA (#5487): the expanded machine row, rendered from the page's own code for the cases a person meets: an online remote with an update available and a dev build version, one that must update,
// an offline one, and the hub's own machine. At a 380 px phone and a 1280 px desktop, in both themes: one launch row (Launch agent first, Launch team, Open in Servers as a text link; Open folder only
// on the hub's own machine, a hint elsewhere), exactly one Update control, no Collapse/Expand button, the version cut before "+" with the full build in its tooltip and name, nothing wider than the
// viewport, and every control keyboard-reachable and named. With QA_SHOTS=<dir> it also saves a screenshot of each menu.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const NOW = new Date().toISOString();
const SERVERS = [
    { id: 'm1', label: 'studio-mac.local', status: 'online', version: '1.4.129-dev+f9fef71f.dirty', last_seen: NOW, url: 'https://studio.example.test:8420', update: 'available', latest_coral: '1.4.134' },
    { id: 'm2', label: 'workstation.lan', status: 'online', version: '1.3.9', last_seen: NOW, update: 'required', latest_coral: '1.4.134', coral_version: '1.3.9' },
    { id: 'm3', label: 'Old Laptop', status: 'unreachable', version: '1.3.9', last_seen: '2026-10-01T00:00:00Z', last_error: 'dial tcp: timeout' },
    { id: 'm4', label: 'build-box', status: 'online', version: '1.4.134', last_seen: NOW },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const shots = process.env.QA_SHOTS || '';
    const bg = {};
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
        await ev(`localStorage.clear(); true`);
        await ev(`(async () => {
            T.state.hub = true; T.state.cloud = false; T.state.servers = ${JSON.stringify(SERVERS)};
            T.state.liveSessions = []; window.__m.agents = {};
            ${JSON.stringify(SERVERS.map(s => s.id))}.concat(['local']).forEach(id => { window.__m.add('solo-' + id, 'S', 3, { sleeping: false, server: id }); });
            localStorage.setItem('coral-group-by-server', 'true');
            T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);

        for (const [w, h, mobile] of [[380, 800, true], [1280, 900, false]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: mobile ? 2 : 1, mobile });
            await Emulation.setEmitTouchEventsForMouse({ enabled: mobile, configuration: 'mobile' });
            if (mobile) { await ev(`(async () => { const mob = await import((window.__coralStatic || '/static/') + 'mobile.js'); mob.syncMobileAgentList(); return true; })()`); await waitFor(`!!document.querySelector('#mobile-agent-list li.server-group-header')`, 'phone server header', 4000); }
            const LIST = mobile ? '#mobile-agent-list' : '#live-sessions-list';
            if (!mobile) { await ev(`T.render.renderLiveSessions(T.state.liveSessions); true`); await waitFor(`!!document.querySelector('#live-sessions-list li.server-group-header[data-server-group="m1"]')`, 'the desktop list drawn again', 4000); }
            for (const theme of ['light', 'dark']) {
                // The bundled Dark theme is applied as inline variables: set them aside for the stylesheet's own light values, and put them back for dark.
                await ev(`(() => { const st = document.documentElement.style;
                    if (${JSON.stringify(theme)} === 'light') { if (!window.__qaVars) { window.__qaVars = []; for (let i = st.length - 1; i >= 0; i--) { const n = st[i]; if (n.startsWith('--')) { window.__qaVars.push([n, st.getPropertyValue(n)]); st.removeProperty(n); } } } }
                    else if (window.__qaVars) { for (const [n, v] of window.__qaVars) st.setProperty(n, v); window.__qaVars = null; }
                    document.documentElement.setAttribute('data-theme', ${JSON.stringify(theme)}); return true; })()`);
                for (const id of ['m1', 'm2', 'm3', 'm4', 'local']) {
                    const tag = `${id} @${w} ${theme}`;
                    const r = await ev(`(() => {
                        document.querySelectorAll('details[data-server-details]').forEach(d => { d.open = false; });
                        const d = document.querySelector('${LIST} li.server-group-header[data-server-group="${id}"] details[data-server-details]');
                        if (!d) return { missing: true };
                        d.open = true;
                        const p = d.querySelector('.server-switcher-panel'); p.scrollIntoView({ block: 'nearest' });
                        const box = el => { const b = el.getBoundingClientRect(); return { l: Math.round(b.left), r: Math.round(b.right), t: Math.round(b.top), b: Math.round(b.bottom), w: Math.round(b.width), h: Math.round(b.height) }; };
                        const btns = [...p.querySelectorAll('button, a')];
                        const launch = [...p.querySelectorAll('[data-server-launch]')].map(b => ({ kind: b.dataset.serverLaunch, text: b.textContent.trim(), disabled: b.disabled, box: box(b) }));
                        const open = p.querySelector('[data-server-open]');
                        const upd = [...p.querySelectorAll('.server-update-badge')];
                        const ver = p.querySelector('.server-version');
                        return { panel: box(p), vw: innerWidth, overflowX: p.scrollWidth > p.clientWidth + 1, docOverflow: document.documentElement.scrollWidth > innerWidth + 1,
                            launch, open: open && { text: open.textContent.trim(), box: box(open), tag: open.tagName },
                            text: p.innerText, hasCollapse: /\\b(Collapse|Expand)\\b/.test(p.innerText) || !!p.querySelector('[data-server-toggle]'),
                            updates: upd.length, updateText: upd.map(u => u.textContent.trim()), ver: ver && { shown: ver.textContent, title: ver.getAttribute('title'), aria: ver.getAttribute('aria-label'), tab: ver.tabIndex },
                            unnamed: btns.filter(b => !(b.getAttribute('aria-label') || b.textContent || '').trim()).length,
                            escapes: [...p.querySelectorAll('*')].filter(el => { const b = el.getBoundingClientRect(); return b.width > 0 && (b.right > p.getBoundingClientRect().right + 1 || b.left < p.getBoundingClientRect().left - 1); }).map(el => el.className || el.tagName),
                            theme: getComputedStyle(document.body).backgroundColor, tooSmall: btns.filter(b => { const x = b.getBoundingClientRect(); return x.width < 24 || x.height < 24; }).map(b => b.textContent.trim()) };
                    })()`);
                    if (r.missing) { check(`${tag}: the machine row exists`, false); continue; }
                    const kinds = r.launch.map(x => x.kind).join();
                    check(`${tag}: launch buttons ${kinds}`, id === 'local' ? kinds === 'agent,team,folder' : kinds === 'agent,team', JSON.stringify(r.launch.map(x => x.text)));
                    check(`${tag}: no Open folder button or text on a remote`, id === 'local' || !/Open folder/.test(r.text));
                    if (id !== 'local' && id !== 'm3') check(`${tag}: a remote says to choose the folder when launching`, /choose it when you launch an agent/i.test(r.text));
                    if (id === 'm3') check(`${tag}: an offline machine's launch buttons are disabled and the reason is said once`, r.launch.every(x => x.disabled) && (r.text.match(/unavailable \(offline\)/g) || []).length === 1, r.text.slice(0, 120));
                    if (id !== 'm3') check(`${tag}: an online machine's launch buttons are enabled`, r.launch.every(x => !x.disabled));
                    check(`${tag}: Launch agent is first`, r.launch[0] && r.launch[0].kind === 'agent');
                    check(`${tag}: Open in Servers is a text link-style control after the buttons`, r.open && r.open.text === 'Open in Servers');
                    check(`${tag}: no Collapse or Expand button in the panel`, !r.hasCollapse);
                    check(`${tag}: nothing wider than the viewport (panel ${r.panel.l}..${r.panel.r} of ${r.vw})`, !r.overflowX && !r.docOverflow && r.panel.l >= 0 && r.panel.r <= r.vw);
                    const mids = r.launch.map(x => x.box).map(b => (b.t + b.b) / 2); const rows = new Set(); mids.forEach(m => { if (![...rows].some(x => Math.abs(x - m) < 12)) rows.add(m); });
                    check(`${tag}: the launch buttons are ${id === 'local' ? 'at most two rows (the hub has a third, Open folder)' : 'one row'} (${rows.size})`, id === 'local' ? rows.size <= 2 : rows.size === 1);
                    const expectUpdate = id === 'm1' || id === 'm2' ? 1 : 0;
                    check(`${tag}: exactly ${expectUpdate} Update control (${JSON.stringify(r.updateText)})`, r.updates === expectUpdate);
                    if (id === 'm1') check(`${tag}: the version is cut before + and the full build is the tooltip and the name`, r.ver && r.ver.shown === '1.4.129-dev' && r.ver.title === '1.4.129-dev+f9fef71f.dirty' && /full build 1\.4\.129-dev\+f9fef71f\.dirty/.test(r.ver.aria) && r.ver.tab === 0, JSON.stringify(r.ver));
                    if (id === 'm4') check(`${tag}: a release version has no tooltip noise`, !r.ver);
                    check(`${tag}: every control is named`, r.unnamed === 0);
                    check(`${tag}: nothing inside the panel pokes out of its box (${JSON.stringify(r.escapes)})`, r.escapes.length === 0);
                    bg[theme] = bg[theme] || r.theme;
                    if (shots) { fs.mkdirSync(shots, { recursive: true }); const { data } = await Page.captureScreenshot({ format: 'png' }); fs.writeFileSync(path.join(shots, `${id}_${w}_${theme}.png`), Buffer.from(data, 'base64')); }
                }
            }
        }
        check(`the two themes are really different (${JSON.stringify(bg)})`, bg.light && bg.dark && bg.light !== bg.dark);
        // keyboard: Tab reaches Launch agent, Launch team, then Open in Servers, in that order
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        const order = await ev(`(() => { const d = document.querySelector('li.server-group-header[data-server-group="m4"] details[data-server-details]'); d.open = true; const p = d.querySelector('.server-switcher-panel');
            return [...p.querySelectorAll('button, a, [tabindex="0"]')].map(b => b.dataset.serverLaunch || (b.dataset.serverOpen !== undefined ? 'open' : b.className)); })()`);
        check(`the tab order is launch agent, launch team, open (${JSON.stringify(order)})`, order[0] === 'agent' && order[1] === 'team' && order[2] === 'open');
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await client.close(); process.exit(failed ? 1 : 0);
    } catch (e) { console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})().catch(e => { console.error(e); process.exitCode = 1; });
