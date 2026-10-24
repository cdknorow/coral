// The phone layout (#4081) at 320, 375 and 414 px with a touch screen: for every top tab, the agent's chat, the side panel's Tasks and Board
// tabs, Settings and every dialog: the page is not wider than the screen, no control is clipped off an edge, no text is cut without an
// ellipsis, every control is at least 44 x 44 px; and with the keyboard up (the window 400 px tall) the composer and the board box stay in
// view. Plus (#4086): a sleeping agent's wake bar, the Launch dialog's second step, the Servers tab with a hub fixture, the terminal with real
// xterm output (a fake terminal socket), landscape at 640 x 320 and 200 percent text on the home and agent views. A real board on the test
// server, mock agents. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const { SCAN } = require('./helpers/phone_scan');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    // settle(ms): wait for the layout to stop moving before a scan measures its geometry (#4768). One shared copy
    // in helpers/wait.js; it is no worse than the old fixed settle-sleeps (caps at ms, never throws) and usually
    // far faster. The panels and dialogs fill over frames with no completion event, so the scans below settle first.
    const { settle } = require('./helpers/wait').bind(ev);
const { removeBoards } = require('./helpers/board_cleanup');
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
            await settle(900);
            tag = String(w);
            await scan('agents list');
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await settle(1800);
            await scan('agent chat');
            await ev(`window.setLiveViewMode('terminal'); true`); await settle(1500); await scan('agent terminal'); await ev(`window.setLiveViewMode('chat'); true`); await settle(500);
            for (const tab of ['history', 'workflows', 'jobs', 'tokens', 'docs', 'board']) { await ev(`window.switchNavTab('${tab}'); true`).catch(() => {}); await settle(1500); await scan('tab ' + tab); }
            await ev(`window.switchNavTab('agents'); true`); await settle(500);
            // the side panel overlay: Tasks and Board
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await settle(1200);
            await ev(`(document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); true`); await settle(800);
            for (const tab of ['tasks', 'board', 'files', 'notes']) { await ev(`window.switchAgenticTab('${tab}', 'top'); true`); await settle(900); await scan('side panel ' + tab, '#agentic-state'); }
            await ev(`window.switchAgenticTab('board', 'top'); true`); await settle(900);
            // keyboard up: the window 400 px tall with a box focused
            await ev(`(document.getElementById('agentic-mobile-close-btn') || document.createElement('i')).click(); true`); await settle(500);
            await ev(`document.getElementById('command-input').focus(); true`);
            await Emulation.setDeviceMetricsOverride({ width: w, height: 400, deviceScaleFactor: 1, mobile: true }); await settle(1000);
            const kb = await ev(`(() => { const r = s => { const e = document.querySelector(s); if (!e) return null; const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, h: b.height }; }; return { input: r('#command-input'), send: r('.command-pane .mobile-send-btn:not(.mobile-send-team-btn)'), msgs: r('#live-history-messages'), vh: innerHeight }; })()`);
            if (!(kb.input && kb.input.top >= 0 && kb.input.bottom <= kb.vh + 0.5)) problems.push(`${w}|keyboard up: the composer box is not fully in view ${JSON.stringify(kb.input)}`);
            if (!(kb.send && kb.send.bottom <= kb.vh + 0.5)) problems.push(`${w}|keyboard up: Send is not in view ${JSON.stringify(kb.send)}`);
            if (!(kb.msgs && kb.msgs.h >= 120)) problems.push(`${w}|keyboard up: the conversation is squeezed to ${kb.msgs && kb.msgs.h} px`);
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); (document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); window.switchAgenticTab('board', 'top'); true`); await settle(1000);
            const kbb = await ev(`(() => { const e = document.getElementById('board-panel-input'); if (!e || !e.getClientRects().length) return null; const b = e.getBoundingClientRect(); e.focus(); return { top: b.top, bottom: b.bottom, vh: innerHeight }; })()`);
            if (kbb && !(kbb.top >= 0 && kbb.bottom <= kbb.vh + 0.5)) problems.push(`${w}|keyboard up: the board message box is not fully in view ${JSON.stringify(kbb)}`);
            await Emulation.setDeviceMetricsOverride({ width: w, height: 800, deviceScaleFactor: 1, mobile: true }); await settle(500);
            await ev(`window.switchNavTab('agents'); true`);
            // Settings and every dialog
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`); await settle(1500); await scan('settings', '#settings-modal'); await ev(`window.hideSettingsModal(); true`);
            const ids = await ev(`[...document.querySelectorAll('.modal[role=dialog]')].map(m => m.id).filter(id => id !== 'settings-modal')`);
            for (const id of ids) {
                await ev(`document.getElementById(${JSON.stringify(id)}).style.display = 'flex'; true`); await settle(120);
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
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('phone-bot-with-a-long-name', 'A', 8, { sleeping: false }); window.__m.add('sleepy-bot', 'B', 5, { sleeping: true }); window.__m.add('archived-bot', 'C', 3, { archived: true }); true`); await settle(900);
            tag = '375 extra';
            await ev(`window.selectLiveSession('sleepy-bot', 'claude', 'sleepy-bot-id', 'local'); true`); await settle(1500);
            await scan('sleeping agent (wake bar)');
            await ev(`window.selectLiveSession('archived-bot', 'claude', 'archived-bot-id', 'local'); true`); await settle(1500);
            await scan('archived agent (unarchive bar)');
            // the terminal with real xterm output
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); window.setLiveViewMode('terminal'); true`);
            await waitFor(`!!window.__tws && !!document.querySelector('#xterm-container .xterm')`, 'the terminal', 15000);
            await ev(`(() => { const b = new TextEncoder().encode(Array.from({ length: 80 }, (_, i) => 'line ' + i + ' of the build output with a fairly long line of text that wraps at phone width\\r\\n').join('')); window.__tws.onmessage({ data: b.buffer }); return true; })()`); await settle(1200);
            await scan('terminal with output'); await ev(`window.setLiveViewMode('chat'); true`); await settle(500);
            // the Launch dialog, step 1 and step 2 (and the Agent Configuration section open)
            await ev(`document.querySelector('[data-on-click="launch-show"]').click(); true`); await settle(1000);
            await scan('launch dialog step 1', '#launch-modal');
            await ev(`{ const c = [...document.querySelectorAll('.launch-type-card, .launch-type-label')].find(e => /New agent/.test(e.textContent)); (c.closest('[data-on-click]') || c).click(); } true`); await settle(1500);
            await scan('launch dialog step 2', '#launch-modal');
            await ev(`{ const t = [...document.querySelectorAll('#launch-step-agent *')].filter(e => /^\\s*Agent Configuration/.test(e.textContent) && e.children.length < 3).pop(); if (t) (t.closest('[data-on-click], button') || t).click(); } true`); await settle(800);
            await scan('launch dialog step 2, Agent Configuration open', '#launch-modal');
            await ev(`document.getElementById('launch-modal').style.display = 'none'; true`);
            // the Servers tab with a hub fixture
            const SERVERS = [{ id: 'local', label: 'studio-mac.local', local: true, status: 'online' }, { id: 'm1', label: 'workstation.lan', url: 'https://workstation.lan:8420', status: 'online', version: '1.4.69', last_seen: now },
                { id: 'm2', label: 'Old Laptop with a rather long display name', url: 'http://192.168.1.50:8420', status: 'unreachable', last_seen: now, last_error: 'dial tcp 192.168.1.50:8420: i/o timeout' },
                { id: 'm3', label: 'epoch', url: 'https://epoch.example.net:8420', status: 'unauthorized', last_seen: now, last_error: 'the server rejected the API key' }];
            await ev(`window.__hubStub = ${JSON.stringify(SERVERS)}; (async () => { const s = await import((window.__coralStatic || '/static/') + 'state.js'); s.state.hub = true; s.state.servers = ${JSON.stringify(SERVERS)}; const t = await import((window.__coralStatic || '/static/') + 'servers_tab.js'); t.updateServersTabVisibility(); return true; })()`);
            await ev(`window.switchNavTab('servers'); true`); await waitFor(`!!document.querySelector('#server-row-m2')`, 'the servers list', 8000); await settle(500);
            await scan('servers tab', '#servers-view');
            await ev(`document.querySelector('[data-action=add]').click(); true`); await settle(600);
            await scan('servers tab, Add form', '#servers-view');
            await ev(`window.switchNavTab('agents'); true`);
            // landscape: 640 x 320
            await Emulation.setDeviceMetricsOverride({ width: 640, height: 320, deviceScaleFactor: 1, mobile: true });
            tag = '640x320';
            await settle(800);
            await scan('landscape: agents list');
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await settle(1500);
            await scan('landscape: agent chat');
            const lk = await ev(`(() => { const r = s => { const e = document.querySelector(s); if (!e) return null; const b = e.getBoundingClientRect(); return { top: b.top, bottom: b.bottom, h: b.height }; }; return { input: r('#command-input'), msgs: r('#live-history-messages'), vh: innerHeight }; })()`);
            if (!(lk.input && lk.input.bottom <= lk.vh + 0.5 && lk.input.top >= 0)) problems.push(`640x320|landscape: the composer box is not in view ${JSON.stringify(lk.input)}`);
            if (!(lk.msgs && lk.msgs.h >= 60)) problems.push(`640x320|landscape: the conversation is squeezed to ${lk.msgs && lk.msgs.h} px`);
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`); await settle(1200);
            await scan('landscape: settings', '#settings-modal'); await ev(`window.hideSettingsModal(); true`);
            // 200 percent text on the home and agent views (the root font size doubled: everything sized in rem or em follows)
            await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
            tag = '390 text 200%';
            await ev(`document.documentElement.style.fontSize = '200%'; window.switchNavTab('agents'); true`); await settle(800);
            await ev(`window.selectLiveSession('', '', '', 'local'); true`).catch(() => {});
            await scan('home / agents list');
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await settle(1500);
            await scan('agent chat');
            await ev(`document.documentElement.style.fontSize = ''; true`);
            // 200 percent page zoom on a 390 px phone is a 195 px wide page: a probe of how far the layout holds
            await Emulation.setDeviceMetricsOverride({ width: 195, height: 422, deviceScaleFactor: 2, mobile: true });
            tag = '195 (390 px at 200% zoom)';
            await ev(`T.render.renderLiveSessions(T.state.liveSessions); true`); await settle(800);
            await ev(`window.selectLiveSession('', '', '', 'local'); true`).catch(() => {});
            await scan('home / agents list');
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await settle(1500);
            await scan('agent chat');
        }
        assert.deepEqual(problems, [], 'phone layout problems:\n  ' + problems.join('\n  '));
        console.log('\nPASS phone layout: 320, 375 and 414 px, every tab, the side panel, Settings, every dialog, the keyboard up');
    } finally { await removeBoards(ev, [board]); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
