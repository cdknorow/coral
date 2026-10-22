// The Servers list's search box (#4778): the shared list bar with a search and a count, above the list, from six machines up. Both lists, against stubbed answers:
//   - a self-hosted hub (servers.js: "N servers"): no box with five; with eight a box above the list says "8 servers"; typing narrows the rows by name, id or address and the count says "k of 8 servers";
//     nothing matching says so (and "Add server" is still there); clearing brings the list back; the periodic refresh keeps the text and the focus; leaving the tab drops the box and its text;
//   - Coral Hub's own list (cloud_servers.js: "N machines"): the same, through the hub's Servers tab in cloud mode;
//   - a phone: the box fits, nothing scrolls sideways.
// Notes and Knowledge have no search box by design (one markdown document per agent, an overview and one document per agent: the browser's find covers them).
// Drives Chrome over CDP through agent_view_harness.js; the cloud mode is the dashboard page's own flag, as in servers_tab_browser.test.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { safePaused } = require('./helpers/paused');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const remotes = n => Array.from({ length: n }, (_, i) => ({ id: 'm' + (i + 1), label: 'machine-' + (i + 1) + (i === 6 ? ' (build)' : ''), url: 'http://10.0.0.' + (i + 11) + ':8420', status: i % 4 === 3 ? 'unreachable' : 'online' }));
const hubRows = n => remotes(n).map(s => ({ id: s.id, label: s.label, online: s.status === 'online', access_mode: 'view', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z' }));

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Fetch, Network } = client;
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const settle = (cond, label) => waitFor(cond, label, 8000);
    const cloudMode = async on => {
        await Fetch.disable();
        if (!on) return;
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/*`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' }] });
    };
    safePaused(client, async e => {
        if (e.request.url.endsWith('/auth/session')) return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
        if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
        const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
        const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace('"mode":"local"', '"mode":"cloud"');
        const headers = (e.responseHeaders || []).filter(h => !/^content-(length|encoding)$/i.test(h.name));
        return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
    });
    const typeInto = (text) => ev(`(() => { const e = document.getElementById('sb-search'); e.focus(); e.value = ${JSON.stringify(text)}; e.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
    const view = (rowSel) => ev(`(() => { const bar = document.getElementById('servers-bar'); const rows = [...document.querySelectorAll(${JSON.stringify(rowSel)})].map(r => r.id);
        const list = document.querySelector('#servers-view .servers-list, #servers-view .cs-table'); const add = document.querySelector('#servers-view [data-action="add"]');
        return { shown: !!bar && !bar.hidden && bar.getClientRects().length > 0, hasBox: !!document.getElementById('sb-search'), count: (bar.querySelector('.list-bar-count') || {}).textContent || '', rows,
            empty: (document.querySelector('#servers-view .servers-empty') || {}).textContent || null, above: !!(list && bar.compareDocumentPosition(list) & Node.DOCUMENT_POSITION_FOLLOWING), filtersBtn: !!bar.querySelector('.list-bar-filter-btn'), chips: bar.querySelectorAll('.list-bar-chip').length,
            addVisible: !!add && add.getClientRects().length > 0, over: document.documentElement.scrollWidth - document.documentElement.clientWidth,
            focus: document.activeElement && document.activeElement.id }; })()`);

    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => { const inner = window.fetch; window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
            const json = (b) => Promise.resolve(new Response(JSON.stringify(b), { status: 200, headers: { 'content-type': 'application/json' } }));
            if (window.__stub && p === '/api/servers' && m === 'GET') return json(window.__cloudMode ? { servers: window.__hubRows || [] } : window.__servers || []);
            if (window.__stub && !window.__cloudMode && p === '/api/health') return json({ status: 'ok', hub: true, version: '1.4.92' });
            return inner(u, o); }; })();` });

        // ── A self-hosted hub ─────────────────────────────────────────────
        await cloudMode(false);
        const local = async (w, mobile) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            const blank = Page.loadEventFired(); await Page.navigate({ url: 'about:blank' }); await blank;
            const loaded = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await loaded; await install();
            await ev(`window.__coralReady.then(() => { window.__stub = true; return true; })`);
        };
        const showLocal = n => ev(`(async () => { window.__servers = ${JSON.stringify([{ id: 'local', label: 'studio', local: true, status: 'online' }, ...remotes(8)].slice(0, n + 1))}; const base = (window.__coralStatic || '/static/');
            const s = await import(base + 'state.js'); s.state.hub = true; s.state.servers = window.__servers; const t = await import(base + 'servers_tab.js'); t.updateServersTabVisibility(); return true; })()`);
        const ROWS = '#servers-view .servers-row[id^="server-row-m"]';
        console.log('self-hosted hub, desktop');
        await local(1200, false);
        await showLocal(5);
        await ev(`window.switchNavTab('servers'); true`);
        await settle(`!!document.querySelector('#server-row-m5')`, 'five servers');
        let v = await view(ROWS);
        check('five servers: no search box (the whole list is on one screen)', !v.shown && !v.hasBox && v.rows.length === 5, JSON.stringify([v.shown, v.hasBox, v.rows.length]));
        await showLocal(8);
        await ev(`(async () => { const t = await import((window.__coralStatic || '/static/') + 'servers_tab.js'); await t.showServersTabContent(); })(); true`);
        await settle(`!!document.querySelector('#server-row-m8') && !!document.getElementById('sb-search')`, 'eight servers and the box');
        v = await view(ROWS);
        check('eight servers: a search box above the list with the count, no Filters button', v.shown && v.above && v.count === '8 servers' && v.rows.length === 8 && !v.filtersBtn, JSON.stringify([v.shown, v.above, v.count, v.rows.length, v.filtersBtn]));
        await shot('servers-search-desktop');
        await typeInto('machine-3');
        await settle(`document.querySelector('#servers-bar .list-bar-count').textContent === '1 of 8 servers'`, 'the search by name');
        v = await view(ROWS);
        check('"machine-3" by name: one row, "1 of 8 servers", the text is kept in the box', v.rows.join() === 'server-row-m3' && v.count === '1 of 8 servers' && v.focus === 'sb-search', JSON.stringify([v.rows, v.count, v.focus]));
        await typeInto('10.0.0.14');
        await settle(`document.querySelector('#servers-bar .list-bar-count').textContent === '1 of 8 servers' && !!document.getElementById('server-row-m4')`, 'the search by address');
        v = await view(ROWS);
        check('an address finds its machine', v.rows.join() === 'server-row-m4', JSON.stringify(v.rows));
        await typeInto('build');
        await settle(`!!document.getElementById('server-row-m7') && document.querySelectorAll('${ROWS}').length === 1`, 'the search by a word of the name');
        await typeInto('zzz-nothing');
        await settle(`!!document.querySelector('#servers-view .servers-empty')`, 'the empty line');
        v = await view(ROWS);
        check('nothing matching says so, the list is the local machine only, and Add server is still there', /No servers match this search/.test(v.empty || '') && v.rows.length === 0 && v.addVisible && v.count === '0 of 8 servers', JSON.stringify([v.empty, v.rows.length, v.addVisible, v.count]));
        await typeInto('machine-1');
        await settle(`document.querySelectorAll('${ROWS}').length === 1 && !!document.getElementById('server-row-m1')`, 'machine-1');
        v = await view(ROWS);
        check('"machine-1": the one machine', v.rows.join() === 'server-row-m1', JSON.stringify(v.rows));
        // the periodic refresh redraws the list: the box, its text and its focus stay
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'servers.js'); await m.renderServersInto(document.getElementById('servers-tab-body')); })(); true`); // what the 5 s refresh does: draws the list again, in place
        await settle(`!!document.getElementById('server-row-m1')`, 'a redraw');
        v = await view(ROWS);
        check('a refresh keeps the search text and the focus in the box', v.rows.join() === 'server-row-m1' && v.focus === 'sb-search' && (await ev(`document.getElementById('sb-search').value`)) === 'machine-1', JSON.stringify([v.rows, v.focus]));
        await ev(`document.querySelector('#servers-bar .list-bar-clear-q').click(); true`);
        await settle(`document.querySelectorAll('${ROWS}').length === 8`, 'cleared');
        v = await view(ROWS);
        check('clearing the search brings every server back', v.rows.length === 8 && v.count === '8 servers', JSON.stringify([v.rows.length, v.count]));
        await typeInto('machine-2');
        await settle(`document.querySelectorAll('${ROWS}').length === 1`, 'a search again');
        await ev(`window.switchNavTab('agents'); true`);
        await ev(`window.switchNavTab('servers'); true`);
        await settle(`!!document.querySelector('#server-row-m8')`, 'the tab again');
        v = await view(ROWS);
        check('leaving the tab drops the search: back on the tab the list is whole and the box empty', v.rows.length === 8 && (await ev(`document.getElementById('sb-search').value`)) === '', JSON.stringify([v.rows.length]));

        console.log('self-hosted hub, phone');
        await local(390, true);
        await showLocal(8);
        await ev(`window.switchNavTab('servers'); true`);
        await settle(`!!document.querySelector('#server-row-m8') && !!document.getElementById('sb-search')`, 'eight servers on a phone');
        await typeInto('machine-1');
        await settle(`document.querySelector('#servers-bar .list-bar-count').textContent === '1 of 8 servers'`, 'the search on a phone');
        v = await view(ROWS);
        check('phone: the box fits and the page does not scroll sideways', v.shown && v.over <= 0, JSON.stringify([v.shown, v.over]));
        await ev(`document.getElementById('sb-search').blur(); true`);
        await shot('servers-search-phone');

        // ── Coral Hub's own list (cloud mode) ─────────────────────────────
        console.log('Coral Hub, desktop');
        await cloudMode(true);
        const hosted = async (n, w, mobile) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            const blank = Page.loadEventFired(); await Page.navigate({ url: 'about:blank' }); await blank;
            const loaded = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await loaded; await install();
            await ev(`(() => { window.__stub = true; window.__cloudMode = true; window.__hubRows = ${JSON.stringify(hubRows(n))}; return true; })()`);
            await ev(`document.getElementById('nav-tab-servers').click(); true`);
        };
        const CS = '#servers-view .cs-row';
        await hosted(5, 1200, false);
        await settle(`document.querySelectorAll('${CS}').length === 5`, 'five machines');
        v = await view(CS);
        check('Coral Hub, five machines: no search box', !v.shown && !v.hasBox, JSON.stringify([v.shown, v.hasBox]));
        await hosted(9, 1200, false);
        await settle(`document.querySelectorAll('${CS}').length === 9 && !!document.getElementById('sb-search')`, 'nine machines and the box');
        v = await view(CS);
        check('Coral Hub, nine machines: the box above the list says "9 machines"', v.shown && v.above && v.count === '9 machines' && v.rows.length === 9 && !v.filtersBtn, JSON.stringify([v.shown, v.above, v.count, v.rows.length]));
        await shot('servers-search-hub-desktop');
        await typeInto('machine-4');
        await settle(`document.querySelectorAll('${CS}').length === 1`, 'a machine by name');
        v = await view(CS);
        check('Coral Hub: a name narrows the machines, "1 of 9 machines"', v.rows.join() === 'm4' || v.rows.length === 1, JSON.stringify([v.rows, v.count]));
        check('Coral Hub: the count says 1 of 9 machines', v.count === '1 of 9 machines', v.count);
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'cloud_servers.js'); await m.renderCloudServersInto(document.getElementById('servers-tab-body')); })(); true`); // what the 5 s refresh does
        await settle(`document.querySelectorAll('${CS}').length === 1 && document.activeElement && document.activeElement.id === 'sb-search'`, 'the list drawn again with the search kept');
        v = await view(CS);
        check('Coral Hub: a refresh keeps the search, the count and the focus in the box', v.rows.length === 1 && v.count === '1 of 9 machines' && v.focus === 'sb-search', JSON.stringify([v.rows, v.count, v.focus]));
        await typeInto('zzz-nothing');
        await settle(`!!document.querySelector('#servers-view .servers-empty')`, 'the empty line');
        v = await view(CS);
        check('Coral Hub: nothing matching says "No machines match this search."', /No machines match this search/.test(v.empty || ''), JSON.stringify(v.empty));
        await ev(`document.querySelector('#servers-bar .list-bar-clear-q').click(); true`);
        await settle(`document.querySelectorAll('${CS}').length === 9`, 'cleared');
        console.log('Coral Hub, phone');
        await hosted(9, 390, true);
        await settle(`document.querySelectorAll('${CS}').length === 9 && !!document.getElementById('sb-search')`, 'nine machines on a phone');
        v = await view(CS);
        check('Coral Hub, phone: the box fits and the page does not scroll sideways', v.shown && v.over <= 0, JSON.stringify([v.shown, v.over]));
        await shot('servers-search-hub-phone');
        await cloudMode(false);
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally {
        try { await client.close(); } catch (_) {}
    }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS servers search: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
