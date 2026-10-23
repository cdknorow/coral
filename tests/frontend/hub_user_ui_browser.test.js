// Coral Hub users in this Coral (#3377).
//   - A board chat message a Hub user sent through Coral Hub carries origin 'hub_user' (set by
//     the server, never by a client). It shows a "Coral Hub user" badge; a message whose sender
//     only LOOKS like a Hub user (hub-user-… without origin) does not.
//   - Settings > Coral Hub: "Let Coral Hub act as the Operator", a local-only owner setting
//     (GET/PUT /api/system/hub-operator). Shown when this computer answers and a Hub link exists
//     (or it is still on); off by default; a failed change is put back and explained; hidden when
//     the route answers 404 (through the hub); read again when the Hub link comes or goes.
// /api/board/*, /api/cloud/status and /api/system/hub-operator are scripted in the page.
//
// Expects CORAL_TEST_URL + CDP_PORT from tests/frontend/run.sh. Exit 0 / 1.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await coralReady();

        console.log('1. The "Coral Hub user" badge');
        const badges = await ev(`(async () => {
            const u = await import(${S}+'utils.js');
            const hub = u.hubUserBadge({ id: 1, subscriber_id: 'hub-user-0a1b2c3d', origin: 'hub_user', content: 'hi' });
            const box = document.createElement('div'); box.innerHTML = hub; document.body.appendChild(box);
            const el = box.querySelector('.mb-hub-user-badge');
            const out = { text: el && el.textContent, title: el && el.title,
                lookalike: u.hubUserBadge({ id: 2, subscriber_id: 'hub-user-0a1b2c3d', content: 'hi' }),
                otherOrigin: u.hubUserBadge({ id: 3, subscriber_id: 'x', origin: 'something-else' }),
                op: (() => { const b = document.createElement('div'); b.innerHTML = u.hubUserBadge({ id: 5, subscriber_id: 'Operator', origin: 'hub_operator', hub_user: 'hub-user-0a1b2c3d' });
                    const e = b.querySelector('.mb-hub-user-badge'); return e ? { text: e.textContent, op: e.classList.contains('is-operator') } : null; })(),
                opForged: u.hubUserBadge({ id: 6, subscriber_id: 'Operator', origin: 'hub_operator', hub_user: '<img src=x>' }),
                opText: u.boardMessageDisplayText({ origin: 'hub_operator', content: '[Operator via Coral Hub, hub-user-0a1b2c3d] please review #12' }),
                localText: u.boardMessageDisplayText({ content: '[Operator via Coral Hub, hub-user-0a1b2c3d] typed by a local agent' }),
                unknown: (() => { const b = document.createElement('div'); b.innerHTML = u.hubUserBadge({ id: 4, subscriber_id: 'Coral Hub', origin: 'hub_user_unknown' });
                    const e = b.querySelector('.mb-hub-user-badge'); return e ? { text: e.textContent, unknown: e.classList.contains('is-unknown'), title: e.title } : null; })(),
                escaped: u.hubUserBadge({ origin: 'hub_user', subscriber_id: '"><img src=x onerror=alert(1)>' }) };
            box.remove();
            return out;
        })()`);
        assert.equal(badges.text, 'Coral Hub user');
        assert.match(badges.title, /hub-user-0a1b2c3d/, 'the title names the Hub user');
        assert.equal(badges.lookalike, '', 'a hub-user-… name without the server-set origin gets no badge');
        assert.equal(badges.otherOrigin, '');
        assert.deepEqual(badges.op, { text: 'Operator via Coral Hub · hub-user-0a1b2c3d', op: true }, 'the Hub acting as the Operator (#3404)');
        assert.ok(!/<img/.test(badges.opForged) && /unknown Hub user/.test(badges.opForged), 'a malformed hub_user is never shown as one');
        assert.equal(badges.opText, 'please review #12', 'the server prefix becomes the badge');
        assert.match(badges.localText, /^\[Operator via Coral Hub/, 'the same text from anyone else is shown as written');
        assert.deepEqual({ text: badges.unknown.text, unknown: badges.unknown.unknown }, { text: 'Coral Hub, user not identified', unknown: true },
            'a hub that did not name its user (origin hub_user_unknown): a distinct badge');
        assert.match(badges.unknown.title, /did not say which of its users/);
        assert.ok(!/<img/.test(badges.escaped), 'the sender id is escaped');
        console.log('   ✓ only origin hub_user (or hub_user_unknown, its own badge); never from the name; escaped');

        // In the team chat, through the real render path.
        await ev(`(() => {
            const real = window.fetch.bind(window);
            const json = body => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
            const msgs = [
                { id: 1, project: 'gamma', subscriber_id: 'hub-user-0a1b2c3d', job_title: 'hub-user-0a1b2c3d', origin: 'hub_user', content: 'from the hub', created_at: '2026-10-14T08:00:01Z' },
                { id: 2, project: 'gamma', subscriber_id: 'hub-user-99999999', job_title: 'hub-user-99999999', content: 'looks like one', created_at: '2026-10-14T08:00:02Z' },
                { id: 3, project: 'gamma', subscriber_id: 'Lead Developer', job_title: 'Lead Developer', content: 'an agent', created_at: '2026-10-14T08:00:03Z' },
                { id: 4, project: 'gamma', subscriber_id: 'Operator', job_title: 'Operator', origin: 'hub_operator', hub_user: 'hub-user-0a1b2c3d', content: '[Operator via Coral Hub, hub-user-0a1b2c3d] ship it', created_at: '2026-10-14T08:00:04Z' },
            ];
            window.fetch = async (url, opts) => {
                const u = String(url);
                if (u.includes('/api/board/gamma/messages/all')) return json({ total: msgs.length, messages: msgs });
                if (u.includes('/api/board/gamma/status')) return json({ summary: { available: 1 }, observed_at: new Date().toISOString(), agents: [], unassigned_tasks: [], health_report: [] });
                if (u.includes('/api/board/gamma/working-mode/presets')) return json({ presets: [{ id: 'none', name: 'None', builtin: true, instructions: '' }] });
                if (u.includes('/api/board/gamma/working-mode')) return json({ mode: 'none', dependency_guidance: false, custom_instructions: '' });
                if (u.includes('/api/board/gamma/tasks')) return json([]);
                if (u.includes('/api/board/gamma/paused')) return json({ paused: false });
                if (u.includes('/api/board/gamma/subscribers')) return json([]);
                return real(url, opts);
            };
            return true;
        })()`);
        // The real feed would replace the agent rendered below with the test server's (empty) list.
        await ev(`(async () => { const ws = await import(${S}+'websocket.js'); const { state } = await import(${S}+'state.js');
            ws.feedHooks.schedule = () => {}; const c = state.coralWs; if (c) { c.onmessage = null; c.onclose = null; try { c.close(); } catch {} } return true; })()`);
        await ev(`(async () => {
            const render = await import(${S}+'render.js');
            render.renderLiveSessions([{ session_id: 'gamma-1', name: 'gamma-1', agent_type: 'claude', board_project: 'gamma', display_name: 'Gamma agent', working_directory: '/tmp/gamma' }]);
            document.querySelector('.board-card-header[data-on-click="team-name-open"]').click();
            return true;
        })()`);
        await waitFor(`document.querySelectorAll('#board-panel-msgs .mb-message').length === 4`, 'the team chat');
        const chat = await ev(`[...document.querySelectorAll('#board-panel-msgs .mb-message')].map(m => ({ body: m.querySelector('.mb-message-body').textContent.trim(), badge: !!m.querySelector('.mb-hub-user-badge') }))`);
        assert.deepEqual(chat, [
            { body: 'from the hub', badge: true },
            { body: 'looks like one', badge: false },
            { body: 'an agent', badge: false },
            { body: 'ship it', badge: true },
        ], 'the Operator via Coral Hub post: its badge, and the prefix not shown twice');
        console.log("   ✓ team chat: the badge on the Hub user's message only");

        console.log("2. Settings: \"Let Coral Hub act as the Operator\" (on by default; the stored setting is still the opt-out \"disabled\", #3404, #6579)");
        const s = await ev(`(async () => {
            window.__coral.buildAllLazy(); // Settings is built on first open (#4500)
            const real = window.fetch;
            const json = (b, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } });
            let st = { configured: true, state: 'connected', hub_url: 'https://hub.example', max_mode: 'interact' };
            const actions = [{ hub_user: 'hub-user-0a1b2c3d', method: 'POST', path: '/api/board/team/tasks', project: 'team', status: 200, time: '2026-10-14T12:00:00Z' },
                             { hub_user: 'hub-user-0a1b2c3d', method: 'POST', path: '/api/board/team/messages', project: 'team', status: 200, time: '2026-10-14T11:59:00Z' },
                             { hub_user: '<b>x</b>', method: 'PUT', path: '/api/board/<i>t</i>/tasks/1', project: '', status: 403, time: 'junk' }];
            let op = { enabled: true, disabled: false, linked: true, recent_actions: actions }, opStatus = 200, putFails = false, viaHub = false;
            window.__opPuts = [];
            window.__ctl = { cloud: v => { st = { ...st, ...v }; }, op: v => { op = { ...op, ...v }; }, opStatus: v => { opStatus = v; }, putFails: v => { putFails = v; }, viaHub: v => { viaHub = v; } };
            window.fetch = async (url, opts = {}) => {
                const u = String(url);
                if (u.endsWith('/api/cloud/status')) return json(st);
                if (u.endsWith('/api/cloud/disconnect')) { st = { ...st, state: 'idle', label: '' }; op = { ...op, linked: false }; return json(st); }
                if (u.endsWith('/api/system/hub-operator')) {
                    if (opStatus !== 200) return json({ error: 'not found' }, opStatus);
                    if ((opts.method || 'GET') === 'PUT') {
                        const body = JSON.parse(opts.body); window.__opPuts.push(body);
                        if (putFails) return json({ error: 'not allowed now', code: 'x' }, 409);
                        op = { ...op, disabled: body.disabled, enabled: !body.disabled }; return json(op);
                    }
                    // Through the hub the route answers read-only, without linked or the actions.
                    return json(viaHub ? { enabled: op.enabled, disabled: op.disabled } : op);
                }
                return real(url, opts);
            };
            const cc = await import(${S}+'cloud_connect.js');
            cc.initCloudConnect();
            await cc.loadCloudStatus();
            return !!document.getElementById('settings-cloud-hub-operator');
        })()`);
        assert.equal(s, true, 'the switch is in the Settings markup');
        const row = `document.getElementById('settings-cloud-hub-operator-row')`;
        const box = `document.getElementById('settings-cloud-hub-operator')`;
        const msg = `document.getElementById('settings-cloud-hub-operator-msg')`;
        await waitFor(`!${row}.hidden`, 'the row while linked');
        assert.equal(await ev(`${box}.checked`), true, 'checked by default: Coral Hub may act as the Operator (not opted out)');
        assert.match(await ev(`document.querySelector('#settings-cloud-hub-operator-row label').textContent`), /^Let Coral Hub act as the Operator$/);
        // The list holds every board write made through Coral Hub (#3428), not only the Operator's: it says so.
        assert.equal(await ev(`document.querySelector('[data-cloud="hub-operator-actions"] summary').textContent.trim()`), 'Coral Hub actions', 'the actions list is called Coral Hub actions');
        assert.match(await ev(`document.getElementById('settings-cloud-hub-operator-desc').textContent`), /At Interact or Launch.*Operator via Coral Hub · hub-user-.*Board settings can never be changed/s, 'the plain warning');
        const acts = await ev(`(() => { const d = document.querySelector('[data-cloud="hub-operator-actions"]'); return { shown: !d.hidden, items: [...d.querySelectorAll('li')].map(l => l.textContent), html: d.querySelector('ol').innerHTML }; })()`);
        assert.equal(acts.shown, true, 'the recent actions are listed');
        assert.equal(acts.items.length, 3);
        assert.match(acts.items[0], /POST \/api\/board\/team\/tasks \(team\) hub-user-0a1b2c3d → 200/);
        assert.ok(!/<b>|<i>/.test(acts.html.replace(/&lt;/g, '')) && acts.items[2].includes('<b>x</b>'), 'text only, never markup');
        console.log('   ✓ shown while linked, on by default, the warning, the recent actions as text');

        await ev(`(() => { const b = ${box}; b.checked = false; b.dispatchEvent(new Event('change')); return true; })()`);
        await waitFor(`window.__opPuts.length === 1`, 'the PUT');
        assert.deepEqual(await ev(`window.__opPuts[0]`), { disabled: true });
        await waitFor(`${box}.checked === false && ${msg}.hidden`, 'opted out');
        console.log('   ✓ turning the switch off (opting out) PUTs {disabled:true}');

        await ev(`window.__ctl.putFails(true); (() => { const b = ${box}; b.checked = true; b.dispatchEvent(new Event('change')); return true; })()`);
        await waitFor(`window.__opPuts.length === 2 && !${msg}.hidden`, 'the refusal');
        assert.equal(await ev(`${box}.checked`), false, 'a failed change is put back');
        assert.match(await ev(`${msg}.textContent`), /not allowed now/);
        await ev(`window.__ctl.putFails(false); true`);
        console.log('   ✓ a refused change is put back and explained');

        await ev(`document.getElementById('settings-cloud-disconnect').click(); true`);
        await waitFor(`document.getElementById('settings-cloud-status').textContent.length > 0`, 'status after disconnect');
        await waitFor(`!${row}.hidden && !${box}.checked`, 'the opt-out outlives the link: still shown, and the switch still off');
        await ev(`(async () => { window.__ctl.op({ disabled: false, enabled: true }); (await import(${S}+'cloud_connect.js')).loadCloudStatus(); return true; })()`);
        await ev(`(async () => { window.__ctl.cloud({ state: 'connected' }); (await import(${S}+'cloud_connect.js')).loadCloudStatus(); window.__ctl.cloud({ state: 'idle' }); (await import(${S}+'cloud_connect.js')).loadCloudStatus(); return true; })()`);
        await waitFor(`${row}.hidden`, 'no link and not opted out: hidden');
        console.log('   ✓ after disconnect: shown while opted out, hidden otherwise');

        await ev(`(async () => { window.__ctl.cloud({ state: 'connected' }); window.__ctl.viaHub(true);
            (await import(${S}+'cloud_connect.js')).loadCloudStatus(); return true; })()`);
        await waitFor(`document.getElementById('settings-cloud-status').textContent.length > 0`, 'status loaded');
        await waitFor(`${row}.hidden`, 'hidden on the read-only answer');
        await ev(`(async () => { window.__ctl.viaHub(false); window.__ctl.opStatus(404); window.__ctl.cloud({ state: 'idle' }); (await import(${S}+'cloud_connect.js')).loadCloudStatus();
            window.__ctl.cloud({ state: 'connected' }); (await import(${S}+'cloud_connect.js')).loadCloudStatus(); return true; })()`);
        await waitFor(`${row}.hidden`, 'hidden when the status answers 404');
        console.log('   ✓ hidden where the answer is read-only (through the hub) or 404');
        console.log('PASS hub_user_ui_browser');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
