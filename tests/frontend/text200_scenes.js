// The scenes of the five text200_*_browser suites: the one suite took 70 s on a fast machine and 119 s on a slow CI runner against the 120 s limit, so each group of scenes is its own suite.
// Views at 200% text on a touch phone (#4344, from the #4276 audit's gaps): every top tab, the side panel's tabs, Settings and every dialog at 390 px with the
// root font doubled and touch on: the page is not wider than the screen, no control runs off an edge, no text is cut without an ellipsis, every control is
// 44 px, and text that is sized in rem/em grows with the root font (the CSS and the inline styles no longer use px font sizes: font_size_units.test.mjs).
// Also the header's glyph buttons (the svg icons and the Chat/Terminal toggle) scale. Run against an isolated dev server.
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { SCAN } = require('./helpers/phone_scan'); // the page scan the phone suites share (#5273 moved it out of phone_layout_browser)
const FONTS = stage => `(() => { const out = []; const vis = e => e.getClientRects().length && getComputedStyle(e).visibility !== 'hidden';
    const root = window.__scanRoot ? document.querySelector(window.__scanRoot) : document.body;
    for (const e of root.querySelectorAll('svg[width][height]')) { if (!vis(e) || !/^[0-9.]+$/.test(e.getAttribute('width'))) continue; const w = e.getBoundingClientRect().width; if ('${stage}' === 'a') e.__w16 = w; else if (e.__w16 && w / e.__w16 < 1.6 && e.__w16 > 0) out.push('svg[' + e.getAttribute('width') + '] in ' + (e.parentElement.id ? '#' + e.parentElement.id : e.parentElement.tagName.toLowerCase() + '.' + String(e.parentElement.className).trim().split(/\\s+/)[0]) + ' ' + Math.round(e.__w16) + '->' + Math.round(w)); }
    for (const e of root.querySelectorAll('*')) { if (!vis(e)) continue; const own = [...e.childNodes].some(n => n.nodeType === 3 && n.textContent.trim()); if (!own) continue;
        const fs = parseFloat(getComputedStyle(e).fontSize);
        if ('${stage}' === 'a') e.__fs16 = fs; else if (e.__fs16 && fs / e.__fs16 < 1.6) out.push((e.id ? '#' + e.id : e.tagName.toLowerCase()) + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/).slice(0, 2).join('.') : '') + ' ' + e.__fs16 + '->' + fs + ' "' + e.textContent.trim().slice(0, 24) + '"'); }
    return out; })()`;
