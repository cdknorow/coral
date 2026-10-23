// The first click on something that needs a lazy module (a task row, a team's button) shows it is working when the link is slow (#5377, UI/UX's note on #5364): after 150 ms the clicked control
// is marked busy (aria-busy, .lazy-loading: a progress cursor, no dimming) until the module has loaded; on a fast link it never flickers; nothing moves and the busy state ends when the load fails. Tested at a throttled
// network (CDP Network.emulateNetworkConditions: every request waits 900 ms) against the isolated dev server, with a bundled page (the lazy modules are separate requests).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const SLOW = { offline: false, latency: 900, downloadThroughput: 200 * 1024, uploadThroughput: 200 * 1024 };

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Network } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a link's slowness is the thing under test; it has no completion event
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        // a control that runs the task-open action, as a task row does, in a list that is on the page
        await ev(`(() => { const row = document.createElement('div'); row.id = 'lb-row'; row.className = 'task-row'; row.dataset.onClick = 'task-open'; row.dataset.taskId = '1'; row.dataset.kind = 'board'; row.textContent = 'a task'; row.tabIndex = 0; document.body.appendChild(row);
            window.__flicker = []; new MutationObserver(() => { const r = document.getElementById('lb-row'); window.__flicker.push(r.hasAttribute('aria-busy') ? 'busy' : 'idle'); }).observe(document.getElementById('lb-row'), { attributes: true, attributeFilter: ['aria-busy'] }); return true; })()`);
        const state = () => ev(`(() => { const r = document.getElementById('lb-row'); return { busy: r.getAttribute('aria-busy'), cls: r.classList.contains('lazy-loading'), cursor: getComputedStyle(r).cursor, opacity: getComputedStyle(r).opacity }; })()`);

        console.log('1. a slow link: busy after 150 ms, until the module is there');
        await Network.enable(); await Network.emulateNetworkConditions(SLOW); await Network.setCacheDisabled({ cacheDisabled: true });
        await ev(`document.getElementById('lb-row').click(); true`);
        const early = await state();
        check(`at once, nothing shows (${JSON.stringify(early)})`, early.busy === null && !early.cls);
        await settle(400);
        const mid = await state();
        check(`400 ms in, the row says it is working: aria-busy and a progress cursor, and its text is not dimmed (${JSON.stringify(mid)})`, mid.busy === 'true' && mid.cls && mid.cursor === 'progress' && mid.opacity === '1');
        await waitFor(`!document.getElementById('lb-row').hasAttribute('aria-busy') && !!document.querySelector('#task-detail-modal, .task-detail-modal, [id*=task-detail]')`, 'the module to load and the dialog to open', 40000);
        const after = await state();
        check(`when the module has loaded the row is as it was (${JSON.stringify(after)})`, after.busy === null && !after.cls && after.opacity === '1');
        await ev(`window.hideTaskDetailModal && window.hideTaskDetailModal(); true`);

        console.log('2. a fast link, the module already there: it never shows');
        await Network.emulateNetworkConditions({ offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 }); await Network.setCacheDisabled({ cacheDisabled: false });
        await ev(`window.__flicker.length = 0; document.getElementById('lb-row').click(); true`); await settle(500);
        check(`no busy state at all (${JSON.stringify(await ev('window.__flicker'))})`, (await ev('window.__flicker.filter(x => x === "busy").length')) === 0);
        await ev(`window.hideTaskDetailModal && window.hideTaskDetailModal(); true`);

        console.log('3. a load that fails leaves nothing stuck, and a slow retry shows busy again');
        await ev(`import(${S} + 'lazy_panels.js').then(m => { m.loadTeamContext.module = null; return true; })`);
        await Network.emulateNetworkConditions({ offline: true, latency: 0, downloadThroughput: 0, uploadThroughput: 0 });
        await ev(`(() => { const b = document.createElement('button'); b.id = 'lb-team'; b.dataset.onClick = 'team-name-open'; b.dataset.board = 'lb-board'; b.textContent = 'team'; document.body.appendChild(b); b.click(); return true; })()`);
        await settle(600);
        check(`offline: the load fails at once, so nothing was ever shown and nothing is stuck (${await ev(`document.getElementById('lb-team').getAttribute('aria-busy')`)})`, (await ev(`document.getElementById('lb-team').getAttribute('aria-busy')`)) === null && (await ev(`!document.getElementById('lb-team').classList.contains('lazy-loading')`)));
        await Network.emulateNetworkConditions(SLOW); await Network.setCacheDisabled({ cacheDisabled: true });
        await ev(`document.getElementById('lb-team').click(); true`); await settle(400);
        const retry = await ev(`document.getElementById('lb-team').getAttribute('aria-busy')`);
        await Network.emulateNetworkConditions({ offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });
        await waitFor(`!document.getElementById('lb-team').hasAttribute('aria-busy')`, 'the busy state to end', 40000);
        check(`the retry on a slow link shows busy (${retry}) and ends it when the module is there`, retry === 'true' && (await ev(`!document.getElementById('lb-team').classList.contains('lazy-loading')`)));
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await client.close(); process.exit(failed ? 1 : 0);
    } catch (e) { console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})();
