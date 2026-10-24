// A worktree a stop kept stays on screen (#6818, UI Accessibility's #6806 finding 3): when a stop keeps a worktree (or cannot remove it) its words, with the path, stay in the notice column until dismissed
// instead of fading in a few seconds, with a Copy path button that copies the path and leaves the notice open; the notice is announced once (a polite status; an alert for a failure). A removed worktree is a
// short toast with no button. Team stop and single agent kill, on a desktop (a phone shows the bar of worktree_dock_browser, #6859). The kill answers are the page's fetch (Backend's #6701 shape). Drives Chrome over CDP through agent_view_harness.js.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await sleep(300); /* sleep-ok: a screenshot waits out transitions */ fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const open = `getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`;
    const PATH = '/tmp/wt/team-k';
    const KEPT = { ok: true, sessions_killed: 1, worktree: { state: 'kept', path: PATH, branch: 'coral-team/team-k', reason: 'it has uncommitted changes' } };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false]]) { // a phone's layout shows the notice as a bar in the page's flow (worktree_dock_browser, #6859)
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(() => { const inner = window.__innerFetch || (window.__innerFetch = window.fetch); window.__answer = { ok: true, sessions_killed: 1 }; window.__copied = null;
                navigator.clipboard.writeText = t => { window.__copied = t; return Promise.resolve(); };
                window.fetch = (url, opts = {}) => { if (/\\/api\\/sessions\\/live\\/(?:team\\/)?[^/]+\\/kill/.test(String(url))) return Promise.resolve(new Response(JSON.stringify(window.__answer), { status: 200, headers: { 'content-type': 'application/json' } })); return inner(url, opts); }; return true; })()`);
            const seed = () => ev(`localStorage.clear(); T.state.liveSessions = []; window.__m.agents = {};
                window.__m.add('team-agent', 'K', 3, { sleeping: false, session_id: 't1', board_project: 'team-k' });
                T.state.liveSessions.forEach(s => { s.branch = 'coral-team/team-k'; s.git_state = { is_worktree: true, base_branch: 'master' }; });
                T.render.renderLiveSessions(T.state.liveSessions); true`);
            const stop = async (answer, keep = false) => {
                await seed();
                const before = 0;
                await ev(`${keep ? '' : "document.querySelectorAll('.toast').forEach(t => t.remove());"} ['notice-live', 'notice-alert'].forEach(id => { const r = document.getElementById(id); if (r) r.textContent = ''; }); window.__answer = ${JSON.stringify(answer)}; true`);
                await ev(`T.render.killBoard('team-k'); true`); await waitFor(open, 'the Kill Team confirmation', 8000);
                await ev(`document.getElementById('confirm-modal-yes').click(); true`);
                await waitFor(keep ? `[...document.querySelectorAll('.toast')].some(x => x.textContent.includes(${JSON.stringify(answer.worktree.path)}))` : `[...document.querySelectorAll('.toast')].filter(x => /worktree/.test(x.textContent)).length > ${before}`, 'the notice', 6000);
                await waitFor(`!!((document.getElementById('notice-live') || {}).textContent || '').trim() || !!((document.getElementById('notice-alert') || {}).textContent || '').trim()`, 'the notice to be announced', 6000);
            };
            const toast = () => ev(`(() => { const t = [...document.querySelectorAll('.toast')].find(x => /worktree/.test(x.textContent)) || document.querySelector('.toast'); if (!t) return null; const b = t.querySelector('.toast-action'); return { text: t.querySelector('.toast-message').textContent, button: b ? b.textContent : null, error: t.classList.contains('error'), close: !!t.querySelector('.toast-close'),
                live: document.getElementById('notice-live').textContent, alert: document.getElementById('notice-alert').textContent }; })()`);

            console.log(`${tag}: a kept worktree`);
            await stop(KEPT);
            let t = await toast();
            check(`${tag}: the notice says why and where, with a Copy path button and a close`, t && /was kept: it has uncommitted changes\. It is at \/tmp\/wt\/team-k/.test(t.text) && t.button === 'Copy path' && t.close, JSON.stringify(t));
            check(`${tag}: announced once, as a status (not an alert)`, t.live === t.text && t.alert === '', JSON.stringify([t.live, t.alert]));
            check(`${tag}: the whole message is on screen, the path included (not clipped to two lines)`, await ev(`(() => { const m = [...document.querySelectorAll('.toast')].find(x => /worktree/.test(x.textContent)).querySelector('.toast-message'); return m.scrollHeight <= m.clientHeight + 1 && m.getBoundingClientRect().height > 0; })()`));
            await sleep(9000); // sleep-ok: a toast of this length faded in about 6 s; this one must still be there
            t = await toast();
            check(`${tag}: it is still on screen after the time a toast of its length stayed`, !!t && t.button === 'Copy path', JSON.stringify(t));
            await shot(`worktree-notice-${w}`);
            check(`${tag}: the focus went to its Copy path button when the answer came to the person's own Kill (the column is last in Tab order)`, await ev(`document.activeElement && document.activeElement.classList.contains('toast-action') && document.activeElement.textContent === 'Copy path'`), await ev(`document.activeElement && (document.activeElement.id || document.activeElement.className || document.activeElement.tagName)`));
            await ev(`document.querySelector('.toast-action').click(); true`);
            await waitFor(`window.__copied !== null`, 'the path to be copied', 4000);
            t = await toast();
            check(`${tag}: Copy path copies the path and leaves the notice open (the button says Copied)`, (await ev(`window.__copied`)) === PATH && !!t && t.button === 'Copied', JSON.stringify(t));
            await ev(`[...document.querySelectorAll('.toast')].find(x => /worktree/.test(x.textContent)).querySelector('.toast-close').click(); true`);
            check(`${tag}: the close dismisses it`, (await toast()) === null);
            await waitFor(`document.activeElement && document.activeElement !== document.body`, 'the focus to come back after the notice closed', 3000);
            check(`${tag}: and the focus does not fall to the page: it goes back to the Agents list or the message box`, await ev(`(() => { const a = document.activeElement; return !!a && a !== document.body && !!(a.matches('input.sidebar-search-input, #command-input') || a.closest('#live-sessions-list')); })()`), await ev(`(() => { const a = document.activeElement; return a ? (a.id || a.className || a.tagName) : 'none'; })()`));

            console.log(`${tag}: could not be removed, removed, and one agent`);
            await stop({ ok: true, sessions_killed: 1, worktree: { state: 'failed', path: PATH, branch: 'coral-team/team-k', reason: 'the worktree is locked' } });
            t = await toast();
            check(`${tag}: a worktree that could not be removed stays too, as an alert, with its path to copy`, t && t.error && t.button === 'Copy path' && /could not be removed: the worktree is locked\. It is at \/tmp\/wt\/team-k/.test(t.text) && t.alert === t.text, JSON.stringify(t));
            await ev(`[...document.querySelectorAll('.toast')].find(x => /worktree/.test(x.textContent)).querySelector('.toast-close').click(); true`);
            await stop({ ok: true, sessions_killed: 1, worktree: { state: 'removed', path: PATH, branch: 'coral-team/team-k' } });
            t = await toast();
            check(`${tag}: a removed worktree is a plain toast with no button`, t && t.button === null && t.text === 'The team stopped and its worktree was removed.', JSON.stringify(t));
            await ev(`document.querySelectorAll('.toast').forEach(x => x.remove()); ['notice-live', 'notice-alert'].forEach(id => { const r = document.getElementById(id); if (r) r.textContent = ''; }); window.__copied = null; localStorage.clear(); T.state.liveSessions = []; window.__m.agents = {};
                window.__m.add('solo', 'K', 3, { sleeping: false, session_id: 'o1' }); T.state.liveSessions.forEach(s => { s.branch = 'coral-team/solo'; s.git_state = { is_worktree: true }; });
                T.render.renderLiveSessions(T.state.liveSessions); window.__answer = ${JSON.stringify({ ok: true, worktree: { state: 'kept', path: '/tmp/wt/solo', branch: 'coral-team/solo', reason: 'it has uncommitted changes' } })}; T.render.killSessionDirect('solo', 'claude', 'o1'); true`);
            await waitFor(open, 'the Kill Session confirmation', 8000); await ev(`document.getElementById('confirm-modal-yes').click(); true`);
            await waitFor(`!!document.querySelector('.toast .toast-action')`, 'the single kill\'s notice', 6000);
            t = await toast();
            check(`${tag}: a single agent's kept worktree stays with its own path`, t && /The agent was killed\. Its worktree was kept/.test(t.text) && t.text.includes('/tmp/wt/solo') && t.button === 'Copy path', JSON.stringify(t));
            await ev(`document.querySelectorAll('.toast').forEach(x => x.remove()); true`);
            console.log(`${tag}: several kept worktrees`);
            for (const n of [1, 2, 3]) await stop({ ok: true, sessions_killed: 1, worktree: { state: 'kept', path: `/tmp/wt/team-${n}`, branch: `coral-team/team-${n}`, reason: 'it has uncommitted changes' } }, true);
            const stacked = await ev(`[...document.querySelectorAll('.toast.worktree-notice')].map(x => x.querySelector('.toast-message').textContent.match(/\\/tmp\\/wt\\/team-\\d/)[0])`);
            check(`${tag}: ${mobile ? 'a phone keeps the newest two kept-worktree notices (the oldest goes)' : 'a desktop keeps all three'}`, mobile ? stacked.join() === '/tmp/wt/team-2,/tmp/wt/team-3' : stacked.join() === '/tmp/wt/team-1,/tmp/wt/team-2,/tmp/wt/team-3', JSON.stringify(stacked));
            await ev(`document.querySelectorAll('.toast').forEach(x => x.remove()); true`);
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} worktree notice checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
