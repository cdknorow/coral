// A phone pass over the views changed on Oct 19 (#5219, the operator uses Coral on a phone): at 320 and 390 px, both themes, with touch: the agent list with the Show branches and Show agent type options, the agent
// page (the capped branch, the model and thinking chips, the composer), Team settings (every tab), the preview in the sheet and full screen, and the artifact viewer with its comments sheet and composer. In each: nothing wider
// than the screen, every control 44 px, the viewer's title readable and its sheet header on one row, a comment composer not under the keyboard (the visual viewport shrinks; a fake one drives mobile.js), and the back
// gesture closing the overlays from the top: the comments sheet, then the viewer, then the preview's full screen. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme } = require('./helpers/contrast');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;
const HEX = 'd'.repeat(64);
const MD = ['# Migration plan', '', '## Steps', '', '1. Add the new columns behind a flag and backfill in batches of 500 rows.', '2. Switch reads to the new columns.', '', '## Risks', '', 'The backfill takes a write lock on large databases, so it must run off-peak.', '', 'Add a flag. Then wait. Add a flag again at the end.'].join('\n');
const LONG = 'worktree-agent-a1b2c3d4e5f60718/feature/calm-the-dashboard-remove-dot-rings-and-honour-reduced-motion';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const STUBFETCH = `(() => {
        const real = window.fetch; window.__calls = []; window.__cs = [
            { id: 1, parent_id: null, anchor: { quote: 'backfill in batches of 500 rows.', prefix: 'columns behind a flag and', suffix: '2. Switch', start_line: 5, end_line: 5 }, body: 'Batches of 500 is too small. Try 5,000.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 120000).toISOString() },
            { id: 2, parent_id: 1, anchor: null, body: 'Agreed. Last run took 9 s per 5,000.', author: { kind: 'hub', issuer: 'coral-hub', subject: 'hub-user-3f2a' }, created_at: new Date(Date.now() - 60000).toISOString() },
            { id: 3, parent_id: null, anchor: { quote: 'takes a write lock on large databases', prefix: 'The backfill', suffix: ', so it must run' }, body: 'Define off-peak: 02:00 to 05:00 UTC?', author: { kind: 'hub', issuer: 'coral-hub', subject: 'hub-user-3f2a' }, created_at: new Date(Date.now() - 3600000).toISOString() },
            { id: 4, parent_id: null, anchor: { quote: 'Run the backfill nightly until it is done.' }, body: 'This step was reworded.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 259200000).toISOString() },
            { id: 5, parent_id: null, anchor: { quote: 'Switch reads to the new columns.' }, body: 'Fine.', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 86400000).toISOString(), resolved_at: new Date().toISOString(), resolved_by: { issuer: 'local', subject: 'Operator' } },
        ]; window.__next = 6; window.__role = 'owner'; window.__locked = false; window.__missing = false;
        const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
        window.fetch = (u, o) => {
            const url = String(u), method = (o && o.method) || 'GET';
            if (url.includes('/api/artifacts/${HEX}/comments')) {
                window.__calls.push({ method, url, body: o && o.body ? JSON.parse(o.body) : null });
                if (window.__missing) return json({ error: 'not found' }, 404);
                const me = window.__role, mine = c => (me === 'owner' && c.author.kind === 'owner'), sub = url.split('/comments')[1].split('?')[0];
                const can = c => ({ reply: me === 'owner' && !window.__locked && c.parent_id == null, edit: mine(c) && !window.__locked, delete: mine(c), resolve: me === 'owner' && !window.__locked && c.parent_id == null });
                if (method === 'GET') return json({ comments: window.__cs.filter(c => !c.deleted_at || window.__cs.some(r => r.parent_id === c.id && !r.deleted_at)).map(c => ({ ...c, deleted: !!c.deleted_at, body: c.deleted_at ? '' : c.body, mine: mine(c), can: can(c) })), locked: window.__locked, you: { kind: me === 'owner' ? 'owner' : 'hub', name: me === 'owner' ? 'Operator' : 'hub-user-3f2a', can_comment: me === 'owner' && !window.__locked, reason: me === 'owner' ? '' : 'Your Coral Hub access is view only.', can_lock: me === 'owner' }, limits: { body: 8192, quote: 2048, comments: 500 } });
                if (me !== 'owner' && sub !== '') return json({ error: 'Your Coral Hub access is view only.' }, 403);
                if (method === 'POST' && sub === '') { const b = JSON.parse(o.body); const c = { id: window.__next++, parent_id: b.parent_id == null ? null : b.parent_id, anchor: b.anchor, body: b.body, author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date().toISOString() }; window.__cs.push(c); return json({ ...c, can: can(c) }, 201); }
                const m = /^\\/(\\d+)(\\/(resolve|reopen))?$/.exec(sub); const c = m && window.__cs.find(x => x.id === Number(m[1]));
                if (m && c) {
                    if (method === 'PATCH') { c.body = JSON.parse(o.body).body; c.edited_at = new Date().toISOString(); return json({ ...c, can: can(c) }); }
                    if (method === 'DELETE') { if (!mine(c)) return json({ error: 'Only the author can delete a comment.' }, 403); c.deleted_at = new Date().toISOString(); return json({ ok: true }); }
                    if (m[3] === 'resolve') { c.resolved_at = new Date().toISOString(); return json({ ...c, can: can(c) }); }
                    if (m[3] === 'reopen') { delete c.resolved_at; return json({ ...c, can: can(c) }); }
                }
                if (sub === '/lock') { window.__locked = method === 'PUT'; return json({ ok: true }); }
                return json({ error: 'nope' }, 404);
            }
            if (url.includes('/api/artifacts/${HEX}')) return Promise.resolve(new Response(${JSON.stringify(MD)}, { status: 200, headers: { 'Content-Type': 'text/markdown' } }));
            return real(u, o);
        };
        return true; })()`;
    const AUDIT = `(root) => { const R = root ? document.querySelector(root) : document.body; if (!R) return { missing: root };
        const vis = e => { const r = e.getBoundingClientRect(); if (r.width <= 0 || r.height <= 0) return false; const cs = getComputedStyle(e); return cs.visibility !== 'hidden' && cs.display !== 'none' && cs.opacity !== '0'; };
        const clipX = e => { for (let p = e.parentElement; p && p !== document.body; p = p.parentElement) { const o = getComputedStyle(p).overflowX; if (o === 'auto' || o === 'scroll' || o === 'hidden' || o === 'clip') { const pr = p.getBoundingClientRect(); if (pr.right <= innerWidth + 1) return true; } } return false; };
        const label = e => (e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') + (typeof e.className === 'string' && e.className ? '.' + e.className.trim().split(/\\s+/).slice(0, 2).join('.') : '') + ' "' + (e.getAttribute('aria-label') || e.title || e.textContent || '').trim().replace(/\\s+/g, ' ').slice(0, 24) + '"');
        const wide = [], small = [];
        const inView = r => r.bottom > 0 && r.top < innerHeight;
        for (const e of R.querySelectorAll('*')) { if (!vis(e)) continue; const r = e.getBoundingClientRect(); if (!inView(r)) continue;
            if ((r.right > innerWidth + 1 || r.left < -1) && !clipX(e)) wide.push(label(e) + ' [' + Math.round(r.left) + '..' + Math.round(r.right) + ']');
            if (e.matches('button, a[href], select, summary, [role=button], [role=tab], input:not([type=hidden]):not([type=checkbox]):not([type=radio]), textarea') && !e.disabled && Math.min(r.width, r.height) < 44 - 0.5) small.push(label(e) + ' ' + Math.round(r.width) + 'x' + Math.round(r.height)); }
        return { over: document.documentElement.scrollWidth - document.documentElement.clientWidth, wide: wide.slice(0, 12), small: small.slice(0, 20), nSmall: small.length }; }`;
    const settle = (ms, quietMs = 120) => require('./helpers/wait').settle(ev, ms, quietMs); // the page's geometry still for quietMs, or ms at most (helpers/wait.js); not a fixed sleep
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name.replace(/[^a-z0-9-]+/gi, '-') + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const tapSel = async sel => { const r = await ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return null; const b = e.getBoundingClientRect(); return { x: b.left + b.width / 2, y: b.top + b.height / 2 }; })()`); assert.ok(r, 'something to tap: ' + sel); await Input.dispatchTouchEvent({ type: 'touchStart', touchPoints: [r] }); await Input.dispatchTouchEvent({ type: 'touchEnd', touchPoints: [] }); await settle(450); };
    const audit = async (tag, root) => {
        const a = await ev(`(${AUDIT})(${JSON.stringify(root || null)})`);
        check(`${tag}: nothing wider than the screen (page overflow ${a.over}, wide: ${JSON.stringify(a.wide)})`, a.over <= 0 && a.wide.length === 0);
        check(`${tag}: every control is 44 px (${JSON.stringify(a.small)})`, a.small.length === 0);
        await shot(tag);
    };
    const setup = async (w, h, theme) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`localStorage.clear(); true`);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await setTheme(ev, theme);
        await ev(STUBFETCH);
    };
    const TEAM = 'pass-team-' + Date.now().toString(36) + Math.floor(Math.random() * 1296).toString(36);
    const scene = () => ev(`(async () => {
        const entry = window.__m.entry.bind(window.__m); window.__m.entry = a => ({ ...entry(a), model: 'claude-opus-4-1-20250805', branch: ${JSON.stringify(LONG)}, repo_name: '', git_state: { is_worktree: true, base_branch: 'main' } });
        T.state.liveSessions = []; window.__m.agents = {};
        window.__m.add('boss', 'A', 3, { sleeping: false, session_id: 'boss-sid' }); window.__m.add('dev-one', 'B', 3, { sleeping: false, session_id: 'dev-sid' });
        T.state.liveSessions.forEach(s => { s.board_name = ${JSON.stringify(TEAM)}; s.board_project = ${JSON.stringify(TEAM)}; });
        T.render.renderLiveSessions(T.state.liveSessions); return T.state.liveSessions.length; })()`);
    const openPreview = async () => {
        await ev(`(async () => { const st = ${S}; const { state } = await import(st + 'state.js'); window.PP = await import(st + 'preview_pane.js'); const { showView } = await import(st + 'utils.js');
            { const real = window.fetch; window.fetch = (u, o) => /\\/api\\/artifacts\\/a{64}/.test(String(u)) ? Promise.resolve(new Response('# Release notes\\n\\n' + 'A line of the notes that is long enough to wrap on a phone.\\n\\n'.repeat(30), { status: 200, headers: { 'Content-Type': 'text/markdown' } })) : real(u, o); }
            state.currentSession = { type: 'live', name: 'boss', session_id: 'boss-sid', agent_type: 'claude' }; showView('live-session-view'); window.PP.setPreviewSession('boss-sid');
            window.PP.openArtifactTab('coral://artifacts/' + 'a'.repeat(64), { name: 'a.md', mediaType: 'text/markdown' }); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('preview-pane')).display !== 'none'`, 'the preview pane', 8000); await settle(500);
    };
    const openViewer = async () => {
        await ev(`(async () => { const v = await import(${S} + 'artifact_viewer.js'); v.openArtifactViewer(${JSON.stringify(HEX)}, { filename: 'plan.md' }); return true; })()`);
        await waitFor(`!!document.querySelector('#artifact-viewer .artifact-viewer-markdown')`, 'the document', 8000); await settle(600);
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        // a visual viewport the suite can shrink, as a phone's keyboard does (Chrome on Android shrinks the visual viewport and leaves the layout viewport): mobile.js reads it
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const hs = {}; const vv = { width: innerWidth, height: innerHeight, offsetTop: 0, offsetLeft: 0, pageTop: 0, pageLeft: 0, scale: 1, addEventListener: (t, f) => (hs[t] = hs[t] || []).push(f), removeEventListener() {} };
            Object.defineProperty(window, 'visualViewport', { value: vv, configurable: true });
            window.__vv = { set(h) { vv.height = h; (hs.resize || []).forEach(f => f()); } }; })()` });
        for (const [w, h] of [[320, 640], [390, 844]]) for (const theme of ['dark', 'light']) {
            const tag = `${w}px ${theme}`;
            console.log(tag);
            // 1. the agent list with the Display options
            await setup(w, h, theme); await scene(); await settle(700);
            await tapSel('.mobile-list-header [data-display-button]');
            await ev(`['branches', 'agenttype'].forEach(k => { const i = document.querySelector('[data-display-input="' + k + '"]'); if (i && !i.checked) i.click(); }); true`); await settle(500);
            await audit(`${tag} the agent list and its Display options`, '#mobile-agent-list');
            // 2. the agent page
            await ev(`window.selectLiveSession('boss', 'claude', 'boss-sid', 'local'); true`); await settle(900);
            await ev(`T.render.updateSessionBranch(${JSON.stringify(LONG)}, '', {}); true`); await settle(400);
            await audit(`${tag} the agent page (header, chips, composer)`, '#live-session-view');
            // 3. Team settings, each tab
            await setup(w, h, theme);
            for (const [id, title] of [['Boss', 'Orchestrator'], ['Dev One', 'Developer']]) await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: ${JSON.stringify(JSON.stringify({ subscriber_id: id, job_title: title, session_name: id.toLowerCase().replace(' ', '-') + '-session' }))} }).then(r => r.status)`);
            await scene(); await settle(700);
            await tapSel('#mobile-session-list .board-card-header .group-kebab-btn');
            await tapSel('#mobile-session-list .board-card-header [data-on-click="team-working-mode"]');
            await waitFor(`!!document.querySelector('#team-settings-workspace [data-settings-tab=health]')`, 'the settings tabs', 15000); await settle(500);
            await audit(`${tag} Team settings, Workflow`, '#team-settings-workspace');
            for (const tab of ['prompts', 'health']) { await tapSel(`#team-settings-workspace [data-settings-tab=${tab}]`); await settle(500); await audit(`${tag} Team settings, ${tab}`, '#team-settings-workspace'); }
            // 4. the preview, in the sheet and full screen
            await setup(w, h, theme); await scene(); await settle(500); await openPreview();
            await audit(`${tag} the preview in the sheet`, '#preview-pane');
            await tapSel('#preview-max-btn'); await settle(500);
            await audit(`${tag} the preview full screen`, '#preview-pane');
            // 5. the artifact viewer and its comments sheet
            await setup(w, h, theme); await openViewer();
            await audit(`${tag} the viewer, sheet shut`, '#artifact-viewer');
            const title = await ev(`(() => { const t = document.querySelector('.artifact-viewer-title'); return { w: t.clientWidth, clipped: t.scrollWidth > t.clientWidth + 1, text: t.textContent }; })()`);
            check(`${tag}: the viewer's title is readable (${title.w} px wide, "${title.text}" whole)`, title.w >= 120 && !title.clipped, JSON.stringify(title));
            await tapSel('#artifact-viewer .ac-toggle');
            await audit(`${tag} the comments sheet`, '#artifact-viewer');
            const head = await ev(`(() => { const t = document.querySelector('#artifact-viewer .ac-title').getBoundingClientRect(), x = document.querySelector('#artifact-viewer .ac-sheet-close').getBoundingClientRect(), f = document.querySelector('#artifact-viewer .ac-showresolved').getBoundingClientRect(); return { sameRow: Math.abs((t.top + t.height / 2) - (x.top + x.height / 2)) < 12, filtersBelow: f.top >= t.bottom - 2 }; })()`);
            check(`${tag}: the sheet's close button is on the title's row and Show resolved has a row of its own`, head.sameRow && head.filtersBelow, JSON.stringify(head));
            await ev(`document.querySelector('#artifact-viewer .ac-whole').click(); true`); await settle(400);
            await ev(`document.querySelector('#artifact-viewer .ac-composer .ac-input').focus(); true`);
            await audit(`${tag} the comment composer`, '#artifact-viewer');
            // the keyboard: the visual viewport shrinks to 55% of the screen; the sheet and its composer stay inside what is left
            const kbH = Math.round(h * 0.55);
            await ev(`window.__vv.set(${kbH}); true`); await settle(500);
            const kb = await ev(`(() => { const v = document.getElementById('artifact-viewer').getBoundingClientRect(), p = document.querySelector('#artifact-viewer .ac-panel').getBoundingClientRect(), t = document.querySelector('#artifact-viewer .ac-composer .ac-input').getBoundingClientRect(), s = document.querySelector('#artifact-viewer .ac-composer .ac-send').getBoundingClientRect(); return { viewerH: Math.round(v.height), panelBottom: Math.round(p.bottom), inputBottom: Math.round(t.bottom), sendBottom: Math.round(s.bottom), inputTop: Math.round(t.top), sendTop: Math.round(s.top), panelTop: Math.round(p.top) }; })()`);
            check(`${tag}: with the keyboard up (${kbH} px left) the viewer fits it, and the sheet and the composer's input and Comment button are inside it`, kb.viewerH <= kbH + 1 && kb.panelBottom <= kbH + 1 && kb.sendBottom <= kbH + 1 && kb.inputTop >= kb.panelTop && kb.sendTop >= 0, JSON.stringify(kb));
            // the Show resolved toggle stays visible above the composer: it is not under it, and it is what a tap at its centre reaches
            const flt = await ev(`(() => { const comp = document.querySelector('#artifact-viewer .ac-composer').getBoundingClientRect(), p = document.querySelector('#artifact-viewer .ac-panel').getBoundingClientRect();
                const fs = [...document.querySelectorAll('#artifact-viewer .ac-showresolved')].map(f => { const r = f.getBoundingClientRect(), top = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); return { w: Math.round(r.width), h: Math.round(r.height), above: r.bottom <= comp.top + 1, reached: !!top && (top === f || f.contains(top)), inPanel: r.top >= p.top - 1 }; });
                return { fs, n: fs.length }; })()`);
            check(`${tag}: with the keyboard up the Show resolved toggle is above the composer and reachable (${JSON.stringify(flt.fs)})`, flt.n === 1 && flt.fs.every(f => f.above && f.reached && f.inPanel && f.h >= 44 && f.w >= 44), JSON.stringify(flt));
            await shot(`${tag} the composer with the keyboard`);
            await ev(`window.__vv.set(${h}); true`); await settle(300);
            // 6. the back gesture, from the top: the sheet, then the viewer, then the preview's full screen
            await setup(w, h, theme); await scene(); await settle(500); await openPreview();
            await tapSel('#preview-max-btn');
            await openViewer();
            await tapSel('#artifact-viewer .ac-toggle');
            const st = () => ev(`({ sheet: document.querySelector('#artifact-viewer .ac-panel').classList.contains('ac-sheet-open'), viewer: !document.getElementById('artifact-viewer').hidden, full: document.getElementById('preview-pane').classList.contains('preview-phone-full'), depth: (history.state && history.state.coralOverlay) || 0 })`);
            let s = await st();
            check(`${tag}: three overlays are open, three history entries (${JSON.stringify(s)})`, s.sheet && s.viewer && s.full && s.depth === 3);
            const back = async () => { await ev(`history.back(); true`); await settle(500); return st(); };
            s = await back(); check(`${tag}: the first Back closes the comments sheet only (${JSON.stringify(s)})`, !s.sheet && s.viewer && s.full);
            s = await back(); check(`${tag}: the second closes the viewer and leaves the preview (${JSON.stringify(s)})`, !s.viewer && s.full);
            s = await back(); check(`${tag}: the third closes the preview's full screen (${JSON.stringify(s)})`, !s.full && s.depth === 0);
            // closed by their own buttons: no entry is left behind
            await openViewer(); await tapSel('#artifact-viewer .ac-toggle'); await tapSel('#artifact-viewer .ac-sheet-close'); await tapSel('.artifact-viewer-close');
            s = await st(); check(`${tag}: closing by the buttons leaves no history entry (${JSON.stringify(s)})`, !s.sheet && !s.viewer && s.depth === 0);
        }
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally { await removeBoards(ev, [TEAM]).catch(() => {}); try { await client.close(); } catch (_) {} }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS phone pass: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
