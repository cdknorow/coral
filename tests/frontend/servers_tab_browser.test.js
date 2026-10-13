// Servers tab (#3070): the Cloud Hub's machines (and a self-hosted hub's remotes) are a top-level tab, not a
// dialog. Pins: when the tab shows, tab/tabpanel roles and focus, each row's access line (Hub / Machine /
// Effective) and 'Raise hub mode' only when the hub is the limit (PATCH {access_mode: <machine>}), Revoke
// contrast, deep links (#servers, #servers/<id>, the agent list's server chip), no overflow at 375 px with the
// actions stacked, and the enroll page's Servers note.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const SERVERS = [
    // The hub limits (View) while the machine allows Launch: Raise offered.
    { id: 'm1', label: 'Box One', online: true, access_mode: 'view', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z' },
    // The machine limits: no Raise.
    { id: 'm2', label: 'Box Two', online: true, access_mode: 'launch', local_mode: 'view', last_seen: '2026-10-13T00:00:00Z' },
    // Nothing limits: no Raise.
    { id: 'm3', label: 'Box Three', online: false, access_mode: 'launch', local_mode: 'launch', last_seen: '2026-10-12T00:00:00Z' },
    // The machine's mode is unknown; the hub's own effective_mode / limited_by win over the computed ones.
    { id: 'm4', label: 'Box Four', online: true, access_mode: 'interact', effective_mode: 'view', limited_by: 'hub', last_seen: '2026-10-13T00:00:00Z' },
];

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
            { urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' },
            { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
        ] });
    };
    Fetch.requestPaused(async e => {
        if (e.request.url.endsWith('/auth/session')) {
            return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
        }
        if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
        const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
        const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace('{"mode":"local"}', '{"mode":"cloud"}');
        const headers = (e.responseHeaders || []).filter(h => !/^content-(length|encoding)$/i.test(h.name));
        return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
    });
    const open = async (hash = '') => {
        await Page.navigate({ url: BASE + '/' + hash });
        await Page.loadEventFired();
        await install();
        await ev(`(() => { T.state.servers = ${JSON.stringify(SERVERS.map(s => ({ id: s.id, label: s.label, status: s.online ? 'online' : 'offline' })))}; return true; })()`);
    };
    const tabState = () => ev(`(() => {
        const tab = document.getElementById('nav-tab-servers'), view = document.getElementById('servers-view');
        return {
            tabShown: !!tab && !tab.hidden && getComputedStyle(tab).display !== 'none',
            role: tab && tab.getAttribute('role'), controls: tab && tab.getAttribute('aria-controls'), selected: tab && tab.getAttribute('aria-selected'),
            viewShown: !!view && getComputedStyle(view).display !== 'none', viewRole: view && view.getAttribute('role'),
            focus: document.activeElement && document.activeElement.id,
        };
    })()`);
    const rows = () => ev(`[...document.querySelectorAll('#servers-view .cs-row')].map(r => ({
        id: r.id, tabindex: r.getAttribute('tabindex'),
        access: (r.querySelector('.cs-access') || {}).textContent || '',
        raise: r.querySelector('.cs-raise') ? r.querySelector('.cs-raise').textContent.trim() : null,
        target: r.classList.contains('server-row-target'),
    }))`);

    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: CLOUD_PRELOAD });

        console.log('1. Cloud Hub: the tab, its roles and focus');
        await cloudMode(true);
        await open();
        await ev(`localStorage.clear(); true`);
        let t = await tabState();
        assert.ok(t.tabShown, 'the Servers tab shows on the Cloud Hub');
        assert.equal(t.role, 'tab'); assert.equal(t.controls, 'servers-view');
        assert.equal(t.selected, 'false', 'not selected before opening');
        await ev(`document.getElementById('nav-tab-servers').click(); true`);
        await waitFor(`document.querySelectorAll('#servers-view .cs-row').length === ${SERVERS.length}`, 'the server rows', 5000);
        t = await tabState();
        assert.ok(t.viewShown && t.viewRole === 'tabpanel', 'the tab panel is shown with role=tabpanel');
        assert.equal(t.selected, 'true', 'aria-selected after opening');
        assert.equal(t.focus, 'servers-tab-title', 'focus moves to the heading');
        assert.equal(await ev(`!!document.getElementById('cloud-servers-modal')`), false, 'no Servers dialog any more');
        console.log('   ✓ tab role/controls/selected, tabpanel, heading focused, no dialog');

        console.log('2. Row access lines and Raise');
        let r = Object.fromEntries((await rows()).map(x => [x.id, x]));
        const access = id => r['server-row-' + id].access.replace(/\s+/g, ' ');
        assert.match(access('m1'), /Hub: View/); assert.match(access('m1'), /Machine: Launch/); assert.match(access('m1'), /Effective: View/);
        assert.equal(r['server-row-m1'].raise, 'Raise hub mode to Launch', 'the hub limits and the machine allows Launch');
        assert.match(access('m2'), /Hub: Launch/); assert.match(access('m2'), /Machine: View/); assert.match(access('m2'), /Effective: View/);
        assert.equal(r['server-row-m2'].raise, null, 'no Raise when the machine is the limit');
        assert.match(access('m3'), /Effective: Launch/);
        assert.equal(r['server-row-m3'].raise, null, 'no Raise when nothing limits');
        assert.match(access('m4'), /Machine: unknown/i, 'an unknown machine mode says so');
        assert.match(access('m4'), /Effective: View/, "the hub's effective_mode wins over the computed one");
        assert.equal(r['server-row-m4'].raise, null, 'no Raise to an unknown machine mode');
        assert.ok(Object.values(r).every(x => x.tabindex === '-1'), 'rows are focus targets (tabindex=-1)');
        await ev(`document.querySelector('#server-row-m1 .cs-raise').click(); true`);
        await waitFor(`window.__patches.length > 0`, 'the PATCH', 3000);
        assert.deepEqual(await ev('window.__patches'), [{ id: 'm1', body: { access_mode: 'launch' } }], 'Raise sends PATCH /api/servers/m1 {access_mode: launch}');
        await waitFor(`!document.querySelector('#server-row-m1 .cs-raise')`, 'Raise gone once the hub allows Launch', 6000);
        console.log('   ✓ Hub/Machine/Effective per row, Raise only for m1, PATCH {access_mode:"launch"}, then gone');

        console.log('3. Revoke reads as an action (contrast >= 4.5:1)');
        const rv = await ev(`(() => {
            const b = document.querySelector('#servers-view .cs-revoke');
            let el = b, bg = 'rgba(0, 0, 0, 0)';
            while (el && /rgba\\(0, 0, 0, 0\\)|transparent/.test(bg)) { bg = getComputedStyle(el).backgroundColor; el = el.parentElement; }
            return { color: getComputedStyle(b).color, bg, disabled: b.disabled, opacity: getComputedStyle(b).opacity };
        })()`);
        const ratio = contrast(rv.color, rv.bg);
        assert.ok(!rv.disabled && Number(rv.opacity) === 1, 'Revoke is enabled and fully opaque');
        assert.ok(ratio >= 4.5, `Revoke contrast ${ratio.toFixed(2)}:1 (${rv.color} on ${rv.bg})`);
        // And on the light theme.
        await ev(`document.documentElement.setAttribute('data-theme', 'light'); true`);
        await sleep(100);
        const lt = await ev(`(() => {
            const b = document.querySelector('#servers-view .cs-revoke');
            let el = b, bg = 'rgba(0, 0, 0, 0)';
            while (el && /rgba\\(0, 0, 0, 0\\)|transparent/.test(bg)) { bg = getComputedStyle(el).backgroundColor; el = el.parentElement; }
            return { color: getComputedStyle(b).color, bg };
        })()`);
        const lratio = contrast(lt.color, lt.bg);
        await ev(`document.documentElement.setAttribute('data-theme', 'dark'); true`);
        assert.ok(lratio >= 4.5, `light theme: Revoke contrast ${lratio.toFixed(2)}:1 (${lt.color} on ${lt.bg})`);
        console.log(`   ✓ Revoke dark ${rv.color} on ${rv.bg}: ${ratio.toFixed(2)}:1; light ${lt.color} on ${lt.bg}: ${lratio.toFixed(2)}:1`);

        console.log('4. Deep links: #servers/<id>, #servers, and the agent list server chip');
        await open('#servers/m2');
        await waitFor(`document.activeElement && document.activeElement.id === 'server-row-m2'`, '#servers/m2 focuses its row', 6000);
        assert.ok((await rows()).find(x => x.id === 'server-row-m2').target, 'the linked row is highlighted');
        assert.equal((await tabState()).selected, 'true');
        await open('#servers');
        await waitFor(`document.activeElement && document.activeElement.id === 'servers-tab-title'`, '#servers focuses the heading', 6000);
        await ev(`window.switchNavTab('agents'); window.__m.add('chip-agent', 'C', 3, { sleeping: false, server: 'm3' }); T.render.renderLiveSessions(T.state.liveSessions); true`);
        const chip = await ev(`(() => { const b = document.querySelector('#live-sessions-list button.server-strip-btn[data-server-open="m3"]'); return b ? { tag: b.tagName, focusable: b.tabIndex >= 0, name: b.textContent.trim() || b.getAttribute('aria-label') } : null; })()`);
        assert.ok(chip && chip.tag === 'BUTTON' && chip.focusable, `the server chip is a keyboard-reachable button (${JSON.stringify(chip)})`);
        await ev(`document.querySelector('#live-sessions-list button.server-strip-btn[data-server-open="m3"]').click(); true`);
        await waitFor(`document.activeElement && document.activeElement.id === 'server-row-m3'`, 'the chip opens the tab at m3', 6000);
        console.log('   ✓ #servers/m2 → row focused + highlighted; #servers → heading; chip button → row m3');

        console.log('5. Phone 375 px: no overflow, actions stacked under the details');
        await Emulation.setDeviceMetricsOverride({ width: 375, height: 812, deviceScaleFactor: 2, mobile: true });
        await open('#servers');
        await waitFor(`document.querySelectorAll('#servers-view .cs-row').length === ${SERVERS.length}`, 'rows at 375', 6000);
        await sleep(200);
        const phone = await ev(`(() => {
            const v = document.getElementById('servers-view');
            const over = el => el.scrollWidth - el.clientWidth;
            const rows = [...v.querySelectorAll('.cs-row')];
            return {
                doc: document.documentElement.scrollWidth - innerWidth, view: over(v), rows: Math.max(...rows.map(over)),
                stacked: rows.every(r => { const a = r.querySelector('.cs-actions'), m = r.querySelector('.cs-main'); return !a || !m || a.getBoundingClientRect().top >= m.getBoundingClientRect().bottom - 1; }),
                inside: rows.every(r => [...r.querySelectorAll('button, select, a')].every(b => { const x = b.getBoundingClientRect(); return x.width === 0 || (x.left >= -1 && x.right <= innerWidth + 1); })),
            };
        })()`);
        assert.ok(phone.doc <= 0 && phone.view <= 0 && phone.rows <= 0, `no horizontal overflow at 375 (${JSON.stringify(phone)})`);
        assert.ok(phone.stacked, 'actions stack under the details');
        assert.ok(phone.inside, 'every control is inside the viewport');
        await Emulation.clearDeviceMetricsOverride();
        console.log('   ✓ overflow 0 (document, view, rows), actions stacked, controls inside');

        console.log('6. Local Coral: hidden without remotes, shown on a hub with one, falls back when it goes');
        await cloudMode(false);
        await open('#servers');
        await sleep(300);
        t = await tabState();
        assert.equal(t.tabShown, false, 'a local Coral with no remotes has no Servers tab');
        assert.equal(t.viewShown, false, '#servers is a no-op there');
        await ev(`(async () => { T.state.hub = true; T.state.servers = [{ id: 'r1', label: 'Remote', status: 'online' }]; (await import((window.__coralStatic||'/static/')+'servers_tab.js')).updateServersTabVisibility(); return true; })()`);
        assert.ok((await tabState()).tabShown, 'a hub with a remote shows the tab');
        await ev(`window.switchNavTab('servers'); true`);
        await waitFor(`document.getElementById('nav-tab-servers').getAttribute('aria-selected') === 'true'`, 'tab selected', 3000);
        await ev(`(async () => { T.state.servers = []; (await import((window.__coralStatic||'/static/')+'servers_tab.js')).updateServersTabVisibility(); return true; })()`);
        t = await tabState();
        assert.equal(t.tabShown, false, 'the last remote gone: tab hidden');
        assert.equal(t.viewShown, false, 'and the active tab falls back to Agents');
        console.log('   ✓ hidden locally, shown with a remote, falls back to Agents when it goes');

        console.log('7. Enroll page points new machines at Servers');
        const enroll = fs.readFileSync(path.join(__dirname, '../../coral-go/pkg/frontend/templates/cloud/enroll.html'), 'utf8');
        assert.match(enroll, /New machines start in View: you can raise access later in <a href="\/#servers">Servers<\/a>\./);
        assert.match(enroll, /<a class="cloud-btn" href="\/#servers">Open Servers<\/a>/);
        console.log('   ✓ review note + done-step Open Servers link to /#servers');

        console.log('\nAll Servers tab checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
