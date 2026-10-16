// The dashboard's own CSP and media rules (#3623, rendering audit F3 stage 1, with Security's SHOULD and
// NIT on #3622; #3645 stage 2):
//  1. the dashboard page sends script-src 'self' (no 'unsafe-inline' since #3645), form-action 'self' and
//     media-src 'self' blob: data:, beside the earlier img-src, base-uri, object-src and
//     frame-ancestors;
//  2. media and embedded documents in agent content (video, audio, source, track, picture, svg,
//     math) are dropped by every renderer, and rendering them into the page makes no request;
//  3. the CSP is the second layer: remote media put into the page directly (bypassing the
//     sanitizer), a form posting elsewhere and a script from elsewhere all fail to reach the
//     counting server; an inline handler and an inline script put into the page don't run (the
//     CSP reports them), while a control naming a data-on-* action still works;
//  4. a notification's web link shows its host next to the label, and "not encrypted" for http.
// A positive control (an XHR from the page to the counting server) must arrive, or zero hits would
// prove nothing.
'use strict';

const http = require('http');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const hits = [];
    const sink = http.createServer((req, res) => {
        hits.push(req.method + ' ' + req.url);
        res.writeHead(200, { 'Content-Type': 'text/plain', 'Access-Control-Allow-Origin': '*' });
        res.end('x');
    });
    await new Promise(r => sink.listen(0, '127.0.0.1', r));
    const SINK = 'http://127.0.0.1:' + sink.address().port;

    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const U = `import((window.__coralStatic || '/static/') + 'utils.js')`;
    try {
        // ── 1. the page's policy ────────────────────────────────────────────────
        await Network.enable();
        let csp = '';
        Network.responseReceived(({ response, type }) => {
            if (type === 'Document' && !csp) csp = (response.headers['Content-Security-Policy'] || response.headers['content-security-policy'] || '');
        });
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();
        await waitFor(`typeof DOMPurify !== 'undefined' && typeof marked !== 'undefined'`, 'the sanitizer and marked');
        check("the dashboard's CSP has no 'unsafe-inline' or 'unsafe-eval'", !/unsafe-inline|unsafe-eval/.test(csp), csp);
        for (const d of ["script-src 'self';", "form-action 'self'", "media-src 'self' blob: data:",
            "img-src 'self' data: blob:", "base-uri 'self'", "object-src 'none'", "frame-ancestors 'self'"]) {
            check(`the dashboard's CSP has ${d}`, csp.includes(d), csp);
        }

        // Positive control: the page itself can reach the counting server.
        await ev(`new Promise(res => { const x = new XMLHttpRequest(); x.onloadend = res; x.open('GET', ${JSON.stringify(SINK + '/control')}); x.send(); })`);
        const controlSeen = hits.some(h => h.endsWith(' /control'));
        check('positive control: the counting server sees a request from the page', controlSeen, JSON.stringify(hits));
        hits.length = 0;

        // ── 2. media in agent content, through every renderer ──────────────────
        const payload = [
            'media test',
            '',
            `<video src="${SINK}/video.mp4" poster="${SINK}/poster.png" autoplay muted controls><source src="${SINK}/source.mp4"><track src="${SINK}/track.vtt"></video>`,
            `<audio src="${SINK}/audio.mp3" autoplay controls></audio>`,
            `<picture><source srcset="${SINK}/picture.webp"><img src="data:image/gif;base64,R0lGODlhAQABAAAAACw=" alt="p"></picture>`,
            `<svg width="10" height="10"><image href="${SINK}/svg-image.png" width="10" height="10"/></svg>`,
            `<math><mi href="${SINK}/math">x</mi></math>`,
            '',
            'kept text',
        ].join('\n');
        const rendered = await ev(`${U}.then(u => {
            const md = ${JSON.stringify(payload)};
            const html = marked.parse(md);
            const outs = {
                'chat (renderMarkdown)': u.renderMarkdown(md, undefined, 'chat'),
                'board (renderMarkdown)': u.renderMarkdown(md, undefined, 'board'),
                'notes (sanitizeMarkdown)': u.sanitizeMarkdown(md, undefined, 'board'),
                'summary (sanitizeHtml, no config)': u.sanitizeHtml(html, undefined, { links: 'board' }),
                'file (renderMarkdownStrict)': u.renderMarkdownStrict(md),
                'file pane (sanitizeHtml raw HTML)': u.sanitizeHtml(html, u.DOMPURIFY_CONFIG, { links: 'file' }),
            };
            const out = {};
            const host = document.createElement('div');
            host.id = 'csp-media-test';
            document.body.appendChild(host);
            for (const [k, h] of Object.entries(outs)) {
                const box = document.createElement('div');
                box.innerHTML = h;
                host.appendChild(box); // in the live document, so anything left would load
                out[k] = {
                    tags: [...new Set([...box.querySelectorAll('video, audio, source, track, picture, svg, math')].map(e => e.nodeName.toLowerCase()))],
                    text: box.textContent.includes('kept text'),
                };
            }
            return out;
        })`);
        for (const [k, r] of Object.entries(rendered)) {
            check(`${k}: no media or embedded document survives`, r.tags.length === 0, JSON.stringify(r.tags));
            check(`${k}: the text survives`, r.text);
        }
        await ev(`new Promise(r => setTimeout(r, 1500))`); // sleep-ok: proves a negative, the rendered content gets this long to reach out
        check('rendering agent media into the page makes no request', hits.length === 0, JSON.stringify(hits));
        hits.length = 0;

        // ── 3. the CSP as the second layer ──────────────────────────────────────
        const blocked = await ev(`new Promise(resolve => {
            const seen = [];
            document.addEventListener('securitypolicyviolation', e => seen.push(e.violatedDirective + ' ' + e.blockedURI));
            const host = document.getElementById('csp-media-test');
            host.insertAdjacentHTML('beforeend',
                '<video src="${SINK}/direct-video.mp4" autoplay muted></video>' +
                '<audio src="${SINK}/direct-audio.mp3" autoplay></audio>');
            const s = document.createElement('script');
            s.src = '${SINK}/direct-script.js';
            host.appendChild(s);
            const f = document.createElement('form');
            f.action = '${SINK}/direct-form'; f.method = 'post'; f.target = 'csp-form-target';
            const frame = document.createElement('iframe');
            frame.name = 'csp-form-target'; frame.style.display = 'none';
            host.appendChild(frame); host.appendChild(f);
            try { f.submit(); } catch (e) { seen.push('submit threw ' + e.name); }
            setTimeout(() => resolve(seen), 1500); // sleep-ok: proves a negative, the direct loads get this long to reach out
        })`);
        check('remote media, a remote script and a form post elsewhere make no request', hits.length === 0, JSON.stringify(hits));
        check('the CSP reports media-src for the direct media', blocked.some(v => /^media-src /.test(v)), JSON.stringify(blocked));
        check('the CSP reports script-src for the remote script', blocked.some(v => /^script-src/.test(v)), JSON.stringify(blocked));
        check('the CSP reports form-action for the form post', blocked.some(v => /^form-action /.test(v)), JSON.stringify(blocked));
        await ev(`document.getElementById('csp-media-test').remove()`);

        // Inline code doesn't run (#3645); a delegated action does.
        const inline = await ev(`new Promise(resolve => {
            const seen = []; window.__qaInline = 0;
            const onViolation = e => seen.push(e.violatedDirective);
            document.addEventListener('securitypolicyviolation', onViolation);
            const host = document.createElement('div'); document.body.appendChild(host);
            host.innerHTML = '<button id="qa-inline-btn" onclick="window.__qaInline++">x</button>';
            document.getElementById('qa-inline-btn').click();
            const s = document.createElement('script'); s.textContent = 'window.__qaInline++'; host.appendChild(s);
            let ran = 0; const keep = window.toggleTopBarSettings; window.toggleTopBarSettings = () => { ran++; };
            const b = document.createElement('button'); b.setAttribute('data-on-click', 'm-toggle-top-bar-settings'); host.appendChild(b); b.click();
            window.toggleTopBarSettings = keep;
            setTimeout(() => { document.removeEventListener('securitypolicyviolation', onViolation); host.remove();
                resolve({ inlineRan: window.__qaInline, actionRan: ran, seen }); }, 500); // sleep-ok: the violation events are queued
        })`);
        check('an inline handler and an inline script put into the page do not run', inline.inlineRan === 0, JSON.stringify(inline));
        check('the CSP reports script-src for each', inline.seen.filter(v => /^script-src/.test(v)).length >= 2, JSON.stringify(inline.seen));
        check('a control naming a data-on-* action still works', inline.actionRan === 1, JSON.stringify(inline));

        // ── 4. a notification's web link shows its host ─────────────────────────
        const notes = await ev(`${U}.then(u => {
            const read = (url, label) => {
                u.showAlertNotification('t', 'm', 'info', { url, label });
                const o = document.querySelector('.alert-notification-overlay:last-of-type');
                const host = o.querySelector('.alert-notification-host');
                const a = o.querySelector('a.alert-notification-link');
                const r = { host: host ? host.textContent : null, link: !!a,
                    described: !!(host && a && host.id && a.getAttribute('aria-describedby') === host.id) };
                o.remove();
                return r;
            };
            return {
                https: read('https://docs.example.test/page?q=1', 'Read the guide'),
                http: read('http://localhost:3000/', 'The dev server'),
                docs: read('docs://workflow-quickstart', 'Quickstart'),
                bad: read('javascript:alert(1)', 'Click me'),
                idn: read('https://ex\u0430mple.test/login', 'example.test'),
                port: read('https://example.test:8443/x', 'Port'),
            };
        })`);
        check('an https link shows its host', notes.https.link && notes.https.host === 'docs.example.test', JSON.stringify(notes.https));
        check('the link is described by its host for a screen reader', notes.https.described && notes.http.described, JSON.stringify([notes.https, notes.http]));
        check('a look-alike (IDN) host shows as punycode, not as the label it imitates', notes.idn.link && /^xn--/.test(notes.idn.host), JSON.stringify(notes.idn));
        check('a host with a port shows the port', notes.port.link && notes.port.host === 'example.test:8443', JSON.stringify(notes.port));
        check('an http link shows its host and "not encrypted"', notes.http.link && notes.http.host === 'localhost:3000 · not encrypted', JSON.stringify(notes.http));
        check('a docs link shows no host', notes.docs.link && notes.docs.host === null, JSON.stringify(notes.docs));
        check('a refused link is text with no host', !notes.bad.link && notes.bad.host === null, JSON.stringify(notes.bad));
    } catch (e) {
        check('suite ran without an exception', false, e.stack || String(e));
    } finally {
        sink.closeAllConnections();
        await new Promise(r => sink.close(r));
        await client.close();
    }
    const passed = results.filter(Boolean).length;
    console.log(`\n${passed}/${results.length} passed`);
    process.exit(passed === results.length && results.length > 0 ? 0 : 1);
})();
