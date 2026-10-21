// Servers tab (#3070): the Cloud Hub's machines (and a self-hosted hub's remotes) are a top-level tab, not a
// dialog. Pins: when the tab shows, the nav button's aria-current, the region and focus, each row's access line (Hub / Machine /
// Effective) and 'Raise hub mode' only when the hub is the limit (PATCH {access_mode: <machine>}), Revoke
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
    // The hub limits (View) while the machine allows Launch: Raise offered.
    { id: 'm1', label: 'Box One', online: true, access_mode: 'view', local_mode: 'launch', last_seen: '2026-10-13T00:00:00Z' },
    // The machine limits: no Raise.
    { id: 'm2', label: 'Box Two', online: true, access_mode: 'launch', local_mode: 'view', last_seen: '2026-10-13T00:00:00Z' },
    // Nothing limits: no Raise.
    { id: 'm3', label: 'Box Three', online: false, access_mode: 'launch', local_mode: 'launch', last_seen: '2026-10-12T00:00:00Z' },
    // The machine's mode is unknown; the hub's own effective_mode / limited_by win over the computed ones.
    { id: 'm4', label: 'Box Four', online: true, access_mode: 'interact', effective_mode: 'view', limited_by: 'hub', last_seen: '2026-10-13T00:00:00Z' },
];

