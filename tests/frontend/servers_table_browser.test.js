// Servers page as a table (#5927) - setup from servers_tab_browser
// Servers tab (#3070): the Cloud Hub's machines (and a self-hosted hub's remotes) are a top-level tab, not a
// dialog. Pins: when the tab shows, the nav button's aria-current, the region and focus, each row's one lower-only
// 'Hub access' control (Follow this computer / Limit to Interact / Limit to View: PATCH {access_mode}, no Raise, no Launch dialog), Revoke
// contrast, deep links (#servers, #servers/<id>, the agent list's server chip), no overflow at 375 px with the
// actions stacked, and the enroll page's Servers note.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { themeJs } = require('./helpers/theme');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const { framesJs } = require('./helpers/wait');

const SERVERS = [
    { id: 'm1', label: 'Box One', online: true, access_mode: 'view', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z' },
    { id: 'm2', label: 'box two', online: true, access_mode: 'follow', local_mode: 'view', last_seen: '2026-10-13T00:00:00Z' },
    { id: 'm3', label: 'Box Three', online: false, access_mode: 'follow', local_mode: 'launch', last_seen: '2026-10-12T00:00:00Z' },
    { id: 'm4', label: 'Box Four', online: true, access_mode: 'interact', effective_mode: 'view', limited_by: 'machine', last_seen: '2026-10-13T00:00:00Z' },
];
const LIVE = [
    { id: 'm1', label: 'Box One', status: 'online', coral_version: '1.4.10', agents: 7, update: 'required', latest_coral: '1.5.0' },
    { id: 'm2', label: 'box two', status: 'online', version: '1.4.9', agents: 1, update: 'available', latest_coral: '1.5.0' },
    { id: 'm3', label: 'Box Three', status: 'offline', coral_version: '1.5.0', agents: 0, update: 'current' },
    { id: 'm4', label: 'Box Four', status: 'online', agents: 12, update: 'unknown' },
];
const DOWNLOAD = 'https://coralhub.dev/coral.html#download';

const CLOUD_PRELOAD = `(() => {
    window.__qaServers = ${JSON.stringify(SERVERS)};
    window.__patches = []; window.__serverGets = 0;
    const inner = window.fetch;
    window.fetch = (url, opts = {}) => {
        const p = new URL(String(url), location.origin).pathname;
        const m = (opts.method || 'GET').toUpperCase();
        if (p === '/api/servers' && m === 'GET') {
            window.__serverGets++;
            return Promise.resolve(new Response(JSON.stringify({ servers: window.__qaServers }), { status: 200, headers: { 'content-type': 'application/json' } }));
        }
        const pm = p.match(/^\\/api\\/servers\\/([^/]+)$/);
        if (pm && m === 'PATCH') {
            const body = JSON.parse(opts.body || '{}');
            window.__patches.push({ id: decodeURIComponent(pm[1]), body });
            const row = window.__qaServers.find(s => s.id === decodeURIComponent(pm[1]));
            if (row && body.access_mode) row.access_mode = body.access_mode;
            return Promise.resolve(new Response(JSON.stringify(row || {}), { status: 200, headers: { 'content-type': 'application/json' } }));
        }
        return inner(url, opts);
    };
})();`;

// WCAG contrast ratio of two 'rgb(...)' colours.
const contrast = (a, b) => {
    const lum = c => {
        const [r, g, bl] = c.match(/\d+(\.\d+)?/g).slice(0, 3).map(Number).map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; });
        return 0.2126 * r + 0.7152 * g + 0.0722 * bl;
    };
    const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p);
    return (x + 0.05) / (y + 0.05);
};

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const { Fetch, Network } = client;

    const cloudMode = async on => {
        await Fetch.disable();
        if (!on) return;
        await Fetch.enable({ patterns: [
            { urlPattern: `${BASE}/*`, resourceType: 'Document', requestStage: 'Response' }, // every dashboard page (#3532)
            { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
        ] });
    };
    safePaused(client, async e => {
        if (e.request.url.endsWith('/auth/session')) {
            return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
        }
        if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
        const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
        const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace('"mode":"local"', '"mode":"cloud"');
        const headers = (e.responseHeaders || []).filter(h => !/^content-(length|encoding)$/i.test(h.name));
        return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
    });
    const open = async (where = '') => { // a path ("servers/m2") or an old hash link ("#servers")
        // A fresh load every time: since #3529 the dashboard URL has no query, so going from
        // /#a to /#b is a same-document hash change that fires no load event. Each wait is
        // registered before its navigation, or about:blank's quick load could be missed.
        const blank = Page.loadEventFired();
        await Page.navigate({ url: 'about:blank' });
        await blank;
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' + where });
        await loaded;
        await install();
        await ev(`(() => { T.state.servers = ${JSON.stringify(LIVE)}; return true; })()`);
    };
    const tabState = () => ev(`(() => {
        const tab = document.getElementById('nav-tab-servers'), view = document.getElementById('servers-view');
        return {
            tabShown: !!tab && !tab.hidden && getComputedStyle(tab).display !== 'none',
            role: tab && tab.getAttribute('role'), controls: tab && tab.getAttribute('aria-controls'), selected: tab && tab.getAttribute('aria-selected'), current: tab && tab.getAttribute('aria-current'),
            viewShown: !!view && getComputedStyle(view).display !== 'none', viewRole: view && view.getAttribute('role'),
            focus: document.activeElement && document.activeElement.id,
        };
    })()`);
    const rows = () => ev(`[...document.querySelectorAll('#servers-view .cs-row')].map(r => ({
        id: r.id, tabindex: r.getAttribute('tabindex'),
        access: (r.querySelector('.cs-access') || {}).textContent || '',
        target: r.classList.contains('server-row-target'),
    }))`);

    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: CLOUD_PRELOAD });
        await cloudMode(true);
        const { themeJs } = require('./helpers/theme');
        const { installAudit, auditRegion, setTheme } = require('./helpers/contrast');
        const shot = async name => { if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); } };
        const table = () => ev(`(() => { const t = document.querySelector('#servers-view table.cs-table'); if (!t) return null;
            const rows = [...t.querySelectorAll('tbody > tr.cs-row')];
            return { role: t.getAttribute('role'), label: t.getAttribute('aria-label'),
                heads: [...t.querySelectorAll('thead th')].map(th => ({ text: th.textContent.replace(/[▲▼]/g, '').trim(), sort: th.getAttribute('aria-sort'), scope: th.getAttribute('scope'), button: !!th.querySelector('button') })),
                names: rows.map(r => r.querySelector('.cs-name').textContent),
                rows: rows.map(r => ({ id: r.id, status: r.querySelector('.cs-col-status').textContent, version: r.querySelector('.cs-col-version').textContent, agents: r.querySelector('.cs-col-agents').textContent,
                    select: r.querySelector('.cs-col-access select').value, selectLabel: r.querySelector('.cs-col-access .sr-only').textContent.trim(),
                    expanded: r.querySelector('.cs-toggle').getAttribute('aria-expanded'), controls: r.querySelector('.cs-toggle').getAttribute('aria-controls'),
                    detailHidden: document.getElementById(r.querySelector('.cs-toggle').getAttribute('aria-controls')).hidden })) }; })()`);
        const sortBy = key => ev(`document.querySelector('#servers-view .cs-sort[data-key="${key}"]').click(); true`);
        const open = async (where = '') => {
            const blank = Page.loadEventFired(); await Page.navigate({ url: 'about:blank' }); await blank;
            const loaded = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' + where }); await loaded;
            await install();
            await ev(`(() => { T.state.servers = ${JSON.stringify(LIVE)}; return true; })()`);
            if (!where) { await ev(`document.getElementById('nav-tab-servers').click(); true`); await waitFor(`!!document.querySelector('#servers-view table.cs-table')`, 'the Servers page', 8000); }
        };

        console.log('1. The Servers page is a table: columns, rows, one details row each, Add a machine at the top');
        await open();
        await waitFor(`document.querySelectorAll('#servers-view tr.cs-row').length === ${SERVERS.length}`, 'the rows', 6000);
        let t = await table();
        assert.equal(t.role, 'table'); assert.equal(t.label, 'Your servers');
        assert.deepEqual(t.heads.map(h => h.text), ['Name', 'Status', 'Coral', 'Agents', 'Hub access']);
        assert.ok(t.heads.every(h => h.scope === 'col' && h.button), 'every header is a button in a column header');
        assert.deepEqual(t.heads.map(h => h.sort), ['ascending', 'none', 'none', 'none', 'none'], 'sorted by name, ascending, to start with');
        assert.deepEqual(t.names, ['Box Four', 'Box One', 'Box Three', 'box two'].sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase())), 'by name, case ignored');
        const m1 = t.rows.find(r => r.id === 'server-row-m1');
        assert.equal(m1.status, 'Online'); assert.equal(m1.version.replace(/\s+/g, ' ').trim(), '1.4.10 Update required'); assert.equal(m1.agents, '7');
        assert.equal(m1.select, 'view'); assert.equal(m1.selectLabel, 'Hub access for Box One');
        assert.ok(t.rows.every(r => r.expanded === 'false' && r.detailHidden), 'every row starts closed');
        const order = await ev(`[...document.getElementById('cs-add-machine').parentElement.children].map(e => e.id || e.className.split(' ')[0])`);
        assert.ok(order.indexOf('cs-add-machine') >= 0 && order.indexOf('cs-add-machine') < order.findIndex(x => /cs-table/.test(x)), 'Add a machine comes before the table: ' + order);
        console.log('   ok');

        console.log('2. Sorting: each header sorts, a second press reverses, aria-sort follows, versions compare as numbers');
        await sortBy('version');
        t = await table();
        assert.deepEqual(t.heads.map(h => h.sort), ['none', 'none', 'ascending', 'none', 'none']);
        assert.deepEqual(t.names, ['Box Four', 'box two', 'Box One', 'Box Three'], '(none), 1.4.9, 1.4.10, 1.5.0: 1.4.10 is after 1.4.9');
        await sortBy('version');
        assert.deepEqual((await table()).names, ['Box Three', 'Box One', 'box two', 'Box Four']);
        await sortBy('agents');
        t = await table(); assert.equal(t.heads[3].sort, 'descending', 'agents start with the most');
        assert.deepEqual(t.names, ['Box Four', 'Box One', 'box two', 'Box Three']);
        await sortBy('status');
        t = await table(); assert.equal(t.heads[1].sort, 'ascending');
        assert.equal(t.rows[0].status, 'Offline', 'ascending: offline first');
        await sortBy('access');
        t = await table(); assert.equal(t.heads[4].sort, 'descending');
        assert.deepEqual(t.names[0], 'Box Three', 'the most access first (following a computer that allows Launch)');
        await sortBy('name');
        assert.deepEqual((await table()).heads.map(h => h.sort), ['ascending', 'none', 'none', 'none', 'none']);
        // The keyboard: a header is a button; Enter and Space press it, and focus stays on it.
        await ev(`document.querySelector('#servers-view .cs-sort[data-key="agents"]').focus(); true`);
        await client.Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' });
        await client.Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
        await waitFor(`document.querySelector('#servers-view .cs-th[aria-sort="descending"]') && document.activeElement && document.activeElement.classList.contains('cs-sort') && document.activeElement.dataset.key === 'agents'`, 'Enter sorts by agents and focus stays on the header', 3000);
        await sortBy('name');
        console.log('   ok');

        console.log('3. A row opens for its details; Rename and Revoke live there; the Hub access control stays in the row');
        await ev(`document.querySelector('#server-row-m1 .cs-toggle').focus(); document.querySelector('#server-row-m1 .cs-toggle').click(); true`);
        await waitFor(`document.querySelector('#server-row-m1 .cs-toggle').getAttribute('aria-expanded') === 'true' && !document.getElementById('server-row-m1-detail').hidden`, 'm1 open', 3000);
        const d = await ev(`(() => { const x = document.getElementById('server-row-m1-detail'); return { text: x.textContent.replace(/\\s+/g, ' '), buttons: [...x.querySelectorAll('button')].map(b => b.textContent.trim()), health: !!x.querySelector('.machine-health-card'),
            access: (x.querySelector('.cs-access') || {}).textContent || '', update: (x.querySelector('.cs-update a') || {}).href || null, focus: document.activeElement && document.activeElement.classList.contains('cs-toggle') }; })()`);
        assert.ok(d.buttons.includes('Rename') && d.buttons.includes('Revoke') && d.buttons.includes('Follow this computer'), JSON.stringify(d.buttons));
        assert.match(d.access.replace(/\s+/g, ' '), /Hub: Limit to View.*Machine: Launch.*Effective: View/);
        assert.equal(d.update, DOWNLOAD, 'the update notice points to the download page, not GitHub');
        assert.match(d.text, /You limited Coral Hub to View on this computer\. It allows Launch\./);
        assert.ok(d.focus, 'focus stays on the toggle');
        assert.equal(await ev(`document.querySelectorAll('#servers-view .cs-detail:not([hidden])').length`), 1, 'only the open row shows details');
        await ev(`document.querySelector('#server-row-m1 .cs-toggle').click(); true`);
        await waitFor(`document.getElementById('server-row-m1-detail').hidden`, 'm1 closed again', 3000);
        // Revoke asks inside the details and keeps the row open.
        await ev(`document.querySelector('#server-row-m3 .cs-toggle').click(); true`);
        await waitFor(`!document.getElementById('server-row-m3-detail').hidden`, 'm3 open', 3000);
        await ev(`document.querySelector('#server-row-m3-detail .cs-revoke').click(); true`);
        await waitFor(`/Confirm revoke/.test(document.getElementById('server-row-m3-detail').textContent)`, 'the revoke confirmation', 3000);
        assert.ok(await ev(`document.getElementById('server-row-m3').querySelector('.cs-toggle').getAttribute('aria-expanded') === 'true'`), 'the row stays open while it asks');
        await ev(`[...document.querySelectorAll('#server-row-m3-detail button')].find(b => b.textContent === 'Cancel').click(); true`);
        // Opening survives the page's refresh (a re-render keeps what is open).
        await ev(`(async () => { const cs = await import((window.__coralStatic || '/static/') + 'cloud_servers.js'); await cs.renderCloudServersInto(document.querySelector('#servers-view .servers-tab') || document.getElementById('servers-view')); return true; })()`).catch(() => {});
        await shot('table-desktop-open');
        console.log('   ok');

        console.log('4. Add a machine: the download link, the steps, a local picture; no brew command, no GitHub releases link');
        const a = await ev(`(() => { const e = document.getElementById('cs-add-machine'); const links = [...e.querySelectorAll('a')].map(x => x.href);
            const imgs = [...e.querySelectorAll('img')].map(i => ({ src: new URL(i.src).origin === location.origin, name: i.src.split('/').pop(), alt: i.alt, shown: getComputedStyle(i).display !== 'none', w: i.getBoundingClientRect().width, nat: i.naturalWidth }));
            return { links, imgs, text: e.textContent.replace(/\\s+/g, ' '), steps: e.querySelectorAll('ol li').length, code: !!e.querySelector('#cs-enroll-code') }; })()`);
        assert.deepEqual(a.links, [DOWNLOAD], 'one link: the download page');
        assert.doesNotMatch(a.text, /brew|github/i, 'no brew command and no GitHub link');
        assert.equal(a.steps, 4); assert.ok(a.code, 'the code form is still there');
        assert.ok(a.imgs.every(i => i.src && i.nat > 0), 'the pictures are this server\'s own and load: ' + JSON.stringify(a.imgs));
        assert.equal(a.imgs.filter(i => i.shown).length, 1, 'one picture per theme is shown');
        assert.match(a.imgs.find(i => i.shown).alt, /What may Coral Hub do on this computer\?/);
        assert.doesNotMatch(await ev(`document.documentElement.outerHTML`), /github\.com\/subgentic\/coral\/releases/, 'the page has no GitHub releases link');
        console.log('   ok');

        console.log('5. Deep link: /servers/<id> opens that machine\'s details');
        await open('servers/m2');
        await waitFor(`document.getElementById('server-row-m2') && document.getElementById('server-row-m2').querySelector('.cs-toggle').getAttribute('aria-expanded') === 'true'`, 'm2 open from the link', 6000);
        console.log('   ok');

        console.log('6. Both themes at 4.5:1 and the shots (desktop, then a phone)');
        for (const [w, h, mobile, tag] of [[1280, 900, false, 'desktop'], [390, 844, true, 'phone']]) {
            await client.Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await open();
            await waitFor(`document.querySelectorAll('#servers-view tr.cs-row').length === ${SERVERS.length}`, 'rows', 6000);
            await ev(`document.querySelector('#server-row-m1 .cs-toggle').click(); true`);
            await installAudit(ev);
            for (const theme of ['dark', 'light']) {
                await setTheme(ev, theme);
                const au = await auditRegion(ev, ['#cs-add-machine', '#servers-view .cs-table']);
                assert.ok(au.checked > 0 && !au.fails.filter(f => !/Revoke/.test(f.text || '')).length, `${tag} ${theme}: 4.5:1 ${JSON.stringify(au.fails).slice(0, 400)}`);
                const over = await ev(`document.documentElement.scrollWidth - innerWidth`);
                assert.ok(over <= 0, `${tag}: no sideways scroll (${over})`);
                await ev(`document.querySelector('#servers-view .cs-table').scrollIntoView(); true`);
                await shot(`table-${tag}-${theme}`);
            }
            await setTheme(ev, 'dark');
        }
        await client.Emulation.clearDeviceMetricsOverride();
        console.log('   ok');
        console.log('\nAll Servers table checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
