// The scenes of the five text200_*_browser suites: the one suite took 70 s on a fast machine and 119 s on a slow CI runner against the 120 s limit, so each group of scenes is its own suite.
// Views at 200% text on a touch phone (#4344, from the #4276 audit's gaps): every top tab, the side panel's tabs, Settings and every dialog at 390 px with the
// root font doubled and touch on: the page is not wider than the screen, no control runs off an edge, no text is cut without an ellipsis, every control is
// 44 px, and text that is sized in rem/em grows with the root font (the CSS and the inline styles no longer use px font sizes: font_size_units.test.mjs).
// Also the header's glyph buttons (the svg icons and the Chat/Terminal toggle) scale. Run against an isolated dev server.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const src = fs.readFileSync(__dirname + '/phone_layout_browser.test.js', 'utf8');
const SCAN = eval(src.match(/const SCAN = (`[\s\S]*?`);/)[1]);
const FONTS = stage => `(() => { const out = []; const vis = e => e.getClientRects().length && getComputedStyle(e).visibility !== 'hidden';
    const root = window.__scanRoot ? document.querySelector(window.__scanRoot) : document.body;
    for (const e of root.querySelectorAll('svg[width][height]')) { if (!vis(e) || !/^[0-9.]+$/.test(e.getAttribute('width'))) continue; const w = e.getBoundingClientRect().width; if ('${stage}' === 'a') e.__w16 = w; else if (e.__w16 && w / e.__w16 < 1.6 && e.__w16 > 0) out.push('svg[' + e.getAttribute('width') + '] in ' + (e.parentElement.id ? '#' + e.parentElement.id : e.parentElement.tagName.toLowerCase() + '.' + String(e.parentElement.className).trim().split(/\\s+/)[0]) + ' ' + Math.round(e.__w16) + '->' + Math.round(w)); }
    for (const e of root.querySelectorAll('*')) { if (!vis(e)) continue; const own = [...e.childNodes].some(n => n.nodeType === 3 && n.textContent.trim()); if (!own) continue;
        const fs = parseFloat(getComputedStyle(e).fontSize);
        if ('${stage}' === 'a') e.__fs16 = fs; else if (e.__fs16 && fs / e.__fs16 < 1.6) out.push((e.id ? '#' + e.id : e.tagName.toLowerCase()) + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/).slice(0, 2).join('.') : '') + ' ' + e.__fs16 + '->' + fs + ' "' + e.textContent.trim().slice(0, 24) + '"'); }
    return out; })()`;