// The live list's servers entries (#3099): version, agent count and the hub's update notice per machine.
const LIVE = [
    { id: 'm1', label: 'Box One', status: 'online', coral_version: '1.4.2', agents: 7, update: 'required', latest_coral: '1.5.0' },
    { id: 'm2', label: 'Box Two', status: 'online', version: '1.4.6', agents: 1, update: 'available', latest_coral: '1.5.0' },
    { id: 'm3', label: 'Box Three', status: 'offline', coral_version: '1.5.0', agents: 0, update: 'current' },
    { id: 'm4', label: 'Box Four', status: 'online', agents: 2, update: 'unknown' },
];
const RELEASES = 'https://github.com/subgentic/coral/releases/latest';

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
        raise: r.querySelector('.cs-raise') ? r.querySelector('.cs-raise').textContent.trim() : null,
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
        // Raising to Launch asks first (#3115). Every way out but 'Raise to Launch' sends nothing.
        const dlgState = () => ev(`(() => {
            const d = document.querySelectorAll('dialog.cs-confirm');
            const x = d[0]; if (!x) return { n: 0 };
            const by = id => (document.getElementById(x.getAttribute(id)) || {}).textContent || null;
            return { n: d.length, open: x.open, modal: x.matches(':modal'), title: by('aria-labelledby'), text: by('aria-describedby'),
                focus: document.activeElement && document.activeElement.textContent, buttons: [...x.querySelectorAll('button')].map(b => b.textContent) };
        })()`);
        const raiseM1 = async () => {
            await ev(`document.querySelector('#server-row-m1 .cs-raise').click(); true`);
            await waitFor(`!!document.querySelector('dialog.cs-confirm[open]')`, 'the Launch confirmation', 3000);
        };
        const noDialog = what => waitFor(`!document.querySelector('dialog.cs-confirm')`, `the dialog gone after ${what}`, 3000);
        const settled = () => ev(framesJs(2));
        // After a cancel the row re-renders; focus goes back to the control the user came from, not the heading.
        const focusIs = (sel, what) => waitFor(`document.activeElement === document.querySelector(${JSON.stringify(sel)})`, what, 3000,
            `(() => { const a = document.activeElement; return a ? a.tagName + '#' + a.id + '.' + a.className : null; })()`);
        await raiseM1();
        let d = await dlgState();
        assert.equal(d.n, 1, 'one dialog');
        assert.ok(d.open && d.modal, 'a modal dialog (showModal)');
        assert.equal(d.title, 'Allow Box One to launch agents from the Coral Hub?', 'aria-labelledby names the server');
        assert.equal(d.text, 'This lets the hub start new processes on that machine.', 'aria-describedby says what Launch allows');
        assert.deepEqual(d.buttons, ['Cancel', 'Raise to Launch']);
        assert.equal(d.focus, 'Cancel', 'Cancel has focus, so Enter does not raise');
        await ev(`[...document.querySelectorAll('dialog.cs-confirm button')].find(b => b.textContent === 'Cancel').click(); true`);
        await noDialog('Cancel');
        await settled();
        assert.deepEqual(await ev('window.__patches'), [], 'Cancel sends nothing');
        await focusIs('#server-row-m1 .cs-raise', 'focus back on Raise after Cancel');
        assert.ok(await ev(`!!document.querySelector('#server-row-m1 .cs-raise')`), 'Raise is still offered after Cancel');

        await raiseM1();
        await client.Input.dispatchKeyEvent({ type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await client.Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await noDialog('Escape');
        await settled();
        assert.deepEqual(await ev('window.__patches'), [], 'Escape sends nothing');
        await focusIs('#server-row-m1 .cs-raise', 'focus back on Raise after Escape');

        await raiseM1();
        await ev(`document.querySelector('dialog.cs-confirm').close(); true`);
        await noDialog('close()');
        await settled();
        assert.deepEqual(await ev('window.__patches'), [], 'closing the dialog any other way sends nothing');
        await focusIs('#server-row-m1 .cs-raise', 'focus back on Raise after close()');

        // A double click on Raise gives one dialog; a double click on 'Raise to Launch' one PATCH.
        await ev(`(() => { const b = document.querySelector('#server-row-m1 .cs-raise'); b.click(); b.click(); return true; })()`);
        await waitFor(`!!document.querySelector('dialog.cs-confirm[open]')`, 'the Launch confirmation', 3000);
        assert.equal((await dlgState()).n, 1, 'a double click on Raise opens one dialog');
        await ev(`(() => { const b = [...document.querySelectorAll('dialog.cs-confirm button')].find(b => b.textContent === 'Raise to Launch'); b.click(); b.click(); return true; })()`);
        await waitFor(`window.__patches.length > 0`, 'the PATCH', 3000);
        await noDialog('confirm');
        await settled();
        assert.deepEqual(await ev('window.__patches'), [{ id: 'm1', body: { access_mode: 'launch' } }], 'a double click on Raise to Launch sends one PATCH');
        // Put m1 back for the rest of the suite.
        await ev(`(async () => { window.__qaServers.find(s => s.id === 'm1').access_mode = 'view'; window.__patches = []; const cs = await import((window.__coralStatic || '/static/') + 'cloud_servers.js'); await cs.renderCloudServersInto(document.getElementById('servers-tab-body')); return true; })()`);
        await waitFor(`!!document.querySelector('#server-row-m1 .cs-raise')`, 'Raise back on m1', 3000);

        // The Access select goes through the same gate: only a raise TO Launch asks.
        const pick = (id, mode) => ev(`(() => { const s = document.querySelector('#server-row-${id} select'); s.value = ${JSON.stringify(mode)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        const selectOf = id => ev(`document.querySelector('#server-row-' + ${JSON.stringify(id)} + ' select').value`);
        await pick('m1', 'interact');
        await waitFor(`window.__patches.length > 0`, 'the Interact PATCH', 3000);
        await settled();
        assert.equal((await dlgState()).n, 0, 'raising to Interact does not ask');
        assert.deepEqual(await ev('window.__patches'), [{ id: 'm1', body: { access_mode: 'interact' } }], 'select Interact: one PATCH, no dialog');
        await waitFor(`document.querySelector('#server-row-m1 select').value === 'interact'`, 'm1 at Interact', 3000);
        await ev(`window.__patches = []; true`);
        await pick('m1', 'launch');
        await waitFor(`!!document.querySelector('dialog.cs-confirm[open]')`, 'the Launch confirmation from the select', 3000);
        assert.equal((await dlgState()).title, 'Allow Box One to launch agents from the Coral Hub?');
        await ev(`[...document.querySelectorAll('dialog.cs-confirm button')].find(b => b.textContent === 'Cancel').click(); true`);
        await noDialog('Cancel from the select');
        await waitFor(`document.querySelector('#server-row-m1 select').value === 'interact'`, 'the select back on the current mode', 3000);
        await settled();
        assert.deepEqual(await ev('window.__patches'), [], 'select Launch then Cancel: nothing sent');
        assert.equal(await selectOf('m1'), 'interact', 'select Launch then Cancel: the select shows the current mode again');
        await focusIs('#server-row-m1 .cs-launch select', 'focus back on the Access select after Cancel');
        // Lowering never asks.
        await pick('m3', 'view');
        await waitFor(`window.__patches.length > 0`, 'the lowering PATCH', 3000);
        await settled();
        assert.equal((await dlgState()).n, 0, 'lowering from Launch does not ask');
        assert.deepEqual(await ev('window.__patches'), [{ id: 'm3', body: { access_mode: 'view' } }]);
        // ...and raising it back through the select asks, and confirming sends exactly that.
        await waitFor(`document.querySelector('#server-row-m3 select').value === 'view'`, 'm3 at View', 3000);
        await ev(`window.__patches = []; true`);
        await pick('m3', 'launch');
        await waitFor(`!!document.querySelector('dialog.cs-confirm[open]')`, 'the Launch confirmation for m3', 3000);
        assert.equal((await dlgState()).title, 'Allow Box Three to launch agents from the Coral Hub?', 'the dialog names the server whose select changed');
        await ev(`[...document.querySelectorAll('dialog.cs-confirm button')].find(b => b.textContent === 'Raise to Launch').click(); true`);
        await waitFor(`window.__patches.length > 0`, 'the m3 PATCH', 3000);
        await settled();
        assert.deepEqual(await ev('window.__patches'), [{ id: 'm3', body: { access_mode: 'launch' } }], 'select Launch then confirm: one PATCH');
        // Put m1 back to View for the original Raise check below.
        // The stub row is reset behind the app's back, so drop the access cache too: since #3138 the app re-reads it after each change (it would still hold m1 at Interact from the PATCH above).
        await ev(`(async () => { window.__qaServers.find(s => s.id === 'm1').access_mode = 'view'; window.__patches = []; const cs = await import((window.__coralStatic || '/static/') + 'cloud_servers.js'); cs.invalidateServerAccess(); await cs.renderCloudServersInto(document.getElementById('servers-tab-body')); return true; })()`);
        await waitFor(`!!document.querySelector('#server-row-m1 .cs-raise') && document.querySelector('#server-row-m3 select').value === 'launch'`, 'm1 back at View, m3 at Launch', 3000);

        // The label is text, never markup; exactly one callback runs.
        const xss = await ev(`(async () => {
            const cs = await import((window.__coralStatic || '/static/') + 'cloud_servers.js');
            const calls = [];
            const dlg = cs.confirmLaunch('<img src=x onerror="window.__qaXss=1">', () => calls.push('yes'), () => calls.push('no'));
            const r = { img: !!dlg.querySelector('img'), title: dlg.querySelector('h3').textContent };
            dlg.querySelector('button').click();
            dlg.close();
            await new Promise(res => setTimeout(res, 0));
            return { ...r, calls, xss: !!window.__qaXss };
        })()`);
        assert.equal(xss.img, false, 'a label with markup renders no element');
        assert.equal(xss.title, 'Allow <img src=x onerror="window.__qaXss=1"> to launch agents from the Coral Hub?');
        assert.equal(xss.xss, false);
        assert.deepEqual(xss.calls, ['no'], 'Cancel then close(): exactly one callback');
        console.log('   ✓ Launch confirmation: modal, labelled, Cancel focused; Cancel/Escape/close send nothing and focus returns to Raise/the select; one dialog and one PATCH on double clicks; select Interact and lowering never ask; select Launch asks and Cancel restores it; label is text');

        // Warm the 15 s access cache with m1 at View, so the check after the Raise shows the cache was dropped (#3125).
        const accessOf = id => ev(`import((window.__coralStatic || '/static/') + 'cloud_servers.js').then(cs => cs.serverAccess(${JSON.stringify(id)})).then(a => a && { effective: a.effective, mayInteract: a.mayInteract })`);
        assert.deepEqual(await accessOf('m1'), { effective: 'view', mayInteract: false }, 'before the Raise, m1 is View');
        await ev(`document.querySelector('#server-row-m1 .cs-raise').click(); true`);
        // Raising to Launch asks first (#3115); confirm it.
        await waitFor(`!!document.querySelector('dialog.cs-confirm[open]')`, 'the Launch confirmation', 3000);
        await ev(`[...document.querySelectorAll('dialog.cs-confirm button')].find(b => b.textContent === 'Raise to Launch').click(); true`);
        await waitFor(`window.__patches.length > 0`, 'the PATCH', 3000);
        assert.deepEqual(await ev('window.__patches'), [{ id: 'm1', body: { access_mode: 'launch' } }], 'Raise sends PATCH /api/servers/m1 {access_mode: launch}');
        assert.deepEqual(await accessOf('m1'), { effective: 'launch', mayInteract: true }, 'after the Raise the next access check sees Launch at once, not up to 15 s later (#3125)');
        await waitFor(`!document.querySelector('#server-row-m1 .cs-raise')`, 'Raise gone once the hub allows Launch', 6000);
        console.log('   ✓ Hub/Machine/Effective per row, Raise only for m1, PATCH {access_mode:"launch"}, then gone');

        console.log('2b. Row meta (status · Coral version · agents · last contact when offline), update badge, no "null", hidden sr-only state');
        const rowInfo = await ev(`Object.fromEntries([...document.querySelectorAll('#servers-view .cs-row')].map(r => {
            const meta = r.querySelector('.cs-meta');
            const badges = [...r.querySelectorAll('a.server-update-badge')].map(a => ({ cls: a.className, href: a.getAttribute('href'), aria: a.getAttribute('aria-label'), target: a.getAttribute('target'), rel: a.getAttribute('rel'), inTitle: !!a.closest('.cs-title') }));
            const title = r.querySelector('.cs-title');
            const sr = title && title.querySelector('.sr-only');
            return [r.id.replace('server-row-', ''), { meta: meta ? meta.textContent.replace(/\\s+/g, ' ').trim() : '', badges, srText: sr ? sr.textContent : null,
                srVisible: sr ? (b => b.width > 2 && b.height > 2)(sr.getBoundingClientRect()) : false, visibleTitle: title ? title.innerText.trim() : '' }];
        }))`);
        const metaOf = id => rowInfo[id].meta;
        assert.match(metaOf('m1'), /^Online · Coral 1\.4\.2 · 7 agents$/, `m1 meta: no 'last contact' while online (#3152) (${metaOf('m1')})`);
        assert.match(metaOf('m2'), /^Online · Coral 1\.4\.6 · 1 agent$/, `m2 meta: version from 'version', singular agent (${metaOf('m2')})`);
        assert.match(metaOf('m3'), /^Offline · Coral 1\.5\.0 · 0 agents · last contact /, `m3 meta: offline shows last contact (${metaOf('m3')})`);
        assert.match(metaOf('m4'), /^Online · 2 agents$/, `m4 meta: no version when unknown (${metaOf('m4')})`);
        const notice = { m1: 'Update required: this Coral (1.4.2) is too old for some Coral Hub features. Update to 1.5.0.', m2: 'Update available: 1.5.0' };
        for (const [id, level] of [['m1', 'required'], ['m2', 'available']]) {
            assert.equal(rowInfo[id].badges.length, 1, `${id}: one update badge`);
            const b = rowInfo[id].badges[0];
            assert.equal(b.cls, `server-update-badge server-update-${level}`);
            assert.equal(b.href, RELEASES, `${id}: constant releases link`);
            assert.equal(b.aria, notice[id], `${id}: aria-label is the notice`);
            assert.ok(b.inTitle, `${id}: badge sits in the row title`);
            assert.equal(b.target, '_blank'); assert.equal(b.rel, 'noopener noreferrer');
        }
        assert.equal(rowInfo.m3.badges.length, 0, 'no badge when current'); assert.equal(rowInfo.m4.badges.length, 0, 'no badge when unknown');
        const nulls = await ev(`(() => { const w = document.createTreeWalker(document.getElementById('servers-view'), NodeFilter.SHOW_TEXT); const out = []; for (let n = w.nextNode(); n; n = w.nextNode()) if (/\\bnull\\b/.test(n.nodeValue)) out.push(n.nodeValue.trim()); return out; })()`);
        assert.deepEqual(nulls, [], 'no literal "null" text in the Servers view');
        for (const id of ['m1', 'm3']) {
            assert.match(rowInfo[id].srText || '', /\((online|offline)\)/, `${id}: status for screen readers`);
            assert.equal(rowInfo[id].srVisible, false, `${id}: the sr-only status is not visible`);
        }
        console.log('   ✓ meta per row (version from coral_version|version, omitted when unknown, 1 agent/N agents), badges m1/m2 only, no "null", sr-only hidden');

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
        assert.match(enroll, /New machines start in View mode \(read-only\): you can raise access later in <a href="\/servers">Servers<\/a>\./);
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
