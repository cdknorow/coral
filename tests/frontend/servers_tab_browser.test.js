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

    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: CLOUD_PRELOAD });

        console.log('1. Coral Hub: the nav button, the view and focus');
        await cloudMode(true);
        await open();
        await ev(`localStorage.clear(); true`);
        let t = await tabState();
        assert.ok(t.tabShown, 'the Servers tab shows on the Coral Hub');
        // Page navigation, not tabs (accessibility pass): no tab role, aria-current marks the open page.
        assert.equal(t.role, null, 'no tab role'); assert.equal(t.controls, 'servers-view');
        assert.equal(t.selected, null, 'no aria-selected'); assert.equal(t.current, null, 'not current before opening');
        await ev(`document.getElementById('nav-tab-servers').click(); true`);
        await waitFor(`document.querySelectorAll('#servers-view .cs-row').length === ${SERVERS.length}`, 'the server rows', 5000);
        t = await tabState();
        assert.ok(t.viewShown && t.viewRole === 'region', 'the view is shown as a labelled region');
        assert.equal(t.current, 'page', 'aria-current=page after opening'); assert.equal(t.selected, null, 'still no aria-selected');
        assert.equal(t.focus, 'servers-tab-title', 'focus moves to the heading');
        assert.equal(await ev(`!!document.getElementById('cloud-servers-modal')`), false, 'no Servers dialog any more');
        console.log('   ✓ nav button aria-current/controls, region, heading focused, no dialog');

        // The row's words hold 4.5:1 in both themes (the control, the helper line, the notes, the Follow button; Revoke has its own check below), and CORAL_SCREENSHOT_DIR keeps the shots to look at.
        const rowShots = async (tag) => {
            const { installAudit, auditRegion, setTheme } = require('./helpers/contrast');
            await installAudit(ev);
            for (const theme of ['dark', 'light']) {
                await setTheme(ev, theme);
                const au = await auditRegion(ev, ['#servers-view .cs-launch', '#servers-view .cs-access-help', '#servers-view .cs-limit', '#servers-view .cs-note', '#servers-view .cs-access-info', '#servers-view .cs-access-matrix']);
                assert.ok(au.checked > 0 && !au.fails.length, `${tag} ${theme}: the row's words hold 4.5:1: ${JSON.stringify(au.fails).slice(0, 300)}`);
                if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/servers-access-${tag}-${theme}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
            }
            await setTheme(ev, 'dark');
        };
        console.log('2. Row access: one lower-only control, no Raise, no Launch dialog');
        const settled = () => ev(framesJs(2));
        const focusIs = (sel, what) => waitFor(`document.activeElement === document.querySelector(${JSON.stringify(sel)})`, what, 3000,
            `(() => { const a = document.activeElement; return a ? a.tagName + '#' + a.id + '.' + a.className : null; })()`);
        const rowView = id => ev(`(() => { const r = document.getElementById('server-row-${id}'), d = document.getElementById('server-row-${id}-detail'); const sel = r.querySelector('.cs-launch select');
            return { options: [...sel.options].map(o => o.textContent + (o.disabled ? ' (disabled)' : '')), value: sel.value, label: r.querySelector('.cs-launch').firstChild.textContent.trim(), describedBy: sel.getAttribute('aria-describedby'),
                help: (document.getElementById(sel.getAttribute('aria-describedby')) || {}).textContent || null,
                effective: [...d.querySelectorAll('.cs-access-effective')].map(n => n.textContent), actions: [...r.querySelectorAll('.cs-col-actions button')].map(b => b.textContent),
                inDetails: [...d.querySelectorAll('button:not(.cs-access-info)')].map(b => b.textContent), follow: !!r.querySelector('.cs-follow, .cs-limit'), raise: !!r.querySelector('.cs-raise'), summary: !!d.querySelector('.cs-access'), dialogs: document.querySelectorAll('dialog.cs-confirm').length }; })()`);
        let r = Object.fromEntries((await rows()).map(x => [x.id, x]));
        assert.ok(Object.values(r).every(x => x.tabindex === '-1'), 'rows are focus targets (tabindex=-1)');
        await ev(`document.querySelectorAll('#servers-view .cs-toggle[aria-expanded="false"]').forEach(b => b.click()); true`);
        await rowShots('desktop');
        // m1: the owner chose View, the computer allows Launch: three options, none disabled; Rename and Revoke are in the row.
        let v = await rowView('m1');
        assert.deepEqual(v.options, ['View', 'Interact', 'Launch']);
        assert.equal(v.value, 'view'); assert.equal(v.label, 'Hub access for Box One');
        assert.deepEqual(v.actions, ['Rename', 'Revoke'], 'Rename and Revoke are row actions in the last column');
        assert.deepEqual(v.inDetails, [], 'no buttons in the details');
        assert.ok(!v.follow && !v.raise && !v.summary && v.dialogs === 0, 'no Follow, no Raise, no Hub/Machine/Effective line, no dialog');
        assert.deepEqual(v.effective, [], 'the effective value shows only when lower than the choice');
        assert.ok(v.describedBy && v.help, 'the help line describes the select');
        // m2: Launch chosen, the computer allows View only: Interact is disabled and the reason is TEXT in the select's description; the effective value shows because it is lower.
        v = await rowView('m2');
        assert.deepEqual(v.options, ['View', 'Interact (disabled)', 'Launch']);
        assert.equal(v.value, 'launch');
        assert.equal(v.help, 'This computer allows up to View; change it in Coral on that computer (Settings > Network and Hub).');
        assert.deepEqual(v.effective, ['Effective: View, limited by this computer']);
        // m3: Launch on a computer that allows Launch: nothing limits, the help says what Launch means.
        v = await rowView('m3');
        assert.deepEqual(v.options, ['View', 'Interact', 'Launch']);
        assert.equal(v.value, 'launch'); assert.deepEqual(v.effective, []); assert.equal(v.help, 'Can run commands on this computer.');
        // m4: the computer has not announced: View until it connects; Launch is disabled.
        v = await rowView('m4');
        assert.deepEqual(v.options, ['View', 'Interact', 'Launch (disabled)']);
        assert.equal(v.value, 'interact'); assert.equal(v.help, 'View until this computer connects.');
        // A choice is immediate: one PATCH with the word, no dialog, focus stays on the select.
        const accessOf = id => ev(`import((window.__coralStatic || '/static/') + 'cloud_servers.js').then(cs => cs.serverAccess(${JSON.stringify(id)})).then(a => a && { effective: a.effective, mayInteract: a.mayInteract })`);
        const pick = (id, mode) => ev(`(() => { const s = document.querySelector('#server-row-${id} select'); s.focus(); s.value = ${JSON.stringify(mode)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await ev(`window.__patches = []; true`);
        await pick('m1', 'launch');
        await waitFor(`window.__patches.length > 0`, 'the Launch PATCH', 3000);
        await settled();
        assert.deepEqual(await ev('window.__patches'), [{ id: 'm1', body: { access_mode: 'launch' } }], 'choosing Launch sends {access_mode: launch}, no confirmation');
        await waitFor(`document.querySelector('#server-row-m1 .cs-launch select').value === 'launch'`, 'm1 at Launch', 3000);
        await focusIs('#server-row-m1 .cs-launch select', 'focus stays on the select');
        assert.deepEqual(await accessOf('m1'), { effective: 'launch', mayInteract: true }, 'the next access check sees Launch at once (#3125)');
        await ev(`window.__patches = []; true`);
        await pick('m3', 'interact');
        await waitFor(`window.__patches.length > 0`, 'the Interact PATCH', 3000);
        await settled();
        assert.deepEqual(await ev('window.__patches'), [{ id: 'm3', body: { access_mode: 'interact' } }]);
        assert.equal((await rowView('m3')).dialogs, 0, 'a lower choice asks nothing');
        console.log('   ✓ Hub access: View / Interact / Launch, an option above the computer disabled with its reason as text, Rename and Revoke in the row, immediate changes, focus kept');

        console.log('2b. Row cells (status, Coral version, agents) and the details line (last contact when offline), update badge, no "null"');
        const rowInfo = await ev(`Object.fromEntries([...document.querySelectorAll('#servers-view .cs-row')].map(r => {
            const d = document.getElementById(r.id + '-detail');
            const badges = [...r.querySelectorAll('a.server-update-badge')].map(a => ({ cls: a.className, href: a.getAttribute('href'), aria: a.getAttribute('aria-label'), target: a.getAttribute('target'), rel: a.getAttribute('rel'), inVersion: !!a.closest('.cs-col-version') }));
            const cell = c => r.querySelector('.cs-col-' + c).textContent.replace(/\\s+/g, ' ').trim();
            return [r.id.replace('server-row-', ''), { status: cell('status'), version: r.querySelector('.cs-col-version span').textContent, agents: cell('agents'), meta: (d.querySelector('.cs-lastseen') || {}).textContent || '', badges }];
        }))`);
        assert.deepEqual([rowInfo.m1.status, rowInfo.m1.version, rowInfo.m1.agents, rowInfo.m1.meta], ['Online', '1.4.2', '7', ''], 'm1: nothing repeated in the details, and no last contact while online (#3152)');
        assert.deepEqual([rowInfo.m2.status, rowInfo.m2.version, rowInfo.m2.agents], ['Online', '1.4.6', '1'], "m2: version from 'version'");
        assert.equal(rowInfo.m3.status, 'Offline'); assert.equal(rowInfo.m3.agents, '0');
        assert.match(rowInfo.m3.meta, /^Last contact /, `m3: offline shows last contact in the details (${rowInfo.m3.meta})`);
        assert.deepEqual([rowInfo.m4.version, rowInfo.m4.agents], ['—', '2'], 'm4: a dash when the version is unknown');
        const notice = { m1: 'Update required: this Coral (1.4.2) is too old for some Coral Hub features. Update to 1.5.0.', m2: 'Update available: 1.5.0' };
        for (const [id, level] of [['m1', 'required'], ['m2', 'available']]) {
            assert.equal(rowInfo[id].badges.length, 1, `${id}: one update badge`);
            const b = rowInfo[id].badges[0];
            assert.equal(b.cls, `server-update-badge server-update-${level}`);
            assert.equal(b.href, RELEASES, `${id}: constant download link`);
            assert.equal(b.aria, notice[id], `${id}: aria-label is the notice`);
            assert.ok(b.inVersion, `${id}: badge sits in the Coral column`);
            assert.equal(b.target, '_blank'); assert.equal(b.rel, 'noopener noreferrer');
        }
        assert.equal(rowInfo.m3.badges.length, 0, 'no badge when current'); assert.equal(rowInfo.m4.badges.length, 0, 'no badge when unknown');
        const nulls = await ev(`(() => { const w = document.createTreeWalker(document.getElementById('servers-view'), NodeFilter.SHOW_TEXT); const out = []; for (let n = w.nextNode(); n; n = w.nextNode()) if (/\\bnull\\b/.test(n.nodeValue)) out.push(n.nodeValue.trim()); return out; })()`);
        assert.deepEqual(nulls, [], 'no literal "null" text in the Servers view');
        console.log('   ✓ cells per row, last contact in the details, badges m1/m2 only in the Coral column, no "null"');

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
        await ev(`${themeJs('light')}; true`);
        await sleep(100);
        const lt = await ev(`(() => {
            const b = document.querySelector('#servers-view .cs-revoke');
            let el = b, bg = 'rgba(0, 0, 0, 0)';
            while (el && /rgba\\(0, 0, 0, 0\\)|transparent/.test(bg)) { bg = getComputedStyle(el).backgroundColor; el = el.parentElement; }
            return { color: getComputedStyle(b).color, bg };
        })()`);
        const lratio = contrast(lt.color, lt.bg);
        await ev(`${themeJs('dark')}; true`);
        assert.ok(lratio >= 4.5, `light theme: Revoke contrast ${lratio.toFixed(2)}:1 (${lt.color} on ${lt.bg})`);
        console.log(`   ✓ Revoke dark ${rv.color} on ${rv.bg}: ${ratio.toFixed(2)}:1; light ${lt.color} on ${lt.bg}: ${lratio.toFixed(2)}:1`);

        console.log('4. Deep links: /servers/<id>, /servers, the old #servers/<id>, and the agent list server chip');
        await open('servers/m2');
        await waitFor(`document.activeElement && document.activeElement.id === 'server-row-m2'`, '/servers/m2 focuses its row', 6000);
        assert.ok((await rows()).find(x => x.id === 'server-row-m2').target, 'the linked row is highlighted');
        assert.equal((await tabState()).current, 'page');
        await open('servers');
        await waitFor(`document.activeElement && document.activeElement.id === 'servers-tab-title'`, '/servers focuses the heading', 6000);
        await open('#servers/m2');
        await waitFor(`document.activeElement && document.activeElement.id === 'server-row-m2'`, 'the old #servers/m2 link focuses its row', 6000);
        assert.equal(await ev('location.pathname + location.hash'), '/servers/m2', 'and its URL becomes the path');
        await ev(`window.switchNavTab('agents'); localStorage.setItem('coral-group-by-server', 'true'); window.__m.add('chip-agent', 'C', 3, { sleeping: false, server: 'm3' }); T.render.renderLiveSessions(T.state.liveSessions); true`);
        // #4932 (replaces the removed search-row pill): a server group header's ⋮ panel has an 'Open in Servers' button that opens its row.
        await ev(`document.querySelector('li.server-group-header[data-server-group="m3"] details[data-server-details]').open = true; true`);
        const item = await ev(`(() => { const b = document.querySelector('li.server-group-header[data-server-group="m3"] button.server-open-btn[data-server-open="m3"]'); return b ? { tag: b.tagName, focusable: b.tabIndex >= 0, text: b.textContent.trim() } : null; })()`);
        assert.ok(item && item.tag === 'BUTTON' && item.focusable, `the header's Open in Servers is a keyboard-reachable button (${JSON.stringify(item)})`);
        assert.equal(item.text, 'Open in Servers', 'its label says what it opens');
        await ev(`document.querySelector('li.server-group-header[data-server-group="m3"] button.server-open-btn[data-server-open="m3"]').click(); true`);
        await waitFor(`document.activeElement && document.activeElement.id === 'server-row-m3'`, 'the header Open in Servers opens the tab at m3', 6000);
        assert.equal(await ev('location.pathname'), '/servers/m3', 'it writes its row\'s path');
        console.log('   ✓ /servers/m2 → row focused + highlighted; /servers → heading; #servers/m2 → /servers/m2; header Open in Servers → row m3');

        console.log('5. Phone 375 px: no overflow, actions stacked under the details');
        await Emulation.setDeviceMetricsOverride({ width: 375, height: 812, deviceScaleFactor: 2, mobile: true });
        await open('servers');
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
        await rowShots('phone');
        await Emulation.clearDeviceMetricsOverride();
        console.log('   ✓ overflow 0 (document, view, rows), actions stacked, controls inside');

        console.log('6. Local Coral: hidden without remotes, shown on a hub with one, falls back when it goes');
        await cloudMode(false);
        await open('#servers');
        await sleep(300);
        t = await tabState();
        assert.equal(t.tabShown, false, 'a local Coral with no remotes has no Servers tab');
        assert.equal(t.viewShown, false, '#servers is a no-op there');
        assert.equal(await ev('location.pathname + location.hash'), '/agents', 'and the URL goes home');
        await ev(`(async () => {
            const real = window.fetch;
            window.fetch = (u, o) => (new URL(String(u), location.origin).pathname === '/api/servers' && (!o || !o.method || o.method === 'GET'))
                ? Promise.resolve(new Response(JSON.stringify([{ id: 'r1', label: 'Remote', url: 'https://r1.example:8420', last_seen: new Date().toISOString(), allow_private: false }]), { status: 200, headers: { 'content-type': 'application/json' } }))
                : real(u, o);
            T.state.hub = true; T.state.servers = [{ id: 'r1', label: 'Remote', status: 'online', version: '1.4.5', agents: 3 }];
            (await import((window.__coralStatic||'/static/')+'servers_tab.js')).updateServersTabVisibility(); return true;
        })()`);
        assert.ok((await tabState()).tabShown, 'a hub with a remote shows the tab');
        await ev(`window.switchNavTab('servers'); true`);
        await waitFor(`document.getElementById('nav-tab-servers').getAttribute('aria-current') === 'page'`, 'nav button current', 3000);
        await waitFor(`!!document.querySelector('#server-row-r1 .servers-row-meta')`, 'self-hosted r1 row', 3000);
        const sh = await ev(`(() => { const r = document.querySelector('#server-row-r1 .servers-row-meta'); return { meta: r.textContent.replace(/\\s+/g, ' ').trim(), badges: document.querySelectorAll('#servers-view a.server-update-badge').length }; })()`);
        assert.match(sh.meta, /^https:\/\/r1\.example:8420 · Coral 1\.4\.5 · 3 agents$/, `self-hosted meta: online per the feed, no 'last contact' (#3152) (${sh.meta})`);
        assert.equal(sh.badges, 0, 'self-hosted rows have no update badge');
        await ev(`(async () => { T.state.servers = []; (await import((window.__coralStatic||'/static/')+'servers_tab.js')).updateServersTabVisibility(); return true; })()`);
        t = await tabState();
        assert.equal(t.tabShown, false, 'the last remote gone: tab hidden');
        assert.equal(t.viewShown, false, 'and the active tab falls back to Agents');
        console.log('   ✓ hidden locally, shown with a remote, falls back to Agents when it goes');

        console.log('7. Enroll page points new machines at Servers');
        const enroll = fs.readFileSync(path.join(__dirname, '../../coral-go/pkg/frontend/templates/cloud/enroll.html'), 'utf8');
        assert.match(enroll, /A machine connected from the command line starts in View \(read-only\) until you change that in Coral on it; you can limit access further in <a href="\/servers">Servers<\/a>\./);
        assert.match(enroll, /<a class="cloud-btn" href="\/servers">Open Servers<\/a>/);
        console.log('   ✓ review note + done-step Open Servers link to /servers');

        console.log('\nAll Servers tab checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
