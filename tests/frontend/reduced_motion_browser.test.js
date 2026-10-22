// prefers-reduced-motion stops every infinite animation, and no dot has an expanding ring (#5140, from the #5138 inventory).
//   - a populated dashboard (agents working, needing input and stuck, in the list and in the collapsed rail; toasts) plus one specimen of every looping indicator the stylesheets define
//     (spinners, shimmer, pulses, the cost Live dot, the workflow dots, the chat's working dots): with reduced motion emulated, document.getAnimations() holds no running animation that loops;
//   - the same page WITHOUT it still has them looping (so the check is not vacuous), and none of them is a ring (a ::after on a status dot);
//   - toasts and overlays run once and at once (no slide that outlasts a frame), smooth scrolling asks for "auto" (motion.js) and no source file asks for 'smooth' by itself.
// Real isolated server, mock agents; drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: an animation's first frames; there is no event for "started"
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const SPECIMENS = ['<span class="status-dot working"></span>', '<span class="status-dot waiting"></span>', '<span class="status-dot stuck"></span>', '<span class="status-dot status-dot-working"></span>',
        '<span class="team-distill-chip"><span class="team-distill-dot"></span></span>', '<span class="hub-indicator" data-state="connecting">hub</span>',
        '<span class="cost-live-indicator"><span class="cost-live-dot"></span></span>', '<span class="task-spinner"></span>', '<button class="refresh-files-btn refreshing"><span class="material-icons">refresh</span></button>',
        '<span class="restart-spinner"></span>', '<span class="notes-spinner"><span></span><span></span><span></span></span>', '<span class="theme-generate-spinner"></span>', '<span class="loading-indicator">Loading</span>',
        '<div class="loading-skeleton" style="width:120px;height:14px"></div>', '<span class="chat-working-dots"><i></i><i></i><i></i></span>', '<span class="cni-head"><span class="cni-dot"></span></span>',
        '<span class="wf-status-dot running"></span>', '<div class="wf-timeline-node running" style="position:relative"></div>', '<span class="material-icons spin">sync</span>'];
    const RING = x => /::after/.test(x) && /(status-dot|session-dot|avatar-status-dot|team-distill-dot|waiting-badge)/.test(x); // a ring on a dot (the skeleton's shimmer is also an ::after)
    const looping = () => ev(`(() => { const sel = (e, p) => { if (!e) return '?'; const cls = (e.className && typeof e.className === 'string' ? e.className : '').trim().split(/\\s+/).filter(Boolean).slice(0, 3).map(c => '.' + c).join(''); return (e.tagName || '').toLowerCase() + cls + (p || ''); };
        return document.getAnimations().filter(a => a.effect && a.effect.getTiming().iterations === Infinity && a.playState === 'running').map(a => (a.animationName || a.transitionProperty || '?') + ' @ ' + sel(a.effect.target, a.effect.pseudoElement)); })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const entry = window.__m.entry.bind(window.__m); const st = { 'rm-work': { working: true }, 'rm-wait': { waiting_for_input: true, waiting_kind: 'permission' }, 'rm-stuck': { stuck: true } };
            window.__m.entry = a => ({ ...entry(a), ...(st[a.session_id] || {}) });
            for (const n of Object.keys(st)) window.__m.add(n, n.slice(3, 4).toUpperCase(), 3, { sleeping: false, session_id: n }); return true; })()`);
        await waitFor(`(window.__qaState.liveSessions || []).length >= 3`, 'agents', 8000);
        await ev(`window.__qaState.liveSessions = window.__qaState.liveSessions.map(s => ({ ...s, ...window.__m.entry(window.__m.agents[s.session_id]) })); import((window.__coralStatic || '/static/') + 'render.js').then(m => m.renderLiveSessions && m.renderLiveSessions(window.__qaState.liveSessions)); true`);
        await ev(`window.selectLiveSession('rm-work', 'claude', 'rm-work', 'local'); true`);
        await waitFor(`document.querySelectorAll('.session-dot.working, .session-dot.waiting, .session-dot.stuck').length >= 3`, 'the row dots', 8000);
        const specimens = () => ev(`(() => { let h = document.getElementById('rm-spec'); if (!h) { h = document.createElement('div'); h.id = 'rm-spec'; h.style.cssText = 'position:fixed;right:8px;bottom:8px;z-index:99999;display:flex;gap:8px;flex-wrap:wrap;width:300px'; document.body.append(h); } h.innerHTML = ${JSON.stringify(SPECIMENS.join(''))}; return true; })()`);
        const notices = () => ev(`import((window.__coralStatic || '/static/') + 'notifications.js').then(m => { m.showToast('Settings saved'); m.showNotificationToast('rm-wait', 'Needs input', () => {}); return true; })`);
        const rail = () => ev(`window.toggleSidebarRail && window.toggleSidebarRail(); true`);

        // 1. normal motion: the page loops, and no dot has a ring
        // Say so: a Windows runner has Settings > Accessibility > Animation effects off, which makes the browser's own answer "reduce" before this test emulates anything.
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-reduced-motion', value: 'no-preference' }] });
        await specimens(); await notices(); await sleep(600); // sleep-ok: an animation's first frames; there is no event for "started"
        const normal = await looping();
        const loops = new Set(normal.map(x => x.split(' @ ')[0]));
        check('without reduced motion the indicators loop (the check below is not vacuous)', ['task-spin', 'skeleton-shimmer', 'loading-spin', 'cost-pulse', 'chat-working-pulse', 'wf-pulse', 'spin', 'pulse-working'].every(n => loops.has(n)), JSON.stringify([...loops]));
        check('and no status dot has an expanding ring: no ::after animation on a dot, in the list, the specimens or the rail', !normal.some(RING) && !loops.has('pulse-ring'), JSON.stringify(normal.filter(RING)));
        const rowDots = await ev(`['working', 'waiting', 'stuck'].map(k => { const e = document.querySelector('#live-sessions-list .session-dot.' + k); return e ? [k, getComputedStyle(e).animationName, getComputedStyle(e, '::after').animationName] : [k, 'missing', '']; })`);
        check('each agent row\'s dot flashes by itself (opacity) and has no ring', JSON.stringify(rowDots) === JSON.stringify([['working', 'pulse-working', 'none'], ['waiting', 'pulse-waiting', 'none'], ['stuck', 'pulse-stuck', 'none']]), JSON.stringify(rowDots));
        await rail(); await sleep(700); // sleep-ok: an animation's first frames; there is no event for "started"
        const railLoops = await looping();
        check('the collapsed rail\'s dots flash by opacity and have no ring', railLoops.some(x => /rail-tile-dot/.test(x) && /^pulse-/.test(x)) && !railLoops.some(RING), JSON.stringify(railLoops.filter(x => /rail-tile-dot/.test(x) || RING(x))));

        // 2. reduced motion: nothing loops
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
        await specimens(); await notices(); await sleep(600); // sleep-ok: an animation's first frames; there is no event for "started"
        const reduced = await looping();
        check('with prefers-reduced-motion no animation loops: list, rail, specimens of every spinner, shimmer and pulse, toasts', reduced.length === 0, JSON.stringify(reduced));
        const scroll = await ev(`import((window.__coralStatic || '/static/') + 'motion.js').then(m => m.scrollBehavior())`);
        check('smooth scrolling asks for "auto"', scroll === 'auto', scroll);
        const toastRun = await ev(`[...document.querySelectorAll('.toast, .notification-toast')].map(t => { const c = getComputedStyle(t); return c.animationDuration.split(', ').map(parseFloat).concat(c.animationDelay.split(', ').map(parseFloat)).join('/'); })`);
        check('toasts take no time to slide and wait no time to fade (a hundredth of a millisecond): they appear where they stay', toastRun.length >= 2 && toastRun.every(r => r.split('/').every(v => v <= 0.001)), JSON.stringify(toastRun));
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-reduced-motion', value: 'no-preference' }] });
        const normalScroll = await ev(`import((window.__coralStatic || '/static/') + 'motion.js').then(m => m.scrollBehavior())`);
        check('and "smooth" for everyone else', normalScroll === 'smooth', normalScroll);

        // 3. no file asks for smooth scrolling by itself
        const dir = path.join(__dirname, '..', '..', 'coral-go', 'pkg', 'frontend', 'static');
        const offenders = fs.readdirSync(dir).filter(f => f.endsWith('.js') && f !== 'motion.js' && f !== 'chat_jump_latest.js').filter(f => /behavior:\s*['"]smooth['"]/.test(fs.readFileSync(path.join(dir, f), 'utf8')));
        check('no module hard-codes behavior: "smooth" (chat_jump_latest.js checks the setting itself)', offenders.length === 0, JSON.stringify(offenders));
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally { try { await client.close(); } catch (_) {} }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS reduced motion: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
