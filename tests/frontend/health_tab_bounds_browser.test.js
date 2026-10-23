// The Health check tab on a 390 px phone (#4259, from the #4258 pass over #4230): at 200 percent text nothing in the panel is clipped by the box it
// sits in (the page does not scroll sideways, so the check also walks the clipping ancestors), the check rows and the inputs are 44 px touch
// targets, the reviewers field's placeholder is short (its explanation is the line above), and the one-line explanations scale with the titles.
// Dark and the real light theme, 100 and 200 percent.
//
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server's real settings API; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const { removeBoards } = require('./helpers/board_cleanup');
const TEAM = 'health-bounds-team';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const theme of ['dark', 'light']) for (const scale of [100, 200]) {
            const name = `${theme}, ${scale}%`;
            console.log(name);
            await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
            await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
            for (const [id, title] of [['Boss', 'Orchestrator'], ['Dev One', 'Developer'], ['A Teammate With A Long Name', 'Quality Assurance Engineer']]) {
                const status = await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: ${JSON.stringify(id)}, job_title: ${JSON.stringify(title)}, session_name: ${JSON.stringify(id.toLowerCase().replace(/ /g, '-') + '-session')} }) }).then(r => r.status)`);
                assert.ok(status < 300, `subscribe ${id}: ${status}`);
            }
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme('${theme}');
                const st = document.createElement('style'); st.textContent = 'html{font-size:${scale}% !important}'; document.head.appendChild(st);
                const w = window.fetch; window.fetch = (u, o = {}) => (/board\\/[^/]+\\/settings$/.test(String(u)) && (o.method || 'GET') === 'GET') ? w(u, o).then(async r => { const b = await r.json(); b.health.held_notice = { on: true, after_min: 15 }; return new Response(JSON.stringify(b), { status: 200, headers: { 'content-type': 'application/json' } }); }) : w(u, o);
                return true; })()`);
            const bg = await ev(`getComputedStyle(document.body).backgroundColor`);
            const lum = bg.match(/\d+/g).slice(0, 3).reduce((a, b) => a + Number(b), 0) / 3;
            assert.equal(theme === 'light', lum > 128, `the ${theme} theme is really on (${bg})`);
            await ev(`document.getElementById('team-settings-workspace')?.remove(); import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
            await waitFor(`!!document.querySelector('[data-settings-tab=health]')`, 'the Health check tab', 15000);
            await ev(`document.querySelector('[data-settings-tab=health]').click(); true`);
            await waitFor(`!document.getElementById('team-settings-health').hidden && !!document.querySelector('#team-settings-health form')`, 'the panel', 5000);

            // 1. Nothing is clipped by the box it sits in, however deep that box is.
            const clipped = await ev(`(() => {
                const panel = document.getElementById('team-settings-health');
                const clipOf = el => { let r = { left: 0, right: innerWidth }; for (let p = el.parentElement; p; p = p.parentElement) { const cs = getComputedStyle(p); if (cs.overflowX !== 'visible') { const b = p.getBoundingClientRect(); r = { left: Math.max(r.left, b.left), right: Math.min(r.right, b.right) }; } } return r; };
                return [...panel.querySelectorAll('fieldset, .health-rule, label, input, select, button, p, legend')].filter(e => e.getBoundingClientRect().width > 0)
                    .filter(e => { const r = e.getBoundingClientRect(), c = clipOf(e); return r.right > c.right + 1 || r.left < c.left - 1; })
                    .map(e => (e.name || e.className.toString() || e.tagName).slice(0, 40) + ' ' + Math.round(e.getBoundingClientRect().right) + '>' + Math.round(clipOf(e).right)); })()`);
            assert.deepEqual(clipped, [], `nothing in the panel is clipped at ${name}: ${JSON.stringify(clipped.slice(0, 6))}`);

            // 2. Touch targets: the check rows' labels, the number and text inputs and the select.
            const small = await ev(`(() => [...document.querySelectorAll('#team-settings-health .health-rule-head, #team-settings-health input[type=number], #team-settings-health input[type=text], #team-settings-health select, #team-settings-health button')]
                .filter(e => e.getBoundingClientRect().width > 0).filter(e => { const ps = getComputedStyle(e, '::after'); return !(ps.content !== 'none' && ps.position === 'absolute' && parseFloat(ps.width) >= 43.5 && parseFloat(ps.height) >= 43.5); }) /* the (i) is 24 px with a 44 px ::after target (#6750) */.filter(e => e.getBoundingClientRect().height < 43.5)
                .map(e => (e.name || e.className.toString() || e.tagName).slice(0, 36) + ' ' + Math.round(e.getBoundingClientRect().height)))()`);
            assert.deepEqual(small, [], `every row and input is at least 44 px tall at ${name}`);

            // The unit stays beside its number at 100 percent (it wrapped under the input once, when the input's width was a percentage).
            if (scale === 100) {
                const wrapped = await ev(`(() => [...document.querySelectorAll('#team-settings-health .health-knob-box')].filter(b => { const i = b.querySelector('input').getBoundingClientRect(); const t = b.lastChild.nodeType === 3 ? b.getBoundingClientRect() : null; return b.getBoundingClientRect().height > i.height + 2; }).map(b => b.textContent.trim()))()`);
                assert.deepEqual(wrapped, [], `each unit sits beside its number at ${name}`);
            }

            // 3. The reviewers field: a short placeholder, the explanation above it.
            const rev = await ev(`(() => { const i = document.querySelector('#team-settings-health input[name=default_reviewers]'); return { ph: i.placeholder, hint: i.closest('label').querySelector('.health-limits').textContent }; })()`);
            assert.equal(rev.ph, 'Agent names (up to 8)');
            assert.match(rev.hint, /up to 8/); // the range is the field's description for a screen reader (not drawn); what no reviewers means is behind the (i)
            assert.match(await ev(`document.getElementById('hr-auto_delegate-w').textContent`), /reviews wait for the orchestrator/);

            // 4. The explanation scales with the title (the same ratio at 100 and 200 percent).
            const ratio = await ev(`(() => { const r = document.querySelector('#team-settings-health .health-rule[data-rule=claim_nudge]'); return parseFloat(getComputedStyle(r.querySelector(':scope > .settings-info-text')).fontSize) / parseFloat(getComputedStyle(r.querySelector('.health-rule-head')).fontSize); })()`);
            assert.ok(ratio > 0.8 && ratio < 1, `the explanation is a little smaller than the title, at every size (${ratio.toFixed(2)} at ${name})`);
            await ev(`document.getElementById('team-settings-workspace')?.remove(); true`);
        }
        console.log('\nAll Health check tab bounds checks passed');
    } finally {
        await removeBoards(ev, [TEAM]);
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
