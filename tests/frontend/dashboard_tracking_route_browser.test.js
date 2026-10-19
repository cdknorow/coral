// Every dashboard event leaves the page only as a POST to the server's tracking endpoint (#4938). dashboard_analytics.js has one transport, emit(): fetch POST /api/tracking/event. This loads the real
// dashboard and fires each of its events (dashboard_ready and dashboard_active_day on load, prompt_submit_requested from the composer's call, dashboard_failed from a load whose sessions fail), recording
// EVERY request the page makes (Chrome's Network domain, so nothing can hide, plus wrappers on fetch, sendBeacon and XHR), and requires: each event is a POST to /api/tracking/event on this origin; no
// request goes to another host; no beacon and no XHR carries anything; and the events carry only bounded props (no text, no session ids). Real isolated server (a dev build: the server drops the events,
// nothing reaches an analytics service). Drives Chrome over CDP.
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const ORIGIN = new URL(BASE).origin;
// the only props each event may carry: a new prop (a session id, a name, text) fails here until it is reviewed and added
const ALLOWED_PROPS = { dashboard_ready: ['page_id'], dashboard_active_day: [], dashboard_failed: ['code'], prompt_submit_requested: ['source'] };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Emulation } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const ev = async expression => { const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 500)); return r.result.value; };
    const waitFor = async (expression, what, ms = 8000) => { const until = Date.now() + ms; for (;;) { if (await ev(expression)) return; if (Date.now() > until) throw new Error('timed out waiting for ' + what); await new Promise(r => setTimeout(r, 25)); } }; // sleep-ok: the page's own events come over frames; there is no completion event
    const S = `(window.__coralStatic || '/static/')`;
    const requests = []; // every request the page makes
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        Network.requestWillBeSent(e => { requests.push({ url: e.request.url, method: e.request.method, body: e.request.postData || '', type: e.type }); });
        Network.webSocketCreated(e => { requests.push({ url: e.url, method: 'WS', body: '', type: 'WebSocket' }); }); // a socket is not a requestWillBeSent
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
        // before any page script: sessions can be made to fail (a dashboard_failed), and fetch, sendBeacon and XHR are recorded as a second view
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__sent = []; const rf = window.fetch;
            window.fetch = function (u, o) { try { window.__sent.push(['fetch', String(u && u.url || u), (o && o.method) || 'GET', (o && o.body) || '']); } catch {}
                if (/\\/api\\/sessions\\/live(\\?|$)/.test(String(u)) && localStorage.getItem('__fail_sessions') === '1') return Promise.resolve(new Response('{}', { status: 500 }));
                return rf.apply(this, arguments); };
            if (navigator.sendBeacon) { const sb = navigator.sendBeacon.bind(navigator); navigator.sendBeacon = (...a) => { window.__sent.push(['beacon', String(a[0]), 'POST', String(a[1] || '')]); return sb(...a); }; }
            const xo = XMLHttpRequest.prototype.open; XMLHttpRequest.prototype.open = function (m, u) { window.__sent.push(['xhr', String(u), m, '']); return xo.apply(this, arguments); };
        })()` });
        const load = async (failSessions) => {
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await ev(`localStorage.setItem('__fail_sessions', ${failSessions ? "'1'" : "'0'"}); true`);
            requests.length = 0;
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        };
        const isEvent = body => /"event"\s*:/.test(body || '');

        await load(false);
        await waitFor(`!!window.__coralReady`, 'the page');
        await ev(`window.__coralReady.then(() => true)`);
        await waitFor(`window.__sent.some(s => /tracking\\/event/.test(s[1]) && /dashboard_ready/.test(s[3]))`, 'dashboard_ready');
        await ev(`(async () => { const m = await import(${S} + 'dashboard_analytics.js'); m.dashboardPromptRequested(); return true; })()`);
        await waitFor(`window.__sent.some(s => /prompt_submit_requested/.test(s[3]))`, 'prompt_submit_requested');
        const okLoad = { net: requests.slice(), sent: await ev(`window.__sent`) };
        await load(true);
        await waitFor(`window.__sent.some(s => /dashboard_failed/.test(s[3]))`, 'dashboard_failed (sessions made to fail)', 12000);
        const failLoad = { net: requests.slice(), sent: await ev(`window.__sent`) };

        for (const [name, run, wanted] of [['a normal load and a prompt', okLoad, ['dashboard_ready', 'dashboard_active_day', 'prompt_submit_requested']], ['a load whose sessions fail', failLoad, ['dashboard_failed']]]) {
            const sentEvents = run.sent.filter(s => isEvent(s[3]));
            const events = new Set(sentEvents.map(s => JSON.parse(s[3]).event));
            check(`${name}: the events seen are ${wanted.join(', ')}`, wanted.every(e => events.has(e)), [...events].join(', '));
            check(`${name}: every request that carries an event is a POST to /api/tracking/event on this origin`, sentEvents.every(s => s[0] === 'fetch' && s[2] === 'POST' && new URL(s[1], BASE).pathname === '/api/tracking/event' && new URL(s[1], BASE).origin === ORIGIN)
                && run.net.filter(r => isEvent(r.body)).every(r => r.method === 'POST' && new URL(r.url).pathname === '/api/tracking/event' && new URL(r.url).origin === ORIGIN), JSON.stringify(sentEvents.map(s => s.slice(0, 3))));
            check(`${name}: no request goes to another host`, run.net.every(r => /^(data|blob):/.test(r.url) || (r.type === 'WebSocket' ? new URL(r.url).host === new URL(ORIGIN).host : new URL(r.url).origin === ORIGIN)), JSON.stringify([...new Set(run.net.map(r => { try { return new URL(r.url).origin; } catch { return r.url.slice(0, 20); } }))]));
            check(`${name}: no beacon and no XHR`, !run.sent.some(s => s[0] === 'beacon' || s[0] === 'xhr'));
            check(`${name}: the events carry only bounded props (only the reviewed prop names, no text, no session id)`, sentEvents.every(s => { const b = JSON.parse(s[3]); return Object.keys(b).sort().join() === 'event,props' && Object.keys(b.props || {}).every(k => (ALLOWED_PROPS[b.event] || []).includes(k)) && Object.values(b.props || {}).every(v => typeof v === 'string' ? v.length <= 64 : typeof v === 'number' || typeof v === 'boolean'); }), JSON.stringify(sentEvents.map(s => JSON.parse(s[3]))));
        }
    } catch (e) { console.error(e && e.stack || e); results.push(false); }
    finally { try { await client.close(); } catch (_) {} }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
