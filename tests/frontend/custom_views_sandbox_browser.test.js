// Custom views run in an opaque-origin sandbox with a read-only bridge; API-created views wait for a click.
// Real Chrome against an isolated Coral (see run_cdp_suites.mjs): the view's script runs for real.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const base = require('./test_base').BASE;
(async () => {
    const c = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const ev = async expression => {
        const r = await c.Runtime.evaluate({ expression: expression.includes('await ') && !expression.startsWith('(async') ? `(async()=>{${expression}})()` : expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
        return r.result.value;
    };
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    try {
        await c.Page.enable();
        await c.Page.navigate({ url: base }); await c.Page.loadEventFired();
        // Against the REAL server: the dashboard can write a view (created_via 'ui'); a plain caller (no fetch metadata, as
        // curl or an agent would be) is refused and creates nothing.
        const plain = await fetch(base + '/api/views', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'agent view', html: '<script>1</script>' }) });
        assert.equal(plain.status, 403, 'a plain POST /api/views is refused');
        const real = await ev(`return fetch('/api/views', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'dashboard view', html: '<h1>x</h1>' }) }).then(async r => ({ status: r.status, body: await r.json() }))`);
        assert.equal(real.status, 201);
        assert.equal(real.body.created_via, 'ui', 'a dashboard write is marked ui');
        const listed = await ev(`fetch('/api/views').then(r => r.json())`);
        assert.deepEqual(listed.map(v => v.name), ['dashboard view'], 'the refused write created nothing');
        await ev(`fetch('/api/views/' + ${real.body.id}, { method: 'DELETE' }).then(r => r.status)`);
        await ev(`(async () => {
            window.cv = await import((window.__coralStatic||'/static/')+'custom_views.js');
            window.probes = {}; window.apiCalls = [];
            window.addEventListener('message', e => { if (e.data && e.data.type === 'probe') window.probes[e.data.name] = e.data.value; });
            const probe = "(function(){ function say(n,v){ coral.get('/api/tags?'+n+'='+v).catch(function(){}); }" +
              "try { parent.document.title; say('parentDom','reachable'); } catch (e) { say('parentDom','blocked'); }" +
              "try { localStorage.getItem('x'); say('storage','reachable'); } catch (e) { say('storage','blocked'); }" +
              "fetch('/api/settings').then(function(){ say('settingsGet','allowed'); }, function(){ say('settingsGet','blocked'); });" +
              "fetch('/api/sessions/live', {method:'POST', body:'{}'}).then(function(){ say('write','allowed'); }, function(){ say('write','blocked'); });" +
              "coral.get('/api/tags').then(function(d){ say('bridgeGet', Array.isArray(d) ? 'ok' : 'bad'); }, function(e){ say('bridgeGet','failed:'+e.message); });" +
              "fetch('/api/tags').then(function(r){ return r.json(); }).then(function(d){ say('fetchShim', Array.isArray(d) ? 'ok' : 'bad'); }, function(){ say('fetchShim','failed'); });" +
              "try { var x = new XMLHttpRequest(); x.open('GET','/api/settings'); x.onload=function(){ say('xhr','allowed'); }; x.onerror=function(){ say('xhr','blocked'); }; x.send(); } catch (e) { say('xhr','blocked'); }" +
              "coral.get('/api/sessions/live/a/capture').then(function(){ say('capture','allowed'); }, function(e){ say('capture', /not available/.test(e.message) ? 'refused' : 'failed'); });" +
              "say('context', typeof window.CORAL_CONTEXT);" +
              "})()";
            const html = '<h1>view</h1><script>' + probe + '<\\/script>';
            const real = window.fetch.bind(window);
            window.fetch = async (url, init) => {
                const u = new URL(url, location.origin);
                if (u.pathname === '/api/views') return new Response(JSON.stringify([
                    { id: 101, name: 'ui view', html, created_via: 'ui' },
                    { id: 102, name: 'agent view', html, created_via: 'api' }]), { headers: { 'Content-Type': 'application/json' } });
                if (u.pathname === '/api/tags') { window.apiCalls.push(u.pathname); for (const [k, v] of u.searchParams) window.probes[k] = v; return new Response('[]', { headers: { 'Content-Type': 'application/json' } }); }
                if (u.pathname === '/api/settings' || u.pathname === '/api/sessions/live' && init && init.method === 'POST') window.apiCalls.push('FORBIDDEN ' + (init && init.method || 'GET') + ' ' + u.pathname);
                return real(url, init);
            };
            document.body.insertAdjacentHTML('beforeend', '<div id="custom-view-tabs"></div><div id="custom-view-panels"></div>');
            await cv.loadCustomViews();
        })()`);

        // A UI-created view runs at once, in an opaque-origin sandbox.
        await ev(`cv.activateCustomView(101)`);
        await sleep(1500);
        const frame = await ev(`(() => { const f = document.querySelector('#agentic-panel-custom-view-101 iframe'); return f && { sandbox: f.getAttribute('sandbox'), csp: f.getAttribute('csp') }; })()`);
        assert.ok(frame, 'the view frame exists');
        assert.equal(frame.sandbox, 'allow-scripts');
        assert.ok(!/same-origin/.test(frame.sandbox));
        assert.match(frame.csp, /default-src 'none'/);
        assert.ok(await ev(`document.querySelector('#agentic-panel-custom-view-101 iframe').srcdoc.includes("frame-src about:")`), 'the view runs inside a host frame whose CSP is frame-src about:');
        const probes = await ev(`window.probes`);
        assert.equal(probes.parentDom, 'blocked', 'the view cannot touch the dashboard DOM');
        assert.equal(probes.storage, 'blocked', 'the view has no storage (opaque origin)');
        assert.equal(probes.settingsGet, 'blocked', 'fetch("/api/settings") from a view fails');
        assert.equal(probes.write, 'blocked', 'a write from a view fails');
        assert.equal(probes.xhr, 'blocked', 'XHR from a view fails');
        assert.equal(probes.bridgeGet, 'ok', 'the bridge answers an allowlisted GET');
        assert.equal(probes.fetchShim, 'ok', 'fetch of an allowlisted GET works through the bridge');
        assert.equal(probes.context, 'object');
        assert.notEqual(probes.capture, 'refused', 'a dashboard-created view may read terminal output');
        const calls = await ev(`window.apiCalls`);
        assert.ok(!calls.some(x => x.startsWith('FORBIDDEN')), 'nothing forbidden reached the API: ' + JSON.stringify(calls));
        assert.ok(calls.includes('/api/tags'), 'the allowlisted read reached the API through the parent');

        // An API-created view waits for a click.
        await ev(`window.probes = {}; cv.activateCustomView(102)`);
        await sleep(500);
        const gate = await ev(`({ gate: !!document.querySelector('#agentic-panel-custom-view-102 .custom-view-gate'), frame: !!document.querySelector('#agentic-panel-custom-view-102 iframe'), text: document.querySelector('#agentic-panel-custom-view-102')?.textContent.slice(0, 600) })`);
        assert.ok(gate.gate && !gate.frame, 'an API-created view is gated: ' + JSON.stringify(gate));
        assert.match(gate.text, /automated caller/);
        assert.match(gate.text, /list of agents, their tasks, tags, board names/, 'the gate says what the view will see');
        assert.deepEqual(await ev(`window.probes`), {}, 'the gated view did not run');
        await ev(`document.querySelector('#agentic-panel-custom-view-102 .custom-view-gate button').click()`);
        await sleep(1200);
        assert.equal(await ev(`!!document.querySelector('#agentic-panel-custom-view-102 iframe')`), true, 'the view runs after the click');
        const apiProbes = await ev(`window.probes`);
        assert.equal(apiProbes.settingsGet, 'blocked');
        assert.equal(apiProbes.capture, 'refused', 'an API-created view cannot read terminal output');
        assert.equal(apiProbes.bridgeGet, 'ok', 'but can read the basic list');
        console.log('custom_views_sandbox_browser.test.js: ok');
    } finally { await c.close(); }
})().catch(e => { console.error(e); process.exit(1); });
