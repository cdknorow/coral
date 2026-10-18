// Through the Coral Hub, killing a whole team and deleting a board need Launch on that machine (#3433).
// The hub-served dashboard (#3443):
//   1. at Interact, the team menu's "Kill All" is shown disabled with "needs Launch" and the reason,
//      and calling it says why (a toast, no dialog) instead of sending a kill per agent;
//   2. the board's Delete button is disabled ("Delete (needs Launch)", the reason as its title), and
//      deleting says why;
//   3. at Launch both are as before;
//   4. a local dashboard is unchanged.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const BOARD = 'team-x';

// The hub's Servers list (machine m1 at a mode the test sets) and a record of kill requests.
const HUB_PRELOAD = `(() => {
    window.__mode = 'interact';
    window.__kills = []; window.__deletes = [];
    const inner = window.fetch;
    window.fetch = (url, opts = {}) => {
        const p = new URL(String(url && url.url || url), location.origin).pathname;
        const m = (opts.method || 'GET').toUpperCase();
        if (p === '/api/servers' && m === 'GET') {
            return Promise.resolve(new Response(JSON.stringify({ servers: [{ id: 'm1', label: 'Box One', online: true, access_mode: window.__mode, local_mode: 'launch' }] }),
                { status: 200, headers: { 'content-type': 'application/json' } }));
        }
        if (/\\/kill$/.test(p) && m === 'POST') { window.__kills.push(p); return Promise.resolve(new Response('{"ok":true}', { status: 200, headers: { 'content-type': 'application/json' } })); }
        if (/^\\/api\\/remote\\/m1\\/api\\/board\\/[^/]+$/.test(p) && m === 'DELETE') { window.__deletes.push(p); return Promise.resolve(new Response('{"ok":true}', { status: 200, headers: { 'content-type': 'application/json' } })); }
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const { Fetch, Network } = client;
    const cloudMode = async on => {
        await Fetch.disable();
        if (!on) return;
        await Fetch.enable({ patterns: [
            { urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' },
            { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' },
        ] });
    };
    safePaused(client, async e => {
        if (e.request.url.endsWith('/auth/session')) {
            return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
        }
        if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
        const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
        const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace('{"mode":"local"}', '{"mode":"cloud"}');
        const headers = (e.responseHeaders || []).filter(h => !/^content-(length|encoding)$/i.test(h.name));
        return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
    });
    const team = `@m1/${BOARD}`;
    const killAll = () => ev(`(() => {
        const b = [...document.querySelectorAll('.overflow-menu-item')].find(x => /Kill All/.test(x.textContent));
        return b ? { disabled: b.disabled, text: b.textContent.replace(/\\s+/g, ' ').trim(), title: b.getAttribute('title') || '', onclick: !!(b.getAttribute('onclick') || b.getAttribute('data-on-click')) } : null;
    })()`);
    const access = mode => ev(`(async () => {
        window.__mode = ${JSON.stringify(mode)};
        (await import((window.__coralStatic || '/static/') + 'cloud_servers.js')).invalidateServerAccess();
        T.render.renderLiveSessions(T.state.liveSessions); // asks again, redraws when the list is in
        return true; })()`);

    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: HUB_PRELOAD });
        await cloudMode(true);
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await install();
        await ev(`(() => { for (const n of ['x-one', 'x-two']) window.__m.add(n, 'X', 2, { sleeping: false, server: 'm1', board_project: ${JSON.stringify(BOARD)} });
            T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);

        console.log('1. Interact: Kill All disabled, with the reason');
        await waitFor(`(() => { const b = [...document.querySelectorAll('.overflow-menu-item')].find(x => /Kill All/.test(x.textContent)); return !!b && b.disabled; })()`, 'Kill All disabled at Interact', 8000);
        let k = await killAll();
        assert.match(k.text, /Kill All needs Launch/);
        assert.match(k.title, /^Blocked: Box One needs Launch/, k.title);
        assert.equal(k.onclick, false, 'nothing to click');
        await ev(`(async () => { await T.render.killBoard(${JSON.stringify(team)}); return true; })()`);
        await waitFor(`[...document.querySelectorAll('.toast .toast-message')].some(t => /needs Launch/.test(t.textContent))`, 'the reason as a toast', 3000);
        assert.equal(await ev(`(() => { const m = document.getElementById('alert-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`), false, 'no dialog');
        assert.deepEqual(await ev(`window.__kills`), [], 'no kill sent');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
        console.log(`   ✓ "${k.text}", title "${k.title.slice(0, 60)}…", killBoard explains in a toast and sends nothing`);

        console.log('2. Interact: the board\'s Delete disabled, with the reason');
        await ev(`(async () => { (await import((window.__coralStatic || '/static/') + 'message_board.js')).selectBoardProject(${JSON.stringify(team)}); return true; })()`);
        await waitFor(`document.getElementById('mb-delete-btn').disabled === true`, 'Delete disabled', 5000);
        const del = await ev(`(() => { const b = document.getElementById('mb-delete-btn'); return { text: b.textContent, title: b.title }; })()`);
        assert.equal(del.text, 'Delete (needs Launch)');
        assert.match(del.title, /^Blocked: Box One needs Launch/);
        await ev(`(async () => { (await import((window.__coralStatic || '/static/') + 'message_board.js')).deleteMessageBoardProject(); return true; })()`);
        await waitFor(`[...document.querySelectorAll('.toast .toast-message')].some(t => /needs Launch/.test(t.textContent))`, 'the reason as a toast', 3000);
        assert.deepEqual(await ev(`window.__deletes`), [], 'no delete sent');
        console.log('   ✓ disabled with the reason; deleting explains and sends nothing');

        console.log('3. Launch: both as before');
        await access('launch');
        await waitFor(`(() => { const b = [...document.querySelectorAll('.overflow-menu-item')].find(x => /Kill All/.test(x.textContent)); return !!b && !b.disabled; })()`, 'Kill All enabled at Launch', 8000);
        k = await killAll();
        assert.equal(k.text, 'Kill All'); assert.equal(k.onclick, true);
        await ev(`(async () => { (await import((window.__coralStatic || '/static/') + 'message_board.js')).selectBoardProject(${JSON.stringify(team)}); return true; })()`);
        await waitFor(`document.getElementById('mb-delete-btn').disabled === false && document.getElementById('mb-delete-btn').textContent === 'Delete'`, 'Delete enabled', 5000);
        console.log('   ✓ Kill All and Delete enabled');

        console.log('4. A local dashboard is unchanged');
        await cloudMode(false);
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await install();
        await ev(`(() => { window.__mode = 'view'; for (const n of ['y-one']) window.__m.add(n, 'Y', 2, { sleeping: false, board_project: 'local-team' });
            T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
        await waitFor(`[...document.querySelectorAll('.overflow-menu-item')].some(x => /Kill All/.test(x.textContent))`, 'the local team menu', 5000);
        k = await killAll();
        assert.equal(k.disabled, false, 'never gated locally'); assert.equal(k.text, 'Kill All');
        console.log('   ✓ local Kill All as before');

        console.log('\nAll hub Launch-action checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
