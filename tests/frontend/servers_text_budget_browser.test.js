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
    // The owner limited it to View while the computer allows Launch: the note, and the way back (Follow this computer).
    { id: 'm1', label: 'Box One', online: true, access_mode: 'view', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z' },
    // Following a computer that allows View only: its note, the path to the question.
    { id: 'm2', label: 'Box Two', online: true, access_mode: 'follow', local_mode: 'view', last_seen: '2026-10-13T00:00:00Z' },
    // Following a computer that allows Launch: nothing limits.
    { id: 'm3', label: 'Box Three', online: false, access_mode: 'follow', local_mode: 'launch', last_seen: '2026-10-12T00:00:00Z' },
    // The computer has not announced: View until it connects; the hub's own effective_mode / limited_by win over the computed ones.
    { id: 'm4', label: 'Box Four', online: true, access_mode: 'interact', effective_mode: 'view', limited_by: 'machine', last_seen: '2026-10-13T00:00:00Z' },
];

// The live list's servers entries (#3099): version, agent count and the hub's update notice per machine.
const LIVE = [
    { id: 'm1', label: 'Box One', status: 'online', coral_version: '1.4.2', agents: 7, update: 'required', latest_coral: '1.5.0' },
    { id: 'm2', label: 'Box Two', status: 'online', version: '1.4.6', agents: 1, update: 'available', latest_coral: '1.5.0' },
    { id: 'm3', label: 'Box Three', status: 'offline', coral_version: '1.5.0', agents: 0, update: 'current' },
    { id: 'm4', label: 'Box Four', status: 'online', agents: 2, update: 'unknown' },
];
const RELEASES = 'https://coralhub.dev/coral.html#download';

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

    const words = () => ev(`(() => { let n = 0; const shown = e => { for (let p = e; p; p = p.parentElement) { const s = getComputedStyle(p); if (s.display === 'none' || s.visibility === 'hidden' || p.hidden) return false; } return true; };
        const w = document.createTreeWalker(document.getElementById('servers-view'), NodeFilter.SHOW_TEXT);
        for (let t = w.nextNode(); t; t = w.nextNode()) { const e = t.parentElement; if (!e || e.closest('script, style, .material-icons, select, option') || !shown(e)) continue; n += t.nodeValue.trim().split(/\\s+/).filter(Boolean).length; }
        return n; })()`);
    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: CLOUD_PRELOAD });
        await cloudMode(true);
        for (const [w, h, mobile, budget] of [[1280, 900, false, 67], [390, 844, true, 66]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await open();
            await ev(mobile ? `(document.querySelector('#nav-more-menu [data-arg="servers"]') || document.getElementById('nav-tab-servers')).click(); true` : `document.getElementById('nav-tab-servers').click(); true`);
            await waitFor(`document.querySelectorAll('#servers-view .cs-row').length === ${SERVERS.length}`, 'the server rows', 8000);
            const n = await words();
            assert.ok(n <= budget, `${w}px: the Servers page shows ${n} words with four computers, budget ${budget}`);
            const st = await ev(`(() => { const sec = document.getElementById('cs-add-machine'), b = document.getElementById('cs-enroll-toggle'); const cs = getComputedStyle(sec); return { intro: !!document.querySelector('.servers-intro'), btn: b.textContent.trim(), border: cs.borderTopWidth, padding: cs.paddingTop, left: Math.round(b.getBoundingClientRect().left) - Math.round(document.querySelector('.cs-table').getBoundingClientRect().left) }; })()`);
            assert.equal(st.intro, false, `${w}px: no intro line ("Manage connected machines" said nothing the page does not)`);
            assert.equal(st.btn, 'Add a computer', `${w}px: the button says computer, as the rest of the dashboard does`);
            assert.ok(st.border === '0px' && st.padding === '0px', `${w}px: closed, Add a computer is a button, not a dashed box around one (${JSON.stringify(st)})`);
            assert.ok(Math.abs(st.left) <= 2, `${w}px: the button lines up with the table (${st.left} px)`);
        }
        console.log('PASS Servers page: within the word budget, no intro line, a plain Add a computer button');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