const GROUPS = ['tabs', 'drawers', 'dialogs', 'wide', 'light'];
module.exports = async function runScenes(group) {
    if (!GROUPS.includes(group)) throw new Error('text200_scenes: unknown group ' + JSON.stringify(group) + ', expected one of ' + GROUPS.join(', '));
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    const report = []; const problems = [];
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const F = window.fetch; window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
            if (window.__hubStub && p === '/api/health') return Promise.resolve(new Response(JSON.stringify({ status: 'ok', hub: true, version: '1.4.75' }), { status: 200 }));
            if (window.__hubStub && p === '/api/servers' && m === 'GET') return Promise.resolve(new Response(JSON.stringify(window.__hubStub), { status: 200 }));
            if (p === '/api/agents/models') return Promise.resolve(new Response(JSON.stringify({ agent_type: 'claude', models: [{ value: 'claude-opus-4-1-20250805', display_name: 'Opus 4.1' }, { value: 'claude-sonnet-4-5', display_name: 'Sonnet 4.5' }], refreshing: false, source: 'account' }), { status: 200, headers: { 'content-type': 'application/json' } }));
            return F(u, o); }; })();` });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const board = 'probe-' + Date.now();
        await ev(`(async () => { for (const c of ['hello team', 'a longer second message with some words']) await fetch('/api/board/${board}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'a', content: c }) }).catch(() => {});
            localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('phone-bot-with-a-long-name', 'A', 8, { sleeping: false, board_project: '${board}' }); window.__m.add('second-bot', 'B', 5, { sleeping: true }); return true; })()`);
        await sleep(900); // sleep-ok: views and dialogs fill from several answers and the layout settles over frames; there is no completion event
        const SERVERS = [{ id: 'local', label: 'studio-mac.local', local: true, status: 'online' }, { id: 'm2', label: 'Old Laptop with a rather long display name', url: 'http://192.168.1.50:8420', status: 'unreachable', last_error: 'dial tcp i/o timeout', warning: 'the key crosses the network unencrypted; use https or a tunnel' }];
        await ev(`window.__hubStub = ${JSON.stringify(SERVERS)}; (async () => { const s = await import((window.__coralStatic || '/static/') + 'state.js'); s.state.hub = true; s.state.servers = ${JSON.stringify(SERVERS)}; return true; })()`);
        const probe = async (name, open, root) => {
            try {
                await ev(`if (!document.getElementById('t200-no-transition')) { const st = document.createElement('style'); st.id = 't200-no-transition'; st.textContent = '* { transition: none !important; animation: none !important; }'; document.head.appendChild(st); } true`); // a transition would leave a computed size mid-way when the page's timeline is paused
                await ev(`document.documentElement.style.fontSize = ''; window.__scanRoot = ${JSON.stringify(root || '')}; true`);
                await open(); await sleep(900); // sleep-ok: views and dialogs fill from several answers and the layout settles over frames; there is no completion event
                await ev(FONTS('a'));
                await ev(`document.documentElement.style.fontSize = '32px'; true`); await sleep(700); // sleep-ok: views and dialogs fill from several answers and the layout settles over frames; there is no completion event
                const r = await ev(SCAN); const grew = await ev(FONTS('b'));
                const off = await ev(`[...(window.__scanRoot ? document.querySelector(window.__scanRoot) : document.body).querySelectorAll('button, a[href], select, input:not([type=hidden])')].filter(e => { if (!e.getClientRects().length || getComputedStyle(e).visibility === 'hidden' || e.closest('.top-nav-tabs, .xterm, [data-scan-skip], .modal-content, #mobile-session-list')) return false; const b = e.getBoundingClientRect(); if (!(b.width > 0 && (b.right > innerWidth + 1 || b.left < -1))) return false; for (let p = e.parentElement; p && p !== document.body; p = p.parentElement) { const pr = p.getBoundingClientRect(); if (pr.width > 0 && (pr.right <= 1 || pr.left >= innerWidth - 1)) return false; } /* inside a drawer that is closed, off the screen by design */ for (let p = e.parentElement; p; p = p.parentElement) { const o = getComputedStyle(p); if (/(auto|scroll)/.test(o.overflowX) && p.scrollWidth > p.clientWidth + 1) return false; } return true; }).map(e => (e.id ? '#' + e.id : e.tagName.toLowerCase() + '.' + String(e.className).trim().split(/\\s+/).slice(0, 2).join('.')) + ' ' + Math.round(e.getBoundingClientRect().left) + '..' + Math.round(e.getBoundingClientRect().right) + ' "' + e.textContent.trim().slice(0, 16) + '"')`);
                if (off.length) problems.push(`${name}: controls off the edge of the screen ${off.slice(0, 4).join('; ')}`);
                const spill = await ev(`[...(window.__scanRoot ? document.querySelector(window.__scanRoot) : document.body).querySelectorAll('button, .btn, a.btn, summary, [role=tab]')].filter(e => e.getClientRects().length && getComputedStyle(e).visibility !== 'hidden' && e.scrollHeight > e.clientHeight + 8 && !/(auto|scroll)/.test(getComputedStyle(e).overflowY)).map(e => (e.id ? '#' + e.id : e.tagName.toLowerCase() + '.' + String(e.className).trim().split(/\\s+/).slice(0, 2).join('.')) + ' ' + e.scrollHeight + '>' + e.clientHeight + ' "' + e.textContent.trim().slice(0, 20) + '"')`);
                if (spill.length) problems.push(`${name}: text spills out of its button (a wrapped label taller than the button; a few px of glyph line, like the row kebab's dots, is not counted) ${spill.slice(0, 4).join('; ')}`);
                if (r.missing) problems.push(name + ': scene not found');
                else {
                    if (r.docOver > 0 || r.nWide) problems.push(`${name}: wider than the screen (docOver ${r.docOver}) ${r.wide.slice(0, 3).join('; ')}`);
                    const clipped = r.clipped.filter(c => !/team-artifacts-caption|sr-only/.test(c)); // visually hidden text (a table caption, screen-reader-only spans)
                    if (clipped.length) problems.push(`${name}: clipped ${clipped.slice(0, 3).join('; ')}`);
                    if (r.nSmall) problems.push(`${name}: smaller than 44 px ${r.small.slice(0, 4).join('; ')}`);
                    const grow = grew;
                    if (grow.length) problems.push(`${name}: text that did not grow with the root font ${[...new Set(grow)].slice(0, 5).join('; ')}`);
                }
                report.push(`${name}: ${r.missing ? 'SCENE MISSING' : `docOver=${r.docOver} wide=${r.nWide}${r.wide.length ? ' [' + r.wide.slice(0, 3).join('; ') + ']' : ''} clipped=${r.nClipped}${r.clipped.length ? ' [' + r.clipped.slice(0, 3).join('; ') + ']' : ''} small=${r.nSmall}${r.small.length ? ' [' + r.small.slice(0, 6).join('; ') + ']' : ''}`} | no-grow=${grew.length}${grew.length ? ' [' + [...new Set(grew)].slice(0, 8).join('; ') + ']' : ''}`);
            } catch (e) { report.push(`${name}: ERROR ${String(e.message).slice(0, 1400)}`); problems.push(`${name}: the probe itself failed: ${String(e.message).slice(0, 300)}`); }
            await ev(`document.documentElement.style.fontSize = ''; true`);
        };
        if (group === 'tabs') {
            for (const tab of ['agents', 'history', 'workflows', 'jobs', 'tokens', 'docs', 'servers', 'board']) await probe('tab ' + tab, () => ev(`window.switchNavTab('${tab}'); true`));
            await ev(`window.switchNavTab('agents'); true`);
            await probe('agent chat', () => ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`));
        }
        if (group === 'drawers') {
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await sleep(900); // sleep-ok: the agent view fills from several answers; there is no completion event
            await ev(`(document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); true`); await sleep(800); // sleep-ok: views and dialogs fill from several answers and the layout settles over frames; there is no completion event
            for (const tab of ['tasks', 'board', 'files', 'notes', 'activity', 'artifacts']) await probe('drawer ' + tab, () => ev(`window.switchAgenticTab('${tab}', 'top'); true`), '#agentic-state');
            await ev(`(document.getElementById('agentic-mobile-close-btn') || document.createElement('i')).click(); true`);
        }
        if (group === 'dialogs') {
            await probe('settings', () => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`), '#settings-modal');
            await ev(`window.hideSettingsModal(); true`);
            const ids = await ev(`[...document.querySelectorAll('.modal[role=dialog]')].map(m => m.id).filter(id => id !== 'settings-modal')`);
            for (const id of ids) { await probe('dialog ' + id, () => ev(`document.getElementById(${JSON.stringify(id)}).style.display = 'flex'; true`), '#' + id); await ev(`document.getElementById(${JSON.stringify(id)}).style.display = 'none'; true`); }
            await probe('launch step 2 (agent) with Agent Configuration open', async () => { await ev(`document.querySelector('[data-on-click="launch-show"]').click(); true`); await sleep(800); // sleep-ok: views and dialogs fill from several answers and the layout settles over frames; there is no completion event
                await ev(`{ const c = [...document.querySelectorAll('.launch-type-card, .launch-type-label')].find(e => /AI Agent/.test(e.textContent)); (c.closest('[data-on-click]') || c).click(); } true`); await sleep(1000); // sleep-ok: views and dialogs fill from several answers and the layout settles over frames; there is no completion event
                await ev(`{ const d = [...document.querySelectorAll('summary')].find(b => b.textContent.trim() === 'Agent Configuration'); if (d) d.parentElement.open = true; } true`); }, '#launch-modal');
            await ev(`document.getElementById('launch-modal').style.display = 'none'; true`);
            await ev(`document.documentElement.style.fontSize = ''; true`);
        }
        if (group === 'drawers') {
            // (#4352) a long list and a task with real content, then the same views on a tablet, in landscape and in the light theme
            await ev(`for (let i = 0; i < 30; i++) window.__m.add('list-agent-' + i + '-with-a-longer-name', 'L', 2, { sleeping: i % 3 === 0 }); true`); await sleep(1200); // sleep-ok: the list renders from several answers
            await probe('agents list, 30 agents', () => ev(`window.switchNavTab('agents'); window.goHome && window.goHome(); true`));
            const taskId = await ev(`(async () => { const r = await fetch('/api/board/${board}/tasks', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title: 'A task with a rather long title that has to wrap in the dialog at large text sizes', body: 'Line one of the body.\\n\\n- a bullet with a long run of words that wraps\\n- another bullet\\n\\n~~~\\nsome_long_command --with-many --flags and-a-very-long-argument-that-does-not-wrap\\n~~~', created_by: 'Operator' }) }).catch(() => null); const d = r && r.ok ? await r.json().catch(() => null) : null; return d && (d.id || (d.task && d.task.id)) || null; })()`);
            if (taskId) {
                await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); (document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); window.switchAgenticTab('tasks', 'top'); true`); await sleep(1500); // sleep-ok: the board's tasks load over a few answers
                await probe('task details with content', () => ev(`window.showTaskDetailModal(${JSON.stringify(taskId)}); true`), '#task-detail-modal');
                await ev(`window.hideTaskDetailModal && window.hideTaskDetailModal(); (document.getElementById('agentic-mobile-close-btn') || document.createElement('i')).click(); true`);
            } else report.push('task details: the board refused the task here, not probed');
        }
        const subset = async (tag) => {
            for (const tab of ['agents', 'history', 'workflows', 'tokens', 'docs']) await probe(tag + ' tab ' + tab, () => ev(`window.switchNavTab('${tab}'); true`));
            await probe(tag + ' settings', () => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`), '#settings-modal');
            await ev(`window.hideSettingsModal(); true`);
            await probe(tag + ' launch step 2', async () => { await ev(`document.querySelector('[data-on-click="launch-show"]').click(); true`); await sleep(800); // sleep-ok: the dialog fills from several answers
                await ev(`{ const c = [...document.querySelectorAll('.launch-type-card, .launch-type-label')].find(e => /AI Agent/.test(e.textContent)); (c.closest('[data-on-click]') || c).click(); } true`); await sleep(1000); }, '#launch-modal'); // sleep-ok: the dialog fills from several answers
            await ev(`document.getElementById('launch-modal').style.display = 'none'; window.switchNavTab('agents'); true`);
        };
        if (group === 'wide') {
            await Emulation.setDeviceMetricsOverride({ width: 820, height: 1180, deviceScaleFactor: 1, mobile: true }); await sleep(600); // sleep-ok: layout settles after the emulation change
            await subset('tablet 820');
            await Emulation.setDeviceMetricsOverride({ width: 844, height: 390, deviceScaleFactor: 1, mobile: true }); await sleep(600); // sleep-ok: layout settles after the emulation change
            await subset('landscape 844x390');
        }
        if (group === 'light') {
            await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await sleep(600); // sleep-ok: layout settles after the emulation change
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars && m.clearCustomThemeVars(); m.applyTheme('light'); return true; })()`); await sleep(500); // sleep-ok: the theme applies over a frame
            await subset('light theme');
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.applyTheme('dark'); return true; })()`);
        }
        if (group === 'tabs') {

            // the header's glyph buttons scale: the svg icons are drawn in rem
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await sleep(900); // sleep-ok: views and dialogs fill from several answers and the layout settles over frames; there is no completion event
            // Each glyph is tagged on the first pass and measured again by tag, so a button that moves into a menu at large text (Saved, #4510) is not compared against its neighbour.
            const gl = async (tag) => ev(`[...document.querySelectorAll('.terminal-header-btn svg, .top-bar-btn svg')].filter(e => e.getClientRects().length).map((e, i) => { if (${tag}) e.dataset.t200 = String(i); return [e.dataset.t200, Math.round(e.getBoundingClientRect().width * 10) / 10]; })`);
            const first = await gl(true); const g16 = first.map(x => x[1]); await ev(`document.documentElement.style.fontSize = '32px'; true`); await sleep(500); const at32 = new Map((await gl(false)).map(x => [x[0], x[1]])); const g32 = first.map(x => at32.get(x[0])).filter(w => w !== undefined); const g16m = first.filter(x => at32.has(x[0])).map(x => x[1]); await ev(`document.documentElement.style.fontSize = ''; true`); // sleep-ok: views and dialogs fill from several answers and the layout settles over frames; there is no completion event
            if (!g16m.length || g16m.some((w, i) => !(g32[i] / w >= 1.9))) problems.push(`header glyph buttons did not scale: ${JSON.stringify(g16)} -> ${JSON.stringify(g32)}`);
            report.push('HEADER GLYPHS ' + JSON.stringify(g16) + ' -> ' + JSON.stringify(g32));
            report.push('HEADER BUTTONS: ' + JSON.stringify(await ev(`[...document.querySelectorAll('.terminal-header button, .terminal-header-btn, .terminal-header a')].filter(e => e.getClientRects().length).map(e => ({ c: String(e.className).slice(0, 30), w: Math.round(e.getBoundingClientRect().width), h: Math.round(e.getBoundingClientRect().height), icon: (e.querySelector('.material-icons, svg') ? (parseFloat(getComputedStyle(e.querySelector('.material-icons') || e.querySelector('svg')).fontSize) || e.querySelector('svg').getBoundingClientRect().width) : null) }))`)));
        }
    } finally { await client.close(); }
    if (process.env.T200_REPORT) throw new Error('\nREPORT (T200_REPORT is set: failing on purpose to print it)\n' + report.join('\n'));
    if (problems.length) throw new Error('\n' + problems.join('\n') + '\n\nFULL REPORT\n' + report.join('\n'));
    console.log('   ✓ ' + group + ': ' + report.length + ' scenes at 390 px, 200% text, touch: no overflow, no clipped text, 44 px targets, text grows, header glyphs scale');
};
