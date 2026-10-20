// A refused owner-only setting is said in our words (#5323). Through Coral Hub the hub answers the Health check, the team settings and the Prompt inspection with its own word (not_found, mode_too_low,
// rate_limited, owner_unverified, settings_unavailable, bad_request, too_large), and this computer with its code (owner_only, local_only, too_large, operator_identity_required) or only a status
// (403 below Launch, 400, 413). Each becomes one short sentence in the Health check tab (save), the team settings (the switches that could not load) and the Prompt inspection; anything else is
// "Unavailable", and the server's own words are never shown. Against stubbed APIs on the isolated dev server, with a leak marker in every refusal's `error` text.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const LEAK = 'LEAKED-SERVER-WORDS';
const SENTENCE = {
    not_found: /was not found/, mode_too_low: /access mode is too low/, rate_limited: /Too many tries/, owner_unverified: /linked to can do that through Coral Hub/,
    settings_unavailable: /not available through Coral Hub/, bad_request: /request was not accepted/, too_large: /too large to save/, unavailable: /^Unavailable$/,
};
// [what the answer is, expected sentence key]
const CASES = [
    [{ status: 404, code: 'not_found' }, 'not_found'], [{ status: 403, code: 'mode_too_low' }, 'mode_too_low'], [{ status: 429, code: 'rate_limited' }, 'rate_limited'],
    [{ status: 403, code: 'owner_unverified' }, 'owner_unverified'], [{ status: 503, code: 'settings_unavailable' }, 'settings_unavailable'], [{ status: 400, code: 'bad_request' }, 'bad_request'],
    [{ status: 413, code: 'too_large' }, 'too_large'],
    [{ status: 403, code: 'owner_only' }, 'owner_unverified'], [{ status: 403, code: 'local_only' }, 'bad_request'], [{ status: 403, code: 'operator_identity_required' }, 'settings_unavailable'],
    [{ status: 403 }, 'mode_too_low'], [{ status: 400 }, 'bad_request'], [{ status: 413 }, 'too_large'],
    [{ status: 500, code: 'something_new' }, 'unavailable'], [{ status: 502 }, 'unavailable'],
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a redraw settles over frames; there is no completion event
    const team = 'or-team-' + Date.now().toString(36);
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const HEALTH = { on: false, interval_min: 10, recipient: '', all_clear: true, default_reviewers: [], auto_delegate: { on: true }, claim_nudge: { on: true, after_min: 10, nudges: 2 }, status_nudge: { on: true, after_min: 20, escalate_after_min: 10 }, mention_renudge: { on: true, after_min: 10 }, held_notice: { on: true, after_min: 10 } };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        const fresh = async () => {
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            // what each owner-only route answers is set per scenario: window.__ans = { settings: {status, code}|null, inspection: ..., put: ..., get: ... }
            await ev(`(() => { const R = window.fetch; window.__ans = {};
                const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
                const refuse = a => json(a.code ? { code: a.code, error: '${LEAK} ' + a.code } : (a.message ? { error: a.message } : {}), a.status);
                window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = (o.method || 'GET').toUpperCase(); const p = url.pathname; const board = p.startsWith('/api/board/');
                    if (board && p.endsWith('/working-mode')) return json({ mode: 'none', dependency_guidance: false, custom_instructions: '' });
                    if (board && p.endsWith('/working-mode/presets')) return json({ presets: [{ id: 'shared_checkout', name: 'Shared checkout', builtin: true, instructions: 'Share one checkout.' }] });
                    if (board && p.endsWith('/settings')) { const a = m === 'PUT' ? window.__ans.put : window.__ans.settings; if (a) return refuse(a); return json({ cross_team_messaging: false, cross_team_artifacts: false, health: ${JSON.stringify(HEALTH)} }); }
                    if (p === '/api/settings/prompt-inspection') { if (window.__ans.inspection) return refuse(window.__ans.inspection); return json({ orchestrator: { scope: 'global', applies: 'x', default: 'd', effective: 'e' }, worker: {}, task: {} }); }
                    if (p === '/api/settings' && m === 'GET') return json({ settings: { board_health_monitor: 'true' } });
                    return R(u, o); };
                return true; })()`);
            await ev(`window.__m.add('or-bot', 'A', 4, { sleeping: false, board_project: ${JSON.stringify(team)} }); true`); await settle(700);
            await ev(`window.selectLiveSession('or-bot', 'claude', 'or-bot-id', 'local'); true`); await settle(900);
        };
        const openTeam = async (ans, tab) => {
            await ev(`window.__ans = ${JSON.stringify(ans)}; true`);
            await ev(`(async () => { const m = await import(${S} + 'team_working_mode.js'); await m.showTeamWorkingMode(${JSON.stringify(team)}, { workspace: true }); window.switchAgenticTab('team-settings', 'top'); return true; })()`);
            await waitFor(`!!document.querySelector('#agentic-panel-team-settings .working-mode-form') && document.querySelector('#agentic-panel-team-settings .working-mode-form').dataset.initialState !== undefined`, 'the team settings', 8000);
            await settle(300);
        };

        console.log('1. the mapping');
        await fresh();
        const unit = await ev(`import(${S} + 'owner_refusal.js').then(m => (${JSON.stringify(CASES.map(c => c[0]))}).map(c => [m.refusalKey(c.status, c.code ? { code: c.code, error: '${LEAK}' } : {}), m.refusalText(c.status, c.code ? { code: c.code, error: '${LEAK}' } : {})]))`);
        CASES.forEach(([c, key], i) => check(`${c.status} ${c.code || '(no code)'} -> ${key} (${unit[i][1]})`, key === 'unavailable' ? unit[i][1] === 'Unavailable' : SENTENCE[key].test(unit[i][1]) && !unit[i][1].includes(LEAK)));
        check('an error word that is only the server\'s (no code) never shows', (await ev(`import(${S} + 'owner_refusal.js').then(m => [m.refusalText(200, { error: 'try the thing' }), m.refusalText(0, null), m.refusalText(500, 'text')])`)).every(t => t === 'Unavailable'));

        const odd = await ev(`import(${S} + 'owner_refusal.js').then(m => [m.refusalText(403, { code: 'whatever', error: '${LEAK}' }), m.refusalText(403, { code: 'constructor' }), m.refusalText(400, { code: 'toString', error: '${LEAK}' }), m.refusalText(502, { code: '', error: '${LEAK}' }), m.refusalText(400, { error: 'Bob is not a member of this team' }, true), m.refusalText(400, { code: 'x', error: 'Bob is not a member of this team' }, true), m.refusalText(403, { error: 'plain words from somewhere' }), m.refusalText(400, { error: 'x'.repeat(300) }, true), m.refusalText(400, { error: 'Bob is not a member of this team' }), m.refusalText(400, { error: 'Bob is not a member of this team' }, false), m.answeredLocally('a-team'), m.answeredLocally('@srv1/a-team')])`);
        check(`a code this build does not know is Unavailable whatever the status; a plain 400 message is shown as it is only when the answer came from this computer, never through Coral Hub or from another server (${JSON.stringify(odd)})`, odd[0] === 'Unavailable' && odd[1] === 'Unavailable' && odd[2] === 'Unavailable' && odd[3] === 'Unavailable' && odd[4] === 'Bob is not a member of this team' && odd[5] === 'Unavailable' && SENTENCE.mode_too_low.test(odd[6]) && odd[7] === 'That request was not accepted.' && odd[8] === 'That request was not accepted.' && odd[9] === 'That request was not accepted.' && odd[10] === true && odd[11] === false);

        console.log('2. the team settings that could not be read (Coral Hub refused them)');
        for (const [c, key] of [CASES[0], CASES[1], CASES[3], CASES[4], CASES[7], CASES[10], CASES[13]]) {
            await fresh(); await openTeam({ settings: c });
            const note = await ev(`(() => { const n = document.querySelector('#agentic-panel-team-settings .team-settings-refusal'); return n && !n.hidden ? { text: n.textContent, role: n.getAttribute('role'), tab: !!document.getElementById('team-settings-tab-health'), off: document.querySelector('#agentic-panel-team-settings [name=cross_team_messaging]').disabled } : null; })()`);
            check(`settings refused ${c.status} ${c.code || ''}: the note says it in our words, the switches are off, no Health check tab (${note && note.text})`, note && note.text.startsWith('Team settings: ') && (key === 'unavailable' ? note.text.endsWith('Unavailable') : SENTENCE[key].test(note.text)) && !note.text.includes(LEAK) && note.role === 'status' && note.off && !note.tab);
        }
        await fresh(); await openTeam({});
        check('nothing refused: no note', await ev(`document.querySelector('#agentic-panel-team-settings .team-settings-refusal').hidden`));

        console.log('3. the Prompt inspection');
        for (const [c, key] of [CASES[0], CASES[1], CASES[3], CASES[6], CASES[7], CASES[11], CASES[14]]) {
            await fresh(); await openTeam({ inspection: c });
            const t = await ev(`document.querySelector('#agentic-panel-team-settings .prompt-inspection-content').textContent.trim()`);
            check(`inspection refused ${c.status} ${c.code || ''}: "${t}"`, t.startsWith('Prompt inspection: ') && (key === 'unavailable' ? t.endsWith('Unavailable') : SENTENCE[key].test(t)) && !t.includes(LEAK));
        }

        console.log('4. the Health check tab: a refused save');
        for (const [c, key] of [CASES[1], CASES[2], CASES[3], CASES[7], CASES[9], CASES[12], CASES[13]]) {
            await fresh(); await openTeam({ put: c });
            await ev(`document.getElementById('team-settings-tab-health').click(); true`);
            await ev(`(() => { const f = document.querySelector('#team-settings-health form'); const on = f.elements.on; on.checked = !on.checked; on.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await ev(`document.querySelector('#team-settings-health form [type=submit]').click(); true`);
            await waitFor(`document.querySelector('#team-settings-health .health-state').classList.contains('health-error')`, 'the refusal', 6000);
            const t = await ev(`document.querySelector('#team-settings-health .health-state').textContent.trim()`);
            check(`health save refused ${c.status} ${c.code || ''}: "${t}"`, t.startsWith('Could not save the health check. ') && (key === 'unavailable' ? t.endsWith('Unavailable') : SENTENCE[key].test(t)) && !t.includes(LEAK) && !(await ev(`document.querySelector('#team-settings-health form [type=submit]').disabled`)));
        }
        { // this computer's own validation message on a 400 is still shown (UI/UX on #5323: it tells the person which reviewer is wrong)
            await fresh(); await openTeam({ put: { status: 400, message: 'Bob is not a member of this team' } });
            await ev(`document.getElementById('team-settings-tab-health').click(); true`);
            await ev(`(() => { const f = document.querySelector('#team-settings-health form'); const on = f.elements.on; on.checked = !on.checked; on.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await ev(`document.querySelector('#team-settings-health form [type=submit]').click(); true`);
            await waitFor(`document.querySelector('#team-settings-health .health-state').classList.contains('health-error')`, 'the validation message', 6000);
            const t = await ev(`document.querySelector('#team-settings-health .health-state').textContent.trim()`);
            check(`a 400 with this computer's plain message keeps it: "${t}"`, t === 'Could not save the health check. Bob is not a member of this team');
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await client.close(); process.exit(failed ? 1 : 0);
    } catch (e) { console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})();
