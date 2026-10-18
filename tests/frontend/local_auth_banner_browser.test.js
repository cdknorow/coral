// The red banner while local sign-in is off (#4291, Security's #4288): GET /api/system/auth-status carries strict_local_auth; false shows the
// banner on the dashboard at startup, true or a missing field (an older server) shows nothing, it has no close button, it is a role=alert
// that is only shown or hidden (a poll that finds nothing changed does not touch it, so it is spoken once), it goes when sign-in comes back,
// it fits a 390 px phone and 200 percent text, and the Coral Hub's view never shows it. The auth-status answer is faked in the page.
// Run against an isolated dev server. BANNER_SHOT_DIR=<dir> saves screenshots of the banner.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { connect } = require('./agent_view_harness');

const TEXT = 'Local sign-in is OFF: any program on this computer can use Coral.';
const FAKE = `(() => {
    window.__auth = { strict_local_auth: false };
    const inner = window.fetch;
    window.__authMode = 'ok'; // ok | 500 | throw | badjson: how the status poll fails
    window.fetch = (url, opts) => {
        if (new URL(String(url), location.origin).pathname !== '/api/system/auth-status') return inner(url, opts);
        if (window.__authMode === 'throw') return Promise.reject(new TypeError('network down'));
        if (window.__authMode === '500') return Promise.resolve(new Response('{"error":"boom"}', { status: 500, headers: { 'content-type': 'application/json' } }));
        if (window.__authMode === 'badjson') return Promise.resolve(new Response('<html>not json', { status: 200, headers: { 'content-type': 'text/html' } }));
        return Promise.resolve(new Response(JSON.stringify({ authenticated: true, method: 'session', password_set: true, local: true, ...window.__auth }), { status: 200, headers: { 'content-type': 'application/json' } }));
    };
})();`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Fetch, Network } = client;
    const shot = async n => { if (process.env.BANNER_SHOT_DIR) require('node:fs').writeFileSync(`${process.env.BANNER_SHOT_DIR}/${n}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const ok = name => console.log(`   ✓ ${name}`);
    const shown = () => ev(`(() => { const b = document.getElementById('local-auth-banner'); return !!b && !b.hidden && b.checkVisibility({ checkVisibilityCSS: true }); })()`);
    const load = async () => { await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install(); };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: FAKE });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await load();
        await ev(`window.__coralReady.then(() => true)`);

        // off: shown at startup
        await waitFor(`!document.getElementById('local-auth-banner').hidden`, 'the banner at startup', 15000);
        const b = await ev(`(() => { const e = document.getElementById('local-auth-banner'); const cs = getComputedStyle(e);
            return { text: e.querySelector('.local-auth-banner-text').textContent.replace(/\\s+/g, ' ').trim(), role: e.getAttribute('role'), buttons: e.querySelectorAll('button, [role=button], a').length, border: cs.borderTopColor, errorColor: getComputedStyle(document.documentElement).getPropertyValue('--error').trim() }; })()`);
        assert.equal(b.text, TEXT); assert.equal(b.role, 'alert'); assert.equal(b.buttons, 0, 'nothing to close or follow');
        assert.ok(b.border && b.border !== 'rgba(0, 0, 0, 0)', 'a red border: ' + b.border);
        assert.equal(await ev(`!document.querySelector('.main-content').contains(document.getElementById('local-auth-banner')) && document.getElementById('local-auth-banner').previousElementSibling.tagName === 'HEADER'`), true, 'a strip under the header, not a child of .main-content');
        assert.equal(await shown(), true);
        await shot('desktop');
        ok('strict_local_auth false: the banner shows at startup with the exact words, a role=alert, no button');

        // a poll that finds the same state does not touch it (the alert is spoken once)
        await ev(`window.__mut = 0; new MutationObserver(r => { window.__mut += r.length; }).observe(document.getElementById('local-auth-banner'), { attributes: true, childList: true, characterData: true, subtree: true }); true`);
        await ev(`import((window.__coralStatic || '/static/') + 'local_auth_banner.js').then(async m => { for (let i = 0; i < 3; i++) await m.checkLocalAuthBanner(); return true; })`);
        assert.equal(await ev(`window.__mut`), 0, 'three polls with nothing changed rewrote nothing');
        ok('three polls that find nothing changed touch nothing, so a screen reader hears it once');

        // phone and 200 percent text
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 800, deviceScaleFactor: 1, mobile: true });
        await ev(`document.documentElement.style.fontSize = '32px'; true`);
        const fit = await ev(`(() => { const e = document.getElementById('local-auth-banner'); const r = e.getBoundingClientRect(); return { right: Math.round(r.right), vw: innerWidth, sw: e.scrollWidth, cw: e.clientWidth, h: Math.round(r.height) }; })()`);
        assert.ok(fit.right <= fit.vw && fit.sw <= fit.cw + 1, JSON.stringify(fit));
        await shot('phone-200');
        await ev(`document.documentElement.style.fontSize = ''; true`);
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        ok('at 390 px and 200% text the banner stays inside the screen');

        // sign-in turned back on, then a server that does not know the field
        const recheck = () => ev(`import((window.__coralStatic || '/static/') + 'local_auth_banner.js').then(m => m.checkLocalAuthBanner()).then(() => true)`);
        await ev(`window.__auth = { strict_local_auth: true }; true`); await recheck();
        assert.equal(await shown(), false, 'strict on: hidden');
        await ev(`window.__auth = { strict_local_auth: false }; true`); await recheck();
        assert.equal(await shown(), true, 'off again: shown');
        await ev(`window.__auth = {}; true`); await recheck();
        assert.equal(await shown(), false, 'field missing (an older server): hidden');
        ok('strict on hides it, off shows it again, a missing field hides it');

        // A failed poll is not an answer: the banner stays as it is, shown or hidden (Security's #4291 follow-up).
        await ev(`window.__auth = { strict_local_auth: false }; true`); await recheck();
        assert.equal(await shown(), true);
        for (const mode of ['500', 'throw', 'badjson']) {
            await ev(`window.__authMode = ${JSON.stringify(mode)}; true`); await recheck();
            assert.equal(await shown(), true, `a failed poll (${mode}) leaves a shown banner shown`);
        }
        await ev(`window.__authMode = 'ok'; window.__auth = { strict_local_auth: true }; true`); await recheck();
        assert.equal(await shown(), false);
        for (const mode of ['500', 'throw', 'badjson']) {
            await ev(`window.__authMode = ${JSON.stringify(mode)}; true`); await recheck();
            assert.equal(await shown(), false, `a failed poll (${mode}) does not show a hidden banner`);
        }
        await ev(`window.__authMode = 'ok'; window.__auth = { strict_local_auth: false }; true`); await recheck();
        assert.equal(await shown(), true, 'and a good answer still changes it');
        ok('a failed poll (500, network error, bad JSON) leaves the banner as it is, shown or hidden');

        // The agent pop-out (/agent/<uuid>) runs the same startup: the banner shows there too and does not break its layout.
        const UUID = require('node:crypto').randomUUID();
        await Page.addScriptToEvaluateOnNewDocument({ source: `window.__qaSeed = [{ name: 'po-bot', prefix: 'P', n: 4, sleeping: false, session_id: ${JSON.stringify(UUID)} }];` });
        await ev(`window.__auth = { strict_local_auth: false }; true`);
        await Page.navigate({ url: `${process.env.CORAL_TEST_URL}/agent/${UUID}` }); await Page.loadEventFired(); await install();
        await waitFor(`!document.getElementById('local-auth-banner').hidden`, 'the banner on the pop-out', 15000);
        for (const [label, w, h, mobile, fs] of [['popout-desktop', 1280, 800, false, ''], ['popout-phone-200', 390, 800, true, '32px']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await ev(`document.documentElement.style.fontSize = ${JSON.stringify(fs)}; true`);
            const po = await ev(`(() => { const e = document.getElementById('local-auth-banner'); const r = e.getBoundingClientRect(); const m = document.querySelector('.main-content').getBoundingClientRect();
                return { right: Math.round(r.right), vw: innerWidth, h: Math.round(r.height), sw: e.scrollWidth, cw: e.clientWidth, docOverflow: document.documentElement.scrollWidth - innerWidth, mainH: Math.round(m.height), visible: e.checkVisibility({ checkVisibilityCSS: true }) }; })()`);
            assert.ok(po.visible && po.right <= po.vw && po.sw <= po.cw + 1 && po.docOverflow <= 0 && po.mainH > 200, label + ' ' + JSON.stringify(po));
            await shot(label);
        }
        await ev(`document.documentElement.style.fontSize = ''; true`);
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        ok('the agent pop-out shows the banner and keeps its layout, at 1280 px and at 390 px with 200% text');

        // The Hub's view never shows it.
        await Network.enable(); await Network.setBypassServiceWorker({ bypass: true });
        await Fetch.enable({ patterns: [{ urlPattern: '*', resourceType: 'Document', requestStage: 'Response' }, { urlPattern: '*/auth/session', requestStage: 'Request' }] });
        safePaused(client, async e => {
            if (e.request.url.endsWith('/auth/session')) {
                return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                    body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
            }
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace(/"mode":\s*"local"/, '"mode":"cloud"');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        await load();
        assert.equal(await ev(`JSON.parse(document.getElementById('coral-boot').textContent).mode`), 'cloud');
        await ev(`new Promise(r => setTimeout(r, 4000))`); // sleep-ok: the banner is not drawn; give a wrongly started check time to show it
        assert.equal(await shown(), false, 'the Hub view never shows it');
        ok('the Coral Hub view never shows it');
    } catch (e) {
        console.error(e);
        process.exitCode = 1;
    } finally {
        await client.close();
    }
})();
