// The workflow run page for a person using a screen reader or only the keyboard, and in the light theme (#6639): a run that is still going keeps its open Output and the focus when the page reads it again; every step says how it went in words (Passed, Failed, Stopped, Skipped) and which step it is,
// the Output buttons of two steps are not the same name twice, "View agent" is a control the keyboard reaches, and the green of a passed step, its exit code, the run's badge and the agent link hold 4.5:1 in the
// light theme (3.75 to 4.44 before). Desktop and a 390 px phone, a failed run (31) and a passed run with an agent step (32) from the census data world (helpers/census_world.js).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { DATA_WORLD } = require('./helpers/census_world');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Page.addScriptToEvaluateOnNewDocument({ source: DATA_WORLD });
        for (const [w, h, mobile, tag] of [[1280, 800, false, 'desktop'], [390, 844, true, 'phone']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
            await installAudit(ev);
            const open = async (id) => {
                await ev(`window.switchNavTab('workflows'); true`);
                await waitFor(`!!document.querySelector('.wf-card[data-on-click="wf-select"]') && document.querySelector('.wf-card[data-on-click="wf-select"]').getClientRects().length > 0`, `${tag}: the workflows`, 10000);
                await ev(`document.querySelector('.wf-card[data-on-click="wf-select"]').click(); true`);
                await waitFor(`!!document.querySelector('#wf-detail-container tr[data-run-id="${id}"]')`, `${tag}: the runs`, 10000);
                await ev(`document.querySelector('#wf-detail-container tr[data-run-id="${id}"]').click(); true`);
                await waitFor(`!!document.querySelector('#wf-run-detail-container .wf-run-step')`, `${tag}: run ${id}`, 10000);
            };
            // the failed run
            await open(31);
            const f = await ev(`(() => { const steps = [...document.querySelectorAll('#wf-run-detail-container .wf-run-step')].map(s => ({ status: (s.querySelector('.wf-run-step-status') || {}).getAttribute && s.querySelector('.wf-run-step-status').getAttribute('aria-label'), name: s.querySelector('.wf-run-step-name').textContent.replace(/\\s+/g, ' ').trim() }));
                const out = [...document.querySelectorAll('#wf-run-detail-container .wf-run-step-output-toggle')].map(b => b.getAttribute('aria-label') || b.textContent.replace(/\\s+/g, ' ').trim());
                return { steps, out }; })()`);
            assert.deepEqual(f.steps.map(s => s.status), ['Passed', 'Failed', 'Skipped'], `${tag}: each step says how it went in words (${JSON.stringify(f.steps)})`);
            assert.ok(f.steps.every((s, i) => s.name.startsWith(`Step ${i + 1}: `)), `${tag}: and which step it is (${JSON.stringify(f.steps)})`);
            assert.equal(new Set(f.out).size, f.out.length, `${tag}: no two Output buttons share a name (${JSON.stringify(f.out)})`);
            assert.ok(f.out.every(n => /^Output/.test(n)) && f.out.every(n => n.includes('Output')), `${tag}: each still contains its visible word`);
            // the passed run with an agent step
            await ev(`document.getElementById('wf-back-btn').click(); true`);
            await open(32);
            const a = await ev(`(() => { const l = document.querySelector('#wf-run-detail-container .wf-step-agent-link'); return l ? { tag: l.tagName, tab: l.tabIndex, role: l.getAttribute('role') } : null; })()`);
            assert.ok(a && (a.tag === 'BUTTON' || a.tag === 'A' || (a.role === 'button' && a.tab === 0)), `${tag}: "View agent" is a control the keyboard reaches (${JSON.stringify(a)})`);
            // light theme contrast of the run page, both runs
            for (const id of [32, 31]) {
                if (id === 31) { await ev(`document.getElementById('wf-back-btn').click(); true`); await open(31); }
                for (const theme of ['dark', 'light']) {
                    await setTheme(ev, theme);
                    const c = await auditRegion(ev, ['#wf-run-detail-container']);
                    assert.ok(c.checked > 0 && !c.fails.length, `${tag}, run ${id}, ${theme}: the run page holds 4.5:1 ` + JSON.stringify(c.fails).slice(0, 500));
                }
            }
            // a run that is still going is read again every few seconds: what the person opened stays open and focus stays where it was
            await ev(`document.getElementById('wf-back-btn').click(); true`);
            await ev(`import((window.__coralStatic || '/static/') + 'workflows.js').then(m => m.selectWorkflowRun(34)).then(() => true)`);
            await waitFor(`!!document.querySelector('#wf-run-detail-container .wf-run-step-output-toggle')`, `${tag}: the running run`, 10000);
            const tg = `document.querySelectorAll('#wf-run-detail-container .wf-run-step-output-toggle')[1]`;
            await ev(`${tg}.click(); ${tg}.focus(); true`);
            const was = await ev(`(() => { const t = ${tg}; return { expanded: t.getAttribute('aria-expanded'), controls: t.getAttribute('aria-controls') }; })()`);
            assert.equal(was.expanded, 'true', `${tag}: the Output of the running step is open`);
            await new Promise(r => setTimeout(r, 3800)); // sleep-ok: the run is read again every 3 seconds
            const now = await ev(`(() => { const t = document.querySelectorAll('#wf-run-detail-container .wf-run-step-output-toggle')[1]; return { expanded: t && t.getAttribute('aria-expanded'), focused: !!t && document.activeElement === t }; })()`);
            assert.deepEqual(now, { expanded: 'true', focused: true }, `${tag}: after the page read the run again, its Output is still open and still has focus`);
            console.log(`   ✓ ${tag}`);
        }
        console.log('PASS the workflow run page: steps say how they went, Output buttons are named apart, View agent is reachable, contrast holds in both themes');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
