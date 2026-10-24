// The containing-block census (#6889, the bug class of #6340): an element with a filter, backdrop-filter, transform (or translate, rotate, scale), perspective, will-change of one of those, contain: layout, paint,
// strict or content, a container-type or content-visibility: auto becomes the containing block of its position:fixed descendants, which are then laid out inside it instead of in the window. A stale agent's
// row had a grey `filter`, its fixed hover tooltip opened 225 px off and grew the list, and the pinned "+ New Agent" jumped on every hover. This walks every position:fixed element in the page (shown or not:
// tooltips, menus, the toasts' column, the notice bar, the dialogs) on the main screens at 1280 and 390 px and fails for any whose ancestor makes such a box. A pair that is meant is listed in INTENDED
// with the reason and the check that proves it. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

// [ancestor selector, fixed descendant selector, why it is meant]
const INTENDED = [
    ['#command-pane', '.model-chip-menu', "a phone's model and thinking menus are a bottom sheet of the composer pane (position: fixed with 8 px side and bottom offsets): the pane spans the screen's width and ends at its foot, and its container-type is what the composer's @container rules need; checked below that the open sheet is inside the screen at 844 and 464 px high"],
];

const SCAN = `(() => { const sel = e => (e.id ? '#' + e.id : e.tagName.toLowerCase()) + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/).slice(0, 3).join('.') : '');
    const why = e => { const s = getComputedStyle(e); const r = [];
        if (s.filter !== 'none') r.push('filter'); if ((s.backdropFilter || s.webkitBackdropFilter || 'none') !== 'none') r.push('backdrop-filter'); if (s.transform !== 'none') r.push('transform');
        if (s.perspective !== 'none') r.push('perspective'); for (const p of ['translate', 'rotate', 'scale']) if (s[p] && s[p] !== 'none') r.push(p);
        if (/transform|perspective|filter/.test(s.willChange)) r.push('will-change ' + s.willChange); if (/layout|paint|strict|content/.test(s.contain)) r.push('contain ' + s.contain);
        if (s.containerType && s.containerType !== 'normal') r.push('container-type ' + s.containerType); if (s.contentVisibility === 'auto') r.push('content-visibility');
        return r; };
    const out = []; let fixed = 0;
    for (const f of document.querySelectorAll('*')) { if (getComputedStyle(f).position !== 'fixed') continue; fixed++;
        for (let p = f.parentElement; p; p = p.parentElement) { const r = why(p); if (r.length) { out.push({ anc: sel(p), reason: r.join(', '), fixed: sel(f), ancEl: p.id ? '#' + p.id : sel(p), fixedClass: typeof f.className === 'string' ? f.className.trim().split(/\\s+/)[0] : '' }); break; } } }
    return { out, fixed }; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const S = `(window.__coralStatic || '/static/')`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w} px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled(mobile ? { enabled: true, maxTouchPoints: 5 } : { enabled: false });
            await Emulation.setEmulatedMedia({ features: mobile ? [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] : [] });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            // a team whose agents are on an unreachable server (stale: the grey filter that was the bug), and a solo agent
            await ev(`(async () => { for (let i = 1; i <= 3; i++) window.__m.add('rb' + i, 'R' + i, 3, { sleeping: false, agent_type: 'claude', session_id: 'rb-' + i, server: 'box-b', board_project: 'team-x' });
                window.__m.add('solo', 'S', 1, { sleeping: false, agent_type: 'claude', session_id: 'solo-id' });
                const ws = await import(${S} + 'websocket.js'); ws.handleCoralMessage({ type: 'coral_diff', changed: [], servers: [{ id: 'box-b', label: 'box-b.lan', status: 'unreachable' }] });
                T.state.liveSessions.forEach(s => { if (s.server === 'box-b') s.stale = true; }); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
            await waitFor(`document.querySelectorAll('#live-sessions-list .session-group-item.session-stale').length >= 3 || document.querySelectorAll('#mobile-agent-list .session-group-item.session-stale').length >= 3`, 'the stale team', 8000);
            const found = new Map(); let fixedSeen = 0;
            const scenes = [
                ['the agent list', 'true'],
                ['an agent open in the chat', `window.selectLiveSession('solo', 'claude', 'solo-id', 'local'); true`],
                ['Settings', `window.showSettingsModal('general'); true`],
                ['New Agent', `window.hideSettingsModal && window.hideSettingsModal(); window.showLaunchModal && window.showLaunchModal(); true`],
                ['Jobs', `window.hideLaunchModal && window.hideLaunchModal(); window.switchNavTab('jobs'); true`],
                ['Workflows', `window.switchNavTab('workflows'); true`],
                ['Docs', `window.switchNavTab('docs'); true`],
                ['the kebab and settings menus', `window.switchNavTab('agents'); document.querySelectorAll('.sidebar-kebab-btn').forEach(b => b.click()); const m = document.querySelector('[aria-controls="top-bar-settings-menu"]'); if (m) m.click(); true`],
                ['toasts and the kept-worktree notice', `import(${S} + 'utils.js').then(u => { u.showToast('Saved.'); u.showToast('Could not save.', true); }); import(${S} + 'worktree_notice.js').then(m => m.showWorktreeNotice({ state: 'kept', path: '/tmp/x', branch: 'b', reason: 'r' }, 'team')); true`],
                ['the composer with its menus', `window.selectLiveSession('solo', 'claude', 'solo-id', 'local'); const c = document.getElementById('command-input'); if (c) { c.value = '/'; c.dispatchEvent(new Event('input', { bubbles: true })); } true`],
            ];
            for (const [name, js] of scenes) {
                await ev(js).catch(() => {}); await sleep(600); // sleep-ok: the screen draws; no single completion event across these scenes
                const r = await ev(SCAN); fixedSeen = Math.max(fixedSeen, r.fixed);
                for (const x of r.out) found.set(`${x.anc} [${x.reason}] holds the fixed ${x.fixed}`, { ...x, scene: name });
            }
            check(`${tag}: the census saw position:fixed elements on the screens (${fixedSeen}), so it is not passing on an empty page`, fixedSeen >= 8, String(fixedSeen));
            const stray = [...found.values()].filter(x => !INTENDED.some(([a, f]) => x.ancEl === a && ('.' + x.fixedClass) === f));
            check(`${tag}: no filter, transform, perspective, will-change, contain or container-type box holds a fixed descendant that is not listed as intended`, stray.length === 0, JSON.stringify(stray.slice(0, 5).map(x => `${x.scene}: ${x.anc} [${x.reason}] holds ${x.fixed}`)));
            if (mobile) {
                // the listed pair: the open sheet is inside the screen with the keyboard down (844) and up (464)
                for (const hh of [844, 464]) {
                    await Emulation.setDeviceMetricsOverride({ width: w, height: hh, deviceScaleFactor: 1, mobile });
                    await ev(`window.selectLiveSession('solo', 'claude', 'solo-id', 'local'); true`); await sleep(500); // sleep-ok: layout settles after a resize
                    await ev(`(() => { const m = document.getElementById('model-chip-menu'); if (m) { m.hidden = false; } return true; })()`);
                    const r = await ev(`(() => { const m = document.getElementById('model-chip-menu'); if (!m) return null; const b = m.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, left: b.left, right: b.right, vh: innerHeight, vw: innerWidth, h: b.height }; })()`);
                    check(`${tag}, ${hh} high: the model sheet (the listed pair) is inside the screen`, r && r.left >= 0 && r.right <= r.vw + 0.5 && r.top >= -0.5 && r.bottom <= r.vh + 0.5, JSON.stringify(r));
                    await ev(`(() => { const m = document.getElementById('model-chip-menu'); if (m) m.hidden = true; return true; })()`);
                }
            }
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} containing-block census checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
