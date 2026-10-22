// A light phone through the Hub (#6114): empty settings ({"settings": {}}, the hub stores no theme), the page follows the system (light), Settings opens showing "System", and a Save that does not touch the theme leaves the page light and
// writes no theme (no theme or custom_theme in the PUT, nothing in the browser's saved choice). Picking Dark does write it, and the page goes dark. Cloud mode, the hub's CSP, 390 px.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { safePaused } = require('./helpers/paused');
const BASE = require('./test_base').BASE;
const HOST = new URL(BASE).host;
const CSP = ["default-src 'self'", "script-src 'self'", "style-src 'self'", "style-src-attr 'unsafe-inline'", "font-src 'self'", "img-src 'self' data: blob:", `connect-src 'self' ws://${HOST}`, "object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'"].join('; ');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Fetch, Network } = client; const puts = [];
    try {
        await Page.enable(); await Network.setBypassServiceWorker({ bypass: true });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: 'light' }] });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/settings`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/themes*`, requestStage: 'Request' }] });
        const json = (e, status, obj) => Fetch.fulfillRequest({ requestId: e.requestId, responseCode: status, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify(obj)).toString('base64') });
        safePaused(client, async e => {
            const u = e.request.url;
            if (u.endsWith('/auth/session')) return json(e, 200, { authenticated: true, email: 'tester@example.com', csrf: 'test-csrf' });
            if (u.endsWith('/api/settings')) { if (e.request.method === 'PUT') { puts.push(JSON.parse(e.request.postData || '{}')); return json(e, 200, { ok: true }); } return json(e, 200, { settings: {} }); }
            if (/\/api\/themes/.test(u)) return json(e, 404, { error: 'theme not found' });
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
            html = html.replace('"mode":"local"', '"mode":"cloud"');
            const headers = (e.responseHeaders || []).filter(h => !/^content-(security-policy|length)$/i.test(h.name)).concat([{ name: 'Content-Security-Policy', value: CSP }]);
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`localStorage.removeItem('coral-theme'); true`);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await new Promise(r => setTimeout(r, 2000)); // sleep-ok: the settings request finishes after the first paint
        assert.equal(await ev(`document.documentElement.getAttribute('data-theme')`), 'light', 'a light phone with nothing stored is light');
        await ev(`window.showSettingsModal(); true`);
        await waitFor(`!!document.getElementById('settings-theme') && document.getElementById('settings-theme').options.length > 0`, 'the theme control', 15000);
        await new Promise(r => setTimeout(r, 800)); // sleep-ok: the custom themes list finishes after the dialog opens
        assert.equal(await ev(`document.getElementById('settings-theme').value`), 'system', 'nothing stored shows System, not Dark');
        // a Save that leaves the theme alone: change another field so there is something to save, as a person would
        await ev(`(() => { const f = document.getElementById('settings-terminal-font-size'); if (f) { f.value = f.value === '14' ? '13' : '14'; f.dispatchEvent(new Event('change', { bubbles: true })); } window.applySettings(); return true; })()`);
        await waitFor(`true`, 'settle', 100);
        await new Promise(r => setTimeout(r, 1500)); // sleep-ok: the save and its effects finish
        assert.equal(await ev(`document.documentElement.getAttribute('data-theme')`), 'light', 'the page stays light after Save');
        assert.equal(await ev(`localStorage.getItem('coral-theme')`), null, "no theme is written into the browser's saved choice");
        assert.ok(puts.length >= 1, 'a save was sent');
        for (const body of puts) assert.ok(!('theme' in body) && !('custom_theme' in body), 'no theme is stored by the save: ' + JSON.stringify(body));
        // picking Dark does write it
        puts.length = 0;
        await ev(`(() => { const s = document.getElementById('settings-theme'); s.value = 'dark'; s.dispatchEvent(new Event('change', { bubbles: true })); window.applySettings(); return true; })()`);
        await new Promise(r => setTimeout(r, 1500)); // sleep-ok: the save and its effects finish
        assert.ok(puts.some(b => b.theme === 'dark'), 'picking Dark is saved: ' + JSON.stringify(puts));
        assert.equal(await ev(`document.documentElement.getAttribute('data-theme')`), 'dark', 'and the page goes dark');
        console.log('PASS Settings on a light phone through the hub: System shown, a Save keeps it light and stores no theme, picking Dark does');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
