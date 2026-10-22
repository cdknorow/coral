// Mobile a11y (#3073, #3098; Coral Hub mobile audit F2 + F4):  run via run_cdp_suites.mjs
// F4: on a coarse pointer (phone 390px, iPad 820px with touch) every visible
//     control is at least 44x44 CSS px; a fine pointer keeps the desktop sizes.
// F2: the expanded Agent UI panel is a named dialog with a 44px Close button;
//     Close and Escape both close it and return focus to the Expand button.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const { framesJs } = require('./helpers/wait');
const sleep = ms => new Promise(r => setTimeout(r, ms));

// Visible, on-screen interactive elements smaller than 44px in either dimension.
const UNDERSIZED = `(() => {
    const out = [];
    for (const el of document.querySelectorAll('button, a[href], [role=button], input:not([type=hidden]), select, summary, [onclick], [data-on-click]')) {
        // A checkbox or radio inside its label is tapped through the label.
        const hit = el.matches('input[type=checkbox], input[type=radio]') && el.closest('label') ? el.closest('label') : el;
        const r = hit.getBoundingClientRect(); const cs = getComputedStyle(el);
        if (!r.width || !r.height || cs.visibility === 'hidden' || cs.display === 'none') continue;
        if (r.bottom < 0 || r.top > innerHeight || r.right < 0 || r.left > innerWidth) continue;
        if (r.width >= 44 && r.height >= 44) continue;
        out.push(Math.round(r.width) + 'x' + Math.round(r.height) + ' ' + (el.id ? '#' + el.id : el.className) + ' [' + (el.getAttribute('aria-label') || el.title || el.textContent.trim()).slice(0, 30) + ']');
    }
    return out;
})()`;

