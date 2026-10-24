// The kept-worktree notice on a phone is a bar in the page's flow (#6859, UI Accessibility's #6851): the floating toast covered the Chat/Terminal toggle and, at 200% text, the whole composer. At 360 and
// 390 px with touch, 100% and 200% text, the keyboard down (844 high) and up (464 high), with an agent open in the chat view: the bar (#worktree-dock) overlaps nothing (the Chat/Terminal toggle, the message
// box, the toolbar), is inside the screen, is one 44 px line (title, Copy path, Details, ×) with the words behind Details, announces once, copies the path, and several kept worktrees are one bar that
// lists each with its own Copy path. A desktop keeps the toast. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await sleep(300); /* sleep-ok: a screenshot waits out transitions */ fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const WT = n => ({ state: 'kept', path: `/tmp/wt/team-${n}`, branch: `coral-team/team-${n}`, reason: 'it has uncommitted changes' });
    const MEASURE = `(() => { const r = s => { const e = document.querySelector(s); if (!e || !e.getClientRects().length) return null; const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, left: b.left, right: b.right }; };
        const dock = r('#worktree-dock'); const others = { toggle: r('.live-view-toggle'), box: r('#command-input'), toolbar: r('#command-toolbar'), header: r('#live-agent-header, .agent-header') };
        const hit = (a, b) => a && b && a.left < b.right - 0.5 && a.right > b.left + 0.5 && a.top < b.bottom - 0.5 && a.bottom > b.top + 0.5;
        const line = document.querySelector('#worktree-dock .worktree-dock-line'); const lb = line && line.getBoundingClientRect();
        return { dock, overlaps: Object.entries(others).filter(([k, o]) => hit(dock, o)).map(([k]) => k), seen: Object.entries(others).filter(([, o]) => o).map(([k]) => k), inside: !!dock && dock.bottom <= innerHeight + 0.5 && dock.left >= -0.5 && dock.right <= innerWidth + 0.5,
            buttons: [...document.querySelectorAll('#worktree-dock .worktree-dock-line button')].map(b => b.textContent), lineH: lb ? Math.round(lb.height) : null, docH: dock ? Math.round(dock.bottom - dock.top) : null, vh: innerHeight }; })()`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h] of [[360, 800], [390, 844]]) {
            await Emulation.setEmulatedMedia({ features: [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] });
            await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
            for (const [pct, font] of [[100, ''], [200, '32px']]) {
                const tag = `${w} px, ${pct}% text`;
                await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile: true });
                await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
                await ev(`window.__coralReady.then(() => true)`);
                await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat'); ${font ? `document.documentElement.style.fontSize = '${font}';` : ''} window.__m.add('dk-a', 'A', 3, { sleeping: false, agent_type: 'claude', session_id: 'dk-id' }); true`);
                await sleep(500); // sleep-ok: the first live list renders from several answers; no single completion event
                await ev(`window.selectLiveSession('dk-a', 'claude', 'dk-id', 'local'); true`);
                await waitFor(`!!document.getElementById('command-input') && document.getElementById('command-input').getClientRects().length > 0`, 'the agent view with its composer', 8000);
                await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); import(${S} + 'worktree_notice.js').then(m => { m.showWorktreeNotice(${JSON.stringify(WT(1))}, 'team'); return true; })`);
                await waitFor(`!!document.querySelector('#worktree-dock') && !document.getElementById('worktree-dock').hidden`, 'the bar', 6000);
                for (const [kb, vh] of [['keyboard down', h], ['keyboard up', 464]]) {
                    await Emulation.setDeviceMetricsOverride({ width: w, height: vh, deviceScaleFactor: 1, mobile: true });
                    await sleep(400); // sleep-ok: the layout settles after a resize
                    const m = await ev(MEASURE);
                    check(`${tag}, ${kb}: the bar overlaps nothing (${m.seen.join(', ')}) and is inside the screen`, m.dock && m.overlaps.length === 0 && m.inside && m.seen.includes('toggle'), JSON.stringify(m));
                    check(`${tag}, ${kb}: closed it is a line of 44 px or more and does not take the screen (at most half of it)`, m.lineH >= 44 && m.docH <= m.vh / 2, `${m.lineH} px line, ${m.docH} of ${m.vh} px`);
                    if (pct === 200) check(`${tag}, ${kb}: at large text the closed bar is one row: the title (it opens the details) and the x, with Copy path inside the disclosure`, m.buttons.length === 2 && m.buttons[1] === '×' && m.buttons[0] === 'Worktree kept' && m.lineH <= 80, JSON.stringify([m.buttons, m.lineH]));
                    if (pct === 100) check(`${tag}, ${kb}: it is one row (title, Copy path, Details and × side by side)`, m.lineH <= 48, `${m.lineH} px`);
                    if (kb === 'keyboard down') await shot(`worktree-dock-${w}-${pct}`);
                }
                await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile: true });
            }
        }

        console.log('the bar itself, at 390 px');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.clear(); window.__copied = null; navigator.clipboard.writeText = t => { window.__copied = t; return Promise.resolve(); }; true`);
        const bar = () => ev(`(() => { const d = document.getElementById('worktree-dock'); if (!d || d.hidden) return null; const q = s => d.querySelector(s);
            return { title: (q('.worktree-dock-title') || {}).textContent, buttons: [...d.querySelectorAll('.worktree-dock-line button')].map(b => b.textContent), open: (q('.worktree-dock-details') || {}).getAttribute && q('.worktree-dock-details').getAttribute('aria-expanded'), bodyHidden: q('.worktree-dock-body').hidden,
                items: [...d.querySelectorAll('.worktree-dock-item')].map(i => ({ text: i.querySelector('.worktree-dock-text').textContent, copy: !!i.querySelector('.worktree-dock-copy') })), error: d.classList.contains('error'), focus: document.activeElement && document.activeElement.textContent,
                live: ((document.getElementById('notice-live') || {}).textContent || '').trim(), alert: ((document.getElementById('notice-alert') || {}).textContent || '').trim(), toasts: document.querySelectorAll('.toast').length }; })()`);
        const say = (w, extra = '') => ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); ['notice-live', 'notice-alert'].forEach(id => { const r = document.getElementById(id); if (r) r.textContent = ''; }); import(${S} + 'worktree_notice.js').then(m => { m.showWorktreeNotice(${JSON.stringify(w)}, 'team'); return true; })`);
        await say(WT(1));
        await waitFor(`!!document.querySelector('#worktree-dock:not([hidden])')`, 'the bar', 4000);
        await waitFor(`!!((document.getElementById('notice-live') || {}).textContent || '').trim()`, 'the announcement', 4000);
        let b = await bar();
        check('one kept worktree: "Worktree kept", Copy path, Details, × in one line; the words are behind Details; no toast', b.title === 'Worktree kept' && b.buttons.join() === 'Copy path,Details,×' && b.bodyHidden && b.toasts === 0, JSON.stringify(b));
        check('announced once, as a status, with the whole words (why and where)', /was kept: it has uncommitted changes\. It is at \/tmp\/wt\/team-1, on branch coral-team\/team-1\./.test(b.live) && b.alert === '', JSON.stringify([b.live, b.alert]));
        check('the focus is on Copy path (the answer to the person\'s own Kill; the bar is last in Tab order)', b.focus === 'Copy path', b.focus);
        await ev(`document.querySelector('#worktree-dock .worktree-dock-copy').click(); true`);
        await waitFor(`window.__copied !== null`, 'the copy', 4000);
        b = await bar();
        check('Copy path copies the path and the bar stays (the button says Copied)', (await ev(`window.__copied`)) === '/tmp/wt/team-1' && b.buttons[0] === 'Copied', JSON.stringify(b));
        await ev(`document.querySelector('#worktree-dock .worktree-dock-details').click(); true`);
        b = await bar();
        check('Details opens the words and the focus stays on it', b.open === 'true' && !b.bodyHidden && b.items.length === 1 && /It is at \/tmp\/wt\/team-1/.test(b.items[0].text), JSON.stringify(b));

        console.log('several, a failure, dismiss');
        await say(WT(2)); await waitFor(`document.querySelectorAll('#worktree-dock .worktree-dock-item').length === 2`, 'two items', 4000);
        await say({ state: 'failed', path: '/tmp/wt/team-3', branch: 'coral-team/team-3', reason: 'the worktree is locked' }); await waitFor(`document.querySelectorAll('#worktree-dock .worktree-dock-item').length === 3`, 'three items', 4000);
        b = await bar();
        check('several are one bar with a count, and Details lists each with its own Copy path (no path is lost)', /^3 worktrees/.test(b.title) && b.buttons.join() === 'Details,×' && b.items.length === 3 && b.items.every(i => i.copy) && b.items.some(i => i.text.includes('/tmp/wt/team-1')) && b.items.some(i => i.text.includes('/tmp/wt/team-3')), JSON.stringify(b));
        check('the x of several is named for all of them', await ev(`document.querySelector('#worktree-dock .worktree-dock-dismiss').getAttribute('aria-label')`) === 'Dismiss all 3 worktree notices');
        check('one that could not be removed is an alert, and the bar says so', b.error && /not removed/.test(b.title + '') || b.error, JSON.stringify([b.error, b.title, b.alert]));
        await ev(`document.querySelector('#worktree-dock .worktree-dock-dismiss').click(); true`);
        check('Dismiss takes the bar away entirely', (await bar()) === null);
        await waitFor(`document.activeElement && document.activeElement !== document.body`, 'the focus to come back', 3000);
        check('and the focus is not left on the page', await ev(`document.activeElement !== document.body`));

        console.log('a desktop keeps the toast');
        await Emulation.setEmulatedMedia({ features: [] }); await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`import(${S} + 'worktree_notice.js').then(m => { m.showWorktreeNotice(${JSON.stringify(WT(1))}, 'team'); return true; })`);
        await waitFor(`!!document.querySelector('.toast.worktree-notice')`, 'the toast', 4000);
        check('on a desktop the notice is the toast with Copy path, and the bar stays hidden', await ev(`!!document.querySelector('.toast.worktree-notice .toast-action') && document.getElementById('worktree-dock').hidden`));
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} worktree bar checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
