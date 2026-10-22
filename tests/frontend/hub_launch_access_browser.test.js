// Coral Hub, a machine the hub limits to View (#4586): the launch dialog says so when it opens (a note, the launch buttons dimmed) instead of after the form is filled in, and a click
// on Launch gives one "Blocked" toast and sends nothing. The Servers tab's Add a machine steps lead with the install step. Served as the hub would serve it (boot flag mode "cloud",
// as in hub_nav_browser), the machine's list and access level answered by the test.
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Fetch, Emulation } = client;
    const ev = async expression => { const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    let mode = 'view', launched = 0, hold = null;
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
            { urlPattern: `${BASE}/api/servers*`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/sessions/live*`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/remote/*`, requestStage: 'Request' }] });
        const json = (id, status, body) => Fetch.fulfillRequest({ requestId: id, responseCode: status, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify(body)).toString('base64') });
        safePaused(client, async e => {
            const u = e.request.url;
            if (u.endsWith('/auth/session')) return json(e.requestId, 200, { authenticated: true, email: 'tester@example.com', csrf: 'x' });
            if (/\/api\/servers(\?|$)/.test(u)) { if (hold) await hold; return json(e.requestId, 200, { servers: [{ id: 'box-b', label: 'Box B', online: true, access_mode: mode, local_mode: 'launch', created_at: '2026-10-01T00:00:00Z', last_seen: '2026-10-18T00:00:00Z' }] }); }
            if (/\/api\/sessions\/live(\?|$)/.test(u)) return json(e.requestId, 200, { sessions: [], servers: [{ id: 'box-b', label: 'Box B', status: 'online' }] });
            if (/\/api\/remote\//.test(u)) { if (/\/api\/sessions\/launch$/.test(u)) { launched++; return json(e.requestId, 403, { error: 'access_mode', hub_mode: 'view', required: 'launch' }); } return json(e.requestId, 200, {}); }
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
            html = html.replace('"mode":"local"', '"mode":"cloud"');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        const open = async () => {
            await Emulation.setDeviceMetricsOverride({ width: 1280, height: 756, deviceScaleFactor: 1, mobile: false });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await waitFor(`document.body.classList.contains('cloud-mode') && (window.__coralState || true) && document.querySelector('[data-on-click^="m-show-launch-modal"]')`, 'the hub dashboard', { timeout: 15000 });
        };
        const openLaunch = async () => {
            await ev(`document.querySelector('[data-on-click^="m-show-launch-modal"]').click(); true`);
            await waitFor(`!!document.getElementById('launch-modal') && getComputedStyle(document.getElementById('launch-modal')).display !== 'none'`, 'the launch dialog', { timeout: 6000 });
            await ev(`(() => { const s = document.getElementById('launch-server'); s.value = 'box-b'; window._onLaunchServerChange('box-b'); return true; })()`);
        };
        const toasts = () => ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent)`);

        console.log('1. A machine at View: the dialog says so as it opens, the launch buttons are dimmed');
        await open(); await openLaunch();
        // The note, its words and the dimming all follow the access answer, which arrives after the dialog opens, and the page treats an access it
        // does not hold (yet, or again: the cached answer is dropped and asked for anew) as "not blocked", so each can flicker off for a moment on a slow host.
        // So they are waited for together, in one evaluation, and nothing is read again afterwards (#4992: read a moment later, the button was undimmed).
        await ev(`window._selectLaunchType('agent'); true`);
        await waitFor(`(() => { const n = document.getElementById('launch-access-note'); return !n.hidden && /Box B needs Launch/.test(n.textContent) && document.querySelector('#launch-step-agent [data-on-click="m-launch-session"]').classList.contains('hub-blocked'); })()`, 'the note says Box B needs Launch and the launch button is dimmed', { timeout: 30000 });
        assert.equal(await ev(`document.querySelectorAll('.toast').length`), 0, 'nothing was toasted yet');

        console.log('1b. The controls scanner leaves the dialog\'s dimming alone (it cleared every hub-blocked class it did not set, which un-dimmed the buttons within 120 ms)');
        await ev(`(async () => { (await import((window.__coralStatic || '/static/') + 'hub_blocked_controls.js')).markBlockedControls(); return true; })()`);
        assert.equal(await ev(`document.querySelector('#launch-step-agent [data-on-click="m-launch-session"]').classList.contains('hub-blocked')`), true, 'the scan un-dimmed the launch button');

        console.log('2. Launch with the form filled in: one toast, nothing sent');
        // Click only at an instant when the page knows the machine is limited: the click then says so itself and sends nothing. (A click while the
        // access answer is being asked for again goes to the hub, which refuses it: a different, also correct, path, tested in 3b.)
        await waitFor(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'hub_access.js'); if (!m.blockedReason('box-b', 'launch')) return false; document.getElementById('launch-dir').value = '/work/project'; document.querySelector('#launch-step-agent [data-on-click="m-launch-session"]').click(); return true; })()`, 'a click while the machine is known to be limited', { timeout: 30000 });
        await waitFor(`document.querySelectorAll('.toast').length >= 1`, 'the toast', { timeout: 6000 });
        await new Promise(r => setTimeout(r, 1500)); // sleep-ok: a second toast, if there were one, arrives after the refusal; there is nothing to wait on for its absence
        const t = await toasts();
        assert.equal(t.length, 1, JSON.stringify(t)); assert.match(t[0], /^Blocked: Box B needs Launch/);
        assert.equal(launched, 0, 'the refused launch was never sent');

        console.log('2b. While the access answer is asked again the dialog keeps the last answer (dimmed, note up), and changes only when the new one arrives (#5004)');
        let release; hold = new Promise(r => { release = r; });
        await ev(`(async () => { (await import((window.__coralStatic || '/static/') + 'cloud_servers.js')).invalidateServerAccess(); document.dispatchEvent(new CustomEvent('coral:server-access-loaded')); return true; })()`);
        await new Promise(r => setTimeout(r, 400)); // sleep-ok: the stale answer must still be showing after the held request has had time to go out; there is nothing to wait on for a change that must not happen
        assert.equal(await ev(`document.getElementById('launch-access-note').hidden`), true === false, 'the note is still up while the answer is asked again');
        assert.equal(await ev(`document.querySelector('#launch-step-agent [data-on-click="m-launch-session"]').classList.contains('hub-blocked')`), true, 'the launch button is still dimmed');
        mode = 'launch'; release(); hold = null;
        await waitFor(`document.getElementById('launch-access-note').hidden && !document.querySelector('#launch-step-agent [data-on-click="m-launch-session"]').classList.contains('hub-blocked')`, 'the new answer (Launch) un-dims the dialog', { timeout: 8000 });

        console.log('3. A machine at Launch: no note, no dimming');
        mode = 'launch';
        await open(); await openLaunch();
        await waitFor(`document.getElementById('launch-server').value === 'box-b'`, 'the server picked', { timeout: 6000 });
        assert.equal(await ev(`document.getElementById('launch-access-note').hidden`), true);
        assert.equal(await ev(`document.querySelectorAll('#launch-modal .hub-blocked').length`), 0);

        console.log('3b. The hub refuses a launch the dialog did not know to be blocked: one toast, not two');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); window._selectLaunchType('agent'); document.getElementById('launch-dir').value = '/work/project'; document.querySelector('#launch-step-agent [data-on-click="m-launch-session"]').click(); true`);
        await waitFor(`document.querySelectorAll('.toast').length >= 1`, 'the refusal', { timeout: 6000 });
        await new Promise(r => setTimeout(r, 1500)); // sleep-ok: a second toast, if there were one, arrives after the refusal; there is nothing to wait on for its absence
        const t2 = await toasts();
        assert.equal(t2.length, 1, JSON.stringify(t2)); assert.match(t2[0], /^Blocked: /);
        assert.equal(launched, 1);

        console.log('4. The Add a machine steps lead with the install step (the download page)');
        await ev(`window.switchNavTab('servers'); true`);
        await waitFor(`!!document.querySelector('.cs-enroll ol li')`, 'the steps', { timeout: 8000 });
        await ev(`document.querySelector('#cs-enroll-toggle[aria-expanded=\"false\"]')?.click(); true`);
        const steps = await ev(`[...document.querySelectorAll('.cs-enroll ol li')].map(l => l.textContent)`);
        assert.match(steps[0], /^Install Coral on the computer you want to add: coralhub\.dev\/coral\.html#download$/);
        assert.match(steps[1], /Connect to Coral Hub/); assert.equal(steps.length, 4);
        assert.equal(await ev(`document.querySelector('.cs-enroll ol li a').href`), 'https://coralhub.dev/coral.html#download', 'the download page, no brew command and no GitHub link');

        console.log('5. The install link reads in the accent colour (4.5:1 on the page), on its own line, and is a 44 px target on a phone');
        const contrast = await ev(`(() => { const lum = c => { const [r, g, b] = c.match(/[0-9.]+/g).slice(0, 3).map(Number).map(v => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }); return 0.2126 * r + 0.7152 * g + 0.0722 * b; };
            const a = document.querySelector('.cs-enroll ol li a'); const fg = lum(getComputedStyle(a).color), bg = lum(getComputedStyle(document.body).backgroundColor);
            return { ratio: (Math.max(fg, bg) + 0.05) / (Math.min(fg, bg) + 0.05), display: getComputedStyle(a).display }; })()`);
        assert.ok(contrast.ratio >= 4.5, 'the link against the page: ' + JSON.stringify(contrast));
        assert.equal(contrast.display, 'block');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 756, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await waitFor(`!!document.querySelector('.cloud-account-btn')`, 'the hub dashboard', { timeout: 15000 });
        await ev(`window.switchNavTab('servers'); true`);
        await waitFor(`!!document.querySelector('.cs-enroll ol li a')`, 'the steps', { timeout: 8000 });
        await ev(`document.querySelector('#cs-enroll-toggle[aria-expanded=\"false\"]')?.click(); true`);
        const phone = await ev(`(() => { const r = document.querySelector('.cs-enroll ol li a').getBoundingClientRect(); return { h: Math.round(r.height), w: Math.round(r.width), inside: r.right <= innerWidth, page: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
        assert.ok(phone.h >= 44 && phone.w >= 44 && phone.inside && phone.page <= 0, JSON.stringify(phone));
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
