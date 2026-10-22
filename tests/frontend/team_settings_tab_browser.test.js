// Team settings on a phone (#6094, from the 157 accessibility pass): with the keyboard, Tab only ever lands inside the dialog. On the Workflow, Health and Prompts tabs, 60 Tab and 60 Shift+Tab presses at 390 px
// never reach the page behind it (the Save and Edit presets buttons sit in the dialog but outside the form: they are part of the cycle, not a way out), and focus never rests on the body for two presses in a row.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const TEAM = 'tab-' + Date.now().toString(36);
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const key = async (k, code, shift) => { const m = shift ? 8 : 0; await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code: k, windowsVirtualKeyCode: code, modifiers: m }); await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k, windowsVirtualKeyCode: code, modifiers: m }); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        assert.equal(await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' }) }).then(r => r.status)`), 200);
        for (const tab of ['workflow', 'health', 'prompts']) {
            await ev(`import(${S} + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })).then(() => true)`);
            await waitFor(`!!document.querySelector('dialog[open] [data-settings-tab=${tab}]')`, `${tab}: its tab`, 10000);
            await ev(`document.querySelector('dialog[open] [data-settings-tab=${tab}]').click(); true`);
            await waitFor(`(() => { const p = document.getElementById('team-settings-${tab}'); return !!p && !p.hidden && p.getClientRects().length > 0; })()`, `${tab}: its panel shown`, 10000);
            await waitFor(`!!document.querySelector('dialog[open] form.working-mode-form')`, `${tab}: the dialog`, 10000);
            await ev(`(() => { const f = document.querySelector('dialog[open] form.working-mode-form'); const e = [...f.querySelectorAll('button, a[href], input, select, textarea, summary')].find(x => x.getClientRects().length && !x.disabled); if (e) e.focus(); return true; })()`);
            const where = () => ev(`(() => { const a = document.activeElement, d = document.querySelector('dialog[open]'); return { inside: !!(a && d && d.contains(a)), body: !a || a === document.body, d: a ? a.tagName.toLowerCase() + (a.id ? '#' + a.id : '') + '.' + String(a.className).slice(0, 30) : 'none' }; })()`);
            for (const back of [false, true]) {
                const onPage = []; let bodyRun = 0, longestBodyRun = 0;
                for (let i = 0; i < 60; i++) {
                    await key('Tab', 9, back); const w = await where();
                    if (!w.inside && !w.body) onPage.push(w.d); // a control of the page behind the dialog
                    bodyRun = w.body ? bodyRun + 1 : 0; longestBodyRun = Math.max(longestBodyRun, bodyRun); // (a modal dialog hands focus to the browser's own controls once at the end of its cycle: one stop on the body, then back in)
                }
                assert.deepEqual(onPage, [], `${tab} ${back ? 'Shift+Tab' : 'Tab'} at 390: no stop on the page behind the dialog`);
                assert.ok(longestBodyRun <= 1, `${tab} ${back ? 'Shift+Tab' : 'Tab'} at 390: focus is never left on the body for two presses (${longestBodyRun})`);
            }
            await ev(`document.querySelectorAll('dialog[open]').forEach(d => d.close()); true`);
        }
        console.log('PASS Team settings: Tab stays inside the dialog on a phone');
    } finally { try { await ev(`fetch('/api/board/${TEAM}', { method: 'DELETE' }).then(() => true)`); } catch (_) {} await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
