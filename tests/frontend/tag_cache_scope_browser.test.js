// The History tag cache (window._allTagsCache, read by folder_tags.js) is this computer's own: a History load on this computer fills it as before, but a History load that reads a linked
// computer's tags through Coral Hub never does (#6115). Chrome over CDP through agent_view_harness.js; the Hub is faked in the page as in hub_blocked_controls_browser.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const HUB_PRELOAD = `(() => {
    const inner = window.fetch;
    window.fetch = (url, opts = {}) => {
        const p = new URL(String(url && url.url || url), location.origin).pathname;
        const m = (opts.method || 'GET').toUpperCase();
        if (p === '/api/servers' && m === 'GET') return Promise.resolve(new Response(JSON.stringify({ servers: [{ id: 'm1', label: 'Box One', online: true, status: 'online', access_mode: 'launch', local_mode: 'launch' }] }), { status: 200, headers: { 'content-type': 'application/json' } }));
        if (p.endsWith('/api/tags') && m === 'GET') { window.__tagCalls = (window.__tagCalls || []).concat(p); return Promise.resolve(new Response(JSON.stringify([{ id: 1, name: p.includes('m1') ? 'remote-tag' : 'local-tag', color: '#112233' }]), { status: 200, headers: { 'content-type': 'application/json' } })); }
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const { Fetch, Network } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const S = `(window.__coralStatic || '/static/')`;
    const cloudMode = async on => {
        await Fetch.disable();
        if (!on) return;
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' }] });
    };
    safePaused(client, async e => {
        if (e.request.url.endsWith('/auth/session')) return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
        if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
        const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
        const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace('"mode":"local"', '"mode":"cloud"');
        const headers = (e.responseHeaders || []).filter(h => !/^content-(length|encoding)$/i.test(h.name));
        return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
    });
    try {
        await Promise.all([Page.enable(), Network.enable()]); await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: HUB_PRELOAD });
        // 1. this computer's dashboard: a History load fills the cache, as in 157
        await cloudMode(false);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        let r = await ev(`(async () => { window._allTagsCache = ['old']; const m = await import(${S} + 'tags.js'); const t = await m.loadAllTags(true); return { returned: t.map(x => x.name), cache: (window._allTagsCache || []).map(x => x.name || x) }; })()`);
        check('this computer: a History tag load fills the cache with its tags', JSON.stringify(r.cache) === '["local-tag"]' && JSON.stringify(r.returned) === '["local-tag"]', JSON.stringify(r));
        r = await ev(`(async () => { window._allTagsCache = ['old']; const m = await import(${S} + 'tags.js'); await m.loadAllTags(false); return (window._allTagsCache || []).map(x => x.name || x); })()`);
        check('this computer: a plain tag load fills the cache too', JSON.stringify(r) === '["local-tag"]', JSON.stringify(r));
        // 2. through Coral Hub: a History load reads a linked computer's tags and must not cache them
        await cloudMode(true);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await waitFor(`!!window.T && !!T.state`, 'the page', 15000);
        r = await ev(`(async () => { const st = (await import(${S} + 'state.js')).state; st.servers = [{ id: 'm1', status: 'online' }]; st.historySourceServer = 'm1'; window._allTagsCache = ['this-computers']; window.__tagCalls = [];
            const m = await import(${S} + 'tags.js'); const t = await m.loadAllTags(true); return { returned: Array.isArray(t) ? t.map(x => x.name) : t, cache: (window._allTagsCache || []).map(x => x.name || x), calls: window.__tagCalls }; })()`);
        check('through Coral Hub: a History tag load reads the linked computer and does not touch the cache', JSON.stringify(r.returned) === '["remote-tag"]' && JSON.stringify(r.cache) === '["this-computers"]' && r.calls.some(c => c.includes('m1')), JSON.stringify(r));
    } finally { await client.close(); }
    const failed = results.filter(x => !x).length;
    console.log(`tag_cache_scope: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
