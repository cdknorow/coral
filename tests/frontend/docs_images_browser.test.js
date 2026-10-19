// A Docs page shows its own screenshots (#5038): ![alt](docs-img/<name>.png) in an agent_docs page becomes this server's own versioned URL for a file in static/docs-img and loads;
// every other image source in docs Markdown stays refused (a remote image, a path walk, an upper-case or other-type name, a query), so the sanitizer's rule for images (#3515) is
// narrowed by exactly one shape. The real Docs page (using-the-dashboard) shows its screenshots loaded, inside the page, with no CSP violation. Drives Chrome over CDP against the isolated
// dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        const load = async path => { const loaded = Page.loadEventFired(); await Page.navigate({ url: BASE + path }); await loaded; await ev(`window.__coralReady.then(() => true)`); };
        await load('/');

        console.log('1. Only the docs-img shape keeps its image');
        const r = await ev(`import((window.__coralStatic || '/static/') + 'utils.js').then(u => {
            const show = md => { const d = document.createElement('div'); d.innerHTML = u.renderDocsMarkdown(md); const i = d.querySelector('img'); return { src: i ? i.getAttribute('src') : null, links: d.querySelectorAll('.blocked-remote-image').length }; };
            return { ok: show('![shot](docs-img/display-panel.png)'), walk: show('![x](docs-img/../display-panel.png)'), upper: show('![x](docs-img/Display-Panel.png)'),
                svg: show('![x](docs-img/display-panel.svg)'), query: show('![x](docs-img/display-panel.png?x=1)'), nested: show('![x](docs-img/a/b.png)'),
                abs: show('![x](/static/docs-img/display-panel.png?x=1)'), remote: show('![x](https://example.test/display-panel.png)'), relative: show('![x](display-panel.png)') };
        })`);
        assert.match(r.ok.src, /^\/static\/(?:(?:b|bb)\/[A-Za-z0-9_-]+\/)?docs-img\/display-panel\.png$/, 'a docs image is this server\'s own URL: ' + JSON.stringify(r.ok));
        for (const k of ['walk', 'upper', 'svg', 'query', 'nested', 'abs', 'remote', 'relative']) assert.equal(r[k].src, null, `${k} keeps no image source: ${JSON.stringify(r[k])}`);
        console.log('   ✓');

        for (const [label, w, h, mobile] of [['2. Desktop', 1280, 900, false], ['3. Phone', 390, 844, true]]) {
            console.log(`${label}: the page shows its screenshots, loaded and inside the page`);
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: mobile ? 2 : 1, mobile });
            await load('/docs/using-the-dashboard');
            await ev(`window.__cspSeen = []; document.addEventListener('securitypolicyviolation', e => window.__cspSeen.push(e.violatedDirective + ' ' + e.blockedURI)); true`);
            await waitFor(`document.querySelectorAll('#docs-content img').length >= 5`, 'the page\'s screenshots', 15000);
            await waitFor(`[...document.querySelectorAll('#docs-content img')].every(i => i.complete && i.naturalWidth > 0)`, 'every screenshot loaded', 15000);
            const m = await ev(`({ n: document.querySelectorAll('#docs-content img').length, widest: Math.max(...[...document.querySelectorAll('#docs-content img')].map(i => Math.round(i.getBoundingClientRect().right))), vw: innerWidth,
                alts: [...document.querySelectorAll('#docs-content img')].every(i => (i.alt || '').length > 5), csp: window.__cspSeen, page: document.documentElement.scrollWidth - document.documentElement.clientWidth })`);
            assert.ok(m.n >= 5, JSON.stringify(m));
            assert.ok(m.widest <= m.vw, 'no screenshot runs off the screen: ' + JSON.stringify(m));
            assert.ok(m.alts, 'each screenshot has a description');
            assert.deepEqual(m.csp, [], 'no CSP violation: ' + JSON.stringify(m.csp));
            assert.ok(m.page <= 1, 'no page-wide horizontal scroll: ' + JSON.stringify(m));
            if (process.env.CORAL_SCREENSHOT_DIR) { // the page as a person sees it: the top, then the model section scrolled to
                require('node:fs').mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
                for (const [name, sel] of [['top', null], ['cost', 'img[src$="cost-by-thinking.png"]']]) {
                    if (sel) await ev(`document.querySelector('#docs-content ${sel}').scrollIntoView({ block: 'center' }); true`); else await ev(`document.getElementById('docs-content').scrollTo(0, 0); window.scrollTo(0, 0); true`);
                    await new Promise(r => setTimeout(r, 300)); // sleep-ok: a screenshot settles layout, there is no event for it
                    require('node:fs').writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/docs-${mobile ? 'phone' : 'desktop'}-${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
                }
            }
            console.log('   ✓ ' + m.n + ' screenshots');
        }
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
