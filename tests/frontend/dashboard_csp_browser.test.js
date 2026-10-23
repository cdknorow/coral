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
//  4. a notification's web link shows its host next to the label, and "not encrypted" for http;
//  5. every other kind of load is held to this server too (#5009): fetch, XHR, WebSocket, EventSource, a beacon, a stylesheet
//     link, an @import, a font, a worker, an object and a data: frame all fail to reach the counting server and
//     are reported under their directive; inline style (an attribute, a <style> element, a CSSOM write) still applies, since the
//     markup and the HTML previews' srcdoc frames need it. The one cross-origin load the dashboard makes on purpose, the Linked preview's http(s) frame, still
//     arrives (frame-src).
// A positive control must arrive, or zero hits would prove nothing: the counting server is asked from a tab with no policy
// (the page itself can no longer reach it: connect-src is this server).
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
        check("the dashboard's CSP has no 'unsafe-eval', and 'unsafe-inline' only in style-src", !/unsafe-eval/.test(csp)
            && csp.split(';').map(d => d.trim()).filter(d => /unsafe-inline/.test(d)).every(d => d.startsWith('style-src ')), csp);
        const pageHost = new URL(BASE).host;
        for (const d of ["default-src 'self';", "script-src 'self';", "style-src 'self' 'unsafe-inline';", "font-src 'self' data:",
            `connect-src 'self' ws://${pageHost} wss://${pageHost};`, "frame-src 'self' http: https:", "worker-src 'self'",
            "form-action 'self'", "media-src 'self' blob: data:",
            "img-src 'self' data: blob:", "base-uri 'self'", "object-src 'none'", "frame-ancestors 'self'"]) {
            check(`the dashboard's CSP has ${d}`, csp.includes(d), csp);
        }

        // Positive control: the browser can reach the counting server. Not from the dashboard page: its connect-src is this server (#5009),
        // so the control is asked from a fresh tab that has no policy.
        const port = Number(process.env.CDP_PORT || 9222);
        const tab = await CDP.New({ port, url: 'about:blank' });
        const other = await CDP({ port, target: tab });
        try {
            await other.Runtime.enable();
            await other.Runtime.evaluate({ expression: `fetch(${JSON.stringify(SINK + '/control')}, { mode: 'no-cors' }).then(() => 1, () => 0)`, awaitPromise: true });
        } finally { await other.close(); await CDP.Close({ port, id: tab.id }); }
        check('positive control: the counting server sees a request from a page with no policy', hits.some(h => h.endsWith(' /control')), JSON.stringify(hits));
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
            let ran = 0; const keep = window.__coral.toggleTopBarSettings; window.__coral.toggleTopBarSettings = () => { ran++; };
            const b = document.createElement('button'); b.setAttribute('data-on-click', 'm-toggle-top-bar-settings'); host.appendChild(b); b.click();
            window.__coral.toggleTopBarSettings = keep;
            setTimeout(() => { document.removeEventListener('securitypolicyviolation', onViolation); host.remove();
                resolve({ inlineRan: window.__qaInline, actionRan: ran, seen }); }, 500); // sleep-ok: the violation events are queued
        })`);
        check('an inline handler and an inline script put into the page do not run', inline.inlineRan === 0, JSON.stringify(inline));
        check('the CSP reports script-src for each', inline.seen.filter(v => /^script-src/.test(v)).length >= 2, JSON.stringify(inline.seen));
        check('a control naming a data-on-* action still works', inline.actionRan === 1, JSON.stringify(inline));

        // ── 5. every other kind of load is held to this server (#5009) ─────────
        hits.length = 0;
        const loads = await ev(`new Promise(resolve => {
            const seen = []; const onV = e => seen.push(e.violatedDirective + ' ' + e.blockedURI);
            document.addEventListener('securitypolicyviolation', onV);
            const host = document.createElement('div'); host.id = 'csp-loads-test'; document.body.appendChild(host);
            const note = [];
            const tryIt = (name, fn) => { try { const r = fn(); if (r && r.catch) r.catch(() => {}); } catch (e) { note.push(name + ' threw ' + e.name); } };
            tryIt('fetch', () => fetch('${SINK}/fetch', { mode: 'no-cors' }));
            tryIt('xhr', () => { const x = new XMLHttpRequest(); x.open('GET', '${SINK}/xhr'); x.send(); });
            tryIt('websocket', () => { const w = new WebSocket('ws://' + new URL('${SINK}').host + '/ws'); w.onerror = () => {}; });
            tryIt('eventsource', () => { const es = new EventSource('${SINK}/es'); es.onerror = () => es.close(); });
            tryIt('beacon', () => navigator.sendBeacon('${SINK}/beacon', 'x'));
            tryIt('stylesheet', () => { const l = document.createElement('link'); l.rel = 'stylesheet'; l.href = '${SINK}/style.css'; host.appendChild(l); });
            tryIt('import', () => { const st = document.createElement('style'); st.textContent = '@import url(${SINK}/imported.css);'; host.appendChild(st); });
            tryIt('font', () => new FontFace('qa-csp', 'url(${SINK}/font.woff2)').load());
            tryIt('worker', () => { const w = new Worker(URL.createObjectURL(new Blob(['postMessage(1)']))); w.onerror = e => e.preventDefault && e.preventDefault(); }); // a worker script from another origin throws before the CSP is asked; a blob: one is what worker-src 'self' refuses
            tryIt('object', () => { const o = document.createElement('object'); o.data = '${SINK}/object.bin'; host.appendChild(o); });
            tryIt('embed', () => { const o = document.createElement('embed'); o.src = '${SINK}/embed.bin'; host.appendChild(o); });
            tryIt('data frame', () => { const f = document.createElement('iframe'); f.src = 'data:text/html,<p>x</p>'; host.appendChild(f); });
            tryIt('linked preview frame', () => { const f = document.createElement('iframe'); f.setAttribute('sandbox', ''); f.src = '${SINK}/frame'; host.appendChild(f); });
            // inline style still applies: an attribute, a <style> element and a CSSOM write
            const el = document.createElement('div'); el.id = 'qa-style-probe'; el.setAttribute('style', 'margin-top: 7px'); host.appendChild(el);
            const st = document.createElement('style'); st.textContent = '#qa-style-probe { padding-top: 9px; }'; host.appendChild(st);
            const cs = getComputedStyle(el);
            const attrApplies = cs.marginTop === '7px', elementApplies = cs.paddingTop === '9px';
            el.style.paddingBottom = '5px'; const cssomApplies = getComputedStyle(el).paddingBottom === '5px';
            setTimeout(() => { document.removeEventListener('securitypolicyviolation', onV); host.remove();
                resolve({ seen, note, attrApplies, elementApplies, cssomApplies }); }, 2000); // sleep-ok: proves a negative, the loads get this long to reach out
        })`);
        const frameArrived = hits.filter(h => h.endsWith(' /frame'));
        const others = hits.filter(h => !h.endsWith(' /frame'));
        check('fetch, XHR, WebSocket, EventSource, a beacon, a stylesheet, an @import, a font, a worker, an object and an embed reach nothing', others.length === 0, JSON.stringify(others));
        check('the Linked preview\'s frame to another origin still loads (frame-src names http and https)', frameArrived.length >= 1, JSON.stringify(hits));
        for (const [d, why] of [['connect-src', 'fetch, XHR, WebSocket, EventSource, beacon'], ['style-src-elem', 'the stylesheet link and the @import'],
            ['font-src', 'the font'], ['worker-src', 'the worker'], ['object-src', 'the object and embed'], ['frame-src', 'the data: frame']]) {
            check(`the CSP reports ${d} for ${why}`, loads.seen.some(v => v.startsWith(d + ' ')), JSON.stringify(loads.seen));
        }
        check('the five connect-src loads are each reported', loads.seen.filter(v => v.startsWith('connect-src ')).length >= 5, JSON.stringify(loads.seen));
        check('a style attribute, an inline <style> element and a CSSOM write still apply', loads.attrApplies && loads.elementApplies && loads.cssomApplies, JSON.stringify(loads));

        // ── 4. a notification's web link shows its host ─────────────────────────
        const notes = await ev(`${U}.then(u => {
            const read = (url, label) => {
                u.showAlertNotification('t', 'm', 'info', { url, label });
                const o = [...document.querySelectorAll('.alert-notification-overlay')].pop(); // the newest by class: :last-of-type is last among the page's divs, and the screen-reader regions (#6226) are divs too
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
