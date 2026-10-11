// Custom views run in an opaque-origin sandbox with a read-only bridge; API-created views wait for a click.
// Real Chrome against an isolated Coral (see run_cdp_suites.mjs): the view's script runs for real.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const base = process.env.CORAL_URL;
if (!base || /:8420(\/|$)/.test(base)) throw Error('Use isolated server');
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
        await ev(`(async () => {
            window.cv = await import('/static/custom_views.js');
            window.probes = {}; window.apiCalls = [];
            window.addEventListener('message', e => { if (e.data && e.data.type === 'probe') window.probes[e.data.name] = e.data.value; });
            const probe = "(function(){ function say(n,v){ parent.postMessage({type:'probe',name:n,value:v},'*'); }" +
              "try { parent.document.title; say('parentDom','reachable'); } catch (e) { say('parentDom','blocked'); }" +
              "try { localStorage.getItem('x'); say('storage','reachable'); } catch (e) { say('storage','blocked'); }" +
              "fetch('/api/settings').then(function(){ say('settingsGet','allowed'); }, function(){ say('settingsGet','blocked'); });" +
              "fetch('/api/sessions/live', {method:'POST', body:'{}'}).then(function(){ say('write','allowed'); }, function(){ say('write','blocked'); });" +
              "coral.get('/api/tags').then(function(d){ say('bridgeGet', Array.isArray(d) ? 'ok' : 'bad'); }, function(e){ say('bridgeGet','failed:'+e.message); });" +
              "fetch('/api/tags').then(function(r){ return r.json(); }).then(function(d){ say('fetchShim', Array.isArray(d) ? 'ok' : 'bad'); }, function(){ say('fetchShim','failed'); });" +
              "try { var x = new XMLHttpRequest(); x.open('GET','/api/settings'); x.onload=function(){ say('xhr','allowed'); }; x.onerror=function(){ say('xhr','blocked'); }; x.send(); } catch (e) { say('xhr','blocked'); }" +
              "say('context', typeof window.CORAL_CONTEXT);" +
              "})()";
            const html = '<h1>view</h1><script>' + probe + '<\\/script>';
            const real = window.fetch.bind(window);
            window.fetch = async (url, init) => {
                const u = new URL(url, location.origin);
                if (u.pathname === '/api/views') return new Response(JSON.stringify([
                    { id: 101, name: 'ui view', html, created_via: 'ui' },
                    { id: 102, name: 'agent view', html, created_via: 'api' }]), { headers: { 'Content-Type': 'application/json' } });
                if (u.pathname === '/api/tags') { window.apiCalls.push(u.pathname); return new Response('[]', { headers: { 'Content-Type': 'application/json' } }); }
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
        const probes = await ev(`window.probes`);
        assert.equal(probes.parentDom, 'blocked', 'the view cannot touch the dashboard DOM');
        assert.equal(probes.storage, 'blocked', 'the view has no storage (opaque origin)');
        assert.equal(probes.settingsGet, 'blocked', 'fetch("/api/settings") from a view fails');
        assert.equal(probes.write, 'blocked', 'a write from a view fails');
        assert.equal(probes.xhr, 'blocked', 'XHR from a view fails');
        assert.equal(probes.bridgeGet, 'ok', 'the bridge answers an allowlisted GET');
        assert.equal(probes.fetchShim, 'ok', 'fetch of an allowlisted GET works through the bridge');
        assert.equal(probes.context, 'object');
        const calls = await ev(`window.apiCalls`);
        assert.ok(!calls.some(x => x.startsWith('FORBIDDEN')), 'nothing forbidden reached the API: ' + JSON.stringify(calls));
        assert.ok(calls.includes('/api/tags'), 'the allowlisted read reached the API through the parent');

        // An API-created view waits for a click.
        await ev(`window.probes = {}; cv.activateCustomView(102)`);
        await sleep(500);
        const gate = await ev(`({ gate: !!document.querySelector('#agentic-panel-custom-view-102 .custom-view-gate'), frame: !!document.querySelector('#agentic-panel-custom-view-102 iframe'), text: document.querySelector('#agentic-panel-custom-view-102')?.textContent.slice(0, 120) })`);
        assert.ok(gate.gate && !gate.frame, 'an API-created view is gated: ' + JSON.stringify(gate));
        assert.match(gate.text, /automated caller/);
        assert.deepEqual(await ev(`window.probes`), {}, 'the gated view did not run');
        await ev(`document.querySelector('#agentic-panel-custom-view-102 .custom-view-gate button').click()`);
        await sleep(1200);
        assert.equal(await ev(`!!document.querySelector('#agentic-panel-custom-view-102 iframe')`), true, 'the view runs after the click');
        assert.equal((await ev(`window.probes`)).settingsGet, 'blocked');
        console.log('custom_views_sandbox_browser.test.js: ok');
    } finally { await c.close(); }
})().catch(e => { console.error(e); process.exit(1); });
