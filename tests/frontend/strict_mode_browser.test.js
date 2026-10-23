// Strict local auth from a person's point of view (#3527, Security's #3509 item 6): on a Coral started with
// --strict-local-auth, a browser signed in the normal way ('coral open', a single-use link) uses the whole
// dashboard: it loads (not the lock page), saves a setting, posts on a board as the Operator, lists teams,
// launches a terminal and streams it over the WebSocket, and kills it. A request without the sign-in cookie
// from Node (no cookie) is refused, so the run proves strict mode is on.
//
// run_cdp_suites.mjs runs it on its strict shard (CORAL_TEST_STRICT=1), with the tab already signed in.
// Anywhere else it has nothing to check and passes.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

if (!process.env.CORAL_TEST_STRICT) {
    console.log('skipped: not a strict-mode shard (run_cdp_suites.mjs --strict)');
    process.exit(0);
}

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
const sleep = ms => new Promise(r => setTimeout(r, ms));

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT) });
    const { Page, Runtime, Network } = client;
    // What the page got refused, and its script errors: a strict-mode break shows up here first.
    const refused = [], errors = [];
    await Network.enable();
    Network.responseReceived(e => { if (e.response.status >= 400) refused.push(`${e.response.status} ${e.response.url.replace(BASE, '')}`); });
    await Runtime.enable();
    Runtime.exceptionThrown(e => errors.push((e.exceptionDetails.exception && e.exceptionDetails.exception.description || e.exceptionDetails.text || '').split('\n')[0]));
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    // A JSON request from the page, with the browser's sign-in cookie.
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} })
        .then(async r => ({ status: r.status, data: await r.json().catch(() => null) }))`);
    let sid = null, name = null;
    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
    try {
        // Strict mode is on: no credential, no answer (Node has no cookie; the page without it neither).
        const anon = await fetch(`${BASE}/api/settings`).then(r => r.status);
        check('strict mode refuses a request with no credential', anon === 401, `status ${anon}`);
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE });
        await loaded;
        for (const end = Date.now() + 10000; Date.now() < end && !(await ev(`!!document.getElementById('live-sessions-list')`)); await sleep(100)) { /* loading */ }
        const pageInfo = () => ev(`({ url: location.href.replace(location.origin, ''), title: document.title, text: document.body.innerText.slice(0, 160).replace(/\\s+/g, ' '),
            api: performance.getEntriesByType('resource').filter(r => /\\/(api|ws)\\//.test(r.name)).length })`);
        const page = await ev(`({ list: !!document.getElementById('live-sessions-list'), locked: /Coral is locked|Sign in to Coral/i.test(document.body.innerText) })`);
        check('the signed-in dashboard loads (not the lock page)', page.list && !page.locked, JSON.stringify(page));
        // (No cookie-less request from the page itself: the dashboard sends the tab to /auth on any 401.)

        // The live feed (/ws/coral): the agent list, its states and the team badges all come through it.
        const feed = await ev(`new Promise(res => {
            // The dashboard's own connection asks for field diffs (websocket.js).
            const w = new WebSocket(location.origin.replace(/^http/, 'ws') + '/ws/coral?diff=fields');
            const t = setTimeout(() => { const st = w.readyState; try { w.close(); } catch {} res({ state: st, message: false }); }, 6000);
            w.onmessage = m => { clearTimeout(t); w.close(); let type = null; try { type = JSON.parse(m.data).type; } catch {} res({ state: 1, message: true, type, head: String(m.data).slice(0, 160) }); };
            w.onclose = e => { clearTimeout(t); res({ state: 3, message: false, code: e.code, reason: e.reason }); };
        })`);
        check('the live feed WebSocket connects and sends', feed.message, JSON.stringify(feed));

        // A 401 that doesn't mean signed out (an agent-only route answers session_token_required to the
        // dashboard) stays the caller's: the tab is NOT sent to /auth (#3528).
        const pageBefore = await ev(`location.pathname`); // the dashboard's page (/agents since #3532)
        const agentOnly = await api('GET', '/api/agent/tasks?session_id=strict-qa');
        await sleep(500); // sleep-ok: proves a negative (no redirect to /auth follows the 401)
        const stayed = await ev(`location.pathname`);
        check('a 401 that is not a sign-out leaves the page where it is', agentOnly.status === 401 && stayed === pageBefore && stayed !== '/auth', JSON.stringify({ status: agentOnly.status, code: agentOnly.data && (agentOnly.data.code || agentOnly.data.error), path: stayed, before: pageBefore }));

        // Settings: read, save, read back, restore.
        const before = await api('GET', '/api/settings');
        const prev = String((before.data && (before.data.settings || before.data)).show_session_summary ?? '');
        const put = await api('PUT', '/api/settings', { show_session_summary: true });
        const after = await api('GET', '/api/settings');
        const now = String((after.data && (after.data.settings || after.data)).show_session_summary ?? '').toLowerCase();
        check('a setting saves', before.status === 200 && put.status === 200 && now === 'true', JSON.stringify({ get: before.status, put: put.status, now }));
        await api('PUT', '/api/settings', { show_session_summary: prev.toLowerCase() === 'true' });

        // A board: the Operator subscribes and posts, as the board view does.
        const board = 'strict-qa-3527';
        const sub = await api('POST', `/api/board/${board}/subscribe`, { subscriber_id: 'Operator', job_title: 'Operator' });
        const post = await api('POST', `/api/board/${board}/messages`, { subscriber_id: 'Operator', as: 'Operator', content: 'strict mode check' });
        const msgs = await api('GET', `/api/board/${board}/messages/all`);
        const seen = Array.isArray(msgs.data) ? msgs.data : (msgs.data && msgs.data.messages) || [];
        check('the Operator posts on a board', sub.status < 300 && post.status < 300 && seen.some(m => m.content === 'strict mode check'),
            JSON.stringify({ subscribe: sub.status, post: post.status, list: msgs.status, n: seen.length }));
        await api('DELETE', `/api/board/${board}`);

        const teams = await api('GET', '/api/teams/all');
        check('the team list loads', teams.status === 200, `status ${teams.status}`);

        // A terminal: launch it, see it in the agent list, stream it over the WebSocket, kill it.
        const launch = await api('POST', '/api/sessions/launch', { working_dir: require('node:os').tmpdir(), agent_type: 'terminal', display_name: 'Strict QA' });
        sid = launch.data && launch.data.session_id;
        name = launch.data && (launch.data.session_name || launch.data.name);
        check('a terminal launches', launch.status === 200 && !!sid, JSON.stringify(launch.data).slice(0, 160));
        let listed = false;
        for (const end = Date.now() + 10000; !listed && Date.now() < end; await sleep(100)) {
            listed = await ev(`!!document.querySelector('#live-sessions-list [data-session-id="${sid}"]')`);
        }
        const why = listed ? undefined : JSON.stringify(await ev(`fetch('/api/sessions/live').then(r => r.json()).then(d => ({
            api: (Array.isArray(d) ? d : d.sessions || []).some(x => x.session_id === ${JSON.stringify(sid)}),
            rows: [...document.querySelectorAll('#live-sessions-list [data-session-id]')].map(e => e.dataset.sessionId).slice(0, 5),
            empty: (document.getElementById('live-sessions-list') || {}).innerText?.slice(0, 120) }))
            .then(async o => { try { const st = (await import((window.__coralStatic || '/static/') + 'state.js')).state;
                return { ...o, live: (st.liveSessions || []).map(x => x.session_id).slice(0, 5), hub: st.hub, cloud: st.cloud, view: document.querySelector('.top-nav-tab.active')?.id }; } catch (e) { return { ...o, stateErr: String(e) }; } })`));
        check('it shows in the agent list', listed, why && `${why} refused=${JSON.stringify(refused.slice(-8))} errors=${JSON.stringify(errors.slice(-4))}`);
        if (!listed) {
            // Is it the live update, or the list itself? A reload loads the list from scratch.
            const reloaded = Page.loadEventFired();
            await Page.reload({});
            await reloaded;
            let afterReload = false;
            for (const end = Date.now() + 10000; !afterReload && Date.now() < end; await sleep(100)) {
                afterReload = await ev(`!!document.querySelector('#live-sessions-list [data-session-id="${sid}"]')`);
            }
            console.log(`  after a reload the terminal is ${afterReload ? '' : 'STILL NOT '}in the list`);
            console.log(`  page after the reload: ${JSON.stringify(await pageInfo())}`);
        }
        const ws = await ev(`new Promise(res => {
            const w = new WebSocket(location.origin.replace(/^http/, 'ws') + '/ws/terminal/' + encodeURIComponent(${JSON.stringify(name)}) + '?agent_type=terminal&session_id=' + ${JSON.stringify(sid)});
            const t = setTimeout(() => { try { w.close(); } catch {} res({ open: w.readyState === 1, data: false }); }, 8000);
            w.onmessage = () => { clearTimeout(t); w.close(); res({ open: true, data: true }); };
            w.onerror = () => { clearTimeout(t); res({ open: false, data: false, error: true }); };
        })`);
        check('its terminal streams over the WebSocket', ws.data, JSON.stringify(ws));
        const kill = await api('POST', `/api/sessions/live/${encodeURIComponent(name)}/kill`, { agent_type: 'terminal', session_id: sid });
        check('it is killed', kill.status === 200, `status ${kill.status}`);
        sid = null;
    } catch (e) {
        check('no error', false, e.message);
    } finally {
        if (sid) await api('POST', `/api/sessions/live/${encodeURIComponent(name)}/kill`, { agent_type: 'terminal', session_id: sid }).catch(() => {});
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