(async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-touch-test-'));
    const bin = path.join(dir, 'coral-agent');
    execFileSync('go', ['build', '-o', bin, './cmd/coral-agent'], { cwd: path.resolve(__dirname, '../../coral-go') });
    const api = async (route, method = 'GET', body) => {
        const r = await fetch(BASE + route, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
        if (!r.ok) throw new Error(`${r.status}: ${await r.text()}`);
        return r.json();
    };
    const launch = await api('/api/sessions/launch', 'POST', { working_dir: dir, agent_type: 'terminal', display_name: 'Touch QA' });
    const sid = launch.session_id;
    const sname = launch.session_name || launch.name || '';
    const env = { ...process.env, CORAL_URL: BASE, CORAL_SESSION_NAME: 'terminal-' + sid }; delete env.TMUX;
    // A repository with one changed file, so the Files tab lists a row (with its star).
    fs.writeFileSync(path.join(dir, '.gitignore'), 'coral-agent\npanel.html\n');
    fs.writeFileSync(path.join(dir, 'notes.md'), 'one\n');
    const git = (...a) => execFileSync('git', ['-c', 'user.email=t@t', '-c', 'user.name=t', ...a], { cwd: dir, stdio: 'ignore' });
    git('init', '-q'); git('add', '.'); git('commit', '-qm', 'init');
    fs.writeFileSync(path.join(dir, 'notes.md'), 'one\ntwo\n');
    const panelFile = path.join(dir, 'panel.html');
    fs.writeFileSync(panelFile, '<p>touch panel</p>');
    execFileSync(bin, ['ui', 'publish', '--id', 'touch', '--title', 'Touch Panel', '--file', panelFile], { env, encoding: 'utf8' });

    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Input, Fetch, Network } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    // Condition waits (#3118).
    const { waitFor, waitStable, nextFrame, coralReady } = require('./helpers/wait').bind(ev);
    const open = async (width, height, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`new Promise(r => { const w = () => typeof window.selectLiveSession === 'function' ? r() : setTimeout(w, 50); w(); })`);
        await ev(`new Promise(r => { const t = Date.now(); const w = () => (window._coralGetLiveSessions?.() || []).some(s => s.session_id === ${JSON.stringify(sid)}) || Date.now() - t > 8000 ? r() : setTimeout(w, 100); w(); })`);
        await coralReady();
        await ev(`window.selectLiveSession(${JSON.stringify(sname)}, 'terminal', ${JSON.stringify(sid)})`);
        // The sweep needs the agent's view on screen, not its terminal attached (which can take longer than the
        // hook's 10 s on a throttled shard): wait for the header, and take the attach if it comes.
        await waitFor(`import((window.__coralStatic||'/static/')+'state.js').then(m => m.state.currentSession?.session_id === ${JSON.stringify(sid)}
            && (document.getElementById('session-name') || {}).textContent?.trim().length > 0)`, 'the agent header for the selected agent');
        await coralReady('agentSelected', sname).catch(() => {});
        // The sweep measures every control: go on once the layout holds still.
        await waitStable(`document.body.getBoundingClientRect().height + ':' + document.querySelectorAll('button, a[href], input, select').length`, 'the layout', 200).catch(() => {});
    };
    const coarse = async on => {
        await Emulation.setEmulatedMedia({ features: on ? [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] : [{ name: 'pointer', value: 'fine' }, { name: 'hover', value: 'hover' }] });
        await Emulation.setTouchEmulationEnabled({ enabled: on, maxTouchPoints: on ? 5 : 1 });
    };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);

        // F4: coarse pointer, phone and iPad.
        await coarse(true);
        await open(390, 844, true);
        assert.ok(await ev(`!!document.querySelector('.mobile-send-btn')`), 'phone view has the composer (a session is selected)');
        assert.deepEqual(await ev(UNDERSIZED), [], 'phone 390: every control is at least 44x44');
        assert.ok(await ev(`document.documentElement.scrollWidth <= innerWidth`), 'phone 390: no horizontal overflow');
        console.log('PASS phone 390 coarse: no control under 44px, no overflow');

        // #4976: the Agent Team dialog's member cards (move up and down, edit, remove) and its Back arrow.
        await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`);
        await waitFor(`!!document.getElementById('team-template-selector') && getComputedStyle(document.getElementById('launch-step-team')).display !== 'none'`, 'the team step');
        await ev(`window._addTeamAgent('Amy', 'First.'); window._addTeamAgent('Bo', 'Second.'); true`); // the dialog starts with no members (#5989)
        await waitFor(`!!document.querySelector('#team-agents-list .team-agent-row')`, 'the team dialog with its member cards');
        const teamSmall = await ev(UNDERSIZED);
        assert.deepEqual(teamSmall, [], 'phone 390, Agent Team dialog: every control, the cards\' buttons too, is at least 44x44: ' + JSON.stringify(teamSmall));
        const cardButtons = await ev(`[...document.querySelectorAll('#team-agents-list .team-agent-row:first-child .team-agent-summary-actions > button')].map(b => { const r = b.getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; })`);
        assert.equal(cardButtons.length, 4, 'a card has move up, move down, edit and remove');
        const gap = await ev(`(() => { const b = document.querySelector('#launch-step-team .launch-back-btn').getBoundingClientRect(), t = document.querySelector('#launch-team-title').firstChild; const range = document.createRange(); const txt = [...document.querySelector('#launch-team-title').childNodes].find(n => n.nodeType === 3 && n.textContent.trim()); range.selectNodeContents(txt); const r = range.getBoundingClientRect(); return Math.round(r.left - b.right); })()`);
        assert.ok(gap >= 6, 'the Back arrow\'s focus ring has room before the title: ' + gap + ' px');
        await ev(`window.hideLaunchModal(); true`);
        console.log('PASS phone 390 coarse: Agent Team dialog member cards and Back arrow');

        // Round 3 (#3131): the top-bar settings dropdown's rows.
        const menuRows = sel => ev(`[...document.querySelectorAll(${JSON.stringify(sel)})].map(e => e.getBoundingClientRect()).filter(r => r.width && r.height).map(r => [Math.round(r.width), Math.round(r.height)])`);
        await ev(`window.__coral.toggleTopBarSettings()`);
        await waitFor(`[...document.querySelectorAll('#top-bar-settings-menu .overflow-menu-item')].filter(e => e.getBoundingClientRect().height).length >= 2`, 'the settings dropdown', { soft: true });
        const settingsRows = await menuRows('#top-bar-settings-menu .overflow-menu-item');
        assert.ok(settingsRows.length >= 2, 'the settings dropdown is open with its rows: ' + JSON.stringify(settingsRows));
        for (const [w, h] of settingsRows) assert.ok(w >= 44 && h >= 44, `settings dropdown row ${w}x${h} under 44px`);
        const settingsSmall = await ev(UNDERSIZED);
        assert.deepEqual(settingsSmall, [], 'phone 390 with the settings dropdown open: every control is at least 44x44: ' + JSON.stringify(settingsSmall));
        await ev(`window.__coral.closeTopBarSettings()`);
        console.log(`PASS phone 390 coarse: settings dropdown rows at least 44px (${settingsRows.length} rows)`);

        // Round 2 (#3098, Coral Hub v1.4.6 re-check): the side panel's tabs, the
        // Files list (favourite stars) and Browse (row icons) on the phone overlay.
        await ev(`toggleAgenticPanel(true)`);
        await waitFor(`(() => { const p = document.getElementById('agentic-state'); return p && !p.classList.contains('collapsed') && p.getBoundingClientRect().width > 0; })()`, 'the side panel', { timeout: 3000 });
        await waitStable(`JSON.stringify(document.getElementById('agentic-state').getBoundingClientRect())`, 'the side panel to finish opening', 150).catch(() => {});
        await ev(`switchAgenticTab('files', 'top')`);
        await ev(`new Promise(r => { const t = Date.now(); const w = () => document.querySelector('#agentic-state .file-star-btn') || Date.now() - t > 15000 ? r() : setTimeout(w, 200); w(); })`);
        assert.ok(await ev(`!!document.querySelector('#agentic-state .file-star-btn')`), 'the Files tab lists the changed file with its star');
        const filesSmall = await ev(UNDERSIZED);
        assert.deepEqual(filesSmall, [], 'phone 390, side panel Files: every control is at least 44x44: ' + JSON.stringify(filesSmall));
        await ev(`switchAgenticTab('browse', 'top')`);
        await ev(`new Promise(r => { const t = Date.now(); const w = () => document.querySelector('#file-explorer-view .explorer-action-btn') || Date.now() - t > 8000 ? r() : setTimeout(w, 100); w(); })`);
        assert.ok(await ev(`!!document.querySelector('#file-explorer-view .explorer-item')`), 'Browse lists the working directory');
        const browseSmall = await ev(UNDERSIZED);
        assert.deepEqual(browseSmall, [], 'phone 390, side panel Browse: every control is at least 44x44: ' + JSON.stringify(browseSmall));
        await ev(`toggleAgenticPanel(false)`);
        console.log('PASS phone 390 coarse: side panel tabs, Files stars and Browse controls are at least 44x44');

        // The Servers tab (the Cloud Hub's list, with its Access select).
        const serversSmall = await ev(`(async () => {
            const real = window.fetch;
            window.fetch = (u, o) => String(u).startsWith('/api/servers')
                ? Promise.resolve(new Response(JSON.stringify({ servers: [{ id: 's1', label: 'Work Mac', online: true, last_seen: '2026-10-13T00:00:00Z', access_mode: 'view', local_mode: 'launch' }] }), { status: 200, headers: { 'content-type': 'application/json' } }))
                : real(u, o);
            const { showView } = await import((window.__coralStatic||'/static/')+'utils.js');
            const cs = await import((window.__coralStatic||'/static/')+'cloud_servers.js');
            const body = document.getElementById('servers-tab-body');
            showView(body.closest('.view, [id$=-view]')?.id || 'servers-view');
            await cs.renderCloudServersInto(body);
            await ${framesJs(2)};
            const hasSelect = !!body.querySelector('.cs-row select');
            const small = ${UNDERSIZED};
            cs.hideCloudServers();
            window.fetch = real;
            return { hasSelect, small };
        })()`);
        assert.ok(serversSmall.hasSelect, 'the Servers row shows its Access select');
        assert.deepEqual(serversSmall.small, [], 'phone 390, Servers tab: every control is at least 44x44: ' + JSON.stringify(serversSmall.small));
        console.log('PASS phone 390 coarse: Servers tab (Access select) at least 44x44');

        // #5053: the Cost view's tables (their sort headers and filter icons), with rows in By Model and By Thinking Level and the request table the server fills.
        const costSmall = await ev(`(async () => {
            const real = window.fetch;
            const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o) => String(u).includes('/api/token-usage/summary') ? json({ totals: { input_tokens: 10, output_tokens: 20, cost_usd: 4 }, by_agent: [],
                by_model: [{ model: 'claude-opus-5-5', agent_type: 'claude', num_sessions: 3, input_tokens: 2000, output_tokens: 300, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 2.5 }],
                by_thinking: [{ agent_type: 'claude', thinking: 'high', input_tokens: 2000, output_tokens: 300, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 4000, cost_usd: 2.0, num_calls: 9 },
                    { agent_type: 'agy', thinking: '', input_tokens: 100, output_tokens: 10, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, cost_usd: 0.25, num_calls: 0 }] })
                : real(u, o);
            window.switchNavTab('tokens');
            const t0 = Date.now();
            while (document.querySelectorAll('#cost-by-thinking tbody tr').length < 2 && Date.now() - t0 < 10000) await new Promise(r => setTimeout(r, 100));
            await ${framesJs(2)};
            const heads = document.querySelectorAll('#cost-by-thinking th .cost-sort-btn').length, filters = document.querySelectorAll('.cost-table .cost-col-filter').length;
            // The sweep skips what is off screen: each table is scrolled to in turn.
            const small = new Set();
            for (const table of document.querySelectorAll('#view-cost .cost-table, #cost-view .cost-table, .cost-table')) {
                table.scrollIntoView({ block: 'center' });
                await ${framesJs(2)};
                for (const s of ${UNDERSIZED}) small.add(s);
            }
            window.fetch = real;
            return { heads, filters, tables: document.querySelectorAll('.cost-table').length, small: [...small] };
        })()`);
        assert.ok(costSmall.heads >= 7 && costSmall.filters >= 2 && costSmall.tables >= 3, 'the Cost view shows its sort headers and filter icons: ' + JSON.stringify(costSmall));
        assert.deepEqual(costSmall.small, [], 'phone 390, Cost tables: every control, the header buttons and filter icons too, is at least 44x44: ' + JSON.stringify(costSmall.small));
        console.log(`PASS phone 390 coarse: Cost tables (${costSmall.heads} sort headers, ${costSmall.filters} filter icons) at least 44x44`);

        // The Cloud Hub sign-in and enroll pages (templates the hub renders with
        // our stylesheets): every input and button is at least 44px tall.
        // On a coarse pointer, and on a fine one at the same width: a phone browser that reports a fine pointer (the hub's mobile run measured 38 to 40 px) gets the same 44px targets (#5637).
        for (const [coarse, page] of [[true, 'signin'], [true, 'enroll'], [false, 'signin'], [false, 'enroll']]) {
            await Emulation.setTouchEmulationEnabled({ enabled: coarse, maxTouchPoints: coarse ? 5 : 1 });
            const tpl = fs.readFileSync(path.resolve(__dirname, '../../coral-go/pkg/frontend/templates/cloud', page + '.html'), 'utf8')
                .replace(/\{\{-?\s*\$base\s*:=[^}]*\}\}/, '').replace(/\{\{\s*\$base\s*\}\}/g, '/static/')
                .replace(/<script[\s\S]*?<\/script>/g, '');
            assert.ok(!/\{\{/.test(tpl), page + ': template fully mapped');
            const { frameTree } = await Page.getFrameTree();
            await Page.setDocumentContent({ frameId: frameTree.frame.id, html: tpl });
            // Every stylesheet loaded (or failed) and the fonts in, then one laid-out frame.
            await ev(`Promise.all(Array.from(document.querySelectorAll('link[rel=stylesheet]')).map(l => l.sheet ? true : new Promise(r => { l.addEventListener('load', r, { once: true }); l.addEventListener('error', r, { once: true }); })))
                .then(() => document.fonts.ready).then(() => ${framesJs(2)}).then(() => true)`);
            // Show every step; a link inside a sentence is exempt (WCAG 2.5.8 inline
            // exception), so it is taken out of the sweep.
            const small = await ev(`(() => {
                for (const el of document.querySelectorAll('[hidden]')) el.hidden = false;
                for (const a of document.querySelectorAll('p a[href]')) if (getComputedStyle(a).display === 'inline') a.removeAttribute('href');
                return ${UNDERSIZED};
            })()`);
            assert.ok(await ev(`document.querySelectorAll('input:not([type=hidden]), .cloud-btn').length`) >= 2, page + ': the page has its inputs and buttons');
            if (page === 'signin') {
                const dv = await ev(`parseFloat(getComputedStyle(document.querySelector('.cloud-divider')).fontSize)`);
                assert.ok(dv >= 13, 'the "or" divider is at least 13px: ' + dv);
            }
            assert.deepEqual(small, [], `phone 390, ${coarse ? 'coarse' : 'fine'} pointer, cloud ${page}: every control is at least 44x44: ` + JSON.stringify(small));
        }
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        console.log('PASS phone 390, coarse and fine pointer: cloud sign-in and enroll inputs and buttons at least 44x44');

        // The Cloud Hub account dropdown (Servers / Sign out, #3131): the dashboard
        // as the hub serves it (cloud-mode boot flag, a signed-in session).
        safePaused(client, async e => {
            if (e.resourceType === 'Document') {
                const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
                const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace('"mode":"local"', '"mode":"cloud"');
                const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
                return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
            }
            return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                body: Buffer.from(JSON.stringify({ authenticated: true, email: 'touch@example.com', csrf: 'x' })).toString('base64') });
        });
        // The service worker would answer the document before Fetch sees it.
        await Network.enable(); await Network.setBypassServiceWorker({ bypass: true });
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: '*/auth/session*', requestStage: 'Request' }] });
        try {
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await waitFor(`!!document.querySelector('.cloud-account-btn')`, 'the hub account button', { soft: true, timeout: 8000 });
            const where = await ev(`({ path: location.pathname, mode: (document.getElementById('coral-boot') || {}).textContent, cloud: document.body.classList.contains('cloud-mode'), btn: !!document.querySelector('.cloud-account-btn') })`);
            assert.ok(where.btn, 'the hub account button is shown: ' + JSON.stringify(where));
            await ev(`document.querySelector('.cloud-account-btn').click()`);
            await waitFor(`[...document.querySelectorAll('#cloud-account-menu .overflow-menu-item')].filter(e => e.getBoundingClientRect().height).length >= 4`, 'the account dropdown', { soft: true });
            const accountRows = await menuRows('#cloud-account-menu .overflow-menu-item');
            assert.equal(accountRows.length, 4, 'the account dropdown shows Servers, Account, Saved for later and Sign out: ' + JSON.stringify(accountRows));
            for (const [w, h] of accountRows) assert.ok(w >= 44 && h >= 44, `account dropdown row ${w}x${h} under 44px`);
            const accountSmall = await ev(`(() => { const m = document.getElementById('cloud-account-menu'); const out = []; for (const el of m.querySelectorAll('button, a[href]')) { const r = el.getBoundingClientRect(); if (r.width && r.height && (r.width < 44 || r.height < 44)) out.push(Math.round(r.width) + 'x' + Math.round(r.height) + ' ' + el.textContent.trim()); } return out; })()`);
            assert.deepEqual(accountSmall, [], 'account dropdown: every control is at least 44x44');
            console.log('PASS phone 390 coarse: account dropdown rows (Servers, Account, Saved for later, Sign out) at least 44px');
        } finally {
            await Fetch.disable();
            await Network.setBypassServiceWorker({ bypass: false });
        }

        await open(820, 1180, false);
        assert.deepEqual(await ev(UNDERSIZED), [], 'iPad 820: every control is at least 44x44');
        await ev(`document.querySelector('.tablet-sidebar-toggle')?.click()`);
        // The sidebar slides in: wait until its row controls are on screen and have stopped moving.
        await waitStable(`JSON.stringify(Array.from(document.querySelectorAll('.sidebar-kebab-btn, .folder-copy-btn')).map(e => e.getBoundingClientRect()).filter(r => r.width && r.height).map(r => [Math.round(r.x), Math.round(r.width), Math.round(r.height)]))`,
            'the sidebar row controls', 150, { accept: v => JSON.parse(v).length >= 2 }).catch(() => {});
        const rows = await ev(`Array.from(document.querySelectorAll('.sidebar-kebab-btn, .folder-copy-btn')).map(e => e.getBoundingClientRect()).filter(r => r.width && r.height).map(r => [Math.round(r.width), Math.round(r.height)])`);
        assert.ok(rows.length >= 2, 'the sidebar shows row kebabs: ' + JSON.stringify(rows));
        for (const [w, h] of rows) assert.ok(w >= 44 && h >= 44, `sidebar row control ${w}x${h} under 44px`);
        console.log(`PASS iPad 820 coarse: no control under 44px; ${rows.length} sidebar row controls >=44`);

        // Round 3 (#3131, Coral Hub v1.4.8 re-check at iPad 768x1024): the docked
        // sidebar (Display options, Collapse, Search agents, agent rows) and the
        // home view's links.
        await open(768, 1024, false);
        await ev(`document.querySelector('.tablet-sidebar-toggle:not([hidden])')?.offsetParent && document.querySelector('.tablet-sidebar-toggle').click()`);
        await waitStable(`JSON.stringify([...document.querySelectorAll('.sidebar-collapse-btn, #agent-search-input, .session-group-item')].map(e => e.getBoundingClientRect()).map(r => [Math.round(r.x), Math.round(r.width), Math.round(r.height)]))`, 'the docked sidebar to settle', 150).catch(() => {});
        const ipadSmall = await ev(UNDERSIZED);
        assert.deepEqual(ipadSmall, [], 'iPad 768 with an agent selected: every control is at least 44x44: ' + JSON.stringify(ipadSmall));
        const agentRows = await menuRows('.session-group-item');
        assert.ok(agentRows.length >= 1, 'the sidebar lists the agent');
        for (const [w, h] of agentRows) assert.ok(h >= 44, `agent row ${w}x${h} under 44px tall`);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady();
        await waitStable(`document.body.getBoundingClientRect().height + ':' + document.querySelectorAll('button, a[href], input, select').length`, 'the home view', 200).catch(() => {});
        await ev(`document.querySelector('.tablet-sidebar-toggle:not([hidden])')?.offsetParent && document.querySelector('.tablet-sidebar-toggle').click()`);
        await waitStable(`JSON.stringify([...document.querySelectorAll('.sidebar-collapse-btn, #agent-search-input, .session-group-item')].map(e => e.getBoundingClientRect()).map(r => [Math.round(r.x), Math.round(r.width), Math.round(r.height)]))`, 'the docked sidebar to settle', 150).catch(() => {});
        const ipadHomeSmall = await ev(UNDERSIZED);
        assert.deepEqual(ipadHomeSmall, [], 'iPad 768 home view: every control is at least 44x44: ' + JSON.stringify(ipadHomeSmall));
        console.log(`PASS iPad 768 coarse: sidebar controls, ${agentRows.length} agent row(s) and the home links at least 44px`);

        // A fine pointer keeps the desktop look.
        await coarse(false);
        await open(1280, 800, false);
        const home = await ev(`(() => { const r = document.querySelector('.top-bar-btn').getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; })()`);
        assert.ok(home[0] < 44 && home[1] < 44, 'desktop (fine pointer) top-bar buttons keep their size: ' + home);
        const deskRow = await ev(`(() => { const r = document.querySelector('#live-sessions-list .session-group-item')?.getBoundingClientRect(); return r ? Math.round(r.height) : 0; })()`);
        assert.ok(deskRow > 0 && deskRow < 44, 'desktop (fine pointer) agent rows keep their compact height: ' + deskRow);
        console.log('PASS desktop fine pointer unchanged (' + home + '; agent row ' + deskRow + 'px)');

        // F2: the expanded Agent UI panel, on a touch tablet where the side panel is on screen.
        await coarse(true);
        await open(820, 1180, false);
        await ev(`(async () => { const ui = await import((window.__coralStatic||'/static/')+'agent_ui.js'); await ui.refreshAgentUI(); window.switchAgenticTab('agent-ui', 'top'); })()`);
        await waitFor(`!!document.querySelector('.agent-ui-home-row') && document.querySelector('.agent-ui-home-row').getBoundingClientRect().height > 0`, 'the Agent UI panel list', { soft: true });
        await nextFrame();
        // The panel list (home rows) is on screen before a panel is opened.
        assert.ok(await ev(`!!document.querySelector('.agent-ui-home-row') && document.querySelector('.agent-ui-home-row').getBoundingClientRect().height > 0`), 'the Agent UI panel list is shown');
        const homeSmall = await ev(UNDERSIZED);
        assert.deepEqual(homeSmall, [], 'iPad 820 with the Agent UI panel list shown: every control is at least 44x44: ' + JSON.stringify(homeSmall));
        await ev(`document.querySelector('.agent-ui-home-row').click()`);
        await waitFor(`Array.from(document.querySelectorAll('.agent-ui-card:not([hidden]) button')).some(x => x.getAttribute('aria-label') === 'Expand panel' && x.getBoundingClientRect().width > 0)`, 'the opened panel', { soft: true });
        await nextFrame();
        const panelSmall = await ev(UNDERSIZED);
        assert.deepEqual(panelSmall, [], 'iPad 820 with the Agent UI panel open: every control is at least 44x44: ' + JSON.stringify(panelSmall));
        const expandOpen = `(() => { const b = Array.from(document.querySelectorAll('.agent-ui-card:not([hidden]) button')).find(x => x.getAttribute('aria-label') === 'Expand panel'); b.focus(); b.click(); })()`;
        const expandSize = await ev(`(() => { const b = Array.from(document.querySelectorAll('.agent-ui-card:not([hidden]) button')).find(x => x.getAttribute('aria-label') === 'Expand panel'); const r = b.getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; })()`);
        assert.ok(expandSize[0] >= 44 && expandSize[1] >= 44, 'Expand is visible and at least 44x44: ' + expandSize);
        await ev(expandOpen);
        await waitFor(`!!document.getElementById('agent-ui-expanded')?.open`, 'the expanded panel', { soft: true });
        const d = await ev(`(() => {
            const dlg = document.getElementById('agent-ui-expanded'); if (!dlg) return null;
            const label = document.getElementById(dlg.getAttribute('aria-labelledby'));
            const close = dlg.querySelector('.agent-ui-expanded-close'); const r = close.getBoundingClientRect();
            return { open: dlg.open, role: dlg.getAttribute('role'), name: label && label.textContent, closeLabel: close.getAttribute('aria-label'),
                     size: [Math.round(r.width), Math.round(r.height)], focused: document.activeElement === close, frame: !!dlg.querySelector('iframe') };
        })()`);
        assert.ok(d && d.open, 'the expanded panel opens');
        assert.equal(d.role, 'dialog');
        assert.equal(d.name, 'Touch Panel', 'the dialog is named by the panel title');
        assert.equal(d.closeLabel, 'Close Touch Panel');
        assert.ok(d.size[0] >= 44 && d.size[1] >= 44, 'Close is at least 44x44: ' + d.size);
        assert.ok(d.focused, 'focus moves to Close');
        assert.ok(d.frame, 'the panel frame is in the dialog');
        const afterClose = `({ gone: !document.getElementById('agent-ui-expanded'), back: document.activeElement && document.activeElement.getAttribute('aria-label') === 'Expand panel', frameInCard: !!document.querySelector('.agent-ui-card:not([hidden]) iframe') })`;
        await ev(`document.querySelector('#agent-ui-expanded .agent-ui-expanded-close').click()`);
        await waitFor(`!document.getElementById('agent-ui-expanded')`, 'the expanded panel to close', { soft: true });
        assert.deepEqual(await ev(afterClose), { gone: true, back: true, frameInCard: true }, 'Close closes and returns focus to Expand');

        await ev(expandOpen);
        await waitFor(`!!document.getElementById('agent-ui-expanded')?.open`, 'the expanded panel', { soft: true });
        await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await waitFor(`!document.getElementById('agent-ui-expanded')`, 'the expanded panel to close', { soft: true });
        assert.deepEqual(await ev(afterClose), { gone: true, back: true, frameInCard: true }, 'Escape closes and returns focus to Expand');
        console.log('PASS F2 expanded panel: named dialog, 44px Close, Close/Escape return focus');
    } finally {
        await client.close();
        try { execFileSync(bin, ['ui', 'remove', '--id', 'touch'], { env, stdio: 'ignore' }); } catch { /* best effort: the panel it published is taken off again (#4920) */ }
        await api(`/api/sessions/live/${encodeURIComponent(sname)}/kill`, 'POST', { agent_type: 'terminal', session_id: sid }).catch(() => {});
        fs.rmSync(dir, { recursive: true, force: true });
    }
})().catch(e => {
    // One compact line, so the runner's output tail shows what differed.
    console.error('FAIL ' + e.message + (e.actual !== undefined ? ' | actual ' + JSON.stringify(e.actual) + ' | expected ' + JSON.stringify(e.expected) : ''));
    process.exitCode = 1;
});
