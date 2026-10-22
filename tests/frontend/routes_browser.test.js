// The dashboard's pages are paths (#3532): every top page loads at its path and survives a reload; a tab
// the user picks is a history entry, and Back and Forward show the page they land on; an agent, a doc, a
// board and the Chats filters are in the URL; an agent's sub-tab is a replace; old hash links become
// their paths; a malformed path goes home; a name from the URL is shown as text, never markup (coral-hub
// Security); the pop-out's URL is never rewritten.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const url = () => ev('location.pathname + location.search + location.hash');
    const tab = () => ev(`(document.querySelector('.top-nav-tab.active') || {}).id || null`);
    const shown = id => `(() => { const e = document.getElementById(${JSON.stringify(id)}); return !!e && getComputedStyle(e).display !== 'none'; })()`;
    const open = async path => {
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + path });
        await loaded;
        await coralReady();
    };
    const reload = async () => {
        const loaded = Page.loadEventFired();
        await Page.reload({});
        await loaded;
        await coralReady();
    };
    const until = (expr, what) => waitFor(expr, what, { timeout: 8000, soft: true });
    const atUrl = want => until(`location.pathname + location.search + location.hash === ${JSON.stringify(want)}`, want);
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)},
        headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} })
        .then(async r => ({ status: r.status, data: await r.json().catch(() => null) }))`);

    let sid = null, name = null;
    const boards = [];
    try {
        await Page.enable();
        await Runtime.enable();

        console.log('1. Every top page loads at its path, and a reload keeps it');
        const PAGES = [['/agents', 'nav-tab-agents'], ['/chats', 'nav-tab-history'], ['/workflows', 'nav-tab-workflows'],
            ['/jobs', 'nav-tab-jobs'], ['/analytics', 'nav-tab-tokens'], ['/docs', 'nav-tab-docs']];
        for (const [path, tabId] of PAGES) {
            await open(path);
            await until(`(document.querySelector('.top-nav-tab.active') || {}).id === ${JSON.stringify(tabId)}`, tabId);
            const first = { url: await url(), tab: await tab() };
            await reload();
            await until(`(document.querySelector('.top-nav-tab.active') || {}).id === ${JSON.stringify(tabId)}`, tabId);
            const again = { url: await url(), tab: await tab() };
            check(`${path} shows its tab, and again after a reload`, first.url === path && first.tab === tabId && again.url === path && again.tab === tabId, JSON.stringify({ first, again }));
        }
        await open('/');
        check('/ is the Agents page', (await url()) === '/agents' && (await tab()) === 'nav-tab-agents', await url());
        check('history.state is the route', JSON.stringify(await ev('history.state.coralRoute')) === JSON.stringify({ view: 'agents' }), JSON.stringify(await ev('history.state')));

        console.log('2. Tabs are history entries: Back and Forward');
        await open('/agents');
        const len0 = await ev('history.length');
        await ev(`document.getElementById('nav-tab-history').click(); true`);
        await atUrl('/chats');
        await ev(`document.getElementById('nav-tab-workflows').click(); true`);
        await atUrl('/workflows');
        check('two tab clicks are two entries', (await ev('history.length')) === len0 + 2, `${len0} -> ${await ev('history.length')}`);
        await ev('history.back(); true');
        await atUrl('/chats');
        await until(`(document.querySelector('.top-nav-tab.active') || {}).id === 'nav-tab-history'`, 'the Chats tab');
        check('Back shows Chats', (await url()) === '/chats' && (await tab()) === 'nav-tab-history', `${await url()} ${await tab()}`);
        await ev('history.back(); true');
        await atUrl('/agents');
        await until(`(document.querySelector('.top-nav-tab.active') || {}).id === 'nav-tab-agents'`, 'the Agents tab');
        check('Back again shows Agents', (await url()) === '/agents' && (await tab()) === 'nav-tab-agents', `${await url()} ${await tab()}`);
        await ev('history.forward(); true');
        await atUrl('/chats');
        await until(`(document.querySelector('.top-nav-tab.active') || {}).id === 'nav-tab-history'`, 'the Chats tab');
        check('Forward shows Chats', (await url()) === '/chats' && (await tab()) === 'nav-tab-history', `${await url()} ${await tab()}`);

        console.log('3. The Chats filters are the URL: a link, a reload, and Back from another tab');
        await open('/chats?q=routesqa');
        await until(`(document.getElementById('history-search') || {}).value === 'routesqa'`, 'the search box');
        check('/chats?q= fills the search box', (await ev(`document.getElementById('history-search').value`)) === 'routesqa' && (await url()) === '/chats?q=routesqa', await url());
        await ev(`document.getElementById('nav-tab-workflows').click(); true`);
        await atUrl('/workflows');
        await ev(`document.getElementById('history-search').value = ''; true`);
        await ev('history.back(); true');
        await atUrl('/chats?q=routesqa');
        await until(`(document.getElementById('history-search') || {}).value === 'routesqa'`, 'the search box again');
        check('Back to the filtered list restores its filter', (await ev(`document.getElementById('history-search').value`)) === 'routesqa', await url());

        console.log('4. Old hash links become their paths (replaced, not pushed)');
        for (const [hash, path, tabId] of [['#tokens', '/analytics', 'nav-tab-tokens'], ['#history', '/chats', 'nav-tab-history'],
            ['#workflows', '/workflows', 'nav-tab-workflows'], ['#agents', '/agents', 'nav-tab-agents']]) {
            await open('/' + hash);
            await until(`(document.querySelector('.top-nav-tab.active') || {}).id === ${JSON.stringify(tabId)}`, tabId);
            check(`/${hash} is ${path}`, (await url()) === path && (await tab()) === tabId, `${await url()} ${await tab()}`);
        }

        console.log('5. A malformed path goes home');
        for (const bad of ['/agents/not-a-uuid', '/team/%E2%80%AEevil', '/board/a%00b', '/docs/a/b', '/chats/a/b', '/workflows/x', '/board/%2E%2E']) {
            await open(bad);
            check(`${bad} goes to /agents`, (await url()) === '/agents' && (await tab()) === 'nav-tab-agents', await url());
        }

        console.log('6. Docs: a doc is a page; Back shows the one before');
        const docs = (await ev(`fetch('/api/agent-docs').then(r => r.json())`)) || []; // the runner syncs agent_docs
        const hooks = docs.find(d => d.name === 'hooks'), readme = docs.find(d => d.name === 'README');
        const activeDoc = `((document.querySelector('#docs-nav-list .active') || {}).textContent || '').trim()`;
        await open('/docs/hooks');
        await until(`${activeDoc} === ${JSON.stringify(hooks.title)}`, 'the hooks doc');
        check('/docs/hooks opens hooks (not README)', (await ev(activeDoc)) === hooks.title && (await url()) === '/docs/hooks', await ev(activeDoc));
        await ev(`[...document.querySelectorAll('#docs-nav-list li')].find(li => li.textContent.trim() === ${JSON.stringify(readme.title)}).click(); true`);
        await atUrl('/docs/README');
        check('choosing README is /docs/README', (await url()) === '/docs/README', await url());
        await ev('history.back(); true');
        await atUrl('/docs/hooks');
        await until(`${activeDoc} === ${JSON.stringify(hooks.title)}`, 'hooks again');
        check('Back shows hooks again', (await ev(activeDoc)) === hooks.title, await ev(activeDoc));

        console.log('7. An agent: its page, its sub-tab (a replace), a reload, Back to the list, an old #chat link');
        const launch = await api('POST', '/api/sessions/launch', { working_dir: require('node:os').tmpdir(), agent_type: 'terminal', display_name: 'Routes QA' });
        sid = launch.data && launch.data.session_id;
        name = launch.data && (launch.data.session_name || launch.data.name);
        check('a terminal launches', launch.status === 200 && !!sid, JSON.stringify(launch.data).slice(0, 160));
        const activeRow = `(document.querySelector('#live-sessions-list .session-group-item.active') || {}).dataset?.sessionId || null`;
        await open(`/agents/${sid}`);
        await until(`${activeRow} === ${JSON.stringify(sid)}`, 'the agent selected');
        check('/agents/<id> selects the agent', (await ev(activeRow)) === sid && (await ev(shown('live-session-view'))) && (await url()) === `/agents/${sid}`, await url());
        const lenA = await ev('history.length');
        await ev(`document.getElementById('agentic-tab-notes').click(); true`);
        await atUrl(`/agents/${sid}/notes`);
        check('a sub-tab is in the URL, as a replace', (await url()) === `/agents/${sid}/notes` && (await ev('history.length')) === lenA, `${await url()} ${lenA} -> ${await ev('history.length')}`);
        await reload();
        await until(`${activeRow} === ${JSON.stringify(sid)} && document.getElementById('agentic-tab-notes').classList.contains('active')`, 'the agent and its Notes tab');
        check('a reload keeps the agent and its sub-tab', (await ev(activeRow)) === sid && (await ev(`document.getElementById('agentic-tab-notes').classList.contains('active')`)), await url());
        await open('/agents');
        await ev(`document.querySelector('#live-sessions-list [data-session-id="${sid}"]').click(); true`);
        await atUrl(`/agents/${sid}`);
        check('choosing the agent in the list is its page', (await url()) === `/agents/${sid}`, await url());
        await ev('history.back(); true');
        await atUrl('/agents');
        await until(shown('welcome-screen'), 'the welcome screen');
        check('Back is the Agents page again', (await url()) === '/agents' && (await ev(shown('welcome-screen'))), await url());
        await open(`/#chat/${sid}`);
        await until(`${activeRow} === ${JSON.stringify(sid)}`, 'the agent from the old link');
        check('an old #chat/<id> link opens the agent at /agents/<id>', (await url()) === `/agents/${sid}` && (await ev(activeRow)) === sid, await url());
        console.log('7b. Pages whose object does not exist: a visible "not found", then the list (design 4.1)');
        const toast = re => `[...document.querySelectorAll('.toast')].some(t => ${re}.test(t.textContent))`;
        for (const [path, list, re] of [
            ['/agents/00000000-0000-4000-8000-000000000000', '/agents', '/not running here/'],
            ['/chats/no-such-chat-3532', '/chats', '/chat was not found/'],
            ['/workflows/987654321', '/workflows', '/workflow was not found/'],
            ['/workflows/987654321/runs/987654321', '/workflows', '/workflow run was not found/'],
            ['/docs/no-such-doc', '/docs', '/document was not found/'],
        ]) {
            await open(path);
            await atUrl(list);
            await until(toast(re), `the toast for ${path}`);
            check(`${path}: "not found", then ${list}`, (await url()) === list && (await ev(toast(re))), `${await url()} ${JSON.stringify(await ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent.trim())`))}`);
        }

        console.log('8. Phone: the agent page, and Back to the list');
        await Emulation.setDeviceMetricsOverride({ width: 375, height: 812, deviceScaleFactor: 2, mobile: true });
        await open(`/agents/${sid}`);
        await until(shown('live-session-view'), 'the agent at 375');
        check('/agents/<id> shows the agent on a phone', (await ev(shown('live-session-view'))) && (await url()) === `/agents/${sid}`, await url());
        await ev('window.mobileBack(); true');
        await atUrl('/agents');
        check('the phone back button is the Agents page', (await url()) === '/agents' && (await ev(shown('mobile-agent-list'))), await url());
        // From the list: choose the agent, Back, Forward (QA's 390 px bug: Forward left the list on screen).
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 3, mobile: true });
        await open('/agents');
        await until(shown('mobile-agent-list'), 'the phone list');
        const lenP = await ev('history.length');
        await ev(`document.querySelector('#mobile-agent-list [data-session-id="${sid}"], #live-sessions-list [data-session-id="${sid}"]').click(); true`);
        await atUrl(`/agents/${sid}`);
        await until(shown('live-session-view'), 'the agent at 390');
        check('choosing the agent on a phone is its page', (await url()) === `/agents/${sid}` && (await ev(shown('live-session-view'))), await url());
        await ev('history.back(); true');
        await atUrl('/agents');
        await until(shown('mobile-agent-list'), 'the list after Back');
        check('Back shows the list', (await ev(shown('mobile-agent-list'))) && !(await ev(shown('live-session-view'))), await url());
        await ev('history.forward(); true');
        await atUrl(`/agents/${sid}`);
        await until(shown('live-session-view'), 'the agent after Forward');
        check('Forward shows the agent again, not the list', (await ev(shown('live-session-view'))) && !(await ev(shown('mobile-agent-list'))), await url());
        await ev('window.mobileBack(); true');
        await atUrl('/agents');
        check('the phone back button goes back (no new entry)', (await url()) === '/agents' && (await ev('history.length')) === lenP + 1 && (await ev(shown('mobile-agent-list'))),
            `${await url()} length ${lenP} -> ${await ev('history.length')}`);
        await Emulation.clearDeviceMetricsOverride();

        console.log('9. A board: its page; a name from the URL is text, never markup');
        const board = 'routes-qa-3532';
        boards.push(board);
        await open(`/board/${board}`);
        await until(`(document.getElementById('messageboard-project-badge') || {}).textContent === ${JSON.stringify(board)}`, 'the board');
        check('/board/<name> opens the board', (await ev(shown('messageboard-view'))) && (await url()) === `/board/${board}`, await url());
        // A #team-view=x link in a post is never a route (Security, #3532). Since #3542 it isn't a
        // link at all: board Markdown shows it as text with its target, and clicking that leaves
        // the board's page, hash and view as they are. (nav_unit covers the router ignoring a hash.)
        await api('POST', `/api/board/${board}/subscribe`, { subscriber_id: 'Routes QA', job_title: 'QA' });
        await api('POST', `/api/board/${board}/messages`, { subscriber_id: 'Routes QA', content: 'see [the team](#team-view=evil) here' });
        await open(`/board/${board}`);
        const frag = `[...document.querySelectorAll('#messageboard-view .mb-message-body .blocked-link')].find(s => s.textContent === 'the team (#team-view=evil)')`;
        await until(`!!(${frag})`, 'the posted link, as text');
        check('a #team-view= link in a post is text, not a link', await ev(`![...document.querySelectorAll('#messageboard-view .mb-message-body a')].some(a => (a.getAttribute('href') || '').includes('team-view'))`), 'an anchor is left');
        await ev(`(${frag}).click(); true`);
        await new Promise(r => setTimeout(r, 300)); // sleep-ok: a negative check, nothing may react
        const after = await ev(`({ path: location.pathname, hash: location.hash, board: ${shown('messageboard-view')}, team: !!(document.getElementById('team-center-view') && !document.getElementById('team-center-view').hidden) })`);
        check('a #team-view= link in a post drives nothing', after.path === `/board/${board}` && !after.hash && after.board && !after.team, JSON.stringify(after));

        // A dashboard page link in a post (#3542 keeps it, rebuilt from the parsed route): a plain
        // click goes through the router, with no page load, and Back returns to the board.
        await api('POST', `/api/board/${board}/messages`, { subscriber_id: 'Routes QA', content: 'open [the chats](/chats?q=routeclick&x=1) now' });
        await open(`/board/${board}`);
        const routeLink = `[...document.querySelectorAll('#messageboard-view .mb-message-body a[data-route]')].find(a => a.textContent === 'the chats')`;
        await until(`!!(${routeLink})`, 'the route link');
        const linkHref = await ev(`(${routeLink}).getAttribute('href')`);
        await ev(`window.__samePage = true; (${routeLink}).dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })); true`);
        await atUrl('/chats?q=routeclick');
        await until(`(document.querySelector('.top-nav-tab.active') || {}).id === 'nav-tab-history'`, 'the Chats tab');
        const rc = await ev(`({ url: location.pathname + location.search, same: window.__samePage === true, box: (document.getElementById('history-search') || {}).value })`);
        check('a page link in a post opens it through the router (no page load, its own query only)', linkHref === '/chats?q=routeclick' && rc.url === '/chats?q=routeclick' && rc.same && rc.box === 'routeclick', JSON.stringify({ linkHref, ...rc }));
        await ev('history.back(); true');
        await atUrl(`/board/${board}`);
        check('and Back returns to the board', (await url()) === `/board/${board}` && (await ev('window.__samePage === true')), await url());

        // "../../api" is a legal name: encoded, it stays one segment under /api/board/.
        const climb = '../../api';
        boards.push(climb);
        await open(`/board/${encodeURIComponent(climb)}`);
        await until(`(document.getElementById('messageboard-project-badge') || {}).textContent === ${JSON.stringify(climb)}`, 'the ../../api board');
        const reqs = await ev(`performance.getEntriesByType('resource').map(r => new URL(r.name).pathname).filter(p => p.startsWith('/api/'))`);
        const outside = reqs.filter(p => /^\/api\/(subscribe|messages|api)\b/.test(p));
        check('a name with ../ stays inside /api/board/', outside.length === 0 && reqs.some(p => p.startsWith('/api/board/..%2F..%2Fapi')), JSON.stringify(outside.length ? outside : reqs.filter(p => p.startsWith('/api/board/')).slice(0, 4)));
        const evil = '<img src=x onerror="window.__routesPwn=1">';
        boards.push(evil);
        await open(`/board/${encodeURIComponent(evil)}`);
        await until(`(document.getElementById('messageboard-project-badge') || {}).textContent === ${JSON.stringify(evil)}`, 'the markup-named board');
        const xss = await ev(`({ text: document.getElementById('messageboard-project-badge').textContent, imgs: document.querySelectorAll('#messageboard-view img[src="x"], #messageboard-project-badge img').length, pwn: window.__routesPwn === 1 })`);
        check('a markup name is shown as text', xss.text === evil && xss.imgs === 0 && !xss.pwn, JSON.stringify(xss));

        console.log('10. The pop-out keeps its URL');
        await Page.navigate({ url: `${BASE}/agent/${sid}` });
        await Page.loadEventFired();
        await until(`!!document.body && document.body.classList.contains('popout-mode') && !!window.__coralReady`, 'the pop-out');
        await until(`document.readyState === 'complete'`, 'loaded');
        check('/agent/<id> is not rewritten', (await url()) === `/agent/${sid}`, await url());
    } catch (e) {
        check('no error', false, e.message);
    } finally {
        try {
            await Emulation.clearDeviceMetricsOverride().catch(() => {});
            if (sid) await api('POST', `/api/sessions/live/${encodeURIComponent(name)}/kill`, { agent_type: 'terminal', session_id: sid });
            for (const b of boards) await api('DELETE', `/api/board/${encodeURIComponent(b)}`);
        } catch { /* best effort */ }
        await client.close();
    }
    const failed = results.filter(x => !x).length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
