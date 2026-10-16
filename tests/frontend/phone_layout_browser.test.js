// The phone layout (#4081) at 320, 375 and 414 px with a touch screen: for every top tab, the agent's chat, the side panel's Tasks and Board
// tabs, Settings and every dialog: the page is not wider than the screen, no control is clipped off an edge, no text is cut without an
// ellipsis, every control is at least 44 x 44 px; and with the keyboard up (the window 400 px tall) the composer and the board box stay in
// view. A real board on the test server, mock agents. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const SCAN = `(() => { const vw = innerWidth; const sel = e => (e.id ? '#' + e.id : e.tagName.toLowerCase()) + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\\\s+/).slice(0, 2).join('.') : '');
        const vis = e => { if (!e.getClientRects().length) return false; const cs = getComputedStyle(e); if (cs.visibility === 'hidden' || cs.display === 'none' || Number(cs.opacity) === 0) return false; for (let p = e.parentElement; p; p = p.parentElement) { if (p.hasAttribute && p.hasAttribute('hidden')) return false; const pc = getComputedStyle(p); if (pc.display === 'none' || pc.visibility === 'hidden') return false; } return true; };
        const root = window.__scanRoot ? document.querySelector(window.__scanRoot) : document.body; if (!root) return { missing: true };
        const wide = [], small = [], clipped = [];
        for (const e of root.querySelectorAll('*')) {
            if (!vis(e)) continue; const b = e.getBoundingClientRect(); if (!b.width || !b.height) continue; const cs = getComputedStyle(e);
            if ((b.right > vw + 1 || b.left < -1) && cs.position !== 'fixed' && !e.closest('[data-scan-skip]')) { // an element wider than the screen, unless a scroller contains it
                let scroller = false; for (let p = e.parentElement; p && p !== document.body; p = p.parentElement) { const o = getComputedStyle(p); if (/(auto|scroll|hidden|clip)/.test(o.overflowX) && p.getBoundingClientRect().right <= vw + 1) { scroller = true; break; } } if (!scroller) wide.push(sel(e) + ' ' + Math.round(b.left) + '..' + Math.round(b.right)); }
            if (e.matches('button, a[href], select, summary, [role=button], [role=tab]') && (b.right > vw + 1 || b.left < -1)) { let hidden = false, scroll = false; for (let p = e.parentElement; p; p = p.parentElement) { const o = getComputedStyle(p); if (/(auto|scroll)/.test(o.overflowX)) { scroll = true; break; } if (/(hidden|clip)/.test(o.overflowX)) hidden = true; } if (hidden && !scroll) clipped.push('CONTROL ' + sel(e) + ' ' + Math.round(b.left) + '..' + Math.round(b.right)); }
            if (e.matches('button, a[href], input:not([type=hidden]):not([type=checkbox]):not([type=radio]), select, textarea, summary, [role=button], [role=tab], [tabindex="0"]') && !e.disabled) {
                if ((b.height < 43.5 || b.width < 43.5) && !(e.tagName === 'A' && e.closest('p, li, span, td, .markdown, .message-text, .chat-bubble')) && !e.closest('.xterm')) small.push(sel(e) + ' ' + Math.round(b.width) + 'x' + Math.round(b.height)); }
            if (e.children.length === 0 && e.textContent.trim() && e.scrollWidth > e.clientWidth + 1 && /(hidden|clip)/.test(cs.overflowX) && cs.textOverflow !== 'ellipsis') clipped.push(sel(e) + ' ' + e.scrollWidth + '>' + e.clientWidth + ' "' + e.textContent.trim().slice(0, 24) + '"');
        }
        const de = document.documentElement; return { docOver: de.scrollWidth - de.clientWidth, wide: wide.slice(0, 8), nWide: wide.length, small: small.slice(0, 40), nSmall: small.length, clipped: clipped.slice(0, 8), nClipped: clipped.length }; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: panels and dialogs fill from several answers; there is no completion event
    const board = 'phone-layout-' + Date.now();
    const problems = [];
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const w of [320, 375, 414]) {
            console.log(w + ' px');
            await Emulation.setDeviceMetricsOverride({ width: w, height: 800, deviceScaleFactor: 1, mobile: true });
            await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(async () => { for (const c of ['hello team', 'a longer second message with some words']) await fetch('/api/board/${board}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'pl', as: 'QA', content: c }) });
                localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('phone-bot-with-a-long-name', 'A', 8, { sleeping: false, board_project: '${board}' }); window.__m.add('second-bot', 'B', 5, { sleeping: true }); return true; })()`);
            await sleep(900); // sleep-ok: panels and dialogs fill from several answers; no completion event
            const scan = async (name, root) => {
                await ev(`window.__scanRoot = ${JSON.stringify(root || '')}; true`);
                const r = await ev(SCAN);
                if (r.missing) { problems.push(`${w}|${name}: scene not found`); return; }
                for (const [what, list, n] of [['wider than the screen', r.wide, r.nWide], ['clipped', r.clipped, r.nClipped], ['smaller than 44 px', r.small, r.nSmall]]) if (n) problems.push(`${w}|${name}: ${n} ${what}: ${list.slice(0, 4).join('; ')}`);
                if (r.docOver > 0) problems.push(`${w}|${name}: the page scrolls sideways by ${r.docOver} px`);
            };
            await scan('agents list');
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await sleep(1800); // sleep-ok: panels and dialogs fill from several answers; no completion event
            await scan('agent chat');
            await ev(`window.setLiveViewMode('terminal'); true`); await sleep(1500); await scan('agent terminal'); await ev(`window.setLiveViewMode('chat'); true`); await sleep(500); // sleep-ok: panels and dialogs fill from several answers; no completion event
            for (const tab of ['history', 'workflows', 'jobs', 'tokens', 'docs', 'board']) { await ev(`window.switchNavTab('${tab}'); true`).catch(() => {}); await sleep(1500); await scan('tab ' + tab); } // sleep-ok: panels and dialogs fill from several answers; no completion event
            await ev(`window.switchNavTab('agents'); true`); await sleep(500); // sleep-ok: panels and dialogs fill from several answers; no completion event
            // the side panel overlay: Tasks and Board
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await sleep(1200); // sleep-ok: panels and dialogs fill from several answers; no completion event
            await ev(`(document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); true`); await sleep(800); // sleep-ok: panels and dialogs fill from several answers; no completion event
            for (const tab of ['tasks', 'board', 'files', 'notes']) { await ev(`window.switchAgenticTab('${tab}', 'top'); true`); await sleep(900); await scan('side panel ' + tab, '#agentic-state'); } // sleep-ok: panels and dialogs fill from several answers; no completion event
            await ev(`window.switchAgenticTab('board', 'top'); true`); await sleep(900); // sleep-ok: panels and dialogs fill from several answers; no completion event
            // keyboard up: the window 400 px tall with a box focused
            await ev(`(document.getElementById('agentic-mobile-close-btn') || document.createElement('i')).click(); true`); await sleep(500); // sleep-ok: panels and dialogs fill from several answers; no completion event
            await ev(`document.getElementById('command-input').focus(); true`);
            await Emulation.setDeviceMetricsOverride({ width: w, height: 400, deviceScaleFactor: 1, mobile: true }); await sleep(1000); // sleep-ok: panels and dialogs fill from several answers; no completion event
            const kb = await ev(`(() => { const r = s => { const e = document.querySelector(s); if (!e) return null; const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, h: b.height }; }; return { input: r('#command-input'), send: r('.command-pane .mobile-send-btn:not(.mobile-send-team-btn)'), msgs: r('#live-history-messages'), vh: innerHeight }; })()`);
            if (!(kb.input && kb.input.top >= 0 && kb.input.bottom <= kb.vh + 0.5)) problems.push(`${w}|keyboard up: the composer box is not fully in view ${JSON.stringify(kb.input)}`);
            if (!(kb.send && kb.send.bottom <= kb.vh + 0.5)) problems.push(`${w}|keyboard up: Send is not in view ${JSON.stringify(kb.send)}`);
            if (!(kb.msgs && kb.msgs.h >= 120)) problems.push(`${w}|keyboard up: the conversation is squeezed to ${kb.msgs && kb.msgs.h} px`);
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); (document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); window.switchAgenticTab('board', 'top'); true`); await sleep(1000); // sleep-ok: panels and dialogs fill from several answers; no completion event
            const kbb = await ev(`(() => { const e = document.getElementById('board-panel-input'); if (!e || !e.getClientRects().length) return null; const b = e.getBoundingClientRect(); e.focus(); return { top: b.top, bottom: b.bottom, vh: innerHeight }; })()`);
            if (kbb && !(kbb.top >= 0 && kbb.bottom <= kbb.vh + 0.5)) problems.push(`${w}|keyboard up: the board message box is not fully in view ${JSON.stringify(kbb)}`);
            await Emulation.setDeviceMetricsOverride({ width: w, height: 800, deviceScaleFactor: 1, mobile: true }); await sleep(500); // sleep-ok: panels and dialogs fill from several answers; no completion event
            await ev(`window.switchNavTab('agents'); true`);
            // Settings and every dialog
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`); await sleep(1500); await scan('settings', '#settings-modal'); await ev(`window.hideSettingsModal(); true`); // sleep-ok: panels and dialogs fill from several answers; no completion event
            const ids = await ev(`[...document.querySelectorAll('.modal[role=dialog]')].map(m => m.id).filter(id => id !== 'settings-modal')`);
            for (const id of ids) {
                await ev(`document.getElementById(${JSON.stringify(id)}).style.display = 'flex'; true`); await sleep(120); // sleep-ok: panels and dialogs fill from several answers; no completion event
                await scan('dialog ' + id, '#' + id);
                await ev(`document.getElementById(${JSON.stringify(id)}).style.display = 'none'; true`);
            }
        }
        assert.deepEqual(problems, [], 'phone layout problems:\n  ' + problems.join('\n  '));
        console.log('\nPASS phone layout: 320, 375 and 414 px, every tab, the side panel, Settings, every dialog, the keyboard up');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