const GROUPS = ['tabs', 'drawers', 'dialogs', 'wide', 'light', 'surfaces'];
module.exports = async function runScenes(group) {
    if (!GROUPS.includes(group)) throw new Error('text200_scenes: unknown group ' + JSON.stringify(group) + ', expected one of ' + GROUPS.join(', '));
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    // settle(ms): wait for the layout to stop moving before a scan measures its geometry (#4768). One shared copy
    // in helpers/wait.js; it is no worse than the old fixed settle-sleeps (caps at ms, never throws) and usually
    // far faster. The views and dialogs fill over frames with no completion event, so the scans below settle first.
    const { settle } = require('./helpers/wait').bind(ev);
const { removeBoards } = require('./helpers/board_cleanup');
    const report = []; const problems = [];
    let board = ''; // the board the scenes make, taken away again in the finally (#4959)
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const F = window.fetch; window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
            if (window.__hubStub && p === '/api/health') return Promise.resolve(new Response(JSON.stringify({ status: 'ok', hub: true, version: '1.4.75' }), { status: 200 }));
            if (window.__hubStub && p === '/api/servers' && m === 'GET') return Promise.resolve(new Response(JSON.stringify(window.__hubStub), { status: 200 }));
            if (p === '/api/agents/models') return Promise.resolve(new Response(JSON.stringify({ agent_type: 'claude', models: [{ value: 'claude-opus-4-1-20250805', display_name: 'Opus 4.1' }, { value: 'claude-sonnet-4-5', display_name: 'Sonnet 4.5' }], thinking: ['low', 'medium', 'high', 'xhigh', 'max'], refreshing: false, source: 'account' }), { status: 200, headers: { 'content-type': 'application/json' } }));
            return F(u, o); }; })();` });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        board = 'probe-' + Date.now();
        await ev(`(async () => { for (const c of ['hello team', 'a longer second message with some words']) await fetch('/api/board/${board}/messages', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'a', content: c }) }).catch(() => {});
            localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('phone-bot-with-a-long-name', 'A', 8, { sleeping: false, board_project: '${board}' }); window.__m.add('second-bot', 'B', 5, { sleeping: true }); return true; })()`);
        await settle(900);
        const SERVERS = [{ id: 'local', label: 'studio-mac.local', local: true, status: 'online' }, { id: 'm2', label: 'Old Laptop with a rather long display name', url: 'http://192.168.1.50:8420', status: 'unreachable', last_error: 'dial tcp i/o timeout', warning: 'the key crosses the network unencrypted; use https or a tunnel' }];
        await ev(`window.__hubStub = ${JSON.stringify(SERVERS)}; (async () => { const s = await import((window.__coralStatic || '/static/') + 'state.js'); s.state.hub = true; s.state.servers = ${JSON.stringify(SERVERS)}; return true; })()`);
        const probe = async (name, open, root, check) => { // check: a JS expression run at 200% that returns a list of problems (the generic scan does not see text that overflows its own button)
            try {
                await ev(`if (!document.getElementById('t200-no-transition')) { const st = document.createElement('style'); st.id = 't200-no-transition'; st.textContent = '* { transition: none !important; animation: none !important; }'; document.head.appendChild(st); } true`); // a transition would leave a computed size mid-way when the page's timeline is paused
                await ev(`document.documentElement.style.fontSize = ''; window.__scanRoot = ${JSON.stringify(root || '')}; true`);
                await open(); await settle(900);
                await ev(FONTS('a'));
                await ev(`document.documentElement.style.fontSize = '32px'; true`); await settle(700);
                const r = await ev(SCAN); const grew = await ev(FONTS('b'));
                if (check) { const bad = await ev(check); if (bad.length) problems.push(`${name}: ${bad.slice(0, 4).join('; ')}`); }
                if (process.env.T200_SHOTS) { fs.mkdirSync(process.env.T200_SHOTS, { recursive: true }); fs.writeFileSync(`${process.env.T200_SHOTS}/${name.replace(/[^a-z0-9]+/gi, '-').toLowerCase()}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
                const off = await ev(`[...(window.__scanRoot ? document.querySelector(window.__scanRoot) : document.body).querySelectorAll('button, a[href], select, input:not([type=hidden])')].filter(e => { if (!e.getClientRects().length || getComputedStyle(e).visibility === 'hidden' || e.closest('.top-nav-tabs, .xterm, [data-scan-skip], .modal-content, #mobile-session-list')) return false; const b = e.getBoundingClientRect(); if (!(b.width > 0 && (b.right > innerWidth + 1 || b.left < -1))) return false; for (let p = e.parentElement; p && p !== document.body; p = p.parentElement) { const pr = p.getBoundingClientRect(); if (pr.width > 0 && (pr.right <= 1 || pr.left >= innerWidth - 1)) return false; } /* inside a drawer that is closed, off the screen by design */ for (let p = e.parentElement; p; p = p.parentElement) { const o = getComputedStyle(p); if (/(auto|scroll)/.test(o.overflowX) && p.scrollWidth > p.clientWidth + 1) return false; } return true; }).map(e => (e.id ? '#' + e.id : e.tagName.toLowerCase() + '.' + String(e.className).trim().split(/\\s+/).slice(0, 2).join('.')) + ' ' + Math.round(e.getBoundingClientRect().left) + '..' + Math.round(e.getBoundingClientRect().right) + ' "' + e.textContent.trim().slice(0, 16) + '"')`);
                if (off.length) problems.push(`${name}: controls off the edge of the screen ${off.slice(0, 4).join('; ')}`);
                const spill = await ev(`[...(window.__scanRoot ? document.querySelector(window.__scanRoot) : document.body).querySelectorAll('button, .btn, a.btn, summary, [role=tab]')].filter(e => e.getClientRects().length && getComputedStyle(e).visibility !== 'hidden' && e.scrollHeight > e.clientHeight + 8 && !/(auto|scroll)/.test(getComputedStyle(e).overflowY)).map(e => (e.id ? '#' + e.id : e.tagName.toLowerCase() + '.' + String(e.className).trim().split(/\\s+/).slice(0, 2).join('.')) + ' ' + e.scrollHeight + '>' + e.clientHeight + ' "' + e.textContent.trim().slice(0, 20) + '"')`);
                if (spill.length) problems.push(`${name}: text spills out of its button (a wrapped label taller than the button; a few px of glyph line, like the row kebab's dots, is not counted) ${spill.slice(0, 4).join('; ')}`);
                if (r.missing) problems.push(name + ': scene not found');
                else {
                    if (r.docOver > 0 || r.nWide) {
                        // The scan names what is wider than the screen; a page that scrolls sideways with none wider (docOver 22, wide=0: the first scene of text200_wide on Windows, 3 of 74 runs) names nothing,
                        // so say which visible elements end past the right edge: the next red shows the cause.
                        const past = r.docOver > 0 && !r.nWide ? await ev(`(() => { const w = document.documentElement.clientWidth; return [...document.querySelectorAll('body *')].filter(e => e.getClientRects().length && getComputedStyle(e).visibility !== 'hidden' && e.getBoundingClientRect().right > w + 0.5).slice(0, 6).map(e => (e.id ? '#' + e.id : e.tagName.toLowerCase()) + (typeof e.className === 'string' && e.className.trim() ? '.' + e.className.trim().split(/\\s+/).slice(0, 2).join('.') : '') + ' right=' + Math.round(e.getBoundingClientRect().right) + ' of ' + w); })()`) : [];
                        problems.push(`${name}: wider than the screen (docOver ${r.docOver}) ${r.wide.slice(0, 3).join('; ')}${past.length ? ' | ends past the right edge: ' + past.join('; ') : ''}`);
                    }
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
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await settle(900);
            await ev(`(document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); true`); await settle(800);
            for (const tab of ['tasks', 'board', 'files', 'notes', 'activity', 'artifacts']) await probe('drawer ' + tab, () => ev(`window.switchAgenticTab('${tab}', 'top'); true`), '#agentic-state');
            await ev(`(document.getElementById('agentic-mobile-close-btn') || document.createElement('i')).click(); true`);
        }
        if (group === 'dialogs') {
            await probe('settings', () => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`), '#settings-modal');
            await ev(`window.hideSettingsModal(); true`);
            const ids = await ev(`[...document.querySelectorAll('.modal[role=dialog]')].map(m => m.id).filter(id => id !== 'settings-modal')`);
            for (const id of ids) { await probe('dialog ' + id, () => ev(`document.getElementById(${JSON.stringify(id)}).style.display = 'flex'; true`), '#' + id); await ev(`document.getElementById(${JSON.stringify(id)}).style.display = 'none'; true`); }
            await probe('launch step 2 (agent) with Agent Configuration open', async () => { await ev(`document.querySelector('[data-on-click="launch-show"]').click(); true`); await settle(800);
                await ev(`{ const c = [...document.querySelectorAll('.launch-type-card, .launch-type-label')].find(e => /New agent/.test(e.textContent)); (c.closest('[data-on-click]') || c).click(); } true`); await settle(1000);
                await ev(`{ const d = [...document.querySelectorAll('summary')].find(b => b.textContent.trim() === 'Agent Configuration'); if (d) d.parentElement.open = true; } true`); }, '#launch-modal');
            await ev(`document.getElementById('launch-modal').style.display = 'none'; true`);
            await ev(`document.documentElement.style.fontSize = ''; true`);
        }
        if (group === 'drawers') {
            // (#4352) a long list and a task with real content, then the same views on a tablet, in landscape and in the light theme
            await ev(`for (let i = 0; i < 30; i++) window.__m.add('list-agent-' + i + '-with-a-longer-name', 'L', 2, { sleeping: i % 3 === 0 }); true`); await settle(1200);
            await probe('agents list, 30 agents', () => ev(`window.switchNavTab('agents'); window.goHome && window.goHome(); true`));
            const taskId = await ev(`(async () => { const r = await fetch('/api/board/${board}/tasks', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title: 'A task with a rather long title that has to wrap in the dialog at large text sizes', body: 'Line one of the body.\\n\\n- a bullet with a long run of words that wraps\\n- another bullet\\n\\n~~~\\nsome_long_command --with-many --flags and-a-very-long-argument-that-does-not-wrap\\n~~~', created_by: 'Operator' }) }).catch(() => null); const d = r && r.ok ? await r.json().catch(() => null) : null; return d && (d.id || (d.task && d.task.id)) || null; })()`);
            if (taskId) {
                await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); (document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); window.switchAgenticTab('tasks', 'top'); true`); await settle(1500);
                await probe('task details with content', () => ev(`window.showTaskDetailModal(${JSON.stringify(taskId)}); true`), '#task-detail-modal');
                await ev(`window.hideTaskDetailModal && window.hideTaskDetailModal(); (document.getElementById('agentic-mobile-close-btn') || document.createElement('i')).click(); true`);
            } else report.push('task details: the board refused the task here, not probed');
        }
        const subset = async (tag) => {
            for (const tab of ['agents', 'history', 'workflows', 'tokens', 'docs']) await probe(tag + ' tab ' + tab, () => ev(`window.switchNavTab('${tab}'); true`));
            await probe(tag + ' settings', () => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'modals.js'); await m.showSettingsModal(); return true; })()`), '#settings-modal');
            await ev(`window.hideSettingsModal(); true`);
            await probe(tag + ' launch step 2', async () => { await ev(`document.querySelector('[data-on-click="launch-show"]').click(); true`); await settle(800);
                await ev(`{ const c = [...document.querySelectorAll('.launch-type-card, .launch-type-label')].find(e => /New agent/.test(e.textContent)); (c.closest('[data-on-click]') || c).click(); } true`); await settle(1000); }, '#launch-modal');
            await ev(`document.getElementById('launch-modal').style.display = 'none'; window.switchNavTab('agents'); true`);
        };
        if (group === 'wide') {
            await Emulation.setDeviceMetricsOverride({ width: 820, height: 1180, deviceScaleFactor: 1, mobile: true }); await settle(600);
            await subset('tablet 820');
            await Emulation.setDeviceMetricsOverride({ width: 844, height: 390, deviceScaleFactor: 1, mobile: true }); await settle(600);
            await subset('landscape 844x390');
        }
        if (group === 'light') {
            await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await settle(600);
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars && m.clearCustomThemeVars(); m.applyTheme('light'); return true; })()`); await settle(500);
            await subset('light theme');
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.applyTheme('dark'); return true; })()`);
        }
        if (group === 'surfaces') {
            // #5058: what shipped on the night of v1.4.102 to v1.4.108, at 200% text: the chat bar's model chip and its menu, the Cost tables, the sectioned dialogs (their
            // section selector and footers), and the agent list's Display panel.
            const feed = extra => ev(`(() => { const a = window.__m.agents['phone-bot-with-a-long-name-id']; window.T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(a), ...${JSON.stringify(extra)} }] }); return true; })()`);
            const openAgent = async () => { await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await settle(900); };
            await probe('chip', async () => { await openAgent(); await feed({ agent_type: 'claude', model: 'claude-opus-4-1-20250805', thinking_actual: 'high', thinking: 'low' }); await waitFor(`!document.getElementById('model-chip-bar').hidden && document.getElementById('model-chip-text').textContent.length > 3`, 'the chip', 8000); await settle(500); }, '#command-pane');
            await probe('chip with its note (Switches after this reply)', async () => { await openAgent(); await feed({ agent_type: 'claude', model: 'claude-opus-4-1-20250805', thinking_actual: 'high', model_switch: { state: 'pending', thinking: 'max', after_reply: true } }); await waitFor(`!document.getElementById('model-chip-note').textContent.includes('after this reply') === false`, 'the note', 8000); await settle(500); }, '#command-pane');
            for (const kind of ['model', 'thinking']) { // the model chip's menu (models) and the thinking chip's (levels)
                await probe(`${kind} chip menu open`, async () => { await openAgent(); await feed({ agent_type: 'claude', model: 'claude-opus-4-1-20250805', thinking_actual: 'high', model_switch: null }); await settle(400);
                    await ev(`document.getElementById('${kind}-chip').click(); true`); await waitFor(`!document.getElementById('${kind}-chip-menu').hidden && document.querySelectorAll('#${kind}-chip-menu .model-chip-item').length > ${kind === 'model' ? 0 : 2}`, 'the ${kind} chip menu', 8000); await settle(900); }, '#command-pane');
                await ev(`(document.getElementById('${kind}-chip-menu') || {}).hidden === false && document.getElementById('${kind}-chip').click(); true`);
            }
            await probe('Cost tables', async () => {
                await ev(`(() => { window.__realFetch = window.fetch; const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
                    window.fetch = (u, o) => { const url = String(u && u.url || u);
                        if (url.includes('/api/token-usage/summary')) return json({ totals: { input_tokens: 4200000, output_tokens: 610000, cost_usd: 143.9 }, by_agent: [],
                            by_model: [{ model: 'claude-opus-5-5', agent_type: 'claude', num_sessions: 3, input_tokens: 2000000, output_tokens: 300000, cache_read_tokens: 10, cache_write_tokens: 5, cost_usd: 62.5 }],
                            by_thinking: [{ agent_type: 'claude', thinking: 'high', input_tokens: 1800000, output_tokens: 260000, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 910000, cost_usd: 64.2, num_calls: 212 },
                                { agent_type: 'agy', thinking: '', input_tokens: 100000, output_tokens: 20000, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, cost_usd: 3.2, num_calls: 0 }] });
                        if (url.includes('/api/token-usage/by-team')) return json({ teams: [{ board_name: 'coral-go-team', num_agents: 6, input_tokens: 9000, output_tokens: 1000, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 8.5 }] });
                        if (url.includes('/api/token-usage/')) return json({});
                        return window.__realFetch(u, o); }; return true; })()`);
                await ev(`window.switchNavTab('tokens'); true`);
                await waitFor(`document.querySelectorAll('#cost-by-thinking tbody tr').length >= 2 && document.querySelectorAll('#cost-by-team tbody tr').length >= 1`, 'the Cost tables', 15000);
                await ev(`document.getElementById('cost-by-thinking').scrollIntoView({ block: 'center' }); true`); // (the scan covers the whole view; the scroll is for a screenshot)
            }, '#cost-dashboard-view', `[...document.querySelectorAll('#cost-dashboard-view .cost-sort-btn')].filter(b => b.getClientRects().length && b.scrollWidth > b.clientWidth + 1).map(b => 'heading text runs out of its button: ' + b.textContent.trim() + ' (' + b.scrollWidth + ' in ' + b.clientWidth + ')')`);
            await ev(`window.fetch = window.__realFetch || window.fetch; window.switchNavTab('agents'); true`);
            await probe('Restart dialog', async () => { await openAgent(); await ev(`window.restartSession(); true`);
                await waitFor(`getComputedStyle(document.getElementById('restart-modal')).display === 'flex' && !!document.querySelector('#restart-modal .acf-thinking-wrap')`, 'the Restart dialog', 8000); await settle(800); }, '#restart-modal');
            await ev(`window.hideRestartModal && window.hideRestartModal(); true`);
            await probe('New Agent dialog, Agent section', async () => { await ev(`(async () => { await window.showLaunchModal(); window._selectLaunchType('agent'); return true; })()`); await settle(900);
                await ev(`document.getElementById('launch-agent-more').dispatchEvent(new CustomEvent('sd-more-open', { bubbles: true })); true`); await settle(300);
                await ev(`(() => { const t = [...document.querySelectorAll('#launch-modal [role="tab"]')].find(x => /Agent\s*$/.test(x.textContent)); if (t) t.click(); const s = document.querySelector('#launch-modal .settings-group-pick select'); if (s && getComputedStyle(s).display !== 'none') { s.value = 'agent'; s.dispatchEvent(new Event('change', { bubbles: true })); } return true; })()`); await waitFor(`!!document.querySelector('#launch-step-agent .acf-thinking-wrap') && document.querySelector('#launch-step-agent .acf-thinking-wrap').getBoundingClientRect().width > 0`, 'the Agent section with its Thinking select', 8000); await settle(700); }, '#launch-modal');
            await ev(`window.hideLaunchModal(); true`);
            await probe('Agent Team dialog', async () => { await ev(`(async () => { await window.showLaunchModal(); window._selectLaunchType('team'); return true; })()`); await waitFor(`getComputedStyle(document.getElementById('launch-step-team')).display !== 'none' && !!document.querySelector('#launch-step-team .team-step-panel:not([hidden])')`, 'the Agent Team dialog', 8000); await settle(1000); }, '#launch-modal');
            // the other two steps (#6397): a template chosen, then Members and Start
            await probe('Agent Team dialog, Members', async () => { await ev(`document.querySelector('#team-template-list [role="option"]').click(); document.getElementById('team-tab-members').click(); true`); await waitFor(`!document.getElementById('team-panel-members').hidden && document.querySelectorAll('#team-agents-list tr').length > 0`, 'the Members step', 8000); await settle(800); }, '#launch-modal');
            await probe('Agent Team dialog, Start', async () => { await ev(`document.getElementById('team-tab-start').click(); true`); await waitFor(`!document.getElementById('team-panel-start').hidden`, 'the Start step', 8000); await settle(800); }, '#launch-modal');
            await ev(`window.hideLaunchModal(); true`);
            await probe('Display panel open', async () => { await ev(`window.switchNavTab('agents'); window.goHome && window.goHome(); true`); await settle(600);
                await ev(`(() => { const b = document.querySelector('.mobile-list-header [data-display-button]'); if (b && b.getAttribute('aria-expanded') !== 'true') b.click(); return true; })()`);
                await waitFor(`!!document.querySelector('.mobile-list-header [data-display-panel]') && !document.querySelector('.mobile-list-header [data-display-panel]').hidden`, 'the Display panel', 6000); await settle(700); }, '.mobile-list-header');
            await ev(`(() => { const b = document.querySelector('.mobile-list-header [data-display-button]'); if (b && b.getAttribute('aria-expanded') === 'true') b.click(); return true; })()`);
        }
        if (group === 'tabs') {

            // the header's glyph buttons scale: the svg icons are drawn in rem
            await ev(`window.selectLiveSession('phone-bot-with-a-long-name', 'claude', 'phone-bot-with-a-long-name-id', 'local'); true`); await settle(900);
            // Each glyph is tagged on the first pass and measured again by tag, so a button that moves into a menu at large text (Saved, #4510) is not compared against its neighbour.
            const gl = async (tag) => ev(`[...document.querySelectorAll('.terminal-header-btn svg, .top-bar-btn svg')].filter(e => e.getClientRects().length).map((e, i) => { if (${tag}) e.dataset.t200 = String(i); return [e.dataset.t200, Math.round(e.getBoundingClientRect().width * 10) / 10]; })`);
            const first = await gl(true); const g16 = first.map(x => x[1]); await ev(`document.documentElement.style.fontSize = '32px'; true`); await settle(500); const at32 = new Map((await gl(false)).map(x => [x[0], x[1]])); const g32 = first.map(x => at32.get(x[0])).filter(w => w !== undefined); const g16m = first.filter(x => at32.has(x[0])).map(x => x[1]); await ev(`document.documentElement.style.fontSize = ''; true`);
            if (!g16m.length || g16m.some((w, i) => !(g32[i] / w >= 1.9))) problems.push(`header glyph buttons did not scale: ${JSON.stringify(g16)} -> ${JSON.stringify(g32)}`);
            report.push('HEADER GLYPHS ' + JSON.stringify(g16) + ' -> ' + JSON.stringify(g32));
            report.push('HEADER BUTTONS: ' + JSON.stringify(await ev(`[...document.querySelectorAll('.terminal-header button, .terminal-header-btn, .terminal-header a')].filter(e => e.getClientRects().length).map(e => ({ c: String(e.className).slice(0, 30), w: Math.round(e.getBoundingClientRect().width), h: Math.round(e.getBoundingClientRect().height), icon: (e.querySelector('.material-icons, svg') ? (parseFloat(getComputedStyle(e.querySelector('.material-icons') || e.querySelector('svg')).fontSize) || e.querySelector('svg').getBoundingClientRect().width) : null) }))`)));
        }
    } finally { await removeBoards(ev, [board]); await client.close(); }
    if (process.env.T200_REPORT) throw new Error('\nREPORT (T200_REPORT is set: failing on purpose to print it)\n' + report.join('\n'));
    if (problems.length) throw new Error('\n' + problems.join('\n') + '\n\nFULL REPORT\n' + report.join('\n'));
    console.log('   ✓ ' + group + ': ' + report.length + ' scenes at 390 px, 200% text, touch: no overflow, no clipped text, 44 px targets, text grows, header glyphs scale');
};
