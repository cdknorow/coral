// The keyboard-only census through Coral Hub at desktop and phone width (#6188, #6226): cloud mode under the hub's CSP, a stubbed two-computer Hub (the one hub_history_board_nav_browser uses), at 1280 and 390 px: a real Tab
// walk (helpers/keyboard_census.js) reaches every visible control on the Hub's screens (Chats and a chat with its tabs, boards and a board, Servers, Inbox, Docs, the account menu, Settings, Launch), each
// stop shows focus, and the dialogs trap Tab, close on Escape and give focus back. KEYBOARD_REPORT=<file> writes the findings to that file instead of failing.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { tap: tapSel, required } = require('./helpers/phone_targets');
const kb = require('./helpers/keyboard_census');
const { setupHubWorld, BASE } = require('./helpers/hub_world');

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    const results = [];
    const found = [];
    const sc = async (name, setup, { expect } = {}) => {
        try { await setup(); if (expect) { await new Promise(r => setTimeout(r, 400)); if (!(await ev(expect))) throw new Error('the scene is not showing (' + expect.slice(0, 80) + ')'); } } catch (e) { results.push({ name, error: String(e && e.message || e).slice(0, 300) }); return; } // sleep-ok: the scene's own transition settles
        await new Promise(r => setTimeout(r, 500)); // sleep-ok: the scene's own transitions settle
        const r = await kb.walk(ev, client.Input);
        results.push({ name, stops: r.stops.length, path: r.stops.map(x => x.d).join(' > ') });
        found.push(...kb.problems(name, r));
        if (!r.stops.length) found.push(`${name}: Tab reached no control at all`);
    };
    const tap = sel => tapSel(ev, sel);
    try {
        await setupHubWorld({ client, Page, Emulation, width: 1280, height: 800, mobile: false });
        const run = async tag => {
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window.switchNavTab==='function' && !!window.__coralReady`, 'dashboard'); await ev(`window.__coralReady.startup('liveList','settings')`);
                const nav = async tab => { await ev(`window.switchNavTab('${tab}'); true`); };
        await sc(`${tag}: hub: home`, async () => { await waitFor(`document.querySelectorAll('button, a[href]').length > 5`, 'home'); });
        await sc(`${tag}: hub: chats list`, async () => { await nav('history'); await waitFor(`document.getElementById('history-sessions-list').textContent.includes('Laptop conversation')`, 'history'); });
        // the Chats page's search through the Hub (#6305): the results, the advanced filters, a search that finds nothing
        const histType = text => ev(`(() => { const i = document.getElementById('history-search'); i.focus(); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        await sc(`${tag}: hub: search results`, async () => { await histType('needle'); await waitFor(`document.querySelectorAll('#chat-search-results .chat-search-hit').length >= 1`, 'search hits', 10000); }, {  expect: `document.querySelectorAll('#chat-search-results .chat-search-hit').length >= 1` });
        await sc(`${tag}: hub: search, advanced filters`, async () => { await tap('#hf-adv-toggle'); await waitFor(`document.getElementById('hf-advanced').style.display !== 'none'`, 'advanced filters', 5000); }, {  expect: `document.getElementById('hf-advanced').style.display !== 'none'` });
        await tap('#hf-adv-toggle');
        await sc(`${tag}: hub: search finds nothing`, async () => { await histType('zzz'); await waitFor(`/No matching chats/.test(document.getElementById('chat-search-results').textContent)`, 'no results', 10000); }, {  expect: `/No matching chats/.test(document.getElementById('chat-search-results').textContent)` });
        await histType(''); await waitFor(`!document.getElementById('history-sessions-list').hidden`, 'the chat list again', 10000);
        await sc(`${tag}: hub: chat transcript`, async () => { await tap('#history-sessions-list [data-on-click="history-open"]'); await waitFor(`document.getElementById('history-messages').textContent.includes('needle from')`, 'chat'); });
        for (const t of ['notes', 'activity', 'tasks', 'agent-notes', 'commits']) await sc(`${tag}: hub: chat tab ${t}`, async () => { await tap(`.history-tab-btn[data-tab="${t}"]`); }, { expect: `document.querySelector('.history-tab-btn[data-tab="${t}"]').classList.contains('active')` });
        await sc(`${tag}: hub: boards list`, async () => { await nav('board'); await waitFor(`document.querySelectorAll('#mb-projects-ul [data-project]').length===2`, 'boards'); });
        await sc(`${tag}: hub: board`, async () => { await ev(`document.querySelector('#mb-projects-ul [data-project="@m1/same-team"]').click();true`); await waitFor(`document.getElementById('mb-messages').textContent.includes('Board message from m1')`, 'board'); });
        await sc(`${tag}: hub: servers`, async () => { await nav('servers'); await waitFor(`document.getElementById('servers-view').getClientRects().length > 0`, 'servers'); });
        await sc(`${tag}: hub: servers, a computer opened`, async () => { await tap('#servers-tab-body .cs-toggle'); }, { expect: `document.querySelector('#servers-tab-body .cs-toggle').getAttribute('aria-expanded') === 'true'` });
        await sc(`${tag}: hub: servers, renaming`, async () => { await tap('#servers-tab-body .cs-rename-btn'); }, { expect: `!!document.querySelector('#servers-tab-body .cs-rename input')` });
        await ev(`document.querySelector('#servers-tab-body .cs-rename button[type=button]')?.click(); true`);
        await sc(`${tag}: hub: inbox`, async () => { await nav('hub-inbox'); }, { expect: `document.getElementById('nav-select').textContent.includes('Inbox') || !!document.getElementById('nav-tab-hub-inbox')` });
        await sc(`${tag}: hub: artifacts`, async () => { await nav('artifacts'); await waitFor(`!!document.getElementById('artifacts-frame')`, 'artifacts frame'); });
        await sc(`${tag}: hub: docs`, async () => { await nav('docs'); });
        await sc(`${tag}: hub: account menu`, async () => { await tap('[aria-label="Account menu"]'); }, { expect: `document.querySelector('[aria-label="Account menu"]').getAttribute('aria-expanded') === 'true'` });
        await ev(`document.body.click(); true`);
        await ev(`(async () => { await window.showSettingsModal(); return true; })()`);
        for (const g of await ev(`[...document.querySelectorAll('#settings-group-select option')].filter(o => !o.hidden).map(o => o.value)`).catch(() => [])) await sc(`${tag}: hub: settings ${g}`, async () => { await ev(`(async () => { await window.showSettingsModal(); return true; })()`); await ev(`(() => { const p = document.getElementById('settings-group-select'); p.value = '${g}'; p.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); });
        await ev(`window.hideSettingsModal(); true`);
        const tagVisibleModal = () => ev(`(() => { document.querySelectorAll('[data-census-box]').forEach(e => e.removeAttribute('data-census-box')); const m = [...document.querySelectorAll('dialog[open], .modal')].filter(e => e.getClientRects().length && getComputedStyle(e).display !== 'none').pop(); if (m) m.setAttribute('data-census-box', ''); return !!m; })()`);
        const dlg = async (name, { opener, open }) => { try { const bad = await kb.dialogCheck(ev, client.Input, { opener, box: '[data-census-box]', open: async () => { await open(); for (let i = 0; i < 30 && !(await tagVisibleModal()); i++) await new Promise(r => setTimeout(r, 100)); } }); results.push({ name, stops: 0, path: '' }); found.push(...bad.map(b => `${name}: ${b}`)); } catch (e) { results.push({ name, error: String(e && e.message || e).slice(0, 300) }); } }; // sleep-ok: the dialog appears when its code has loaded
        await dlg(`${tag}: hub: dialog launch`, { opener: '[aria-label="Account menu"]', open: () => ev(`(async () => { await window.showLaunchModal(); return true; })()`) });
        await dlg(`${tag}: hub: dialog settings`, { opener: '[aria-label="Account menu"]', open: () => ev(`(async () => { await window.showSettingsModal(); return true; })()`) });
        // no computer connected (#6351): the home is one "Connect your first computer" panel with its link
        const noComputers = await Page.addScriptToEvaluateOnNewDocument({ source: 'window.__noComputers = true;' });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`typeof window.switchNavTab==='function' && !!window.__coralReady`, 'dashboard'); await ev(`window.__coralReady.startup('liveList','settings')`);
        await sc(`${tag}: hub: home, no computer connected`, async () => { await waitFor(`/Connect your first computer/.test(document.body.textContent) && !!document.querySelector('a[href="/machines/add"]')`, 'the connect panel', 10000); }, { expect: `[...document.querySelectorAll('a[href="/machines/add"]')].some(a => a.getClientRects().length > 0)` });
        await Page.removeScriptToEvaluateOnNewDocument({ identifier: noComputers.identifier });
        };
        await run('desktop');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await run('phone');
        } finally { await client.close(); }
    const bad = results.filter(r => r.error).map(r => `${r.name}: the scene did not open (${r.error})`).concat(found, required(results, ['hub: home', 'hub: home, no computer connected', 'hub: chats list', 'hub: search results', 'hub: search, advanced filters', 'hub: search finds nothing', 'hub: chat transcript', 'hub: chat tab commits', 'hub: boards list', 'hub: board', 'hub: servers', 'hub: servers, a computer opened', 'hub: servers, renaming', 'hub: inbox', 'hub: artifacts', 'hub: docs', 'hub: account menu', 'hub: dialog launch', 'hub: dialog settings'].flatMap(n => ['desktop', 'phone'].map(t => `${t}: ${n}`))));
    if (process.env.KEYBOARD_REPORT) { require('node:fs').writeFileSync(process.env.KEYBOARD_REPORT, results.map(r => `${r.name}: ${r.error || r.stops + ' stops'}${process.env.KEYBOARD_PATHS ? '\n    ' + r.path : ''}`).join('\n') + '\n--\n' + bad.join('\n') + '\n'); process.exit(0); }
    assert.deepEqual(bad, [], 'keyboard-only problems through the Hub:\n  ' + bad.join('\n  '));
    console.log(`PASS ${results.length} Hub scenes: Tab reaches every control and shows where it is`);
})().catch(e => { console.error(e); process.exit(1); });
