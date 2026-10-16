// #3769 (from the #3735 sweep): through the Coral Hub a machine can be limited to View or Interact. A control the tier
// does not allow is dimmed with the reason as its title (it stays clickable, so a click still says why):
//   - the command pane's Send, Send options, quick commands, macros and raw keys, and an agent menu's Kill, Rename, Sleep
//     and Archive need Interact; Restart (and Wake / Restart on the sleeping bar) need Launch;
//   - at View both groups are dimmed, at Interact only the Launch ones, at Launch none;
//   - controls that read or only rearrange the list (Session Info, Move Up) are never dimmed;
//   - a local dashboard is unchanged.
// The hub is faked as in hub_launch_actions_browser: the page's boot data says "cloud" and /api/servers is answered in the page.
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const HUB_PRELOAD = `(() => {
    window.__mode = 'view';
    const inner = window.fetch;
    window.fetch = (url, opts = {}) => {
        const p = new URL(String(url && url.url || url), location.origin).pathname;
        if (p === '/api/servers' && (opts.method || 'GET').toUpperCase() === 'GET') {
            return Promise.resolve(new Response(JSON.stringify({ servers: [{ id: 'm1', label: 'Box One', online: true, access_mode: window.__mode, local_mode: 'launch' }] }),
                { status: 200, headers: { 'content-type': 'application/json' } }));
        }
        return inner(url, opts);
    };
})();`;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    const { Fetch, Network } = client;
    const { waitStable } = require('./helpers/wait').bind(ev);
    // The marking follows a redraw by ~120 ms: wait until the number of dimmed controls stops changing.
    const marked = () => waitStable(`document.querySelectorAll('.hub-blocked').length`, 'the dimmed controls to settle', 450);
    const cloudMode = async on => {
        await Fetch.disable();
        if (!on) return;
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' }] });
    };
    Fetch.requestPaused(async e => {
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
    // What is dimmed, by action, inside the agent's row menu, the command pane and the sleeping bar.
    const state = () => ev(`(() => {
        const out = { row: {}, pane: {}, bar: {} };
        const pick = (root, into, attr) => { for (const el of root.querySelectorAll('[' + attr + ']')) { const a = el.getAttribute(attr); if (!a) continue; into[a] = { blocked: el.classList.contains('hub-blocked'), aria: el.getAttribute('aria-disabled'), disabled: !!el.disabled, title: el.title || '' }; } };
        const row = document.querySelector('#live-sessions-list [data-session-id="bc-awake-id"]');
        if (row) pick(row, out.row, 'data-on-click');
        pick(document.getElementById('command-pane'), out.pane, 'data-on-click');
        pick(document.getElementById('sleeping-composer'), out.bar, 'data-action');
        return out;
    })()`);
    const access = mode => ev(`(async () => {
        window.__mode = ${JSON.stringify(mode)};
        (await import((window.__coralStatic || '/static/') + 'cloud_servers.js')).invalidateServerAccess();
        T.render.renderLiveSessions(T.state.liveSessions);
        // Nothing is redrawn, so nothing marks again until the 5 s look: do that look now (the first asks the hub, its answer marks).
        (await import((window.__coralStatic || '/static/') + 'hub_blocked_controls.js')).markBlockedControls();
        return true; })()`);
    const blockedAs = (group, names, want, tier, label) => {
        for (const n of names) {
            assert.ok(group[n], `${label}: ${n} exists`);
            assert.equal(group[n].blocked, want, `${label}: ${n} ${want ? 'is' : 'is not'} dimmed (${JSON.stringify(group[n])})`);
            if (want) {
                assert.equal(group[n].aria, 'true', `${label}: ${n} is aria-disabled`);
                assert.equal(group[n].disabled, false, `${label}: ${n} stays clickable so the click can say why`);
                assert.match(group[n].title, new RegExp(`^Blocked: Box One needs ${tier}`), `${label}: ${n} title gives the reason (${group[n].title})`);
            } else {
                assert.ok(!/^Blocked:/.test(group[n].title), `${label}: ${n} has no refusal title`);
            }
        }
    };
    const INTERACT_PANE = ['cmd-send', 'cmd-send-menu', 'cmd-raw-keys', 'cmd-macro'];
    const INTERACT_ROW = ['agent-kill', 'agent-rename', 'agent-sleep'];
    const NEVER_ROW = ['agent-info', 'agent-move'];
    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setBypassServiceWorker({ bypass: true });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.addScriptToEvaluateOnNewDocument({ source: HUB_PRELOAD });
        await cloudMode(true);
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('bc-awake', 'A', 3, { sleeping: false, server: 'm1' }); window.__m.add('bc-sleep', 'S', 3, { sleeping: true, server: 'm1' }); T.render.renderLiveSessions(T.state.liveSessions); true`);
        await ev(`window.selectLiveSession('bc-awake', 'claude', 'bc-awake-id', 'm1'); true`);
        await waitFor(`!!document.querySelector('#command-pane [data-on-click=cmd-send]')`, 'the composer', 8000);

        console.log('1. View: everything that needs Interact or Launch is dimmed with its reason');
        await waitFor(`!!document.querySelector('#command-pane [data-on-click=cmd-send].hub-blocked')`, 'Send dimmed at View', 6000);
        await waitFor(`!!document.querySelector('#live-sessions-list [data-session-id="bc-awake-id"] [data-on-click=agent-kill].hub-blocked')`, 'Kill dimmed at View', 6000);
        let s = await state();
        blockedAs(s.pane, INTERACT_PANE, true, 'Interact', 'View pane');
        blockedAs(s.row, INTERACT_ROW, true, 'Interact', 'View row');
        blockedAs(s.row, ['agent-restart'], true, 'Launch', 'View row');
        blockedAs(s.row, NEVER_ROW, false, '', 'View row (reads or rearranges)');
        console.log(`   ✓ pane ${INTERACT_PANE.join(', ')}; row ${INTERACT_ROW.join(', ')} (Interact) and agent-restart (Launch); Session Info and Move not dimmed`);

        console.log('2. Interact: only the Launch controls stay dimmed');
        await access('interact');
        await waitFor(`!!document.querySelector('#live-sessions-list [data-session-id="bc-awake-id"] [data-on-click=agent-restart].hub-blocked')`, 'Restart dimmed at Interact', 6000);
        await marked();
        s = await state();
        blockedAs(s.pane, INTERACT_PANE, false, '', 'Interact pane');
        blockedAs(s.row, INTERACT_ROW, false, '', 'Interact row');
        blockedAs(s.row, ['agent-restart'], true, 'Launch', 'Interact row');
        console.log('   ✓ Send, keys, macros, Kill, Rename, Sleep normal; Restart still says it needs Launch');

        console.log('3. Launch: nothing is dimmed');
        await access('launch');
        await marked();
        s = await state();
        for (const g of [s.pane, s.row]) assert.deepEqual(Object.entries(g).filter(([, v]) => v.blocked).map(([k]) => k), [], 'nothing dimmed at Launch');
        console.log('   ✓ no dimmed control');

        console.log('4. The sleeping bar: Wake and Restart need Launch, Unarchive needs Interact');
        await access('view');
        await ev(`window.selectLiveSession('bc-sleep', 'claude', 'bc-sleep-id', 'm1'); true`);
        await waitFor(`!!document.querySelector('#sleeping-composer [data-action=wake].hub-blocked')`, 'Wake dimmed at View', 8000);
        s = await state();
        blockedAs(s.bar, ['wake', 'restart'], true, 'Launch', 'sleeping bar');
        assert.equal(s.bar.unarchive.blocked, true, 'Unarchive is dimmed at View too');
        assert.match(s.bar.unarchive.title, /needs Interact/, 'and says Interact');
        await access('interact');
        await marked();
        s = await state();
        assert.equal(s.bar.wake.blocked, true, 'Wake still needs Launch at Interact');
        console.log('   ✓ Wake / Restart: Launch; Unarchive: Interact');

        console.log('5. A local dashboard is unchanged');
        await cloudMode(false);
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__mode = 'view'; window.__m.add('bc-awake', 'A', 3, { sleeping: false }); T.render.renderLiveSessions(T.state.liveSessions); window.selectLiveSession('bc-awake', 'claude', 'bc-awake-id', 'local'); true`);
        await waitFor(`!!document.querySelector('#command-pane [data-on-click=cmd-send]')`, 'the local composer', 8000);
        await ev(`new Promise(r => setTimeout(r, 600))`); // sleep-ok: the marking runs 120 ms after a redraw; nothing may appear
        assert.equal(await ev(`document.querySelectorAll('.hub-blocked').length`), 0, 'nothing dimmed on a local dashboard');
        console.log('   ✓ no dimmed control locally');

        console.log('\nAll hub blocked-control checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
