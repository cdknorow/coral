// Coral Hub View mode on phones (#3411). The read-only chip fits the header at 375 and 414 px
// (shrinks with "…", the full reason in its title and on tap), and the composer looks read-only:
// disabled with "View only through Coral Hub", Send greyed out, no "Click to type" hint. Raising
// the access restores all of it.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const width of [375, 414]) {
            await Emulation.setDeviceMetricsOverride({ width, height: 800, deviceScaleFactor: 2, mobile: true });
            await Page.navigate({ url: BASE }); await Page.loadEventFired();
            await install();
            await ev(`(async () => { localStorage.setItem('coral-live-view-mode', 'terminal');
                window.__m.add('ro-a', 'R', 2, { sleeping: false, session_id: 'ro-a-id' });
                window.T.render.renderLiveSessions(window.T.state.liveSessions);
                window.selectLiveSession('ro-a', 'claude', 'ro-a-id', 'local');
                await window.__coralReady.agentSelected('ro-a-id');
                const ha = await import((window.__coralStatic||'/static/') + 'hub_access.js'); window.__ha = ha;
                ha.setHubReadOnly(window.T.state.currentSession, { hub: 'view', effective: 'view', limitedBy: 'hub', label: 'build-box-with-a-long-name.example.internal' });
                return true; })()`);
            const v = await ev(`(() => {
                const chip = document.getElementById('hub-readonly-chip'); const r = chip.getBoundingClientRect();
                const head = chip.closest('.terminal-header-left') || chip.parentElement; const h = head.getBoundingClientRect();
                const cs = getComputedStyle(chip);
                const input = document.getElementById('command-input');
                const send = document.querySelector('#command-pane .mobile-send-btn');
                const hint = document.querySelector('#terminal-statusbar .xterm-focus-hint');
                return { shown: chip.textContent !== '' && r.width > 0, inside: r.left >= h.left - 1 && r.right <= h.right + 1 && r.right <= innerWidth + 1,
                    ellipsis: cs.textOverflow, title: chip.title, text: chip.textContent, tab: chip.tabIndex,
                    disabled: input.disabled, placeholder: input.placeholder,
                    sendOff: !!send && getComputedStyle(send).pointerEvents === 'none',
                    hintHidden: !hint || getComputedStyle(hint).display === 'none' }; })()`);
            assert.equal(v.shown, true, `${width}: the chip is shown`);
            assert.equal(v.inside, true, `${width}: it fits the header and the screen`);
            assert.equal(v.ellipsis, 'ellipsis', `${width}: truncated with …`);
            assert.match(v.title, /Blocked: build-box-with-a-long-name\.example\.internal is read-only|needs Interact/, `${width}: the full reason in its title`);
            assert.equal(v.tab, 0, `${width}: focusable, so a tap/Enter can say it`);
            assert.equal(v.disabled, true, `${width}: the composer is disabled`);
            assert.equal(v.placeholder, 'View only through Coral Hub');
            assert.equal(v.sendOff, true, `${width}: Send is greyed out`);
            assert.equal(v.hintHidden, true, `${width}: no "Click to type"`);

            await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); document.getElementById('hub-readonly-chip').click(); true`);
            await waitFor(`[...document.querySelectorAll('.toast .toast-message')].some(t => /needs Interact|read-only/.test(t.textContent))`, `${width}: the reason on tap`);

            await ev(`window.__ha.setHubReadOnly(window.T.state.currentSession, null); true`);
            const back = await ev(`(() => { const input = document.getElementById('command-input'); const send = document.querySelector('#command-pane .mobile-send-btn');
                return { chip: document.getElementById('hub-readonly-chip').textContent === '', disabled: input.disabled, placeholder: input.placeholder,
                    send: !!send && getComputedStyle(send).pointerEvents !== 'none' }; })()`);
            assert.equal(back.chip, true, `${width}: chip gone`);
            assert.equal(back.disabled, false, `${width}: composer usable again`);
            assert.notEqual(back.placeholder, 'View only through Coral Hub', `${width}: its own placeholder back`);
            assert.equal(back.send, true, `${width}: Send usable again`);
            console.log(`   ✓ ${width} px: chip fits (… + title + tap), composer read-only, restored when raised`);
        }
        console.log('PASS hub_view_only_mobile_browser');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
