// The agent is chosen in New Agent's first step (#6199, Codex Test's first-run walk): Codex was only reachable under More options > Agent while the hidden default was Claude. The four agents are a radio group under the folder,
// the installed ones first and the others marked "not installed"; it follows the form's own agent select (both ways); with no saved default and Claude missing, an installed agent starts selected. At 1280 and 390 px.
// The CLI check is stood in per agent. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const stub = installed => ev(`(() => { if (!window.__realFetch) window.__realFetch = window.fetch; const real = window.__realFetch; window.fetch = async (u, o = {}) => { const p = String(u), m = /\\/api\\/system\\/cli-check\\?(?:.*&)?type=([a-z]+)/.exec(p);
        if (m) { const found = ${JSON.stringify(installed)}.includes(m[1]); return new Response(JSON.stringify({ found, agent_type: m[1], status: found ? 'available' : 'missing' }), { headers: { 'Content-Type': 'application/json' } }); } return real(u, o); }; return true; })()`);
    const open = async () => {
        await ev(`(async () => { await window.showLaunchModal(); document.querySelector('#launch-step-chooser [data-arg="agent"]').click(); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none' && !!document.getElementById('launch-agent-next')`, 'the folder step', 8000);
        await ev(`(() => { const i = document.getElementById('launch-dir'); if (!i.value) i.value = '/tmp'; document.getElementById('launch-agent-next').click(); return true; })()`); // the agent is chosen in the second step (#6333)
        await waitFor(`!document.getElementById('launch-agent-pick').hidden && getComputedStyle(document.getElementById('launch-agent-pick')).display !== 'none'`, 'the agent choice', 8000);
    };
    const state = () => ev(`(() => { const g = document.getElementById('launch-agent-pick'); const sel = document.querySelector('#launch-agent-acf .acf-agent-type'); return { labels: [...g.querySelectorAll('label')].map(l => l.textContent.trim()), checked: (g.querySelector('input:checked') || {}).value || null, select: sel.value, inMore: !!g.closest('#launch-agent-more'), shown: g.getClientRects().length > 0, role: g.getAttribute('role'), name: (document.getElementById(g.getAttribute('aria-labelledby')) || {}).textContent }; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(async () => { const m = await import(${S} + 'launch_models.js'); return true; })()`);
            // Claude and Antigravity missing, Codex and Pi installed, nothing ever chosen: Codex and Pi first, Codex selected
            await stub(['codex', 'pi']);
            await open();
            let s = await state();
            check(`${tag}: the agent choice is in the second step, not behind a disclosure, a named radio group`, s.shown && !s.inMore && s.role === 'radiogroup' && s.name === 'Agent:', JSON.stringify([s.shown, s.inMore, s.role, s.name]));
            check(`${tag}: installed agents first, the others marked`, JSON.stringify(s.labels) === JSON.stringify(['Codex', 'Pi', 'Claude not installed', 'Antigravity not installed']), JSON.stringify(s.labels));
            check(`${tag}: with no default chosen and Claude missing, the first installed agent is selected (the form's select agrees)`, s.checked === 'codex' && s.select === 'codex', JSON.stringify([s.checked, s.select]));
            // choosing in the first step drives the form
            await ev(`document.querySelector('#launch-agent-pick input[value="pi"]').click(); true`);
            s = await state();
            const cfg = await ev(`import(${S} + 'agent_config_form.js').then(m => m.getAgentConfig('launch-agent-acf').agentType)`);
            check(`${tag}: picking Pi sets the form's agent (what Launch sends)`, s.select === 'pi' && cfg === 'pi', JSON.stringify([s.select, cfg]));
            // and the form drives the choice
            await ev(`(() => { const sel = document.querySelector('#launch-agent-acf .acf-agent-type'); sel.value = 'agy'; sel.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            s = await state();
            check(`${tag}: changing the agent under More options moves the choice here`, s.checked === 'agy', JSON.stringify([s.checked, s.select]));
            // each radio is a real target
            const sizes = await ev(`[...document.querySelectorAll('#launch-agent-pick label')].map(l => { const b = l.getBoundingClientRect(); return [Math.round(b.width), Math.round(b.height)]; })`);
            check(`${tag}: ${mobile ? 'each choice is 44 px high' : 'each choice is a clear target'}`, sizes.length === 4 && sizes.every(z => z[1] >= (mobile ? 44 : 28)), JSON.stringify(sizes));
            const fits = await ev(`(() => { const r = document.getElementById('launch-agent-pick').getBoundingClientRect(); return r.right <= innerWidth + 1 && r.left >= 0; })()`);
            check(`${tag}: it fits the screen`, fits);
            // everything installed: the default order, Claude selected
            await ev(`window.hideLaunchModal(); true`);
            await stub(['claude', 'codex', 'agy', 'pi']);
            await open();
            s = await state();
            check(`${tag}: everything installed: the usual order, Claude selected, nothing marked`, JSON.stringify(s.labels) === JSON.stringify(['Claude', 'Codex', 'Antigravity', 'Pi']) && s.checked === 'claude', JSON.stringify([s.labels, s.checked]));
            await ev(`window.hideLaunchModal(); true`);
            // a CLI check that fails: nothing is marked, the default stays
            await ev(`(() => { const real = window.__realFetch; window.fetch = async (u, o = {}) => { if (/cli-check/.test(String(u))) return new Response('x', { status: 500 }); return real(u, o); }; return true; })()`);
            await open();
            s = await state();
            check(`${tag}: when the check cannot answer, nothing is marked and Claude stays the default`, s.labels.every(l => !/not installed/.test(l)) && s.checked === 'claude', JSON.stringify([s.labels, s.checked]));
            await ev(`window.hideLaunchModal(); true`);
        }
    } finally { /* the harness closes the page */ }
    const failed = results.filter(r => !r).length;
    console.log(`launch_agent_pick: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
