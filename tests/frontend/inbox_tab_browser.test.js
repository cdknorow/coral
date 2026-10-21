// The Inbox tab (#4718): a hub-only main tab that shows the hub's /inbox page in a frame, with the number of agents waiting for input as its badge.
//   - A local Coral shows its own Inbox in the same tab (#4811: Needs you and Saved for later, no frame, inbox_local_browser); where the hub serves the dashboard it is this page's frame, right
//     after Servers and before Docs.
//   - The badge is the number of agents the feed says wait for input (99+ past 99), nothing when none do, and it follows the next feed message; the
//     tab's name says the number.
//   - Opening the tab shows its view and loads the frame from the constant address /inbox (once, kept after), with referrerpolicy no-referrer; the
//     tab writes no address of its own; nothing is posted to or read from the frame (no postMessage and no message listener in its source).
//   - On a phone it is one more entry in the view list, and choosing it shows the same view.
//   - The dashboard's CSP leaves a same-origin frame allowed (a frame-src, if there is one, names 'self').
// Served as the hub would serve it (the boot flag says mode "cloud", as in hub_first_run_browser), with its page /inbox answered by the test.
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. Exit 0 / 1.
'use strict';
const fs = require('node:fs');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const { themeJs } = require('./helpers/theme');
const BASE = require('./test_base').BASE;
const { safePaused } = require('./helpers/paused');
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Network, Fetch, Emulation } = client;
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor } = require('./helpers/wait').bind(ev);
    const { framesJs } = require('./helpers/wait');
