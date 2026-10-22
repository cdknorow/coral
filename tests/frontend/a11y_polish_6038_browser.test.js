// Accessibility polish from UI Accessibility's 156 pass (#6038): at 390 px the vendor install link in the CLI-missing note is a target of at least 24 px, the Workflow switch's label row (Team settings)
// is at least 24 px (it is 44 px, as the other switches), and the top-bar Settings menu opened with Enter takes the focus into the menu, where Escape closes it and gives the focus back to its button.
// The routes are stubbed; the pieces are drawn by the app's own modules.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a layout settle after a redraw; there is no event for it
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const code = { Escape: 27, Enter: 13 };
    const key = async k => { const o = { key: k, code: k, windowsVirtualKeyCode: code[k] }; await client.Input.dispatchKeyEvent({ type: 'keyDown', text: k === 'Enter' ? '\r' : undefined, ...o }); await client.Input.dispatchKeyEvent({ type: 'keyUp', ...o }); };
    const team = 'a11y6038-' + Date.now().toString(36);
    const load = async w => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: 800, deviceScaleFactor: 1, mobile: w < 600 });
        await Emulation.setTouchEmulationEnabled({ enabled: w < 600 });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
    };
    const stub = () => ev(`(() => { const R0 = window.fetch; const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
        window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = (o.method || 'GET').toUpperCase(); const p = url.pathname;
            if (p === '/api/system/cli-check') return json({ found: false, configured: false, install_url: 'https://code.claude.com/docs/en/overview', install_command: 'npm install -g @anthropic-ai/claude-code' });
            if (p.startsWith('/api/board/') && p.endsWith('/working-mode')) return json({ mode: 'none', dependency_guidance: false, custom_instructions: '' });
            if (p.startsWith('/api/board/') && p.endsWith('/working-mode/presets')) return json({ presets: [{ id: 'shared_checkout', name: 'Shared checkout', builtin: true, instructions: 'x' }] });
            if (p.startsWith('/api/board/') && p.endsWith('/settings')) return json({ code: 'mode_too_low', error: 'x' }, 403);
            if (p === '/api/settings/prompt-inspection') return json({ code: 'not_found', error: 'x' }, 404);
            if (p === '/api/settings' && m === 'GET') return json({ settings: { board_health_monitor: 'true' } });
            return R0(u, o); }; return true; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const w of [390, 1280]) {
            console.log(`-- ${w} px`);
            await load(w); await stub();
            // 1. the Settings menu by Enter
            await ev(`document.querySelector('[aria-controls="top-bar-settings-menu"]').focus(); true`);
            await key('Enter'); await settle(300);
            const open = await ev(`(() => { const m = document.getElementById('top-bar-settings-menu'), a = document.activeElement; return { shown: !!m && m.style.display !== 'none' && m.getClientRects().length > 0, inMenu: !!a && !!a.closest('#top-bar-settings-menu'), item: !!a && a.classList.contains('overflow-menu-item') }; })()`);
            check(`${w}: Enter on the Settings button opens the menu and the focus is on its first item (${JSON.stringify(open)})`, open.shown && open.inMenu && open.item);
            await key('Escape'); await settle(300);
            const shut = await ev(`(() => { const m = document.getElementById('top-bar-settings-menu'); return { closed: !m || m.style.display === 'none', onButton: document.activeElement === document.querySelector('[aria-controls="top-bar-settings-menu"]') }; })()`);
            check(`${w}: Escape closes it and the focus is back on the button (${JSON.stringify(shut)})`, shut.closed && shut.onButton);
            // 2. the install link
            await ev(`(async () => { const el = document.createElement('div'); el.id = 'qa-cli'; el.className = 'acf-cli-warning'; document.body.appendChild(el); const m = await import(${S} + 'prerequisites.js'); await m.checkAgentCLI('claude', el, { force: true }); return true; })()`);
            await waitFor(`!!document.querySelector('#qa-cli .cli-install-link a')`, `${w}: the install link`, 8000);
            const link = await ev(`(() => { const a = document.querySelector('#qa-cli .cli-install-link a'); const r = a.getBoundingClientRect(); return { h: Math.round(r.height), w: Math.round(r.width), text: a.textContent }; })()`);
            check(`${w}: the vendor install link is a target of at least ${Number(w) <= 767 ? 44 : 24} px (${JSON.stringify(link)})`, link.h >= (Number(w) <= 767 ? 44 : 24) && link.w >= 24); // 44 px on a phone (#6094)
            // 2b. the sidebar's search box shows a ring on focus, not only a border colour (#6094)
            const ring = await ev(`(() => { const i = document.getElementById('agent-search-input'); if (!i || !i.getClientRects().length) return null; i.focus(); const cs = getComputedStyle(i); return { style: cs.outlineStyle, width: parseFloat(cs.outlineWidth) }; })()`);
            if (ring) check(`${w}: the sidebar search has a visible focus ring (${JSON.stringify(ring)})`, ring.style !== 'none' && ring.width >= 2);
            // 3. the Workflow switch
            await ev(`window.__m.add('p-bot', 'A', 4, { sleeping: false, board_project: ${JSON.stringify(team)} }); true`); await settle(600);
            await ev(`window.selectLiveSession('p-bot', 'claude', 'p-bot-id', 'local'); true`); await settle(900);
            await ev(`(async () => { const m = await import(${S} + 'team_working_mode.js'); await m.showTeamWorkingMode(${JSON.stringify(team)}, { workspace: true }); window.switchAgenticTab('team-settings', 'top'); return true; })()`);
            await waitFor(`!!document.querySelector('#agentic-panel-team-settings .working-mode-form input[name="dependency_guidance"]')`, `${w}: the Workflow panel`, 10000);
            if (w < 600) { await ev(`(() => { const p = document.getElementById('agentic-state'); if (p && p.getClientRects().length === 0) { const b = document.querySelector('button[title="Toggle side panel"]'); b && b.click(); } return true; })()`); await settle(500); }
            const sw = await ev(`(() => { const i = document.querySelector('#agentic-panel-team-settings input[name="dependency_guidance"]'), l = i.closest('label'); const r = l.getBoundingClientRect(); return { label: Math.round(r.height), text: l.textContent.trim() }; })()`);
            check(`${w}: the Workflow switch's label row is at least 24 px tall at a phone's width (${JSON.stringify(sw)})`, w >= 600 || sw.label >= 24);
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        assert.equal(failed, 0, failed + ' checks failed');
    } finally { await removeBoards(ev, [team]).catch(() => {}); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
