// Two frictions from the phone first-run walk (#6383 ranks 6 and 9, #6407): New Agent's second step asks for the agent twice (the radio group, then an "Agent Type" select among the model fields) and sets Save Persona, Export and
// Import side by side before a first agent exists; and the "Claude isn't signed in" line shows on a home that has no Claude agent (a Codex-only first run). Now: the radio group is the one place that names the agent (the form's own select
// stays, hidden, and still follows it), Save Persona is the one persona button and Export / Import are one disclosure away, and the sign-in line is there only while a Claude agent of this computer is. At 1280 and 390 px.
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const shown = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && e.getClientRects().length > 0 && e.checkVisibility({ contentVisibilityAuto: true, visibilityProperty: true }); })()`; // a closed <details> hides its content by content-visibility
    const open = async () => {
        await ev(`(async () => { await window.showLaunchModal(); document.querySelector('#launch-step-chooser [data-arg="agent"]').click(); return true; })()`);
        await waitFor(`getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none' && !!document.getElementById('launch-agent-next')`, 'the folder step', 8000);
        await ev(`(() => { const i = document.getElementById('launch-dir'); if (!i.value) i.value = '/tmp'; document.getElementById('launch-agent-next').click(); return true; })()`);
        await waitFor(`!document.getElementById('launch-agent-pick').hidden && getComputedStyle(document.getElementById('launch-agent-pick')).display !== 'none'`, 'the agent choice', 8000);
    };
    const section = name => ev(`import(${S} + 'section_dialog.js').then(m => { const shell = document.querySelector('#launch-step-agent .settings-shell'); document.getElementById('launch-more-options').open = true; /* a phone keeps the sections under More options (#7066) */ m.showSection(shell, ${JSON.stringify(name)}); return true; })`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);

            console.log(`${tag}: New Agent, second step`);
            await open();
            // the model section is where the form's own "Agent Type" select sat
            const sections = await ev(`[...document.querySelectorAll('.settings-group-pick select option')].map(o => o.value + ':' + o.textContent.trim())`);
            const modelKey = (sections.find(s => /model|agent/i.test(s.split(':')[1]) && !/persona/i.test(s)) || '').split(':')[0];
            if (modelKey) await section(modelKey);
            const dup = await ev(`(() => { const sel = document.querySelector('#launch-agent-acf .acf-agent-type'); const lab = sel && sel.closest('label'); return { selectInDom: !!sel, labelShown: !!lab && lab.getClientRects().length > 0, radios: document.querySelectorAll('#launch-agent-pick input').length }; })()`);
            check(`${tag}: the agent is named once: the radio group shows, and the form's "Agent Type" select is not on screen`, dup.radios === 4 && dup.selectInDom && !dup.labelShown, JSON.stringify([dup, sections]));
            await ev(`document.querySelector('#launch-agent-pick input[value="codex"]').click(); true`);
            check(`${tag}: choosing in the group still drives the hidden select`, (await ev(`document.querySelector('#launch-agent-acf .acf-agent-type').value`)) === 'codex');

            await section('workspace');
            const on = await ev(`({ save: ${shown('#agent-save-persona-btn')}, exp: ${shown('.agent-persona-actions [data-on-click="m-export-personas"]')}, imp: ${shown('.agent-persona-actions [data-on-click="m-import-personas"]')} })`);
            check(`${tag}: Save Persona is the one persona button on show`, on.save && !on.exp && !on.imp, JSON.stringify(on));
            const more = await ev(`(() => { const d = document.querySelector('.agent-persona-actions details'); const s = d && d.querySelector('summary'); return { has: !!d, text: s ? s.textContent.trim() : '', open: !!d && d.open }; })()`);
            check(`${tag}: Export and Import are in a closed disclosure named for them ("Export or import")`, more.has && !more.open && /^export or import$/i.test(more.text), JSON.stringify(more));
            await ev(`document.querySelector('.agent-persona-actions details summary').click(); true`);
            check(`${tag}: opened, both buttons show`, await ev(shown('.agent-persona-actions [data-on-click="m-export-personas"]')) && await ev(shown('.agent-persona-actions [data-on-click="m-import-personas"]')));
            const fit = await ev(`(() => { const p = document.querySelector('#launch-step-agent'); return document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1; })()`);
            check(`${tag}: no sideways scroll on the page`, fit);
            await ev(`document.querySelector('#launch-agent-back, #launch-step-agent [data-on-click="launch-hide"]') && 0; true`);
        }

        console.log('The Claude sign-in line');
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false }); await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const line = async sessions => ev(`(async () => {
            const st = await import(${S} + 'state.js'), si = await import(${S} + 'signin_line.js');
            st.state.liveSessions = ${JSON.stringify(sessions)};
            si.applySigninStatus({ claude_signed_in: false }); si.syncSigninLine();
            const l = document.getElementById('claude-signin-line'); return !l.hidden && l.getClientRects().length > 0;
        })()`);
        const row = (type, extra = {}) => ({ session_id: type + '-1', name: type + '-1', agent_type: type, server: 'local', ...extra });
        check('no agent at all: not signed in is not shown', (await line([])) === false);
        check('only a Codex agent: not shown (a Codex-only first run has nothing waiting on Claude)', (await line([row('codex')])) === false);
        check('a Claude agent of this computer: shown', (await line([row('codex'), row('claude')])) === true);
        check('the Claude agent goes away: it goes with it', (await line([row('codex')])) === false);
        check('a Claude agent on another server does not count', (await line([row('claude', { server: 'other-box' })])) === false);
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} phone walk checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
