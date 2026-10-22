// The dashboard as the Cloud Hub serves it (#6087): the cloud-mode boot flag, the hub's CSP, a phone (390 px), and the hub's answers for the theme: empty settings ({"settings": {}}, the hub stores no theme), a 404 for /api/themes/<name>,
// and a theme answer whose variables are empty. In every case, in the phone's light and dark system theme, the chat's bubbles keep readable colours: the page follows the system theme, the stylesheet's tokens are not emptied, and no inline
// style is applied that the CSP refuses. (A white bubble with light text, or the reverse, is what an emptied token gives.)
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { safePaused } = require('./helpers/paused');
const BASE = require('./test_base').BASE;
const HOST = new URL(BASE).host;
const CSP = ["default-src 'self'", "script-src 'self'", "style-src 'self'", "style-src-attr 'unsafe-inline'", "font-src 'self'", "img-src 'self' data: blob:", `connect-src 'self' ws://${HOST}`, "object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'"].join('; ');

const ANSWERS = {
    '404 for every theme': { status: 404, body: { error: 'theme not found' } },
    'a theme with empty variables': { status: 200, body: { name: 'Dark', theme: { base: 'GhostV3', description: 'x', variables: { '--bg-tertiary': '', '--bg-primary': '  ', '--text-primary': '', '--chat-human-bg': '', '--chat-assistant-bg': '', '--accent': '' } } } },
    'no answer at all (the request fails)': null,
};

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Fetch, Network } = client;
    try {
        await Page.enable(); await Network.setBypassServiceWorker({ bypass: true });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        let answer = ANSWERS['404 for every theme']; let storedTheme = '';
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/settings`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/themes/*`, requestStage: 'Request' }] });
        const json = (e, status, obj) => Fetch.fulfillRequest({ requestId: e.requestId, responseCode: status, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify(obj)).toString('base64') });
        safePaused(client, async e => {
            const u = e.request.url;
            if (u.endsWith('/auth/session')) return json(e, 200, { authenticated: true, email: 'tester@example.com', csrf: 'test-csrf' });
            if (u.endsWith('/api/settings') && e.request.method === 'GET') return json(e, 200, { settings: storedTheme ? { custom_theme: storedTheme } : {} }); // the hub stores no theme for this user, or a custom one
            if (/\/api\/themes\//.test(u)) return answer === null ? Fetch.failRequest({ requestId: e.requestId, errorReason: 'Failed' }) : json(e, answer.status, answer.body);
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
            html = html.replace('"mode":"local"', '"mode":"cloud"');
            const headers = (e.responseHeaders || []).filter(h => !/^content-(security-policy|length)$/i.test(h.name)).concat([{ name: 'Content-Security-Policy', value: CSP }]);
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        const lum = c => { const m = c.match(/[\d.]+/g).map(Number); const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(m[0]) + 0.7152 * f(m[1]) + 0.0722 * f(m[2]); };
        const ratio = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
        const over = (fg, bg) => { const f = fg.match(/[\d.]+/g).map(Number); const b = bg.match(/[\d.]+/g).map(Number); const a = f.length > 3 ? f[3] : 1; return `rgb(${f.slice(0, 3).map((v, i) => Math.round(v * a + b[i] * (1 - a))).join(',')})`; };
        for (const [name, ans] of Object.entries(ANSWERS)) {
            answer = ans;
            for (const scheme of ['dark', 'light']) {
                await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: scheme }] });
                await Page.navigate({ url: 'about:blank' }); await Page.loadEventFired();
                await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
                await ev(`localStorage.removeItem('coral-theme'); true`);
                await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
                assert.equal(await ev(`JSON.parse(document.getElementById('coral-boot').textContent).mode`), 'cloud');
                await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('bub', 'B', 4, { sleeping: false }); true`);
                await ev(`window.selectLiveSession('bub', 'claude', 'bub-id', 'local'); true`);
                await waitFor(`document.querySelectorAll('#live-history-messages .chat-bubble').length >= 2`, 'bubbles', 20000);
                await new Promise(r => setTimeout(r, 2500)); // sleep-ok: the settings and theme requests finish after the first paint
                const got = await ev(`(() => { const cs = x => getComputedStyle(x); const bodyBg = cs(document.body).backgroundColor; const h = document.querySelector('#live-history-messages .chat-bubble.human'); const a = document.querySelector('#live-history-messages .chat-bubble.assistant');
                    return { theme: document.documentElement.getAttribute('data-theme'), bodyBg, human: { bg: cs(h).backgroundColor, color: cs(h).color }, asst: { bg: cs(a).backgroundColor, color: cs(a).color }, tokens: ['--bg-primary', '--bg-tertiary', '--text-primary'].map(t => cs(document.documentElement).getPropertyValue(t).trim()) }; })()`);
                const tag = `${name}, ${scheme} phone`;
                assert.equal(got.theme, scheme, `${tag}: the page follows the system theme (${JSON.stringify(got)})`);
                assert.ok(got.tokens.every(t => t), `${tag}: no token is emptied ${JSON.stringify(got.tokens)}`);
                for (const [who, b] of [['human', got.human], ['assistant', got.asst]]) {
                    const eff = over(b.bg, got.bodyBg);
                    assert.ok(ratio(b.color, eff) >= 4.5, `${tag}: the ${who} bubble's text is readable on it (text ${b.color} on ${eff}, page ${got.bodyBg})`);
                }
                assert.equal(lum(got.bodyBg) < 0.5, scheme === 'dark', `${tag}: the page background matches the theme (${got.bodyBg})`);
            }
            console.log('PASS ' + name);
        }
        // A stored custom theme the hub answers well: the page shows it, and the browser's saved choice (a person's own, light) is not rewritten by the page load (#6087).
        storedTheme = 'Obsidian'; answer = { status: 200, body: { name: 'Obsidian', theme: { base: 'dark', variables: { '--accent': '#58a6ff' } } } };
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: 'light' }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`localStorage.setItem('coral-theme', 'light'); true`);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await new Promise(r => setTimeout(r, 2500)); // sleep-ok: the settings and theme requests finish after the first paint
        assert.equal(await ev(`document.documentElement.getAttribute('data-theme')`), 'dark', 'the stored custom theme (base dark) is what the page shows');
        assert.equal(await ev(`localStorage.getItem('coral-theme')`), 'light', "the person's saved choice is not overwritten by what the hub says");
        console.log('PASS a stored custom theme does not rewrite the saved choice');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
