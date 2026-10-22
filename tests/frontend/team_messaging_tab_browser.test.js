// The Messaging tab of Team settings (#5572): the one place for cross-team messaging. Against the real server for the two switches (read, saved with the tab's own Save, only what changed), with a thin layer over fetch
// for what a fresh server cannot answer yet (the status and the linked teams the server will send, the server's refusals, an unreadable settings answer, this computer's Coral Hub id). Checked: the tab is always there; one
// status line and one primary button whose words and kind follow the state (off: turn it on; waiting and ready: the constant Coral Hub link; stale: check the connection; old team: create a new one); the two switches
// are on this tab and not on Workflow; Save sends only what changed; a refusal is said in our words; the linked teams are text only (markup, control and bidi characters, a bad id, more than 50); How this works is closed,
// holds this computer's id with Copy; the unsaved-changes guard sees a draft. Desktop and 390 px, both themes, 44 px targets, 4.5:1 text, no sideways scroll.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

const HUB_LINKS = 'https://app.coralhub.dev/team-links';
const SID = '0b7c2e4a-5d1f-4c8e-9a36-2f4d8e1b7c90';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const TEAM = 'xt-team-' + Date.now().toString(36);
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name.replace(/[^a-z0-9-]+/gi, '-') + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const settle = (ms, quietMs = 120) => require('./helpers/wait').settle(ev, ms, quietMs);
    const call = (method, p, body) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const board = (p = '', method = 'GET', body) => call(method, `/api/board/${TEAM}${p}`, body);
    // The layer: state (the cross-team-state answer), settingsGet (an answer for GET settings: unreadable), put (the answer to the next save), cloud (what /api/cloud/status says), puts (every save), and recorders for the actions.
    const layer = () => ev(`(() => { if (!window.__realFetch) window.__realFetch = window.fetch; const R = window.__realFetch; window.__xt = window.__xt || { state: undefined, settingsGet: null, put: null, puts: [], cloud: null, calls: [], clip: [] };
        window.showSettingsModal = g => { window.__xt.calls.push('settings:' + g); };
        window.showLaunchModal = () => { window.__xt.calls.push('launch'); }; window._selectLaunchType = t => { window.__xt.calls.push('type:' + t); };
        Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async t => { window.__xt.clip.push(t); } } });
        const json = (b, s) => Promise.resolve(new Response(JSON.stringify(b), { status: s || 200, headers: { 'content-type': 'application/json' } }));
        window.fetch = (u, o = {}) => { const p = new URL(String(u && u.url || u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
            if (p === '/api/board/${TEAM}/cross-team-state' && m === 'GET' && window.__xt.state !== undefined) return window.__xt.state === null ? json({ error: 'none' }, 404) : json(window.__xt.state);
            if (p === '/api/board/${TEAM}/settings' && m === 'GET' && window.__xt.settingsGet) return json(window.__xt.settingsGet.body, window.__xt.settingsGet.status);
            if (p === '/api/board/${TEAM}/settings' && m === 'PUT' && (b => 'cross_team_messaging' in b || 'cross_team_artifacts' in b)(JSON.parse(o.body))) { window.__xt.puts.push(JSON.parse(o.body)); if (window.__xt.put) { const r = window.__xt.put; window.__xt.put = null; return json(r.body, r.status); } }
            if (p === '/api/cloud/status' && window.__xt.cloud) return json(window.__xt.cloud);
            return R(u, o); }; return true; })()`);
    const open = async (opts = {}) => {
        await ev(`document.getElementById('team-settings-workspace')?.remove(); document.getElementById('team-working-mode-dialog')?.remove(); import(${S} + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
        await waitFor(`${opts.noHealth ? 'true' : "!!document.querySelector('[data-settings-tab=health]')"} && !!document.querySelector('#team-settings-messaging form, #team-settings-messaging .ct-unavailable')`, 'the settings dialog and the Messaging panel', 15000);
        await ev(`document.querySelector('[data-settings-tab=messaging]').click(); true`);
        await settle(150);
    };
    const P = '#team-settings-messaging';
    const panel = () => ev(`(() => { const q = s => document.querySelector('${P} ' + s); const all = s => [...document.querySelectorAll('${P} ' + s)];
        const dot = q('.xt-head .update-state-dot'); const prim = q('.xt-primary');
        return { line: (q('.xt-line') || {}).textContent || '', dot: dot ? dot.dataset.k || '' : null, prim: prim ? { tag: prim.tagName, text: prim.textContent.replace(/\\s+/g, ' ').trim(), href: prim.getAttribute('href'), target: prim.target, rel: prim.rel, act: prim.dataset.act || '' } : null,
            msg: q('input[name=cross_team_messaging]') ? q('input[name=cross_team_messaging]').checked : null, files: q('input[name=cross_team_artifacts]') ? q('input[name=cross_team_artifacts]').checked : null,
            labels: all('.xt-switches label').map(l => l.textContent.trim()), teams: all('.xt-teams li').map(li => ({ name: li.querySelector('b').textContent, sub: li.querySelector('.xt-sub').textContent })), manage: (q('.xt-links a') || {}).href || null, heading: (q('.xt-links .xt-h') || {}).textContent || null,
            how: q('.xt-how') ? q('.xt-how').open : null, sid: (q('.xt-sid') || {}).textContent || '', save: q('[type=submit]') ? q('[type=submit]').disabled : null, stateText: (q('.xt-state') || {}).textContent || '',
            unavailable: (q('.ct-unavailable') || {}).textContent || '', msgDisabled: q('input[name=cross_team_messaging]') ? q('input[name=cross_team_messaging]').disabled : null, filesDisabled: q('input[name=cross_team_artifacts]') ? q('input[name=cross_team_artifacts]').disabled : null, locked: (q('.xt-locked') || {}).textContent || '', saveHidden: q('.xt-save') ? q('.xt-save').hidden : null, pwn: !!window.__pwn, imgs: all('img').length + all('script').length, workflowHasSwitch: !!document.querySelector('#team-settings-workflow [name=cross_team_messaging], #team-settings-workflow [name=cross_team_artifacts]') }; })()`);
    const click = sel => ev(`document.querySelector(${JSON.stringify(sel)}).click(); true`);
    const setState = st => ev(`window.__xt.state = ${JSON.stringify(st)}; true`);
    const links = n => Array.from({ length: n }, (_, i) => ({ server_label: 'box-' + i, server_id: 'srv-' + i, team_label: 'Team ' + i, refreshed: '2026-10-21T10:00:00Z' }));
    const views = [{ name: 'desktop', w: 1280, h: 900, phone: false, theme: 'dark' }, { name: 'desktop', w: 1280, h: 900, phone: false, theme: 'light' }, { name: '390px', w: 390, h: 844, phone: true, theme: 'dark' }, { name: '390px', w: 390, h: 844, phone: true, theme: 'light' }];
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const view of views) {
            const tag = `${view.name} ${view.theme}`; console.log(tag);
            await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: view.phone });
            await Emulation.setTouchEmulationEnabled({ enabled: view.phone });
            await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: view.theme }] });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, view.theme);
            const sub = await board('/subscribe', 'POST', { subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: TEAM + '-session' });
            assert.ok(sub.status < 300, JSON.stringify(sub));
            await board('/settings', 'PUT', { subscriber_id: 'Operator', cross_team_messaging: false, cross_team_artifacts: false });
            await layer();

            // 1. a server that sends no status (cross-team-state answers 404): the tab is there, the line is made from the switch, the primary turns it on
            await setState(null);
            await open();
            let p = await panel();
            check(`${tag}: the tab is there and the switches are on it, not on Workflow (${JSON.stringify(p.labels)})`, JSON.stringify(p.labels) === JSON.stringify(['Message teams on other computers (through Coral Hub links)', 'Share files with teams on other computers (through Coral Hub links)']) && !p.workflowHasSwitch);
            check(`${tag}: off: one status line (${p.line}) with a neutral dot and one primary button "${p.prim && p.prim.text}"`, p.line === 'Cross-team messaging is off for this team.' && p.dot === '' && p.prim.text === 'Turn on cross-team messaging' && p.prim.act === 'turn-on' && p.msg === false && p.files === false);
            check(`${tag}: How this works is closed; Save is off`, p.how === false && p.save === true);
            await click(`${P} .xt-primary`); await settle(150);
            p = await panel();
            check(`${tag}: "Turn on" ticks the switch in the form only: the line follows it, Save is on, nothing is saved yet (${p.line})`, p.msg === true && p.line === 'Cross-team messaging is on for this team.' && p.save === false && (await ev(`window.__xt.puts.length`)) === 0);
            check(`${tag}: the primary is now the Coral Hub link: the constant page, a new tab, noopener, and its name says so (${p.prim.href})`, p.prim.tag === 'A' && p.prim.href === HUB_LINKS && p.prim.target === '_blank' && /noopener/.test(p.prim.rel) && /Link a team on another Coral \(opens Coral Hub in a new tab\)/.test(p.prim.text));
            check(`${tag}: the unsaved change is guarded`, await ev(`import(${S} + 'team_working_mode.js').then(m => m.hasUnsavedTeamSettings())`));
            await click(`${P} [type=submit]`); await waitFor(`document.querySelector('${P} .xt-state').textContent === 'Saved.'`, `${tag}: saved`, 8000);
            const put1 = await ev(`window.__xt.puts[window.__xt.puts.length - 1]`);
            check(`${tag}: Save sent only what changed (${JSON.stringify(put1)}) and the server kept it`, JSON.stringify(put1) === JSON.stringify({ subscriber_id: 'Operator', cross_team_messaging: true }) && (await board('/settings')).body.cross_team_messaging === true);
            p = await panel(); check(`${tag}: after Save nothing is unsaved (Save is off, the guard is quiet)`, p.save === true && !(await ev(`import(${S} + 'team_working_mode.js').then(m => m.hasUnsavedTeamSettings())`)));
            // the files switch on its own
            await ev(`(() => { const c = document.querySelector('${P} input[name=cross_team_artifacts]'); c.checked = true; c.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); await settle(100);
            await click(`${P} [type=submit]`); await waitFor(`document.querySelector('${P} .xt-state').textContent === 'Saved.' && window.__xt.puts.length === 2`, `${tag}: files saved`, 8000);
            check(`${tag}: the files switch is saved on its own (${JSON.stringify(await ev('window.__xt.puts[1]'))})`, JSON.stringify(await ev('window.__xt.puts[1]')) === JSON.stringify({ subscriber_id: 'Operator', cross_team_artifacts: true }) && (await board('/settings')).body.cross_team_artifacts === true);
            await board('/settings', 'PUT', { subscriber_id: 'Operator', cross_team_messaging: false, cross_team_artifacts: false });

            // 2. the server's states: its words as text, the dot and the button from the state
            const base = { enforced: false, allowed: [], max_entries: 200, was_open: false, suggestions: [] };
            for (const [state, text, dot, prim, act, href] of [['waiting', 'No team on another Coral is linked to this team yet.', 'warm', 'Link a team on another Coral', '', HUB_LINKS], ['ready', '2 teams on other Corals can message this team.', 'ok', 'Link a team on another Coral', '', HUB_LINKS],
                ['off', 'Cross-team messaging is off on <b>this</b> team. Turn it on in Team settings > Messaging.', '', 'Turn on cross-team messaging', 'turn-on', null], ['stale', 'Waiting for Coral Hub: messages from other teams are refused until this computer reconnects. Last heard from Coral Hub today 14:02. Check the connection in Settings > Coral Hub.', 'bad', 'Check the Coral Hub connection', 'check-hub', null],
                ['old', 'This team was made before Coral Hub links existed, so it cannot use the relay.', 'bad', 'Create a new team', 'new-team', null]]) {
                await setState({ ...base, status: { state, text }, links: state === 'old' ? links(1) : state === 'ready' ? links(2) : [] });
                await open();
                p = await panel();
                check(`${tag}: ${state}: the server's words are shown as text (${p.line.slice(0, 50)}), the dot is "${dot}" and the primary is "${prim}"`, p.line === text && p.imgs === 0 && p.dot === dot && p.prim.text.startsWith(prim) && p.prim.act === act && (href === null || p.prim.href === href), JSON.stringify([p.line === text, p.imgs, p.dot === dot, p.prim.text.startsWith(prim), p.prim.act === act, p.line]));
                if (state === 'ready') {
                    check(`${tag}: ready: the linked teams show name, machine and short server id, with Manage in Coral Hub (${JSON.stringify(p.teams)})`, p.teams.length === 2 && p.teams[0].name === 'Team 0' && /box-0 · server srv-0 · confirmed /.test(p.teams[0].sub) && p.manage === HUB_LINKS);
                    await shot(`messaging-ready-${view.name}-${view.theme}`);
                }
                if (state === 'old') check(`${tag}: an old team shows no linked teams`, p.teams.length === 0);
                if (state === 'stale') { await click(`${P} .xt-primary`); check(`${tag}: stale: the button opens Settings > Network and Hub`, (await ev(`window.__xt.calls.includes('settings:network')`))); }
                if (state === 'old') { await ev(`window.__xt.calls.length = 0; true`); await click(`${P} .xt-primary`); check(`${tag}: old: the button opens the launch dialog on Agent Team`, JSON.stringify(await ev(`window.__xt.calls`)) === JSON.stringify(['launch', 'type:team'])); }
            }
            // stale keeps the last known rows but says they are not active: the heading must not claim they can message this team
            await setState({ ...base, status: { state: 'stale', text: 'Waiting for Coral Hub: messages from other teams are refused until this computer reconnects.' }, links: links(2) });
            await open(); p = await panel();
            check(`${tag}: stale: the rows stay, under a heading that says they are not active (${p.heading})`, p.teams.length === 2 && p.heading === 'Last known links (not active until Coral Hub reconnects)' && /refused until this computer reconnects/.test(p.line));
            await shot(`messaging-stale-${view.name}-${view.theme}`);
            await setState({ ...base, status: { state: 'ready', text: '2 teams on other Corals can message this team.' }, links: links(2) }); await open(); p = await panel();
            check(`${tag}: ready: the heading says they can message this team (${p.heading})`, p.heading === 'Linked teams that can message this one');
            // a draft is not a state: with the server saying ready, turning the switch off in the form makes the line follow the form
            await setState({ ...base, status: { state: 'ready', text: '2 teams on other Corals can message this team.' }, links: links(2) });
            await board('/settings', 'PUT', { subscriber_id: 'Operator', cross_team_messaging: true });
            await open();
            await ev(`(() => { const c = document.querySelector('${P} input[name=cross_team_messaging]'); c.checked = false; c.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); await settle(100);
            p = await panel(); check(`${tag}: a draft is not a state: with the switch unticked the line says off and the primary turns it back on (${p.line})`, p.line === 'Cross-team messaging is off for this team.' && p.prim.act === 'turn-on');
            // a server that sends no state at all
            await setState(null); await open(); p = await panel();
            check(`${tag}: no state route: the tab still works from the switch alone (${p.line})`, p.msg === true && p.line === 'Cross-team messaging is on for this team.' && p.teams.length === 0);

            // 3. the linked teams are text only, bounded, and a bad id is dropped
            const hostile = links(60).map((l, i) => i === 0 ? { server_label: '<img src=x onerror="window.__pwn=1">box', server_id: 'srv "><b>', team_label: 'Evil\u202eteam\u0000 <b>x</b>', refreshed: 'not a date' } : l);
            await setState({ ...base, status: { state: 'ready', text: 'ok' }, links: hostile }); await open(); p = await panel();
            check(`${tag}: linked teams: markup, bidi and control characters are text or gone, a bad server id is dropped, at most 50 are drawn (${p.teams.length}; ${JSON.stringify(p.teams[0])})`, p.teams.length === 50 && p.imgs === 0 && !p.pwn && !/\u202e|\u0000/.test(p.teams[0].name) && /<b>x<\/b>/.test(p.teams[0].name) && !/server srv "/.test(p.teams[0].sub) && !/confirmed/.test(p.teams[0].sub));

            // 4. How this works: closed, the id with Copy
            await setState({ ...base, status: { state: 'waiting', text: 'No team on another Coral is linked to this team yet.' }, links: [] });
            await ev(`window.__xt.cloud = { configured: true, state: 'connected', hub_url: 'https://hub.example', label: 'build-box', server_id: ${JSON.stringify(SID)}, max_mode: 'view' }; true`);
            await open();
            await click(`${P} .xt-how > summary`); await settle(100);
            p = await panel();
            check(`${tag}: How this works opens, and holds this computer's Coral Hub id (${p.sid})`, p.how === true && p.sid === SID);
            await click(`${P} [data-act=copy-id]`); await waitFor(`window.__xt.clip.length === 1`, `${tag}: the copy`, 4000);
            check(`${tag}: Copy puts exactly the id on the clipboard (${JSON.stringify(await ev('window.__xt.clip'))})`, JSON.stringify(await ev('window.__xt.clip')) === JSON.stringify([SID]));
            await shot(`messaging-how-${view.name}-${view.theme}`);
            await ev(`window.__xt.cloud = { configured: true, state: 'connected', server_id: 'bad id <b>', max_mode: 'view' }; true`); await open(); await click(`${P} .xt-how > summary`); await settle(100);
            check(`${tag}: an id that is not an id is not shown`, (await panel()).sid === '');
            await ev(`window.__xt.cloud = null; true`);

            // 5. the server's refusals are said in our words, the draft stays
            await board('/settings', 'PUT', { subscriber_id: 'Operator', cross_team_messaging: false, cross_team_artifacts: false });
            await setState(undefined); await open();
            await ev(`(() => { const c = document.querySelector('${P} input[name=cross_team_messaging]'); c.checked = true; c.dispatchEvent(new Event('change', { bubbles: true })); window.__xt.put = { status: 403, body: { code: 'owner_only', error: 'secret words' } }; return true; })()`); await settle(100);
            await click(`${P} [type=submit]`); await waitFor(`/Could not save/.test(document.querySelector('${P} .xt-state').textContent)`, `${tag}: the refusal`, 6000);
            p = await panel();
            check(`${tag}: a refusal is said in our words, never the server's, and the draft stays (${p.stateText})`, /^Could not save\. Only the account/.test(p.stateText) && !/secret words|owner_only/.test(p.stateText) && p.msg === true && p.save === false);
            // 6. settings that cannot be read
            await ev(`window.__xt.settingsGet = { status: 403, body: { code: 'mode_too_low' } }; true`); await open({ noHealth: true });
            p = await panel();
            check(`${tag}: settings that cannot be read: the tab says so in our words and has no switches (${p.unavailable})`, /^Messaging settings: /.test(p.unavailable) && p.msg === null && p.files === null);
            await ev(`window.__xt.settingsGet = null; true`);

            // 6b. through Coral Hub (the page itself is not booted as the Hub: a seam says it is): the switches are shown, disabled, with the reason, until the machine says this request may change them
            await ev(`import(${S} + 'team_cross_team_settings.js').then(m => m._setHubForTests(true)).then(() => true)`);
            await setState(null); await open(); p = await panel();
            check(`${tag}: Hub, the machine says nothing: both switches are disabled with the plain reason, no Save, no turn-on button (${p.locked})`, p.msgDisabled === true && p.filesDisabled === true && p.locked === 'This setting can only be changed on the computer itself.' && p.saveHidden === true && (!p.prim || p.prim.act !== 'turn-on'));
            await setState({ ...base, can_change: false, owner_path_live: true, status: { state: 'waiting', text: 'No team on another Coral is linked to this team yet.' }, links: [] }); await open(); p = await panel();
            check(`${tag}: Hub, a viewer who is not the owner: disabled, and the reason names the owner's way (${p.locked})`, p.msgDisabled === true && p.locked === 'This setting can only be changed on the computer itself, or by the owner through Coral Hub.');
            await shot(`messaging-hub-locked-${view.name}-${view.theme}`);
            await setState({ ...base, can_change: true, owner_path_live: true, status: { state: 'off', text: 'Cross-team messaging is off for this team.' }, links: [] }); await open(); p = await panel();
            check(`${tag}: Hub, the owner path live: the switches work and there is no locked line (${JSON.stringify([p.msgDisabled, p.locked])})`, p.msgDisabled === false && p.filesDisabled === false && p.locked === '' && p.saveHidden === false && p.prim.act === 'turn-on');
            await ev(`(() => { const c = document.querySelector('${P} input[name=cross_team_messaging]'); c.checked = true; c.dispatchEvent(new Event('change', { bubbles: true })); window.__xt.put = { status: 404, body: { error: 'not_found' } }; return true; })()`); await settle(100);
            await click(`${P} [type=submit]`); await waitFor(`/Could not save/.test(document.querySelector('${P} .xt-state').textContent)`, `${tag}: the Hub refusal`, 6000);
            p = await panel();
            check(`${tag}: Hub, a 404 on the save is the plain sentence, never a raw word (${p.stateText})`, p.stateText === 'Could not save. This setting can only be changed on the computer itself, or by the owner through Coral Hub.' && !/not_found/.test(p.stateText) && p.msg === true);
            await ev(`import(${S} + 'team_cross_team_settings.js').then(m => m._setHubForTests(null)).then(() => true)`);

            // 7. layout, keyboard, contrast
            await setState({ ...base, status: { state: 'ready', text: '2 teams on other Corals can message this team.' }, links: links(2) }); await open();
            const g = await ev(`(() => { const pn = document.querySelector('${P}'); const t = [...pn.querySelectorAll('button, a.btn, .xt-switches label, summary')].filter(e => e.getClientRects().length).map(e => Math.round(e.getBoundingClientRect().height)); return { over: Math.max(0, pn.scrollWidth - pn.clientWidth), docOver: Math.max(0, document.documentElement.scrollWidth - innerWidth), minTarget: Math.min(...t) }; })()`);
            check(`${tag}: nothing wider than the panel or the page (${g.over}, ${g.docOver})`, g.over <= 0 && g.docOver <= 0);
            if (view.phone) check(`${tag}: every control is 44 px or more (${g.minTarget})`, g.minTarget >= 44);
            const au = await auditRegion(ev, [P], { focus: [`${P} .xt-primary`, `${P} input[name=cross_team_messaging]`, `${P} .xt-how > summary`] });
            check(`${tag}: readable (4.5:1) with a focus ring (${au.checked} checked)`, au.fails.length === 0, JSON.stringify(au.fails.slice(0, 2)));
            await ev(`document.querySelector('${P} .xt-primary').focus(); true`);
            const key = async (k, code, vk, text) => { const b = { key: k, code, windowsVirtualKeyCode: vk }; await Input.dispatchKeyEvent({ type: 'keyDown', ...b, ...(text ? { text } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...b }); };
            await key('Tab', 'Tab', 9); check(`${tag}: Tab goes from the primary to the messaging switch`, (await ev(`document.activeElement.name`)) === 'cross_team_messaging');
            await key(' ', 'Space', 32, ' '); await settle(100);
            check(`${tag}: Space toggles it (off to on here) and the line follows the form, not the server's last word`, (await panel()).msg === true && /is on for this team/.test((await panel()).line));
            await board('/settings', 'PUT', { subscriber_id: 'Operator', cross_team_messaging: false, cross_team_artifacts: false });

            // 8. the real server (Backend's patch 1, #5572): a team whose board id is the old kind (this suite's) gets the "old" status, and the tab shows its words and the one way out (QA #5584)
            await setState(undefined); await open(); p = await panel();
            check(`${tag}: the real server's status for an old-kind board is shown as its own words with the "Create a new team" button (${p.line})`, /^This team was made before Coral Hub links existed/.test(p.line) && p.prim && p.prim.act === 'new-team' && p.dot === 'bad');
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed}/${results.length} checks passed`);
        if (failed) process.exitCode = 1;
    } finally { await removeBoards(ev, [TEAM]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
