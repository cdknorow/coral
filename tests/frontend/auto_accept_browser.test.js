// #3182 Auto-accept, the dashboard side (the server-side answering has its own Go tests):
//   - the AUTO chip on an agent's row while it is on, and in the selected agent's header
//   - the menu switch: turning it ON asks first (Cancel focused; Cancel, Escape and the backdrop keep it off and send
//     nothing), Enable posts .../auto-accept/enable {acknowledged: true}; turning it OFF (.../disable) is immediate
//   - agent types Coral cannot answer for show the switch disabled with the reason
//   - when Coral pauses it (the same prompt kept coming back) the user is told once
//   - its events are in Activity with their own icon and filter
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Input, Emulation } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const key = async (k, code, keyCode) => {
        await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code, windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code, windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode });
    };
    const S = `(window.__coralStatic||'/static/')`;
    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        // Headless Chrome can mark this tab hidden after a modal <dialog> is reopened, which stops animation frames
        // (the list renders on the next frame). Keep it the focused, visible page, as it is for a person using it.
        await Emulation.setFocusEmulationEnabled({ enabled: true });
        await Page.bringToFront();
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await coralReady();

        // Three agents from a stub list; PUT .../auto-accept answers from a stub and is recorded.
        await ev(`(async () => {
            const real = window.fetch.bind(window);
            window.__puts = [];
            window.fetch = async (url, opts) => {
                const u = String(url);
                const m = /\\/api\\/sessions\\/live\\/([^/]+)\\/auto-accept\\/(enable|disable)$/.exec(u);
                if (m && opts && opts.method === 'POST') {
                    const body = JSON.parse(opts.body);
                    window.__puts.push({ id: decodeURIComponent(m[1]), action: m[2], body });
                    return new Response(JSON.stringify({ auto_accept: m[2] === 'enable', auto_accept_paused_reason: null }), { headers: { 'Content-Type': 'application/json' } });
                }
                if (u.endsWith('/api/auto-accept/disable-all') && opts && opts.method === 'POST') {
                    window.__puts.push({ id: '*', action: 'disable-all' });
                    return new Response(JSON.stringify({ stopped: 2 }), { headers: { 'Content-Type': 'application/json' } });
                }
                return real(url, opts);
            };
            const { state } = await import(${S}+'state.js');
            const render = await import(${S}+'render.js');
            const aa = await import(${S}+'auto_accept.js');
            window.__state = state; window.__render = render; window.__aa = aa;
            state.liveSessions = [
                { session_id: 'aa-off', name: 'aa-off', display_name: 'Builder', agent_type: 'claude', working_directory: '/tmp/a', auto_accept: false, auto_accept_supported: true },
                { session_id: 'aa-on', name: 'aa-on', display_name: 'Runner', agent_type: 'claude', working_directory: '/tmp/a', auto_accept: true, auto_accept_supported: true, auto_accept_source: 'hub', auto_accept_until: Math.floor(Date.now() / 1000) + 40 * 60 },
                { session_id: 'aa-pi', name: 'aa-pi', display_name: 'Pi agent', agent_type: 'pi', working_directory: '/tmp/a', auto_accept: false, auto_accept_supported: false, auto_accept_unsupported_code: 'agent_type_unsupported' },
            ];
            render.renderLiveSessions(state.liveSessions);
            return true;
        })()`);
        const row = id => `document.querySelector('.session-group-item[data-session-id="${id}"]')`;
        // A row's menu is drawn when it opens (#4611): opening it first is part of reaching its item.
        const menuItem = id => `(window.closeSidebarKebabs(), ${row(id)}.querySelector('.sidebar-kebab-btn').click(), ${row(id)}.querySelector('.auto-accept-menu-item'))`;

        console.log('1. AUTO chip on the row while auto-accept is on');
        assert.equal(await ev(`!!${row('aa-on')}.querySelector('.auto-accept-chip')`), true, 'on: chip');
        assert.equal(await ev(`!!${row('aa-off')}.querySelector('.auto-accept-chip')`), false, 'off: no chip');
        const onItem = await ev(`(() => { const b = ${menuItem('aa-on')}; return [b.getAttribute('role'), b.getAttribute('aria-checked'), b.querySelector('.auto-accept-state').textContent, b.title, ${row('aa-on')}.querySelector('.auto-accept-chip').title]; })()`);
        assert.deepEqual(onItem.slice(0, 2), ['menuitemcheckbox', 'true']);
        assert.match(onItem[2], /^On until \d\d?:\d\d/, 'the time limit is shown');
        assert.match(onItem[3], /until \d\d?:\d\d(\s?[AP]M)?, turned on from the Coral Hub/, 'source and limit on the switch');
        assert.match(onItem[4], /Auto-accept is on \(until .*turned on from the Coral Hub\)/, 'and on the chip');
        assert.deepEqual(await ev(`(() => { const b = ${menuItem('aa-off')}; return [b.getAttribute('aria-checked'), b.querySelector('.auto-accept-state').textContent]; })()`), ['false', 'Off']);
        console.log('   ✓ chip and switch state');

        console.log('2. An agent type Coral cannot answer for: switch disabled with the reason');
        const pi = await ev(`(() => { const b = ${menuItem('aa-pi')}; return { disabled: b.disabled, title: b.title }; })()`);
        assert.equal(pi.disabled, true);
        assert.match(pi.title, /Not available for pi yet/);
        console.log('   ✓ disabled, "Not available for pi yet"');

        console.log('3. Turning it on asks first; Cancel, Escape and the backdrop keep it off');
        const open = `(() => { ${menuItem('aa-off')}.click(); return true; })()`;
        const dialog = `(() => { const d = document.querySelector('dialog.auto-accept-confirm'); return d && d.open ? { focus: document.activeElement?.textContent, title: d.querySelector('h3').textContent, text: d.querySelector('#auto-accept-confirm-text').textContent, agent: d.querySelector('.auto-accept-confirm-agent').textContent, modal: d.matches(':modal') } : null; })()`;
        await ev(open);
        const d = await waitFor(dialog, 'the warning dialog');
        assert.equal(d.focus, 'Cancel', 'Cancel has focus');
        assert.equal(d.modal, true, 'a modal dialog');
        assert.equal(d.title, 'Turn on auto-accept for this agent?');
        assert.equal(d.agent, 'Builder', 'the dialog names the agent as the sidebar does (display name)');
        // The warning promises no safety net for risky actions (#3182, operator): it says they can be approved.
        assert.match(d.text, /which can include risky actions such as deleting files, force-pushing, running sudo or changing files outside the agent’s folder/);
        assert.match(d.text, /Questions and plan approvals still wait for you\. It turns off when the agent restarts or wakes\./);
        assert.doesNotMatch(d.text, /risky actions[^.]*still wait/, 'no claim that risky actions wait');
        // The risk stands out and what still waits has its own line (#3218), in the operator's exact words.
        const parts = await ev(`(() => { const t = document.getElementById('auto-accept-confirm-text'); const ps = [...t.querySelectorAll('p')];
            return { strong: t.querySelector('strong')?.textContent, weight: Number(getComputedStyle(t.querySelector('strong')).fontWeight), lines: ps.map(p => p.textContent), whole: t.textContent }; })()`);
        assert.equal(parts.strong, 'which can include risky actions such as deleting files, force-pushing, running sudo or changing files outside the agent’s folder.');
        assert.ok(parts.weight >= 600, 'the risk sentence is bold');
        assert.equal(parts.lines.length, 2, 'two lines');
        assert.equal(parts.lines[1], 'Questions and plan approvals still wait for you. It turns off when the agent restarts or wakes.');
        assert.equal(parts.whole, 'Coral will answer “Yes” to this agent’s permission prompts for you, which can include risky actions such as deleting files, force-pushing, running sudo or changing files outside the agent’s folder. Only turn this on for a task you trust, ideally in a sandbox or a disposable worktree. Questions and plan approvals still wait for you. It turns off when the agent restarts or wakes.', 'the operator’s words, unchanged');
        assert.deepEqual(await ev(`(() => { const sel = document.getElementById('auto-accept-duration'); return [sel.value, [...sel.options].map(o => o.text)]; })()`),
            ['', ['No time limit', '15 minutes', '30 minutes', '1 hour', '2 hours', '4 hours', '8 hours']], 'local: no time limit by default');
        await ev(`[...document.querySelectorAll('dialog.auto-accept-confirm button')].find(b => b.textContent === 'Cancel').click(); true`);
        await waitFor(`!document.querySelector('dialog.auto-accept-confirm')`, 'closed by Cancel');
        await ev(open); await waitFor(dialog, 'the dialog again');
        await key('Escape', 'Escape', 27);
        await waitFor(`!document.querySelector('dialog.auto-accept-confirm')`, 'closed by Escape');
        await ev(open); await waitFor(dialog, 'the dialog once more');
        await ev(`(() => { const d = document.querySelector('dialog.auto-accept-confirm'); const r = d.getBoundingClientRect();
            d.dispatchEvent(new MouseEvent('click', { bubbles: true, clientX: r.left - 5, clientY: r.top - 5 })); return true; })()`);
        await waitFor(`!document.querySelector('dialog.auto-accept-confirm')`, 'closed by the backdrop');
        assert.deepEqual(await ev(`window.__puts`), [], 'nothing sent while it was declined');
        assert.equal(await ev(`window.__state.liveSessions.find(s => s.session_id === 'aa-off').auto_accept`), false, 'still off');
        console.log('   ✓ Cancel focused; Cancel / Escape / backdrop: no request, stays off');

        console.log('4. Enable sends the acknowledgement; the row gets its chip');
        await ev(open); await waitFor(dialog, 'the dialog to enable');
        await ev(`(() => { document.getElementById('auto-accept-duration').value = '1800'; [...document.querySelectorAll('dialog.auto-accept-confirm button')].find(b => b.textContent === 'Enable auto-accept').click(); return true; })()`);
        await waitFor(`window.__puts.length === 1`, 'the PUT');
        assert.deepEqual(await ev(`window.__puts[0]`), { id: 'aa-off', action: 'enable', body: { session_id: 'aa-off', acknowledged: true, duration_s: 1800 } });
        await waitFor(`!!${row('aa-off')}?.querySelector('.auto-accept-chip')`, 'the chip on the enabled row');
        assert.equal(await ev(`${menuItem('aa-off')}.getAttribute('aria-checked')`), 'true');
        console.log('   ✓ POST .../enable {acknowledged: true, duration_s: 1800}; chip shown');

        console.log('5. Header chip for the selected agent; a click turns it off at once');
        await ev(`(() => { window.__state.currentSession = { type: 'live', name: 'aa-on', session_id: 'aa-on', agent_type: 'claude' }; window.__aa.syncAutoAcceptHeader(); return true; })()`);
        const chip = await ev(`(() => { const c = document.getElementById('auto-accept-header-chip'); return c && { tag: c.tagName, text: c.textContent, label: c.getAttribute('aria-label'), inHeader: !!c.closest('.terminal-header-actions') }; })()`);
        assert.deepEqual({ ...chip, text: chip.text.replace(/\d+m$/, 'Nm') }, { tag: 'BUTTON', text: 'AUTO Nm', label: 'Auto-accept is on. Turn it off', inHeader: true }, 'countdown in the last hour');
        assert.ok(/^AUTO (39|40)m$/.test(chip.text), chip.text);
        await ev(`document.getElementById('auto-accept-header-chip').click(); true`);
        await waitFor(`window.__puts.length === 2`, 'the PUT to turn it off');
        assert.equal(await ev(`!!document.querySelector('dialog.auto-accept-confirm')`), false, 'no dialog for turning it off');
        assert.deepEqual(await ev(`window.__puts[1]`), { id: 'aa-on', action: 'disable', body: { session_id: 'aa-on' } });
        await waitFor(`!document.getElementById('auto-accept-header-chip') && !${row('aa-on')}?.querySelector('.auto-accept-chip')`, 'chips gone');
        console.log('   ✓ header chip; off without a dialog; chips removed');

        console.log('6. Coral turned it off (a code): told once, in words, and the reason is on the switch');
        await ev(`(() => { const s = window.__state.liveSessions.find(s => s.session_id === 'aa-on'); s.auto_accept = false; s.auto_accept_until = null; s.auto_accept_source = null; s.auto_accept_paused_code = 'repeat_loop';
            document.querySelectorAll('.toast').forEach(t => t.remove());
            window.__aa.noteAutoAcceptPauses(window.__state.liveSessions); window.__aa.noteAutoAcceptPauses(window.__state.liveSessions);
            window.__render.renderLiveSessions(window.__state.liveSessions); return true; })()`);
        const toasts = await ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent).filter(t => /Auto-accept turned off/.test(t))`);
        assert.equal(toasts.length, 1, `one notice (${JSON.stringify(toasts)})`);
        assert.match(toasts[0], /"Runner": the same prompt repeated 5 times in a minute/);
        assert.equal(await ev(`${menuItem('aa-on')}.title`), 'Turned off: the same prompt repeated 5 times in a minute');
        assert.equal(await ev(`window.__aa.pausedText('some_future_code')`), 'Coral turned it off', 'an unknown code still reads sensibly');
        await ev(`(() => { document.querySelectorAll('.toast').forEach(t => t.remove()); const s = window.__state.liveSessions.find(x => x.session_id === 'aa-off'); const was = { ...s }; s.auto_accept = false; s.auto_accept_paused_code = 'stopped_by_owner'; window.__aa.noteAutoAcceptPauses(window.__state.liveSessions); Object.assign(s, was); return true; })()`);
        assert.equal((await ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent)`)).filter(t => /turned off for "Builder"/.test(t)).length, 0, 'no notice for your own stop (e.g. after a reload)');
        console.log('   ✓ one toast; reason on the switch');

        console.log('7. Activity shows the auto-accept events with their own icon and filter');
        await ev(`(async () => {
            const as = await import(${S}+'agentic_state.js');
            if (!document.getElementById('agentic-state-events')) document.body.insertAdjacentHTML('beforeend', '<div id="event-search-bar"></div><div id="agentic-state-events"></div>');
            window.__state.currentAgentEvents = [
                { id: 5, event_type: 'auto_accept_enabled', summary: 'Auto-accept turned on from the dashboard', created_at: '2026-10-13T10:00:05Z' },
                { id: 4, event_type: 'auto_accept_cleared', summary: 'Auto-accept turned off: the agent restarted', created_at: '2026-10-13T10:00:04Z' },
                { id: 3, event_type: 'auto_accept_paused', summary: 'Auto-accept paused: the same prompt repeated 5 times in a minute', created_at: '2026-10-13T10:00:03Z' },
                { id: 2, event_type: 'auto_accept_skipped', summary: 'Not auto-accepted: unrecognised prompt', created_at: '2026-10-13T10:00:02Z' },
                { id: 1, event_type: 'auto_accept', summary: 'Auto-approved: Bash npm test', created_at: '2026-10-13T10:00:01Z' },
            ];
            as.renderEventTimeline();
            return true;
        })()`);
        const items = await ev(`[...document.querySelectorAll('#agentic-state-events .event-item')].map(e => ({ icon: e.querySelector('.event-icon').className, text: e.querySelector('.event-summary').textContent }))`);
        assert.deepEqual(items.map(i => i.text), ['Auto-accept turned on from the dashboard', 'Auto-accept turned off: the agent restarted', 'Auto-accept paused: the same prompt repeated 5 times in a minute', 'Not auto-accepted: unrecognised prompt', 'Auto-approved: Bash npm test']);
        assert.match(items[4].icon, /tool-auto-accept\b/);
        assert.match(items[2].icon, /tool-auto-accept-paused/);
        assert.match(items[3].icon, /tool-auto-accept-skipped/);
        assert.match(items[1].icon, /tool-auto-accept-skipped/);
        assert.match(items[0].icon, /tool-auto-accept-paused/);
        await ev(`document.getElementById('ev-g-autoaccept').click(); true`); // the bar's Show Auto-accept box (#4798)
        assert.equal(await ev(`document.querySelectorAll('#agentic-state-events .event-item').length`), 0, 'the Auto-accept filter hides them');
        await ev(`document.getElementById('ev-g-autoaccept').click(); true`);
        assert.equal(await ev(`document.querySelectorAll('#agentic-state-events .event-item').length`), 5, 'and unticking again shows them');
        console.log('   ✓ five events, own icons, filterable');

        console.log('8. Stop all auto-accept: shown only while some agent has it on; stops them all');
        await ev(`window.__coral.toggleTopBarSettings(); window.__coral.closeTopBarSettings(); true`); // the top-bar menu is built on first open (#4500)
        await ev(`(() => { for (const s of window.__state.liveSessions) { s.auto_accept = s.session_id !== 'aa-pi'; s.auto_accept_paused_code = null; } window.__aa.noteAutoAcceptPauses(window.__state.liveSessions); return true; })()`);
        assert.equal(await ev(`document.getElementById('stop-all-auto-accept').hidden`), false, 'shown with agents on');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); window.stopAllAutoAccept(); true`);
        await waitFor(`window.__puts.some(p => p.action === 'disable-all')`, 'the disable-all request');
        await waitFor(`window.__state.liveSessions.every(s => !s.auto_accept) && document.getElementById('stop-all-auto-accept').hidden`, 'all off and the item hidden');
        const after = await ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent)`);
        assert.ok(after.some(t => /Auto-accept stopped for 2 agents/.test(t)), JSON.stringify(after));
        await ev(`window.__aa.noteAutoAcceptPauses(window.__state.liveSessions); true`);
        assert.equal((await ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent)`)).filter(t => /turned off for/.test(t)).length, 0, 'no per-agent "turned off" toasts for your own stop-all');
        console.log('   ✓ one request, all off, item hidden, one summary toast');

        console.log('9. The Coral Hub dialog requires a time limit (1 hour by default)');
        await ev(`window.__aa.confirmAutoAccept('Hub agent', () => {}, () => {}, { hub: true }); true`);
        assert.deepEqual(await ev(`(() => { const sel = document.getElementById('auto-accept-duration'); return [sel.value, sel.options[0].text, [...sel.options].some(o => o.value === '')]; })()`), ['3600', '15 minutes', false]);
        await ev(`[...document.querySelectorAll('dialog.auto-accept-confirm button')].find(b => b.textContent === 'Cancel').click(); true`);
        console.log('   ✓ 1 hour preselected, no "No time limit"');

        console.log('10. Settings: "Coral Hub can turn on auto-accept" shows only with Launch, on by default, saves locally');
        const cloudRow = await ev(`(async () => {
            window.__coral.buildAllLazy(); // Settings is built on first open (#4500)
            if (!document.getElementById('settings-cloud-allow-auto-accept')) return 'missing';
            const real = window.fetch;
            window.__cloudPuts = [];
            let st = { configured: true, state: 'connected', hub_url: 'https://hub.example', max_mode: 'interact', allow_hub_auto_accept: true };
            window.__setCloud = next => { st = { ...st, ...next }; };
            window.fetch = async (url, opts) => {
                const u = String(url);
                if (u.endsWith('/api/cloud/status')) return new Response(JSON.stringify(st), { headers: { 'Content-Type': 'application/json' } });
                if (u.endsWith('/api/cloud/allow-auto-accept')) { const body = JSON.parse(opts.body); window.__cloudPuts.push({ method: opts.method, body }); st.allow_hub_auto_accept = body.allow; return new Response(JSON.stringify(st), { headers: { 'Content-Type': 'application/json' } }); }
                return real(url, opts);
            };
            const cc = await import(${S}+'cloud_connect.js');
            cc.initCloudConnect();
            await cc.loadCloudStatus();
            const box = document.getElementById('settings-cloud-allow-auto-accept');
            const row = document.getElementById('settings-cloud-auto-accept-row');
            const interact = { shown: !row.hidden };
            window.__setCloud({ max_mode: 'launch' });
            await cc.loadCloudStatus();
            const launch = { shown: !row.hidden, checked: box.checked };
            window.__setCloud({ allow_hub_auto_accept: undefined });
            await cc.loadCloudStatus();
            launch.defaultChecked = box.checked;
            box.checked = false; box.dispatchEvent(new Event('change'));
            return { interact, launch };
        })()`);
        assert.notEqual(cloudRow, 'missing', 'the switch is in the Settings markup');
        assert.deepEqual(cloudRow.interact, { shown: false }, 'hidden below Launch');
        assert.deepEqual(cloudRow.launch, { shown: true, checked: true, defaultChecked: true }, 'shown at Launch, on (also when the server does not say)');
        await waitFor(`window.__cloudPuts.length === 1`, 'the local PUT');
        assert.deepEqual(await ev(`window.__cloudPuts[0]`), { method: 'PUT', body: { allow: false } });
        console.log('   ✓ hidden below Launch; on by default; PUT /api/cloud/allow-auto-accept {allow}');

        // Who last changed it (#5847): status allow_hub_auto_accept_changed {by, user, at}, beside the switch, said in words, nothing before a change
        const lines = await ev(`(async () => {
            const cc = await import(${S}+'cloud_connect.js');
            const line = document.getElementById('settings-cloud-auto-accept-changed'), box = document.getElementById('settings-cloud-allow-auto-accept');
            const read = async next => { window.__setCloud({ max_mode: 'launch', allow_hub_auto_accept_changed: undefined, ...next }); await cc.loadCloudStatus(); return { text: line.textContent, shown: !line.hidden }; };
            const out = {};
            out.never = await read({ allow_hub_auto_accept: true });
            out.hubOn = await read({ allow_hub_auto_accept: true, allow_hub_auto_accept_changed: { by: 'hub', user: 'hub-user-3f2a', at: '2026-10-21T20:15:00Z' } });
            out.gap = parseFloat(getComputedStyle(line).marginTop); // the pane may not be shown here, so the spacing is read from the style, not measured
            out.hubOff = await read({ allow_hub_auto_accept: false, allow_hub_auto_accept_changed: { by: 'hub', user: 'hub-user-3f2a', at: '2026-10-21T20:15:00Z' } });
            out.local = await read({ allow_hub_auto_accept: false, allow_hub_auto_accept_changed: { by: 'local', at: '2026-10-21T20:15:00Z' } });
            out.nasty = await read({ allow_hub_auto_accept: true, allow_hub_auto_accept_changed: { by: 'hub', user: '<img src=x onerror=1>\u202e\u0007Eve', at: 'not a time' } });
            out.badBy = await read({ allow_hub_auto_accept: true, allow_hub_auto_accept_changed: { by: 'someone', at: '2026-10-21T20:15:00Z' } });
            out.belowLaunch = await read({ max_mode: 'interact', allow_hub_auto_accept_changed: { by: 'hub', user: 'u', at: '2026-10-21T20:15:00Z' } });
            out.described = box.getAttribute('aria-describedby');
            out.img = document.querySelectorAll('#settings-cloud-auto-accept-changed img').length;
            return out;
        })()`);
        assert.deepEqual(lines.never, { text: '', shown: false }, 'nothing is said before it was changed');
        assert.match(lines.hubOn.text, /^Turned on from Coral Hub by hub-user-3f2a, .+2026.*$/); assert.equal(lines.hubOn.shown, true);
        assert.match(lines.hubOff.text, /^Turned off from Coral Hub by hub-user-3f2a, /);
        assert.match(lines.local.text, /^Turned off on this computer, /);
        assert.ok(/^Turned on from Coral Hub by <img src=x onerror=1>Eve$/.test(lines.nasty.text) && lines.img === 0, 'a hub user label is text, stripped of control and bidi characters: ' + JSON.stringify(lines.nasty));
        assert.deepEqual(lines.badBy, { text: '', shown: false }, 'an unknown "by" says nothing');
        assert.equal(lines.belowLaunch.shown, false, 'hidden with its row below Launch');
        assert.ok(lines.gap >= 6, 'the last-changed line has its own spacing under the description, not butted against it: ' + lines.gap);
        assert.ok(/settings-cloud-auto-accept-desc/.test(lines.described) && /settings-cloud-auto-accept-changed/.test(lines.described), 'the switch is described by the line too: ' + lines.described);
        console.log('   ✓ the last-changed line: hub user or this computer, on or off, nothing before a change, text only');

        console.log('10b. Settings: "Hide tokens from Coral Hub viewers who can only look" (#3560, #3958) is on by default and saves locally');
        const redactRow = await ev(`(async () => {
            const box = document.getElementById('settings-cloud-redact-tokens');
            if (!box) return 'missing';
            const prev = window.fetch;
            window.__redactPuts = [];
            window.fetch = async (url, opts) => {
                if (String(url).endsWith('/api/cloud/redact-tokens')) {
                    const body = JSON.parse(opts.body);
                    window.__redactPuts.push({ method: opts.method, body });
                    window.__setCloud({ redact_tokens: body.redact });
                }
                return prev(url, opts);
            };
            const cc = await import(${S}+'cloud_connect.js');
            window.__setCloud({ redact_tokens: undefined });
            await cc.loadCloudStatus();
            const byDefault = box.checked;
            window.__setCloud({ redact_tokens: false });
            await cc.loadCloudStatus();
            const off = box.checked;
            window.__setCloud({ redact_tokens: true });
            await cc.loadCloudStatus();
            box.checked = false; box.dispatchEvent(new Event('change'));
            return { byDefault, off, describedBy: box.getAttribute('aria-describedby') };
        })()`);
        assert.notEqual(redactRow, 'missing', 'the switch is in the Settings markup');
        assert.deepEqual(redactRow, { byDefault: true, off: false, describedBy: 'settings-cloud-redact-desc' });
        await waitFor(`window.__redactPuts.length === 1`, 'the local PUT');
        assert.deepEqual(await ev(`window.__redactPuts[0]`), { method: 'PUT', body: { redact: false } });
        console.log('   ✓ on by default (also when the server does not say); PUT /api/cloud/redact-tokens {redact}');

        console.log('11. A refused enable says why, in words (403 codes)');
        const refusals = await ev(`(async () => {
            const real = window.fetch; const out = {};
            for (const code of ['agent_identity', 'no_dashboard_provenance', 'hub_auto_accept_disabled']) {
                window.fetch = async (url, opts) => String(url).includes('/auto-accept/enable')
                    ? new Response(JSON.stringify({ error: 'refused', code }), { status: 403, headers: { 'Content-Type': 'application/json' } })
                    : real(url, opts);
                document.querySelectorAll('.toast').forEach(t => t.remove());
                const s = window.__state.liveSessions.find(x => x.session_id === 'aa-off'); s.auto_accept = false;
                const p = window.__coral.toggleAutoAccept('aa-off', 'aa-off');
                await new Promise(r => setTimeout(r, 0));
                [...document.querySelectorAll('dialog.auto-accept-confirm button')].find(b => b.textContent === 'Enable auto-accept').click();
                await p;
                out[code] = [...document.querySelectorAll('.toast')].map(t => t.textContent).join(' | ');
            }
            window.fetch = real;
            return out;
        })()`);
        assert.match(refusals.agent_identity, /only be turned on by you, in the dashboard, not by an agent/);
        assert.match(refusals.no_dashboard_provenance, /only be turned on from the dashboard/);
        assert.match(refusals.hub_auto_accept_disabled, /turned off Coral Hub auto-accept there/);
        assert.equal(await ev(`window.__state.liveSessions.find(x => x.session_id === 'aa-off').auto_accept`), false, 'still off after a refusal');
        assert.equal(await ev(`window.__aa.pausedText('coral_restarted')`), 'Coral restarted (an auto-accept turned on from the Coral Hub ends then)');
        console.log('   ✓ three refusal codes in words; coral_restarted worded');

        console.log('12. Popout (no agent menu): the header control is the switch, asks first, then shows AUTO');
        // The real server's feed knows no such agent: keep it from replacing this step's fixture mid-step.
        const { identifier: noFeed } = await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            const W = window.WebSocket;
            window.WebSocket = class extends W { constructor(u, p) { if (/\\/ws\\/coral/.test(String(u))) { super('ws://127.0.0.1:9/'); return; } super(u, p); } };
        })()` });
        await Page.navigate({ url: BASE + '/agent/aaaaaaaa-1111-2222-3333-444444444444' }); await Page.loadEventFired();
        await Page.removeScriptToEvaluateOnNewDocument({ identifier: noFeed });
        await waitFor(`!!document.querySelector('.terminal-header-actions')`, 'the popout header');
        const pop = await ev(`(async () => {
            const real = window.fetch; window.__popPuts = [];
            window.fetch = async (url, opts) => {
                const u = String(url);
                if (/\\/auto-accept\\/(enable|disable)$/.test(u) && opts && opts.method === 'POST') {
                    const on = u.endsWith('/enable'); window.__popPuts.push({ on, body: JSON.parse(opts.body) });
                    return new Response(JSON.stringify({ auto_accept: on, auto_accept_until: null, auto_accept_source: on ? 'local' : null, auto_accept_paused_code: null }), { headers: { 'Content-Type': 'application/json' } });
                }
                return real(url, opts);
            };
            const { state } = await import(${S}+'state.js');
            const aa = await import(${S}+'auto_accept.js');
            const popout = await import(${S}+'popout.js');
            const sess = { session_id: 'aaaaaaaa-1111-2222-3333-444444444444', name: 'pop', display_name: 'Popped', agent_type: 'claude', working_directory: '/tmp/p', auto_accept: false, auto_accept_supported: true };
            state.liveSessions = [sess]; state.currentSession = { type: 'live', name: 'pop', session_id: sess.session_id, agent_type: 'claude' };
            aa.syncAutoAcceptHeader();
            const b = document.getElementById('auto-accept-header-chip');
            return { isPopout: popout.isPopout(), text: b && b.textContent, pressed: b && b.getAttribute('aria-pressed'), cls: b && b.className };
        })()`);
        assert.equal(pop.isPopout, true, 'the page is in popout mode');
        assert.deepEqual([pop.text, pop.pressed, pop.cls], ['Auto-accept', 'false', 'auto-accept-header-off'], 'an off switch in the header');
        await ev(`document.getElementById('auto-accept-header-chip').click(); true`);
        await waitFor(`!!document.querySelector('dialog.auto-accept-confirm')?.open`, 'the warning in the popout');
        assert.equal(await ev(`document.activeElement?.textContent`), 'Cancel', 'Cancel focused');
        await ev(`[...document.querySelectorAll('dialog.auto-accept-confirm button')].find(b => b.textContent === 'Enable auto-accept').click(); true`);
        await waitFor(`window.__popPuts.length === 1`, 'the enable request');
        assert.deepEqual(await ev(`window.__popPuts[0]`), { on: true, body: { session_id: 'aaaaaaaa-1111-2222-3333-444444444444', acknowledged: true } });
        await waitFor(`document.getElementById('auto-accept-header-chip')?.textContent === 'AUTO' && document.getElementById('auto-accept-header-chip').getAttribute('aria-pressed') === 'true'`, 'the AUTO chip after enabling');
        // A popout may not have its agent in the live list: the switch must still know it is on.
        await ev(`(async () => { const { state } = await import(${S}+'state.js'); state.liveSessions = []; return true; })()`);
        await ev(`document.getElementById('auto-accept-header-chip').click(); true`);
        await waitFor(`window.__popPuts.length === 2 && document.getElementById('auto-accept-header-chip')?.textContent === 'Auto-accept'`, 'off again, back to the switch');
        assert.equal(await ev(`window.__popPuts[1].on`), false);
        console.log('   ✓ switch in the popout header: asks, enables, AUTO, turns off');

        console.log('PASS auto_accept_browser');
    } finally {
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
})().catch(err => { console.error(err); process.exit(1); });
