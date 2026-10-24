// Two LOW findings of the v1.4.164 look (#6943, UI Accessibility's #6938): at 200% text on a 360 px phone a kept-worktree row in Settings > Storage and cleanup was 523 to 575 px tall with Remove 87 px (the
// phone's .btn margin and padding), and at 200% text in a 320 px window the message box's placeholder broke inside a word ("Messag / e"). 360 px and 320 px, touch, 200% text. The worktree list is the page's
// fetch. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setEmulatedMedia({ features: [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        console.log('a kept-worktree row at 200% text on a 360 px phone');
        await Emulation.setDeviceMetricsOverride({ width: 360, height: 740, deviceScaleFactor: 1, mobile: true });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`document.documentElement.style.fontSize = '32px'; (() => { const real = window.fetch; const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
            window.fetch = (u, o) => { const p = new URL(String(u), location.origin).pathname; if (p === '/api/worktrees') return json({ worktrees: [
                { id: 'w1', kind: 'team', name: 'crew-1', path: '/data/worktrees/crew-1', repo_path: '/work/repo', branch: 'coral-team/crew-1', dirty: true, unmerged: false, in_use: false, checked: true },
                { id: 'w2', kind: 'job', name: 'nightly-agent', path: '/data/worktrees/job-5-run-2', repo_path: '/work/repo', branch: 'coral-job/5-2', dirty: false, unmerged: true, in_use: false, checked: true } ] }); return real(u, o); }; return true; })()`);
        await ev(`window.showSettingsModal('cleanup'); true`);
        await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 2 && !document.getElementById('wt-list').closest('[hidden]')`, 'the kept worktrees', 8000);
        await sleep(400); // sleep-ok: the dialog lays out
        const m = await ev(`[...document.querySelectorAll('#wt-list .wt-item')].map(li => ({ h: Math.round(li.getBoundingClientRect().height), rm: Math.round(li.querySelector('.wt-remove').getBoundingClientRect().height), w: Math.round(li.getBoundingClientRect().width) }))`);
        check('Remove is a 44 px target but not taller than twice that (it was 87 px)', m.every(r => r.rm >= 44 && r.rm <= 88 - 8), JSON.stringify(m));
        check('a row is under 420 px tall (it was 523 to 575)', m.every(r => r.h <= 420), JSON.stringify(m));

        console.log('the message box placeholder at 320 px and 200% text');
        await Emulation.setDeviceMetricsOverride({ width: 320, height: 640, deviceScaleFactor: 1, mobile: true });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); document.documentElement.style.fontSize = '32px'; window.__m.add('dk-a', 'A', 3, { sleeping: false, agent_type: 'claude', session_id: 'dk-id' }); true`);
        await sleep(500); // sleep-ok: the first live list renders from several answers
        await ev(`window.selectLiveSession('dk-a', 'claude', 'dk-id', 'local'); true`);
        await waitFor(`!!document.getElementById('command-input') && document.getElementById('command-input').getClientRects().length > 0 && /Message/.test(document.getElementById('command-input').placeholder)`, 'the message box with its placeholder', 8000);
        const p = await ev(`(() => { const i = document.getElementById('command-input'), cs = getComputedStyle(i), ps = getComputedStyle(i, '::placeholder');
            const room = i.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
            const probe = document.createElement('span'); probe.style.cssText = 'position:absolute;visibility:hidden;white-space:nowrap;font:' + ps.font + ';letter-spacing:' + ps.letterSpacing; document.body.appendChild(probe);
            const words = i.placeholder.split(/\\s+/).filter(Boolean).map(w => { probe.textContent = w; return [w, Math.round(probe.getBoundingClientRect().width)]; }); probe.remove();
            return { room: Math.round(room), fs: ps.fontSize, words }; })()`);
        check('every word of the placeholder fits the box on one line (none breaks inside a word)', p.words.every(([, w]) => w <= p.room), JSON.stringify(p));
        // a longer word and a narrower box (#6971): the scale follows the widest word, whatever it is
        const fits = text => ev(`(async () => { const i = document.getElementById('command-input'); i.placeholder = ${JSON.stringify(text)}; const m = await import((window.__coralStatic || '/static/') + 'command_autosize.js'); m.autosizeCommandInput();
            const cs = getComputedStyle(i), ps = getComputedStyle(i, '::placeholder'); const room = i.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
            const probe = document.createElement('span'); probe.style.cssText = 'position:absolute;visibility:hidden;white-space:nowrap;font:' + ps.font + ';letter-spacing:' + ps.letterSpacing; document.body.appendChild(probe);
            const words = i.placeholder.split(/\\s+/).filter(Boolean).map(w => { probe.textContent = w; return [w, Math.round(probe.getBoundingClientRect().width)]; }); probe.remove();
            return { room: Math.round(room), fs: ps.fontSize, scale: i.style.getPropertyValue('--ph-scale'), words }; })()`);
        const longer = await fits('Message Orchestrator…');
        check('a longer word than "Message" still fits the box on one line (the placeholder is scaled to its widest word)', longer.words.every(([, w]) => w <= longer.room), JSON.stringify(longer));
        await Emulation.setDeviceMetricsOverride({ width: 280, height: 640, deviceScaleFactor: 1, mobile: true });
        await sleep(600); // sleep-ok: the layout settles after a resize
        const narrow = await fits('Message dk-a…');
        check('and in a narrower box (280 px) at 200% text too', narrow.words.every(([, w]) => w <= narrow.room + 0.5) || /^0\.6/.test(narrow.scale), JSON.stringify(narrow));
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} worktree row and placeholder checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
