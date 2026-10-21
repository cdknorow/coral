// #5510 (the dashboard half of #5508): the Messaging tab of Team settings, "Who may message this board", against Backend's real routes (cross-team-state, cross-team-candidates, settings/log and
// the PUT of the whole list) on an isolated dev server with the cross_team_allowlist flag turned on (and put back). The real flow: no tab while the flag is off; the prompt for a board that was
// open; add from this computer's boards and by id (a bad id is refused before any request); Save through the real PUT; the list and the log read back; Remove; a reopened tab shows what was kept.
// A thin wrapper in the page overrides only what a fresh server cannot produce: suggestions and the cap (the state), and the server's refusals of a save. Both themes, desktop and phone
// (44 px, 4.5:1, no sideways scroll).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const { removeBoards } = require('./helpers/board_cleanup');
const { guardSettings } = require('./helpers/settings_guard');
const BASE = require('./test_base').BASE;

const XID = n => 'x-' + n.repeat(16);
const ENTRY = (n, label) => ({ server: 'local', cross_team_id: XID(n), label, known_here: true });
const OTHER = { server: 'hub-srv-1', cross_team_id: XID('z'), label: 'Design team', known_here: false };

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const RUN = Date.now().toString(36);
    const TEAM = 'ct-team-' + RUN, ALPHA = 'ct-alpha-' + RUN, BRAVO = 'ct-bravo-' + RUN;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const P = '#team-settings-messaging';
    const call = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const board = (name, path = '', method = 'GET', body) => call(method, `/api/board/${name}${path}`, body);
    // A thin layer over the real fetch: window.__ct.state replaces the state route's answer, window.__ct.put answers the next save, and every save is recorded.
    const layer = () => ev(`(() => { if (!window.__realFetch) window.__realFetch = window.fetch; const R = window.__realFetch; window.__ct = window.__ct || { state: null, put: null, puts: [] };
        window.fetch = (u, o = {}) => { const p = new URL(String(u && u.url || u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
            if (p === '/api/board/${TEAM}/cross-team-state' && m === 'GET' && window.__ct.state) return Promise.resolve(new Response(JSON.stringify(window.__ct.state), { status: 200, headers: { 'content-type': 'application/json' } }));
            if (p === '/api/board/${TEAM}/settings' && m === 'PUT' && JSON.parse(o.body).cross_team_allowed) { window.__ct.puts.push(JSON.parse(o.body)); if (window.__ct.put) { const r = window.__ct.put; window.__ct.put = null; return Promise.resolve(new Response(JSON.stringify(r.body), { status: r.status, headers: { 'content-type': 'application/json' } })); } }
            return R(u, o); }; return true; })()`);
    const open = async () => {
        await ev(`document.getElementById('team-settings-workspace')?.remove(); document.getElementById('team-working-mode-dialog')?.remove(); import(${S} + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
        await waitFor(`!!document.querySelector('[data-settings-tab=health]')`, 'the settings dialog', 15000);
    };
    const tabClick = () => waitFor(`(() => { const t = document.querySelector('[data-settings-tab=messaging]'); if (!t) return false; t.click(); const p = document.querySelector('${P}'); return !!p && !p.hidden && !!p.querySelector('form .ct-section'); })()`, 'the Messaging tab', 10000);
    const click = sel => ev(`document.querySelector(${JSON.stringify(sel)}).click(); true`);
    const names = () => ev(`[...document.querySelectorAll('${P} .ct-row')].map(r => r.querySelector('.ct-name').textContent)`);
    const status = () => ev(`document.querySelector('${P} .ct-state').textContent`);
    const active = () => ev(`(() => { const a = document.activeElement; return a ? (a.getAttribute('aria-label') || a.dataset.act || a.name || a.tagName) : ''; })()`);
    const setVal = (name, v) => ev(`(() => { const e = document.querySelector('${P} [name=${name}]'); e.value = ${JSON.stringify(v)}; e.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
    const pickFor = label => ev(`(() => { const o = [...document.querySelectorAll('${P} [name=pick] option')].find(x => x.textContent === ${JSON.stringify(label)}); if (!o) return false; document.querySelector('${P} [name=pick]').value = o.value; return true; })()`);
    const stateNow = async () => (await board(TEAM, '/cross-team-state')).body;
    const guard = await guardSettings(); // the flag is turned on for the run and put back
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        for (const name of [TEAM, ALPHA, BRAVO]) {
            const r = await board(name, '/subscribe', 'POST', { subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: name + '-session' });
            assert.ok(r.status < 300, `subscribe ${name}: ${JSON.stringify(r)}`);
        }
        // the board accepted messages from any team before the list existed: its old switch is on, its list empty
        const sw = await board(TEAM, '/settings', 'PUT', { subscriber_id: 'Operator', cross_team_messaging: true });
        assert.ok(sw.status < 300, `the old switch: ${JSON.stringify(sw)}`);
        await layer();

        console.log('1. flag off: the tab is there (the list can be prepared), says it is not in force, and asks nothing');
        assert.ok((await call('PUT', '/api/settings', { cross_team_allowlist: 'false' })).status < 300);
        await ev(`localStorage.removeItem('coral-ct-prompt:${TEAM}'); true`);
        await open(); await tabClick();
        check('the Messaging tab shows while the server does not enforce the list', true);
        check('a line says the list is not in force yet', /not in force yet/.test(await ev(`document.querySelector('${P} .ct-notice')?.textContent || ''`)));
        check('the one-time prompt is not shown while the list is not enforced (even for a board that was open)', await ev(`!document.querySelector('${P} .ct-prompt')`));
        assert.ok(await pickFor(ALPHA)); await click(`${P} [data-act=add-pick]`);
        if (process.env.CORAL_SCREENSHOT_DIR) require('node:fs').writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/messaging-not-enforced.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
        check('the list can be edited and Save is on (nothing is sent until Save)', (await names()).length === 1 && !(await ev(`document.querySelector('${P} [type=submit]').disabled`)) && (await ev(`window.__ct.puts.length`)) === 0);

        console.log('2. flag on: the tab, the prompt, an empty list');
        // the strip is drawn with the Messaging tab and nothing is added to it after the Health check tab arrives (#5518): the order is final when it first shows, and when Health check shows
        await ev(`window.__strip = { first: null, atHealth: null }; new MutationObserver(() => { const nav = document.querySelector('.team-settings-tabs'); if (!nav) return; const ids = [...nav.querySelectorAll('[data-settings-tab]')].map(t => t.dataset.settingsTab); if (!window.__strip.first) window.__strip.first = ids; if (ids.includes('health') && !window.__strip.atHealth) window.__strip.atHealth = ids; }).observe(document.documentElement, { childList: true, subtree: true }); true`);
        assert.ok((await call('PUT', '/api/settings', { cross_team_allowlist: 'true' })).status < 300);
        await ev(`localStorage.removeItem('coral-ct-prompt:${TEAM}'); true`);
        await open(); await tabClick();
        const strip = await ev(`window.__strip`);
        check(`the strip is final when it first shows (it already has Messaging) and when Health check arrives (${JSON.stringify(strip)})`, strip.first.includes('messaging') && JSON.stringify(strip.atHealth) === JSON.stringify(['workflow', 'prompts', 'health', 'messaging']));
        check('the notice is gone once the list is enforced, and the tab follows Health check, the panel is linked and the main Save bar is hidden', await ev(`(() => { const t = [...document.querySelectorAll('[data-settings-tab]')].map(x => x.dataset.settingsTab); const p = document.querySelector('${P}'); return t[t.length - 1] === 'messaging' && t[t.length - 2] === 'health' && p.getAttribute('role') === 'tabpanel' && p.getAttribute('aria-labelledby') === 'team-settings-tab-messaging' && document.querySelector('form.working-mode-form').hidden && !document.querySelector('${P} .ct-notice'); })()`));
        check('a board that was open is asked once, and nothing is added or saved by showing it', await ev(`!!document.querySelector('${P} .ct-prompt') && /accepted messages from any team/.test(document.querySelector('${P} .ct-prompt').textContent) && /No team sent it a message/.test(document.querySelector('${P} .ct-prompt').textContent)`) && (await names()).length === 0 && /Every message from another team is refused/.test(await ev(`document.querySelector('${P} .ct-empty').textContent`)) && (await ev(`document.querySelector('${P} [type=submit]').disabled`)));
        check('the count says 0 of 200', (await ev(`document.querySelector('${P} .ct-count').textContent`)) === '0 of 200 teams');
        await click(`${P} [data-act=dismiss]`);
        check('Not now adds nothing, says so, and stays dismissed on the next open', /Nothing was added/.test(await status()) && (await names()).length === 0);

        console.log('3. add, and Save through the real PUT');
        check('the picker offers this computer\'s other boards (not this one)', await ev(`(() => { const o = [...document.querySelectorAll('${P} [name=pick] option')].map(x => x.textContent); return o.includes(${JSON.stringify(ALPHA)}) && o.includes(${JSON.stringify(BRAVO)}) && !o.includes(${JSON.stringify(TEAM)}); })()`));
        assert.ok(await pickFor(ALPHA)); await click(`${P} [data-act=add-pick]`);
        assert.ok(await pickFor(BRAVO)); await click(`${P} [data-act=add-pick]`);
        check('two boards are added from the picker and leave it', JSON.stringify(await names()) === JSON.stringify([ALPHA, BRAVO]) && !(await ev(`[...document.querySelectorAll('${P} [name=pick] option')].some(o => o.textContent === ${JSON.stringify(ALPHA)})`)));
        await click(`${P} [data-act=add-other]`);
        check('adding by id with nothing typed is refused with a reason', /server id/.test(await status()));
        await setVal('other_server', 'Bad Id!'); await setVal('other_board', XID('k')); await click(`${P} [data-act=add-other]`);
        check('a server id with capitals or a space is refused before any request', /server id/.test(await status()) && (await names()).length === 2 && (await ev(`window.__ct.puts.length`)) === 0);
        await setVal('other_server', 'hub-srv-2'); await setVal('other_board', 'b-oldstyleid'); await click(`${P} [data-act=add-other]`);
        check('a team id that is not x- and 16 characters is refused', /team\'s id/.test(await status()) && (await names()).length === 2);
        await setVal('other_server', 'hub-srv-2'); await setVal('other_board', XID('k')); await setVal('other_label', 'Kappa'); await click(`${P} [data-act=add-other]`);
        check('a team on another Coral is added by ids with its name', (await names())[2] === 'Kappa');
        await setVal('other_server', 'hub-srv-2'); await setVal('other_board', XID('k')); await click(`${P} [data-act=add-other]`);
        check('the same team twice is refused', /already on the list/.test(await status()) && (await names()).length === 3);
        check('nothing was sent yet and unsaved changes are guarded', (await ev(`window.__ct.puts.length`)) === 0 && await ev(`import(${S} + 'team_working_mode.js').then(m => m.hasUnsavedTeamSettings())`));
        await ev(`window.__ct.put = { status: 400, body: { code: 'bad_allowlist', row: 2, field: 'cross_team_id', message: 'the id is not a team id' } }; true`);
        await click(`${P} [type=submit]`); await waitFor(`/Could not save/.test(document.querySelector('${P} .ct-state').textContent)`, 'the bad list answer', 6000);
        check('the server\'s refusal of a bad list names the row and the field, the draft stays', /Row 3 \(cross team id\): the id is not a team id/.test(await status()) && (await names()).length === 3);
        await ev(`window.__ct.put = { status: 429, body: { code: 'edit_rate_limited' } }; true`);
        await click(`${P} [type=submit]`); await waitFor(`/Too many changes/.test(document.querySelector('${P} .ct-state').textContent)`, 'the rate limit answer', 6000);
        check('the edit limit is said in words', /Too many changes in the last hour/.test(await status()));
        await ev(`window.__ct.put = { status: 403, body: { code: 'owner_only' } }; true`);
        await click(`${P} [type=submit]`); await waitFor(`/Could not save/.test(document.querySelector('${P} .ct-state').textContent)`, 'the owner answer', 6000);
        check('another refusal is said in our words, not the server\'s', /Could not save the list\. Only the account/.test(await status()) && !/owner_only/.test(await status()));
        await click(`${P} [type=submit]`); await waitFor(`document.querySelector('${P} .ct-state').textContent === 'Saved.'`, 'saved', 8000);
        const put = await ev(`window.__ct.puts[window.__ct.puts.length - 1]`);
        check('Save sends the whole list as {server, cross_team_id, label}', put.subscriber_id === 'Operator' && put.cross_team_allowed.length === 3 && put.cross_team_allowed.every(e => ['cross_team_id', 'server'].every(k => k in e)) && put.cross_team_allowed.some(e => e.server === 'hub-srv-2' && e.cross_team_id === XID('k') && e.label === 'Kappa'), JSON.stringify(put));
        const st = await stateNow();
        check('the server kept the three entries, and the board is no longer "was open" (it has a list)', st.allowed.length === 3 && st.enforced === true && st.was_open === false, JSON.stringify(st).slice(0, 300));
        check('after Save nothing is unsaved and the tab shows what the server kept', !(await ev(`import(${S} + 'team_working_mode.js').then(m => m.hasUnsavedTeamSettings())`)) && JSON.stringify(await names()) === JSON.stringify([ALPHA, BRAVO, 'Kappa']));
        const logText = await ev(`document.querySelector('${P} .ct-log')?.textContent || ''`);
        check('the log says what the edit added', new RegExp(`added .*${ALPHA}`).test(logText) && /Kappa on hub-srv-2/.test(logText), logText.slice(0, 300));

        console.log('4. reopen, Remove');
        await open(); await tabClick();
        check('a reopened tab shows the saved list, and no prompt', JSON.stringify(await names()) === JSON.stringify([ALPHA, BRAVO, 'Kappa']) && await ev(`!document.querySelector('${P} .ct-prompt')`));
        check('a board of this computer that exists is not "not known here"; each row names the Coral and the id', (await ev(`[...document.querySelectorAll('${P} .ct-row')].map(r => !!r.querySelector('.ct-unknown'))`)).join() === 'false,false,false' && (await ev(`document.querySelector('${P} .ct-row:nth-child(3) .ct-id').textContent`)) === `hub-srv-2 · ${XID('k')}`);
        await ev(`document.querySelector('${P} .ct-row:nth-child(2) .ct-remove').focus(); true`);
        await click(`${P} .ct-row:nth-child(2) .ct-remove`);
        check('Remove takes the team off, says so, and keeps the keyboard in the list', !(await names()).includes(BRAVO) && /Removed /.test(await status()) && /^Remove /.test(await active()), await active());
        check('the removal is only a draft until Save: Save is on and the server still has all three', !(await ev(`document.querySelector('${P} [type=submit]').disabled`)) && (await stateNow()).allowed.length === 3); // one real save per run: the server limits an owner to 30 edits an hour, and a repeat of this suite (--stress) must stay under it

        console.log('5. suggestions, the cap');
        await ev(`window.__ct.state = ${JSON.stringify({ enforced: true, max_entries: 2, was_open: true, self_cross_team_id: XID('s'), allowed: [ENTRY('a', 'Alpha team'), ENTRY('b', 'Bravo team')],
            suggestions: [{ server: 'local', cross_team_id: XID('c'), label: 'Charlie team', label_is_a_claim: false, last_seen: '2026-10-19T10:00:00Z', count: 4, listable: true },
                { server: 'hub-srv-1', cross_team_id: XID('y'), label: 'Ops team', label_is_a_claim: true, last_seen: '2026-10-18T10:00:00Z', count: 1, listable: true },
                { server: 'hub-srv-3', cross_team_id: '', label: 'Old team', label_is_a_claim: true, last_seen: '2026-10-01T10:00:00Z', count: 2, listable: false }] })}; localStorage.removeItem('coral-ct-prompt:${TEAM}'); true`);
        await open(); await tabClick();
        check('at the cap both Add buttons are off and the count says the list is full', (await ev(`document.querySelector('${P} [data-act=add-pick]').disabled && document.querySelector('${P} [data-act=add-other]').disabled`)) && /2 of 2 teams: the list is full/.test(await ev(`document.querySelector('${P} .ct-count').textContent`)));
        const sg = await ev(`[...document.querySelectorAll('${P} input[name=suggest]')].map(c => ({ checked: c.checked, disabled: c.disabled, text: c.closest('label').textContent }))`);
        check('three suggestions, all unchecked; one seen before ids existed is disabled and says why; a claimed name is marked', sg.length === 3 && sg.every(x => !x.checked) && sg[2].disabled && /cannot be added yet/.test(sg[2].text) && /name given by the sender/.test(sg[1].text) && !sg[0].disabled, JSON.stringify(sg).slice(0, 300));
        await click(`${P} [data-act=add-checked]`);
        check('Add checked with none checked says so and adds nothing', /Check the teams/.test(await status()) && (await names()).length === 2);
        await click(`${P} input[name=suggest]`); await click(`${P} [data-act=add-checked]`);
        check('at the cap a checked suggestion is not added past it', (await names()).length === 2);
        await ev(`window.__ct.state.max_entries = 200; localStorage.removeItem('coral-ct-prompt:${TEAM}'); true`);
        await open(); await tabClick();
        await click(`${P} input[name=suggest]`); await click(`${P} [data-act=add-checked]`);
        check('a checked suggestion is added to the draft (not saved), the prompt goes, Save is on', JSON.stringify(await names()) === JSON.stringify(['Alpha team', 'Bravo team', 'Charlie team']) && (await ev(`!document.querySelector('${P} .ct-prompt')`)) && !(await ev(`document.querySelector('${P} [type=submit]').disabled`)));
        await ev(`window.__ct.state = null; true`);

        console.log('6. themes, desktop and phone');
        for (const [view, theme] of [[{ w: 1280, h: 900, phone: false }, 'light'], [{ w: 1280, h: 900, phone: false }, 'dark'], [{ w: 390, h: 844, phone: true }, 'light'], [{ w: 390, h: 844, phone: true }, 'dark']]) {
            const tag = `${view.w}px ${theme}`;
            await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: view.phone });
            await Emulation.setTouchEmulationEnabled({ enabled: view.phone });
            await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, theme); await layer();
            await ev(`window.__ct.state = ${JSON.stringify({ enforced: true, max_entries: 200, was_open: true, allowed: [ENTRY('a', 'Alpha team'), OTHER, { server: 'local', cross_team_id: XID('q'), label: 'Gone team with a rather long name that has to wrap on a phone', known_here: false }],
                suggestions: [{ server: 'local', cross_team_id: XID('b'), label: 'Bravo team', label_is_a_claim: false, last_seen: '2026-10-19T10:00:00Z', count: 4, listable: true }] })}; localStorage.removeItem('coral-ct-prompt:${TEAM}'); true`);
            await open(); await tabClick();
            await ev(`document.querySelector('${P} .ct-other').open = true; true`);
            const g = await ev(`(() => { const p = document.querySelector('${P}'); const t = [...p.querySelectorAll('button, select, input[type=text], summary, .ct-check')].filter(e => e.getClientRects().length).map(e => Math.round(e.getBoundingClientRect().height)); return { over: Math.max(0, p.scrollWidth - p.clientWidth), docOver: Math.max(0, document.documentElement.scrollWidth - innerWidth), minTarget: Math.min(...t) }; })()`);
            check(`${tag}: no sideways scroll (${g.over}, ${g.docOver})`, g.over <= 0 && g.docOver <= 0);
            if (view.phone) check(`${tag}: every control is 44 px or more (${g.minTarget})`, g.minTarget >= 44);
            if (process.env.CORAL_SCREENSHOT_DIR) require('node:fs').writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/messaging-${view.w}-${theme}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
            const au = await auditRegion(ev, [P], { focus: [`${P} [data-act=add-checked]`, `${P} .ct-remove`, `${P} [name=pick]`] });
            check(`${tag}: readable (4.5:1) with a focus ring (${au.checked} checked)`, au.fails.length === 0, JSON.stringify(au.fails.slice(0, 2)));
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed}/${results.length} checks passed`);
        if (failed) process.exitCode = 1;
    } finally { await removeBoards(ev, [TEAM, ALPHA, BRAVO]); await guard.restore(); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