const { settle } = require('./helpers/wait');
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: proves a negative (nothing loaded) and lets the page's first requests settle
    let cloud = true, inboxHits = 0;
    const SW = path.join(__dirname, '..', '..', 'coral-go', 'pkg', 'frontend', 'static');
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
            { urlPattern: `${BASE}/api/servers*`, requestStage: 'Request' }, { urlPattern: `${BASE}/api/sessions/live*`, requestStage: 'Request' }, { urlPattern: `${BASE}/inbox*`, requestStage: 'Request' }] });
        const json = (id, status, body) => Fetch.fulfillRequest({ requestId: id, responseCode: status, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify(body)).toString('base64') });
        let csp = '';
        safePaused(client, async e => {
            const u = e.request.url;
            if (u.endsWith('/auth/session')) return json(e.requestId, 200, { authenticated: true, email: 'tester@example.com', csrf: 'x' });
            if (/\/api\/servers(\?|$)/.test(u)) return json(e.requestId, 200, { servers: [{ id: 'box-a', label: 'Box A', online: true, created_at: '2026-10-01T00:00:00Z', last_seen: '2026-10-18T00:00:00Z' }] });
            if (/\/api\/sessions\/live(\?|$)/.test(u)) return json(e.requestId, 200, cloud ? { sessions: [], servers: [{ id: 'box-a', label: 'Box A', status: 'online' }] } : []);
            if (/\/inbox(\?|$)/.test(u)) { inboxHits++; return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'text/html' }], body: Buffer.from('<!doctype html><title>Inbox</title><p>The hub inbox</p>').toString('base64') }); }
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
            if (cloud) html = html.replace('"mode":"local"', '"mode":"cloud"');
            csp = ((e.responseHeaders || []).find(h => /^content-security-policy$/i.test(h.name)) || {}).value || csp;
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        const open = async (w, h) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: w < 700 ? 2 : 1, mobile: w < 700 });
            await Emulation.setTouchEmulationEnabled({ enabled: w < 700 });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await settle(ev, 2500); // sleep-ok: the page and its first lists arrive from requests; there is no completion event for them
            check(`the page is served as the ${cloud ? 'hub' : 'local Coral'}`, (await ev(`document.body.classList.contains('cloud-mode')`)) === cloud);
        };
        const visible = id => ev(`(() => { const e = document.getElementById(${JSON.stringify(id)}); return !!e && !e.hidden && getComputedStyle(e).display !== 'none' && e.getClientRects().length > 0; })()`);
        const feed = n => ev(`(async () => { const w = await import((window.__coralStatic || '/static/') + 'websocket.js');
            w.handleCoralMessage({ type: 'coral_update', sessions: Array.from({ length: ${n.total} }, (_, i) => ({ name: 'a' + i, session_id: 'a' + i + '-id', agent_type: 'claude', status: 'Idle', waiting_for_input: i < ${n.waiting} })), servers: [] }); return true; })()`);
        const badge = () => ev(`(() => { const b = document.getElementById('nav-tab-hub-inbox-badge'), t = document.getElementById('nav-tab-hub-inbox'); return { text: b.textContent, hidden: b.hidden, label: t.getAttribute('aria-label') }; })()`);

        console.log('2. Where the hub serves the dashboard: after Servers, before Docs');
        cloud = true;
        await open(1280, 756);
        check('the tab is shown', await visible('nav-tab-hub-inbox'));
        const order = await ev(`[...document.querySelectorAll('.top-nav-tabs > .top-nav-tab')].filter(el => !el.hidden && getComputedStyle(el).display !== 'none').map(el => el.dataset.arg)`);
        check('right after Servers and before Docs', order.indexOf('hub-inbox') === order.indexOf('servers') + 1 && order.indexOf('hub-inbox') < order.indexOf('docs'), JSON.stringify(order));
        check('its name is Inbox, with no number yet', (await ev(`document.getElementById('nav-tab-hub-inbox').firstChild.textContent`)) === 'Inbox' && (await badge()).hidden === true);

        console.log('3. The badge is the number of agents waiting for input');
        await feed({ total: 5, waiting: 2 });
        let b = await badge();
        check('two waiting: the badge says 2 and the tab says so', b.text === '2' && !b.hidden && /2 waiting/.test(b.label || ''), JSON.stringify(b));
        await feed({ total: 5, waiting: 1 });
        b = await badge(); check('it follows the next message', b.text === '1' && !b.hidden, JSON.stringify(b));
        await feed({ total: 120, waiting: 105 });
        b = await badge(); check('past 99 it says 99+', b.text === '99+', JSON.stringify(b));
        await feed({ total: 5, waiting: 0 });
        b = await badge(); check('none waiting: no badge and the plain name', b.hidden && b.text === '' && b.label === null, JSON.stringify(b));
        await ev(framesJs(4)); // the row's own redraw after the badge changed has finished
        await ev(`window.__navMut = 0; new MutationObserver(r => { window.__navMut += r.length; window.__navWhat = (window.__navWhat || []).concat(r.map(x => x.type + ':' + (x.target.id || x.target.className || x.target.nodeName) + ':' + (x.attributeName || ''))); }).observe(document.querySelector('.top-nav-tabs'), { attributes: true, childList: true, characterData: true, subtree: true }); true`);
        await feed({ total: 3, waiting: 0 }); await feed({ total: 3, waiting: 0 });
        check('a feed message that changes no count does not touch the tab row (the row redraws on any change)', (await ev(`new Promise(r => setTimeout(r, 50)).then(() => window.__navMut)`)) === 0, JSON.stringify(await ev(`window.__navWhat`))); // sleep-ok: the observer reports after the messages' tasks
        await feed({ total: 3, waiting: 2 });
        check('and one that does change it does', (await ev(`new Promise(r => setTimeout(r, 50)).then(() => window.__navMut)`)) > 0); // sleep-ok: the observer reports after the message's task
        await feed({ total: 3, waiting: 3 });
        await feed({ total: 3, waiting: 3 });
        check('a visible badge is not a bare dot: it has text', (await ev(`document.getElementById('nav-tab-hub-inbox-badge').textContent.length > 0`)));

        const ratio = theme => ev(`(() => { ${themeJs(theme)}; const b = document.getElementById('nav-tab-hub-inbox-badge'), cs = getComputedStyle(b);
            const px = c => { const x = document.createElement('canvas').getContext('2d'); x.fillStyle = '#000'; x.fillStyle = c; x.fillRect(0, 0, 1, 1); return [...x.getImageData(0, 0, 1, 1).data].slice(0, 3); };
            const L = ([r, g, bl]) => [r, g, bl].map(v => v / 255).map(v => v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4).reduce((a, v, i) => a + v * [0.2126, 0.7152, 0.0722][i], 0);
            const a = L(px(cs.color)), c = L(px(cs.backgroundColor)); return (Math.max(a, c) + 0.05) / (Math.min(a, c) + 0.05); })()`);
        for (const theme of ['dark', 'light']) { const r = await ratio(theme); check(`the badge's 11px number reads at 4.5:1 or more in the ${theme} theme`, r >= 4.5, r.toFixed(2)); }
        await ev(`document.documentElement.removeAttribute('data-theme'); true`);

        console.log('4. Opening the tab loads the page from the constant address, once');
        check('before it is opened there is no frame at all (no page carries an empty one) and the page was not asked for', (await ev(`document.getElementById('inbox-frame')`)) === null && (await ev(`document.querySelectorAll('iframe').length`)) === 0 && inboxHits === 0, String(await ev(`document.querySelectorAll('iframe').length`)));
        const pathBefore = await ev(`location.pathname + location.search + location.hash`);
        await ev(`document.getElementById('nav-tab-hub-inbox').click(); true`);
        check('the view is shown', await waitFor(`getComputedStyle(document.getElementById('inbox-view')).display !== 'none'`, 'the inbox view', { soft: true, timeout: 6000 }));
        check('the tab is the current one', (await ev(`document.getElementById('nav-tab-hub-inbox').getAttribute('aria-current')`)) === 'page');
        const fr = await ev(`(() => { const f = document.getElementById('inbox-frame'); return { src: f.getAttribute('src'), rp: f.getAttribute('referrerpolicy'), sandbox: f.getAttribute('sandbox'), allow: f.getAttribute('allow'), tag: f.tagName }; })()`);
        check('the frame address is the constant /inbox', fr.src === '/inbox' && fr.tag === 'IFRAME', JSON.stringify(fr));
        check('with referrerpolicy no-referrer', fr.rp === 'no-referrer');
        check('and no powerful features (allow is empty)', fr.allow === '', JSON.stringify(fr.allow));
        check('the frame loaded the page', await waitFor(`!!document.getElementById('inbox-frame').contentDocument && /hub inbox/.test(document.getElementById('inbox-frame').contentDocument.body.textContent)`, 'the framed page', { soft: true, timeout: 6000 }));
        check('the page was asked for once', inboxHits === 1, String(inboxHits));
        check('the tab wrote no address of its own', (await ev(`location.pathname + location.search + location.hash`)) === pathBefore, await ev(`location.pathname`));
        check('the other views are hidden', (await ev(`['welcome-screen', 'servers-view', 'live-session-view', 'mobile-agent-list'].every(id => { const e = document.getElementById(id); return !e || getComputedStyle(e).display === 'none'; })`)));
        const box = await ev(`(() => { const r = document.getElementById('inbox-frame').getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height) }; })()`);
        check('the frame fills the view', box.w > 600 && box.h > 400, JSON.stringify(box));
        await ev(`document.getElementById('nav-tab-agents').click(); true`);
        await ev(`document.getElementById('nav-tab-hub-inbox').click(); true`); await sleep(600); // sleep-ok: proves a negative, a second load would be asked for in this time
        check('going away and back keeps the page (no second load)', inboxHits === 1, String(inboxHits));

        console.log('5. Nothing goes between the dashboard and the frame');
        const own = fs.readFileSync(path.join(SW, 'inbox_tab.js'), 'utf8');
        check('inbox_tab.js has no postMessage and no message listener', !/postMessage|addEventListener\(\s*['"]message['"]|onmessage|contentWindow/.test(own));
        const tpl = fs.readFileSync(path.join(__dirname, '..', '..', 'coral-go', 'pkg', 'frontend', 'templates', 'includes', 'views', 'inbox.html'), 'utf8');
        check('the template has no iframe, and the frame is made in code with the attributes above', !/<iframe/.test(tpl) && /createElement\('iframe'\)/.test(own) && /setAttribute\('allow', ''\)/.test(own));

        console.log('6. The dashboard\'s CSP leaves a same-origin frame allowed');
        const fs_ = /(?:^|;)\s*frame-src\s+([^;]*)/.exec(csp);
        check('the page has a CSP', csp.length > 0);
        check("it has no frame-src, or one that names 'self'", !fs_ || /'self'/.test(fs_[1]), fs_ ? fs_[0] : 'no frame-src');
        check('and it does not let another site frame the dashboard', /frame-ancestors\s+'self'/.test(csp));

        console.log('7. A phone: one more entry in the view list');
        await open(390, 756);
        await feed({ total: 4, waiting: 3 });
        await ev(`document.getElementById('nav-select').click(); true`);
        check('the list has Inbox after Servers', await waitFor(`!!document.querySelector('#nav-more-menu [data-arg="hub-inbox"]')`, 'the Inbox entry', { soft: true, timeout: 5000 }));
        const items = await ev(`[...document.querySelectorAll('#nav-more-menu .nav-more-item')].map(e => e.children[1].textContent.trim())`);
        check('named Inbox, in order', items.indexOf('Inbox') === items.indexOf('Servers') + 1 && items.indexOf('Inbox') > -1, JSON.stringify(items));
        const entry = await ev(`(() => { const e = document.querySelector('#nav-more-menu [data-arg="hub-inbox"]'); const b = e.querySelector('.nav-tab-badge'); return { count: b ? b.textContent : null, label: e.getAttribute('aria-label'), others: [...document.querySelectorAll('#nav-more-menu .nav-more-item')].filter(x => x !== e && x.querySelector('.nav-tab-badge')).length }; })()`);
        check('the Inbox entry shows the waiting number and says it', entry.count === '3' && /3 waiting for input/.test(entry.label || '') && entry.others === 0, JSON.stringify(entry));
        await ev(`document.querySelector('#nav-more-menu [data-arg="hub-inbox"]').click(); true`);
        check('choosing it shows the view and the frame', await waitFor(`getComputedStyle(document.getElementById('inbox-view')).display !== 'none' && document.getElementById('inbox-frame').getAttribute('src') === '/inbox'`, 'the view', { soft: true, timeout: 6000 }));
        check('the view list names the current view', await waitFor(`/Inbox/.test(document.getElementById('nav-select-label').textContent)`, 'the label', { soft: true, timeout: 4000 }), await ev(`document.getElementById('nav-select-label').textContent.trim()`));
        check('nothing scrolls sideways', (await ev(`document.documentElement.scrollWidth - document.documentElement.clientWidth`)) <= 0);
        await feed({ total: 4, waiting: 0 });
        await ev(`document.getElementById('nav-select').click(); true`);
        check('with none waiting the entry has no number and the plain name', await waitFor(`!!document.querySelector('#nav-more-menu [data-arg="hub-inbox"]') && !document.querySelector('#nav-more-menu [data-arg="hub-inbox"] .nav-tab-badge') && document.querySelector('#nav-more-menu [data-arg="hub-inbox"]').getAttribute('aria-label') === null`, 'the plain entry', { soft: true, timeout: 4000 }));
        console.log('8. A local Coral shows its own Inbox (#4811), never the hub frame (last: the page is cached by then, so the boot flag is rewritten for it only)');
        cloud = false;
        await open(1280, 756);
        check('the tab is shown', (await visible('nav-tab-hub-inbox')) === true);
        check('and is in the list of views, right after Agents', (await ev(`[...document.querySelectorAll('.top-nav-tabs > .top-nav-tab[data-arg]')].filter(el => !el.hidden && el.style.display !== 'none').map(el => el.dataset.arg).slice(0, 2).join()`)) === 'agents,hub-inbox');
        check('there is no frame on a local Coral', (await ev(`document.getElementById('inbox-frame')`)) === null && (await ev(`document.querySelectorAll('iframe').length`)) === 0);
        await ev(`document.getElementById('nav-tab-hub-inbox').click(); true`);
        check('opening it shows the dashboard\'s own Inbox (Needs you, Saved for later), still no frame', await waitFor(`document.getElementById('inbox-local') && !document.getElementById('inbox-local').hidden && !!document.getElementById('inbox-tab-needs') && !!document.getElementById('inbox-tab-saved') && !!document.getElementById('saved-view')`, 'the local Inbox', { soft: true, timeout: 6000 }) && (await ev(`document.querySelectorAll('iframe').length`)) === 0);
        await feed({ total: 3, waiting: 2 });
        check('a feed with agents waiting shows the badge there (2), as on the hub', await waitFor(`document.getElementById('nav-tab-hub-inbox-badge').textContent === '2' && !document.getElementById('nav-tab-hub-inbox-badge').hidden`, 'the badge', { soft: true, timeout: 6000 }));
        check('and the hub page was not asked for by the local page', inboxHits === 2, String(inboxHits));

    } finally { await client.close(); }
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(failed ? `\n${failed} FAILED` : '\nALL PASS');
    process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error(e); process.exit(1); });
