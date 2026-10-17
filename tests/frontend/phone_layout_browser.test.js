// The phone layout (#4081) at 320, 375 and 414 px with a touch screen: for every top tab, the agent's chat, the side panel's Tasks and Board
// tabs, Settings and every dialog: the page is not wider than the screen, no control is clipped off an edge, no text is cut without an
// ellipsis, every control is at least 44 x 44 px; and with the keyboard up (the window 400 px tall) the composer and the board box stay in
// view. Plus (#4086): a sleeping agent's wake bar, the Launch dialog's second step, the Servers tab with a hub fixture, the terminal with real
// xterm output (a fake terminal socket), landscape at 640 x 320 and 200 percent text on the home and agent views. A real board on the test
// server, mock agents. Run against an isolated dev server.
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
    let tag = '';
        const scan = async (name, root, { sideways = true } = {}) => {
                await ev(`window.__scanRoot = ${JSON.stringify(root || '')}; true`);
                const r = await ev(SCAN);
                if (r.missing) { problems.push(`${tag}|${name}: scene not found`); return; }
                for (const [what, list, n] of [['wider than the screen', r.wide, r.nWide], ['clipped', r.clipped, r.nClipped], ['smaller than 44 px', r.small, r.nSmall]]) if (n) problems.push(`${tag}|${name}: ${n} ${what}: ${list.slice(0, 4).join('; ')}`);
                if (r.docOver > 0 && sideways) {
                    const who = await ev(`[...document.querySelectorAll('body *')].filter(e => { const b = e.getBoundingClientRect(); return (b.width && b.right > innerWidth + 1 && getComputedStyle(e).display !== 'none') || e.scrollWidth > innerWidth + 1; }).slice(0, 4).map(e => { const chain = []; for (let p = e; p && p !== document.body; p = p.parentElement) chain.push((p.id ? '#' + p.id : p.tagName.toLowerCase()) + '.' + String(p.className).split(' ')[0]); return chain.slice(0, 3).join(' < ') + ' ' + Math.round(e.getBoundingClientRect().right); })`);
                    problems.push(`${tag}|${name}: the page scrolls sideways by ${r.docOver} px (${who.join('; ')})`);
                }
            };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        // a fake terminal socket (the xterm and its fit are real) and, once a scene asks for it, a hub: /api/health says so and /api/servers lists some
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const R = window.WebSocket; class Fake { constructor(url) { this.readyState = 0; this.binaryType = 'arraybuffer'; window.__tws = this; setTimeout(() => { this.readyState = 1; this.onopen && this.onopen({}); }, 50); }
            send() {} close() { this.readyState = 3; } addEventListener() {} removeEventListener() {} }
            const W = function (url, p) { return /\\/ws\\/terminal\\//.test(String(url)) ? new Fake(url) : new R(url, p); }; W.OPEN = 1; W.CONNECTING = 0; W.CLOSING = 2; W.CLOSED = 3; W.prototype = R.prototype; window.WebSocket = W;
            const F = window.fetch; window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
                if (window.__hubStub && p === '/api/health') return Promise.resolve(new Response(JSON.stringify({ status: 'ok', hub: true, version: '1.4.69' }), { status: 200 }));
                if (window.__hubStub && p === '/api/servers' && m === 'GET') return Promise.resolve(new Response(JSON.stringify(window.__hubStub), { status: 200 }));
                return F(u, o); }; })();` });
        for (const w of [320, 375, 414]) {
            console.log(w + ' px');
            await Emulation.setDeviceMetricsOverride({ width: w, height: 800, deviceScaleFactor: 1, mobile: true });
            await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(async () => { for (const c of ['hello team', 'a longer second message with some words']) await fetch('/api/board/${board}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'pl', as: 'QA', content: c }) });
                localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('phone-bot-with-a-long-name', 'A', 8, { sleeping: false, board_project: '${board}' }); window.__m.add('second-bot', 'B', 5, { sleeping: true }); return true; })()`);
            await sleep(900); // sleep-ok: panels and dialogs fill from several answers; no completion event
            tag = String(w);
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
        // ── The scenes the first pass could not reach (#4086), at 375 px ──
        {
            const w = 375;
            await Emulation.setDeviceMetricsOverride({ width: w, height: 800, deviceScaleFactor: 1, mobile: true });
            await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            const now = new Date().toISOString();
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('phone-bot-with-a-long-name', 'A', 8, { sleeping: false }); window.__m.add('sleepy-bot', 'B', 5, { sleeping: true }); window.__m.add('archived-bot', 'C', 3, { archived: true }); true`); await sleep(900); // sleep-ok: the first live list renders from several answers; no completion event
            tag = '375 extra';
            await ev(`window.selectLiveSession('sleepy-bot', 'claude', 'sleepy-bot-id', 'local'); true`); await sleep(1500); // sleep-ok: the Chat view settles over several answers
            await scan('sleeping agent (wake bar)');
            await ev(`window.selectLiveSession('archived-bot', 'claude', 'archived-bot-id', 'local'); true`); await sleep(1500); // sleep-ok: the Chat view settles over several answers
            await scan('archived agent (unarchive bar)');
            // the terminal with real xterm output
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); window.setLiveViewMode('terminal'); true`);
            await waitFor(`!!window.__tws && !!document.querySelector('#xterm-container .xterm')`, 'the terminal', 15000);
            await ev(`(() => { const b = new TextEncoder().encode(Array.from({ length: 80 }, (_, i) => 'line ' + i + ' of the build output with a fairly long line of text that wraps at phone width\\r\\n').join('')); window.__tws.onmessage({ data: b.buffer }); return true; })()`); await sleep(1200); // sleep-ok: xterm writes and the fit run over frames
            await scan('terminal with output'); await ev(`window.setLiveViewMode('chat'); true`); await sleep(500); // sleep-ok: the view switches over a frame
            // the Launch dialog, step 1 and step 2 (and the Agent Configuration section open)
            await ev(`document.querySelector('[data-on-click="launch-show"]').click(); true`); await sleep(1000); // sleep-ok: the dialog fills from several answers
            await scan('launch dialog step 1', '#launch-modal');
            await ev(`{ const c = [...document.querySelectorAll('.launch-type-card, .launch-type-label')].find(e => /AI Agent/.test(e.textContent)); (c.closest('[data-on-click]') || c).click(); } true`); await sleep(1500); // sleep-ok: the dialog fills from several answers
            await scan('launch dialog step 2', '#launch-modal');
            await ev(`{ const t = [...document.querySelectorAll('#launch-step-agent *')].filter(e => /^\\s*Agent Configuration/.test(e.textContent) && e.children.length < 3).pop(); if (t) (t.closest('[data-on-click], button') || t).click(); } true`); await sleep(800); // sleep-ok: the section opens over a frame
            await scan('launch dialog step 2, Agent Configuration open', '#launch-modal');
            await ev(`document.getElementById('launch-modal').style.display = 'none'; true`);
            // the Servers tab with a hub fixture
            const SERVERS = [{ id: 'local', label: 'studio-mac.local', local: true, status: 'online' }, { id: 'm1', label: 'workstation.lan', url: 'https://workstation.lan:8420', status: 'online', version: '1.4.69', last_seen: now },
                { id: 'm2', label: 'Old Laptop with a rather long display name', url: 'http://192.168.1.50:8420', status: 'unreachable', last_seen: now, last_error: 'dial tcp 192.168.1.50:8420: i/o timeout' },
                { id: 'm3', label: 'epoch', url: 'https://epoch.example.net:8420', status: 'unauthorized', last_seen: now, last_error: 'the server rejected the API key' }];
            await ev(`window.__hubStub = ${JSON.stringify(SERVERS)}; (async () => { const s = await import((window.__coralStatic || '/static/') + 'state.js'); s.state.hub = true; s.state.servers = ${JSON.stringify(SERVERS)}; const t = await import((window.__coralStatic || '/static/') + 'servers_tab.js'); t.updateServersTabVisibility(); return true; })()`);
            await ev(`window.switchNavTab('servers'); true`); await waitFor(`!!document.querySelector('#server-row-m2')`, 'the servers list', 8000); await sleep(500); // sleep-ok: the rows settle over a frame
            await scan('servers tab', '#servers-view');
            await ev(`document.querySelector('[data-action=add]').click(); true`); await sleep(600); // sleep-ok: the form opens over a frame
            await scan('servers tab, Add form', '#servers-view');
            await ev(`window.switchNavTab('agents'); true`);
            // landscape: 640 x 320
            await Emulation.setDeviceMetricsOverride({ width: 640, height: 320, deviceScaleFactor: 1, mobile: true });
            tag = '640x320';
            await sleep(800); // sleep-ok: the layout re-flows over frames
            await scan('landscape: agents list');
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await sleep(1500); // sleep-ok: the Chat view settles over several answers
            await scan('landscape: agent chat');
            const lk = await ev(`(() => { const r = s => { const e = document.querySelector(s); if (!e) return null; const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, h: b.height }; }; return { input: r('#command-input'), msgs: r('#live-history-messages'), vh: innerHeight }; })()`);
            if (!(lk.input && lk.input.bottom <= lk.vh + 0.5 && lk.input.top >= 0)) problems.push(`640x320|landscape: the composer box is not in view ${JSON.stringify(lk.input)}`);
            if (!(lk.msgs && lk.msgs.h >= 60)) problems.push(`640x320|landscape: the conversation is squeezed to ${lk.msgs && lk.msgs.h} px`);
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`); await sleep(1200); // sleep-ok: Settings fills from several answers
            await scan('landscape: settings', '#settings-modal'); await ev(`window.hideSettingsModal(); true`);
            // 200 percent text on the home and agent views (the root font size doubled: everything sized in rem or em follows)
            await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
            tag = '390 text 200%';
            await ev(`document.documentElement.style.fontSize = '200%'; window.switchNavTab('agents'); true`); await sleep(800); // sleep-ok: the layout re-flows over frames
            await ev(`window.selectLiveSession('', '', '', 'local'); true`).catch(() => {});
            await scan('home / agents list');
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await sleep(1500); // sleep-ok: the Chat view settles over several answers
            await scan('agent chat');
            await ev(`document.documentElement.style.fontSize = ''; true`);
            // 200 percent page zoom on a 390 px phone is a 195 px wide page: a probe of how far the layout holds
            await Emulation.setDeviceMetricsOverride({ width: 195, height: 422, deviceScaleFactor: 2, mobile: true });
            tag = '195 (390 px at 200% zoom)';
            await ev(`T.render.renderLiveSessions(T.state.liveSessions); true`); await sleep(800); // sleep-ok: the layout re-flows over frames
            await ev(`window.selectLiveSession('', '', '', 'local'); true`).catch(() => {});
            await scan('home / agents list');
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await sleep(1500); // sleep-ok: the Chat view settles over several answers
            await scan('agent chat');
        }
        assert.deepEqual(problems, [], 'phone layout problems:\n  ' + problems.join('\n  '));
        console.log('\nPASS phone layout: 320, 375 and 414 px, every tab, the side panel, Settings, every dialog, the keyboard up');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
