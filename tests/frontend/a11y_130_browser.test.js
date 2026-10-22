// A keyboard and screen-reader pass over the 129-131 UI (#5371): the filter icon panels of Tasks and Team Artifacts (Enter opens, focus goes in, Escape closes and returns to the icon), the notice pills'
// live region (announces once), the docked preview's comments toggle and sheet (name starts with what it shows, Escape closes the sheet and focus returns to the toggle), the owner refusal messages (status
// regions) and the Updating badge (named, not focusable). Real key events over CDP against the isolated dev server; the comments, owner routes and hub status are stood in for.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { installAudit, setTheme } = require('./helpers/contrast');
const { STUB: COMMENTS_STUB, HEX } = require('./helpers/comments_scale');
const PANELS = require('./helpers/list_bars_panels');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

const CONTENT = ['# Pricing', '', 'The starter plan costs nine dollars a month and includes three seats for the whole team.', '', 'The team plan costs thirty dollars a month and includes twenty seats and the audit log.'].join('\n');
const FX = {
    filename: 'pricing.md', content: CONTENT, size: CONTENT.length, mediaType: 'text/markdown',
    comments: [
        { id: 1, parent_id: null, anchor: { quote: 'costs nine dollars a month', prefix: '', suffix: '' }, body: 'Is this per seat?', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 3600000).toISOString() },
        { id: 2, parent_id: 1, anchor: null, body: 'Per team.', author: { kind: 'hub', issuer: 'coral-hub', subject: 'hub-user-3f2a', name: 'Reviewer' }, created_at: new Date(Date.now() - 60000).toISOString() },
        { id: 3, parent_id: null, anchor: { quote: 'costs thirty dollars a month', prefix: '', suffix: '' }, body: 'Annual discount?', author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(Date.now() - 7200000).toISOString() },
    ],
};

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a layout settle after a tap or a redraw; there is no event for it
    const ctx = { ev, waitFor, Page, Emulation, client, install, BASE, assert, settle: (c, l) => waitFor(c, l, 10000) };
    const view = async (w, theme) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: 800, deviceScaleFactor: 1, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
    };
    const load = async (w, theme) => {
        await view(w, theme);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, theme);
    };
    const team = 'a11y-' + Date.now().toString(36);
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const code = { Tab: 9, Escape: 27, Enter: 13 };
    const key = async k => { const o = { key: k, code: k, windowsVirtualKeyCode: code[k] }; await client.Input.dispatchKeyEvent({ type: 'keyDown', text: k === 'Enter' ? '\r' : undefined, ...o }); await client.Input.dispatchKeyEvent({ type: 'keyUp', ...o }); };
    const active = () => ev(`(() => { const a = document.activeElement; if (!a || a === document.body) return { none: true }; return { id: a.id, cls: String(a.className), inMenu: !!a.closest('.list-bar-menu'), onScreen: a.getClientRects().length > 0, outline: getComputedStyle(a).outlineStyle !== 'none' && parseFloat(getComputedStyle(a).outlineWidth) >= 2 }; })()`);
    const stubBoard = () => ev(`(() => { const R0 = window.fetch; const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
        window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = (o.method || 'GET').toUpperCase(); const p = url.pathname;
            if (p.startsWith('/api/board/') && p.endsWith('/working-mode')) return json({ mode: 'none', dependency_guidance: false, custom_instructions: '' });
            if (p.startsWith('/api/board/') && p.endsWith('/working-mode/presets')) return json({ presets: [{ id: 'shared_checkout', name: 'Shared checkout', builtin: true, instructions: 'x' }] });
            if (p.startsWith('/api/board/') && p.endsWith('/settings')) return json({ code: 'mode_too_low', error: 'x' }, 403);
            if (p === '/api/settings/prompt-inspection') return json({ code: 'not_found', error: 'x' }, 404);
            if (p === '/api/settings' && m === 'GET') return json({ settings: { board_health_monitor: 'true' } });
            return R0(u, o); }; return true; })()`);
    const openPreview = async () => {
        await ev(COMMENTS_STUB(FX));
        await ev(`(async () => { const st = ${S}; const { state } = await import(st + 'state.js'); window.PP = await import(st + 'preview_pane.js'); const { showView } = await import(st + 'utils.js');
            state.currentSession = { type: 'live', name: 'boss', session_id: 'boss-sid', agent_type: 'claude' }; showView('live-session-view'); window.PP.setPreviewSession('boss-sid');
            window.PP.openArtifactTab('coral://artifacts/' + ${JSON.stringify(HEX)}, { name: 'pricing.md', mediaType: 'text/markdown' }); return true; })()`);
        await waitFor(`!!document.querySelector('#preview-body .preview-pane-markdown') && document.querySelectorAll('#preview-main .ac-card').length > 0`, 'the document and its comments', 10000); await settle(500);
    };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        for (const k of ['tasks', 'artifacts']) await PANELS.find(p => p.key === k).prepare?.(ctx);
        // 1. the filter icon panels
        console.log('1. the filter icon panels');
        for (const k of ['tasks', 'artifacts']) {
            const root = k === 'tasks' ? '#agentic-state' : '#agentic-panel-team-artifacts';
            await PANELS.find(p => p.key === k).show(ctx, { w: 1280, theme: 'light', phone: false }); await settle(400);
            const t = await ev(`(() => { const b = document.querySelector('${root} .list-bar-toggle'), m = document.querySelector('${root} .list-bar-menu'); return { name: b.getAttribute('aria-label'), ctl: b.getAttribute('aria-controls'), menu: m.id, role: m.getAttribute('role'), label: m.getAttribute('aria-label') }; })()`);
            check(`${k}: the icon is named with its state, points at its panel, which is a labelled group (${JSON.stringify(t)})`, /^Filters and sort/.test(t.name) && t.ctl === t.menu && t.role === 'group' && /Filters and sort/.test(t.label));
            await ev(`(() => { const b = document.querySelector('${root} .list-bar-toggle'); if (!document.querySelector('${root} .list-bar-menu').hidden) b.click(); b.focus(); return true; })()`); await settle(200);
            await key('Enter'); await settle(300);
            const o = await ev(`(() => { const m = document.querySelector('${root} .list-bar-menu'); return { hidden: m.hidden, exp: document.querySelector('${root} .list-bar-toggle').getAttribute('aria-expanded'), inside: m.contains(document.activeElement) }; })()`);
            check(`${k}: Enter on the icon opens the panel, says so, and moves focus into it (${JSON.stringify(o)})`, !o.hidden && o.exp === 'true' && o.inside);
            await key('Tab'); const a1 = await active();
            check(`${k}: Tab goes on through the panel with a visible focus ring (${JSON.stringify(a1)})`, !a1.none && a1.onScreen && a1.outline);
            await key('Escape'); await settle(300);
            const c = await ev(`({ hidden: document.querySelector('${root} .list-bar-menu').hidden, exp: document.querySelector('${root} .list-bar-toggle').getAttribute('aria-expanded'), onToggle: document.activeElement === document.querySelector('${root} .list-bar-toggle') })`);
            check(`${k}: Escape closes the panel and focus is back on the icon (${JSON.stringify(c)})`, c.hidden && c.exp === 'false' && c.onToggle);
        }
        // 2. the notice pills' live region
        console.log('2. the notice pills');
        await load(1280, 'light');
        await ev(`window.__m.add('p-bot', 'A', 4, { sleeping: false }); true`); await settle(500);
        await ev(`window.selectLiveSession('p-bot', 'claude', 'p-bot-id', 'local'); true`); await settle(1000);
        const stack = await ev(`(() => { const s = document.querySelector('.live-pills-stack'); return { live: s.getAttribute('aria-live'), role: s.getAttribute('role'), kids: [...s.children].map(c => c.id) }; })()`);
        check(`the pills live in one polite status region that is always on the page (${JSON.stringify(stack)})`, stack.live === 'polite' && stack.role === 'status' && stack.kids.includes('waiting-banner') && stack.kids.includes('server-unreachable-banner'));
        await ev(`window.__muts = []; new MutationObserver(m => { for (const x of m) window.__muts.push(x.type === 'attributes' ? 'hidden' : 'text'); }).observe(document.querySelector('.live-pills-stack'), { childList: true, characterData: true, subtree: true, attributes: true, attributeFilter: ['hidden'] }); true`);
        await ev(`import(${S} + 'dom_write.js').then(m => { m.setPill(document.getElementById('waiting-banner'), true, 'waiting text'); return true; })`); await settle(2500);
        const first = await ev('window.__muts.slice()');
        await ev(`import(${S} + 'dom_write.js').then(m => { m.setPill(document.getElementById('waiting-banner'), true, 'waiting text'); m.setPill(document.getElementById('waiting-banner'), true, 'waiting text'); return true; })`); await settle(800);
        const again = (await ev('window.__muts.length')) - first.length;
        check(`a pill that shows is announced once (${first.join(', ')}), and the same text set again changes nothing (${again})`, first.filter(x => x === 'text').length === 1 && again === 0);

        // 3. the preview pane's comments toggle and sheet (a pane narrower than 720 px draws the sheet)
        console.log('3. the comments toggle and sheet');
        await load(390, 'light'); await openPreview();
        const tg = await ev(`(() => { const t = document.querySelector('#preview-toolbar .ac-toggle'); return { text: t.textContent.trim(), name: t.getAttribute('aria-label'), exp: t.getAttribute('aria-expanded'), tag: t.tagName }; })()`);
        check(`the toggle is a button whose name starts with its visible words (${JSON.stringify(tg)})`, tg.tag === 'BUTTON' && tg.name.startsWith(tg.text) && tg.exp === 'false');
        await ev(`document.querySelector('#preview-toolbar .ac-toggle').focus(); true`); await key('Enter'); await settle(600);
        const op = await ev(`(() => { const t = document.querySelector('#preview-toolbar .ac-toggle'); return { exp: t.getAttribute('aria-expanded'), text: t.textContent.trim(), name: t.getAttribute('aria-label'), open: document.querySelector('#preview-main .ac-panel').classList.contains('ac-sheet-open') }; })()`);
        check(`Enter opens the sheet; the toggle says so and still starts its name with its words (${JSON.stringify(op)})`, op.open && op.exp === 'true' && op.name.startsWith(op.text));
        await key('Tab'); const inSheet = await ev(`!!document.activeElement.closest('#preview-main .ac-panel')`);
        check(`Tab from the toggle goes into the open sheet (${inSheet})`, inSheet);
        await key('Escape'); await settle(500);
        const cl = await ev(`(() => { const t = document.querySelector('#preview-toolbar .ac-toggle'); return { open: document.querySelector('#preview-main .ac-panel').classList.contains('ac-sheet-open'), exp: t.getAttribute('aria-expanded'), onToggle: document.activeElement === t, inert: document.querySelector('#preview-main .ac-panel').inert }; })()`);
        check(`Escape closes the sheet, the toggle says so, focus is on the toggle and the shut sheet is out of reach (${JSON.stringify(cl)})`, !cl.open && cl.exp === 'false' && cl.onToggle && cl.inert);

        // 4. the owner refusal messages
        console.log('4. the owner refusal messages');
        const team = 'a11y-' + Date.now().toString(36);
        await load(1280, 'light'); await stubBoard();
        await ev(`window.__m.add('p-bot', 'A', 4, { sleeping: false, board_project: ${JSON.stringify(team)} }); true`); await settle(600);
        await ev(`window.selectLiveSession('p-bot', 'claude', 'p-bot-id', 'local'); true`); await settle(900);
        await ev(`(async () => { const m = await import(${S} + 'team_working_mode.js'); await m.showTeamWorkingMode(${JSON.stringify(team)}, { workspace: true }); window.switchAgenticTab('team-settings', 'top'); return true; })()`);
        await waitFor(`!!document.querySelector('#agentic-panel-team-settings .working-mode-form') && document.querySelector('#agentic-panel-team-settings .working-mode-form').dataset.initialState !== undefined`, 'the team settings', 10000); await settle(500);
        const ref = await ev(`(() => { const n = document.querySelector('#team-settings-messaging .ct-unavailable'), i = document.querySelector('.prompt-inspection-content'); return { note: n && { role: n.getAttribute('role'), shown: !n.hidden && n.getClientRects().length > 0 }, inspection: i && i.getAttribute('aria-live'), switches: [...document.querySelectorAll('#agentic-panel-team-settings [name=cross_team_messaging], #agentic-panel-team-settings [name=cross_team_artifacts]')].map(e => e.disabled) }; })()`);
        check(`the refusal note (on the Messaging tab) and the Prompt inspection message are live regions, and there are no switches to change when the settings could not be read (${JSON.stringify(ref)})`, ref.note && ref.note.role === 'status' && ref.inspection === 'polite' && ref.switches.length === 0);

        // 5. the Updating badge
        console.log('5. the Updating badge');
        await load(1280, 'light');
                await ev(`import((window.__coralStatic || '/static/') + 'update_flag.js').then(m => { m.setUpdatesEnabled(true); return true; })`);
        await ev(`(() => { const R = window.fetch; const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname; if (p === '/api/servers') return json({ servers: [{ id: 'm1', label: 'Alpha', online: true, access_mode: 'launch', local_mode: 'launch', created_at: '2026-10-01T00:00:00Z', last_seen: '2026-10-20T08:00:00Z' }] }); if (/\\/api\\/system\\/update$/.test(p)) return json({ relaunching: true }); return R(u, o); };
            const h = document.createElement('div'); h.id = 'rb-host'; document.body.appendChild(h); return true; })()`);
        await ev(`import(${S} + 'cloud_servers.js').then(m => m.renderCloudServersInto(document.getElementById('rb-host')))`);
        await waitFor(`!!document.querySelector('.server-relaunch-badge')`, 'the badge', 5000);
        const bd = await ev(`(() => { const b = document.querySelector('.server-relaunch-badge'); return { role: b.getAttribute('role'), text: b.textContent.trim(), name: b.getAttribute('aria-label'), tab: b.tabIndex }; })()`);
        check(`the badge is an image named from its visible word and not a tab stop (${JSON.stringify(bd)})`, bd.role === 'img' && bd.name.startsWith(bd.text) && bd.tab === -1);

        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        assert.equal(failed, 0, failed + ' checks failed');
    } finally { await removeBoards(ev, [ctx.TBOARD, team].filter(Boolean)).catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
