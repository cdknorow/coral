// A worktree a stop kept stays on screen as one compact bar (#6818, #6859, #6943: the desktop's floating toast was 422 px tall at 200% text and covered the top bar and Settings' Save and Cancel, so the
// notice is the bar of worktree_dock.js on every layout): "Worktree kept" with Copy path, Details and a x on one line, the words behind Details, announced once (a status; an alert when the worktree could not
// be removed), focus to its first button after the person's own Kill and back to the Agents list on dismiss, several as one "N worktrees kept" whose x says "Dismiss all N worktree notices"; a removal is a short
// toast. The real Kill Team and Kill Session flows on a desktop window at 100% and 200% text; the bar never overlaps the top bar or Settings' Save and Cancel. The kill answers are the page's fetch (Backend's
// #6701 shape). The phone's layout is worktree_dock_browser. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
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
        for (const [pct, font] of [[100, ''], [200, '32px']]) {
            const tag = `1280 px, ${pct}% text`;
            await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`${font ? `document.documentElement.style.fontSize = '${font}';` : ''} (() => { const inner = window.__innerFetch || (window.__innerFetch = window.fetch); window.__answer = { ok: true, sessions_killed: 1 }; window.__copied = null;
                navigator.clipboard.writeText = t => { window.__copied = t; return Promise.resolve(); };
                window.fetch = (url, opts = {}) => { if (/\\/api\\/sessions\\/live\\/(?:team\\/)?[^/]+\\/kill/.test(String(url))) return Promise.resolve(new Response(JSON.stringify(window.__answer), { status: 200, headers: { 'content-type': 'application/json' } })); return inner(url, opts); }; return true; })()`);
            const seed = () => ev(`localStorage.clear(); T.state.liveSessions = []; window.__m.agents = {};
                window.__m.add('team-agent', 'K', 3, { sleeping: false, session_id: 't1', board_project: 'team-k' });
                T.state.liveSessions.forEach(s => { s.branch = 'coral-team/team-k'; s.git_state = { is_worktree: true, base_branch: 'master' }; });
                T.render.renderLiveSessions(T.state.liveSessions); true`);
            const clearAll = () => ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); ['notice-live', 'notice-alert'].forEach(id => { const r = document.getElementById(id); if (r) r.textContent = ''; }); true`);
            const stop = async (answer, wait) => {
                await seed();
                await ev(`['notice-live', 'notice-alert'].forEach(id => { const r = document.getElementById(id); if (r) r.textContent = ''; }); true`);
                await ev(`window.__answer = ${JSON.stringify(answer)}; T.render.killBoard('team-k'); true`); await waitFor(open, 'the Kill Team confirmation', 8000);
                await ev(`document.getElementById('confirm-modal-yes').click(); true`);
                await waitFor(wait, 'the notice', 6000);
                await waitFor(`/worktree/.test(((document.getElementById('notice-live') || {}).textContent || '') + ((document.getElementById('notice-alert') || {}).textContent || ''))`, 'the notice to be announced (not a leftover "Path copied.")', 6000);
            };
            const dock = () => ev(`(() => { const d = document.getElementById('worktree-dock'); if (!d || d.hidden) return null; const q = s => d.querySelector(s);
                return { title: (q('.worktree-dock-title') || {}).textContent, buttons: [...d.querySelectorAll('.worktree-dock-line button')].map(b => b.textContent), x: (q('.worktree-dock-dismiss') || {}).getAttribute && q('.worktree-dock-dismiss').getAttribute('aria-label'), error: d.classList.contains('error'),
                    items: [...d.querySelectorAll('.worktree-dock-item')].map(i => ({ text: i.querySelector('.worktree-dock-text').textContent, copy: !!i.querySelector('.worktree-dock-copy') })), focus: document.activeElement && document.activeElement.textContent,
                    live: ((document.getElementById('notice-live') || {}).textContent || '').trim(), alert: ((document.getElementById('notice-alert') || {}).textContent || '').trim(), toasts: [...document.querySelectorAll('.toast')].map(t => t.textContent) }; })()`);
            const DOCK_UP = `!!document.querySelector('#worktree-dock:not([hidden])')`;

            console.log(`${tag}: a kept worktree`);
            await stop(KEPT, DOCK_UP);
            let b = await dock();
            const big = pct === 200;
            check(`${tag}: one compact bar ${big ? '(the title that opens the details, and the x)' : '(Worktree kept, Copy path, Details, x)'}, no toast, the words behind Details`, big ? b.buttons.length === 2 && b.buttons[1] === '×' && b.toasts.length === 0 : b.title === 'Worktree kept' && b.buttons.join() === 'Copy path,Details,×' && b.toasts.length === 0, JSON.stringify(b));
            check(`${tag}: announced once, as a status, with the whole words (why and where)`, /was kept: it has uncommitted changes\. It is at \/tmp\/wt\/team-k, on branch coral-team\/team-k\./.test(b.live) && b.alert === '', JSON.stringify([b.live, b.alert]));
            check(`${tag}: the focus went to its first button when the answer came to the person's own Kill (it is last in Tab order)`, b.focus === (big ? 'Worktree kept' : 'Copy path'), String(b.focus));
            // layout: it overlaps neither the top bar nor Settings' Save and Cancel (Settings open, the bar in the page under its overlay)
            await ev(`window.showSettingsModal('general'); true`);
            await waitFor(`!!document.getElementById('settings-save-btn') && document.getElementById('settings-save-btn').getClientRects().length > 0`, 'Settings', 8000);
            await sleep(400); // sleep-ok: the dialog lays out
            const lay = await ev(`(() => { const r = s => { const e = document.querySelector(s); if (!e || !e.getClientRects().length) return null; const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, left: b.left, right: b.right }; }; const dk = r('#worktree-dock'); const hit = (a, c) => a && c && a.left < c.right - 0.5 && a.right > c.left + 0.5 && a.top < c.bottom - 0.5 && a.bottom > c.top + 0.5;
                const cancel = [...document.querySelectorAll('#settings-modal button')].find(x => /^Cancel$/.test(x.textContent.trim())); const cr = cancel && cancel.getBoundingClientRect();
                return { dock: dk, topbar: r('.top-bar'), save: r('#settings-save-btn'), overlapsTop: hit(dk, r('.top-bar')), overlapsSave: hit(dk, r('#settings-save-btn')), overlapsCancel: !!cr && hit(dk, { top: cr.top, bottom: cr.bottom, left: cr.left, right: cr.right }), height: dk ? Math.round(dk.bottom - dk.top) : null, vh: innerHeight }; })()`);
            check(`${tag}: the bar covers neither the top bar nor Settings' Save and Cancel, and is ${big ? 'at most 100' : 'at most 70'} px tall`, lay.dock && !lay.overlapsTop && !lay.overlapsSave && !lay.overlapsCancel && lay.height <= (big ? 100 : 70), JSON.stringify(lay));
            await shot(`worktree-bar-settings-${pct}`);
            await ev(`window.hideSettingsModal && window.hideSettingsModal(); true`);
            await ev(`document.querySelector('#worktree-dock .worktree-dock-details').click(); true`);
            b = await dock();
            check(`${tag}: Details opens the words and the path`, b.items.length === 1 && /It is at \/tmp\/wt\/team-k/.test(b.items[0].text), JSON.stringify(b.items));
            await ev(`(() => { const c = document.querySelector('#worktree-dock .worktree-dock-copy'); c.click(); return true; })()`);
            await waitFor(`window.__copied !== null`, 'the path to be copied', 4000);
            check(`${tag}: Copy path copies the path and the bar stays`, (await ev(`window.__copied`)) === PATH && !!(await dock()));
            await ev(`document.querySelector('#worktree-dock .worktree-dock-dismiss').focus(); document.querySelector('#worktree-dock .worktree-dock-dismiss').click(); true`);
            check(`${tag}: the x dismisses it`, (await dock()) === null);
            await waitFor(`document.activeElement && document.activeElement !== document.body`, 'the focus to come back', 3000);
            check(`${tag}: and the focus goes back to the Agents list or the message box, not to the page`, await ev(`(() => { const a = document.activeElement; return !!a && a !== document.body && !!(a.matches('input.sidebar-search-input, #command-input') || a.closest('#live-sessions-list')); })()`));

            console.log(`${tag}: could not be removed, removed, and one agent`);
            await stop({ ok: true, sessions_killed: 1, worktree: { state: 'failed', path: PATH, branch: 'coral-team/team-k', reason: 'the worktree is locked' } }, DOCK_UP);
            b = await dock();
            check(`${tag}: a worktree that could not be removed is the bar too, as an alert`, b.error && /could not be removed: the worktree is locked\. It is at \/tmp\/wt\/team-k/.test(b.alert) && !/worktree/.test(b.live), JSON.stringify(b));
            await ev(`document.querySelector('#worktree-dock .worktree-dock-dismiss').click(); true`); await clearAll();
            await stop({ ok: true, sessions_killed: 1, worktree: { state: 'removed', path: PATH, branch: 'coral-team/team-k' } }, `[...document.querySelectorAll('.toast')].some(x => /worktree was removed/.test(x.textContent))`);
            b = await dock();
            check(`${tag}: a removed worktree is a plain toast and the bar stays away`, b === null, JSON.stringify(b));
            await clearAll();
            await ev(`localStorage.clear(); T.state.liveSessions = []; window.__m.agents = {};
                window.__m.add('solo', 'K', 3, { sleeping: false, session_id: 'o1' }); T.state.liveSessions.forEach(s => { s.branch = 'coral-team/solo'; s.git_state = { is_worktree: true }; });
                T.render.renderLiveSessions(T.state.liveSessions); window.__answer = ${JSON.stringify({ ok: true, worktree: { state: 'kept', path: '/tmp/wt/solo', branch: 'coral-team/solo', reason: 'it has uncommitted changes' } })}; T.render.killSessionDirect('solo', 'claude', 'o1'); true`);
            await waitFor(open, 'the Kill Session confirmation', 8000); await ev(`document.getElementById('confirm-modal-yes').click(); true`);
            await waitFor(DOCK_UP, "the single kill's bar", 6000);
            await ev(`document.querySelector('#worktree-dock .worktree-dock-details').click(); true`);
            b = await dock();
            check(`${tag}: a single agent's kept worktree is the bar with its own path`, b.items.length === 1 && /The agent was killed\. Its worktree was kept/.test(b.items[0].text) && b.items[0].text.includes('/tmp/wt/solo'), JSON.stringify(b.items));
            await ev(`document.querySelector('#worktree-dock .worktree-dock-dismiss').click(); true`); await clearAll();

            console.log(`${tag}: several kept worktrees are one bar`);
            for (const n of [1, 2, 3]) await stop({ ok: true, sessions_killed: 1, worktree: { state: 'kept', path: `/tmp/wt/team-${n}`, branch: `coral-team/team-${n}`, reason: 'it has uncommitted changes' } }, `[...document.querySelectorAll('#worktree-dock .worktree-dock-item')].length === ${n}`);
            b = await dock();
            check(`${tag}: one bar, "3 worktrees kept", each in its details with its own Copy path, and the x says it dismisses all`, /^3 worktrees kept/.test(b.title || '') || big, JSON.stringify([b.title, b.x]));
            check(`${tag}: the x is named "Dismiss all 3 worktree notices" and the details list three paths each with Copy path`, b.x === 'Dismiss all 3 worktree notices' && b.items.length === 3 && b.items.every(i => i.copy) && [1, 2, 3].every(n => b.items.some(i => i.text.includes(`/tmp/wt/team-${n}`))), JSON.stringify(b));
            await clearAll();
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} worktree notice checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
