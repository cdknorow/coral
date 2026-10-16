// Panels that load on first use (#3083 phase 2): Workflows, the token/cost dashboard, Docs, the theme
// editor, the template browser and chat search. Pins: a cold dashboard requests none of them; each module
// is fetched once and its window stubs become the real functions; a tab switch during the import keeps the
// newer choice; a failed import shows a toast and the next use fetches again (?retry=N) and works; inline
// handlers that call a not-yet-loaded panel (selectWorkflow / selectWorkflowRun) work on the first click.
//
// Module requests are watched (Network) and failed or held (Fetch) through CDP.
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

const MODULES = ['workflows.js', 'cost_dashboard.js', 'docs.js', 'theme_config.js', 'template_browser.js', 'chat_search.js'];

(async () => {
    const { client, Page, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const { Network, Fetch } = client;

    const seen = [];
    // Condition waits (#3118). quietSeen(): no new intercepted request for 300 ms; waitForSeen(): until one matches.
    const { nextFrame, coralReady } = require('./helpers/wait').bind(ev);
    const quietSeen = async (ms = 300) => { let n = seen.length, since = Date.now(); const end = since + 8000;
        while (Date.now() - since < ms && Date.now() < end) { await sleep(20); if (seen.length !== n) { n = seen.length; since = Date.now(); } } };
    const waitForSeen = async (pred, what, ms = 8000) => { const end = Date.now() + ms; while (Date.now() < end && !seen.some(pred)) await sleep(20); };
    let plan = {};
    const held = [];
    const modOf = url => { const m = url.match(/\/static\/(?:b\/[^/]+\/)?([a-z_]+\.js)(\?[^#]*)?$/); return m ? m[1] + (m[2] || '') : null; };
    Network.requestWillBeSent(e => { const m = modOf(e.request.url); if (m) seen.push(m); else if (/\/api\/workflows/.test(e.request.url)) seen.push('API ' + e.request.url.replace(/^.*\/api\//, '/api/')); });
    Fetch.requestPaused(async e => {
        const m = modOf(e.request.url) || '';
        const base = m.replace(/\?.*$/, '');
        const p = plan[base];
        if (p === 'fail') { delete plan[base]; return Fetch.failRequest({ requestId: e.requestId, errorReason: 'Failed' }); }
        if (p === 'hold') { held.push(e.requestId); return; }
        return Fetch.continueRequest({ requestId: e.requestId });
    });
    const release = async () => { while (held.length) await Fetch.continueRequest({ requestId: held.shift() }); };
    const count = name => seen.filter(x => x === name || x.startsWith(name + '?')).length;
    const fresh = async () => {
        seen.length = 0; plan = {};
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await coralReady();
    };
    const visible = id => ev(`(() => { const v = document.getElementById(${JSON.stringify(id)}); return !!v && getComputedStyle(v).display !== 'none'; })()`);
    const toasts = () => ev(`[...document.querySelectorAll('.toast')].map(t => (t.querySelector('.toast-message') || t).textContent.trim())`);

    try {
        await Promise.all([Page.enable(), Network.enable()]);
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Network.setBypassServiceWorker({ bypass: true });
        await Fetch.enable({ patterns: MODULES.map(m => ({ urlPattern: `*/${m}*`, requestStage: 'Request' })) });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });

        console.log('1. A cold dashboard loads none of the six panels; their window functions are stubs');
        await fresh();
        await quietSeen(); // startup is done (fresh); no panel request follows
        for (const m of MODULES) assert.equal(count(m), 0, `${m} not requested on a cold load (${JSON.stringify(seen.filter(x => !x.startsWith('API')).slice(0, 80))})`);
        const stubs = await ev(`['showWorkflowsTab','selectWorkflow','selectWorkflowRun','showCostDashboard','showDocsTab','showThemeConfigurator','showTemplateBrowser','searchChats'].filter(n => typeof window[n] !== 'function')`);
        assert.deepEqual(stubs, [], 'every lazy window function exists (as a stub) for the actions that call it');
        console.log('   ✓ 0 requests for the 6 modules; stubs in place');

        console.log('2. Each panel loads once on first use; stubs become the real functions');
        for (const [tab, mod, view] of [['workflows', 'workflows.js', 'workflows-view'], ['docs', 'docs.js', 'docs-view'], ['tokens', 'cost_dashboard.js', 'cost-dashboard-view']]) {
            const stubBefore = await ev(`String(window[${JSON.stringify({ workflows: 'showWorkflowsTab', docs: 'showDocsTab', tokens: 'showCostDashboard' }[tab])}])`);
            await ev(`window.switchNavTab(${JSON.stringify(tab)}); true`);
            await waitFor(`(() => { const v = document.getElementById(${JSON.stringify(view)}); return !!v && getComputedStyle(v).display !== 'none'; })()`, `${view} after switching to ${tab}`, 8000);
            assert.equal(count(mod), 1, `${mod} fetched once`);
            const after = await ev(`String(window[${JSON.stringify({ workflows: 'showWorkflowsTab', docs: 'showDocsTab', tokens: 'showCostDashboard' }[tab])}])`);
            assert.notEqual(after, stubBefore, `${tab}: the window function is now the module's own`);
            await ev(`window.switchNavTab('agents'); true`); await waitFor(`document.querySelector('.top-nav-tab.active')?.id === 'nav-tab-agents'`, 'the Agents tab', 3000);
            await ev(`window.switchNavTab(${JSON.stringify(tab)}); true`);
            assert.ok(await visible(view), `${tab}: shows at once the second time`);
            assert.equal(count(mod), 1, `${mod} not fetched again`);
            await ev(`window.switchNavTab('agents'); true`); await waitFor(`document.querySelector('.top-nav-tab.active')?.id === 'nav-tab-agents'`, 'the Agents tab', 3000);
        }
        await ev(`window.showThemeConfigurator(); true`);
        await waitFor(`(() => { const m = document.getElementById('theme-configurator-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`, 'theme configurator', 8000);
        await ev(`window.hideThemeConfigurator(); true`);
        await ev(`window.showTemplateBrowser(); true`);
        await waitFor(`(() => { const m = document.getElementById('template-browser-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`, 'template browser', 8000);
        await ev(`(() => { const m = document.getElementById('template-browser-modal'); if (m) m.style.display = 'none'; return true; })()`);
        await ev(`window.switchNavTab('history'); true`); await waitFor(`document.querySelector('.top-nav-tab.active')?.id === 'nav-tab-history'`, 'the History tab', 3000); await quietSeen();
        await ev(`window.searchChats('needle', 1); true`);
        await waitFor(`(() => { const c = document.getElementById('chat-search-results'); return !!c && !c.hidden; })()`, 'chat search results', 8000);
        for (const m of ['theme_config.js', 'template_browser.js', 'chat_search.js']) assert.equal(count(m), 1, `${m} fetched once`);
        await ev(`window.switchNavTab('agents'); true`);
        console.log('   ✓ workflows / docs / tokens views, theme editor, template browser, chat search: one request each, real functions after');

        console.log('3. Switching away while a panel loads keeps the newer choice');
        await fresh();
        plan['workflows.js'] = 'hold';
        await ev(`window.switchNavTab('workflows'); true`);
        for (let i = 0; i < 40 && !held.length; i++) await sleep(25);
        assert.ok(held.length > 0, 'the workflows import was requested and held');
        await ev(`window.switchNavTab('agents'); true`);
        await sleep(100);
        delete plan['workflows.js'];
        await release();
        await waitFor(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'lazy_panels.js'); return !!m.loadWorkflows.module; })()`, 'workflows module loaded', 8000);
        await nextFrame(); await quietSeen(); // the view would have switched by now
        assert.equal(await visible('workflows-view'), false, 'the workflows view did not take over');
        assert.equal(await ev(`document.querySelector('.top-nav-tab.active, [data-tab].active')?.textContent?.trim() || ''`).then(t => /workflow/i.test(t)), false, 'the Workflows tab is not marked active');
        console.log('   ✓ held import, switched to Agents, released: Workflows did not take over');

        console.log('4. A failed import shows a toast; the next use fetches again (?retry=1) and works');
        await fresh();
        plan['docs.js'] = 'fail';
        await ev(`window.switchNavTab('docs'); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Could not load Docs/.test(t.textContent))`, 'the Docs failure toast', 8000);
        const t = (await toasts()).find(x => /Could not load Docs/.test(x));
        assert.match(t, /^Could not load Docs\. Check the connection to Coral, then try again\./);
        assert.equal(await visible('docs-view'), false, 'no Docs view after a failed load');
        await ev(`window.switchNavTab('agents'); true`); await sleep(100);
        await ev(`window.switchNavTab('docs'); true`);
        await waitFor(`(() => { const v = document.getElementById('docs-view'); return !!v && getComputedStyle(v).display !== 'none'; })()`, 'Docs after the retry', 8000);
        assert.ok(seen.includes('docs.js?retry=1'), `the retry imported docs.js?retry=1 (${JSON.stringify(seen.filter(x => x.startsWith('docs')))})`);
        // A failed panel opened through an inline-handler stub retries the same way.
        plan['theme_config.js'] = 'fail';
        await ev(`window.showThemeConfigurator(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Could not load the theme editor/.test(t.textContent))`, 'theme editor failure toast', 8000);
        await ev(`window.showThemeConfigurator(); true`);
        await waitFor(`(() => { const m = document.getElementById('theme-configurator-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`, 'theme editor after retry', 8000);
        assert.ok(seen.includes('theme_config.js?retry=1'), 'theme editor retried with ?retry=1');
        await ev(`window.hideThemeConfigurator(); true`);
        console.log('   ✓ Docs + theme editor: toast, then ?retry=1 → works');

        // Since #3645 a control names an action; the Jobs view's workflow and run links are the real ones
        // that call into the workflows panel before it has loaded (scheduler.js loads up front).
        console.log('5. Actions into a not-yet-loaded panel work on the first click');
        await fresh();
        await ev(`(() => {
            const host = document.createElement('div'); host.id = 'qa-inline';
            host.innerHTML = '<a href="#" id="qa-wf" data-on-click="job-workflow" data-workflow-id="7">wf</a> <button id="qa-run" data-on-click="job-run" data-run-id="9">run</button>';
            document.body.appendChild(host); return true;
        })()`);
        await ev(`document.getElementById('qa-wf').click(); true`);
        await waitFor(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'lazy_panels.js'); return !!m.loadWorkflows.module; })()`, 'workflows loaded by an action', 8000);
        await waitForSeen(x => /^API \/api\/workflows\/7\b/.test(x), 'selectWorkflow(7) after loading'); await quietSeen();
        assert.equal(count('workflows.js'), 1, 'one import for the action');
        assert.ok(seen.some(x => /^API \/api\/workflows\/7\b/.test(x)), `selectWorkflow(7) ran after loading (${JSON.stringify(seen.filter(x => x.startsWith('API')))})`);
        await ev(`document.getElementById('qa-run').click(); true`);
        await waitForSeen(x => /^API \/api\/workflows\/runs\/9\b/.test(x), 'selectWorkflowRun(9)');
        assert.ok(seen.some(x => /^API \/api\/workflows\/runs\/9\b/.test(x)), `selectWorkflowRun(9) ran (${JSON.stringify(seen.filter(x => x.startsWith('API')))})`);
        assert.equal(count('workflows.js'), 1, 'still one import');
        await ev(`document.getElementById('qa-inline').remove(); true`);
        console.log('   ✓ selectWorkflow(7) and selectWorkflowRun(9) from data-on-click actions: loaded once, both ran');

        console.log('\nAll lazy panel checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
