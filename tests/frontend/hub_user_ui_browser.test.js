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
            document.querySelector('.team-open-btn').click();
            return true;
        })()`);
        await waitFor(`document.querySelectorAll('#board-panel-msgs .mb-message').length === 3`, 'the team chat');
        const chat = await ev(`[...document.querySelectorAll('#board-panel-msgs .mb-message')].map(m => ({ body: m.querySelector('.mb-message-body').textContent.trim(), badge: !!m.querySelector('.mb-hub-user-badge') }))`);
        assert.deepEqual(chat, [
            { body: 'from the hub', badge: true },
            { body: 'looks like one', badge: false },
            { body: 'an agent', badge: false },
        ]);
        console.log("   ✓ team chat: the badge on the Hub user's message only");

        console.log('2. Settings: "Let Coral Hub act as the Operator"');
        const s = await ev(`(async () => {
            const real = window.fetch;
            const json = (b, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } });
            let st = { configured: true, state: 'connected', hub_url: 'https://hub.example', max_mode: 'interact' };
            let op = { enabled: false, linked: true }, opStatus = 200, putFails = false;
            window.__opPuts = [];
            window.__ctl = { cloud: v => { st = { ...st, ...v }; }, op: v => { op = { ...op, ...v }; }, opStatus: v => { opStatus = v; }, putFails: v => { putFails = v; } };
            window.fetch = async (url, opts = {}) => {
                const u = String(url);
                if (u.endsWith('/api/cloud/status')) return json(st);
                if (u.endsWith('/api/cloud/disconnect')) { st = { ...st, state: 'idle', label: '' }; op = { enabled: false, linked: false }; return json(st); }
                if (u.endsWith('/api/system/hub-operator')) {
                    if (opStatus !== 200) return json({ error: 'not found' }, opStatus);
                    if ((opts.method || 'GET') === 'PUT') {
                        const body = JSON.parse(opts.body); window.__opPuts.push(body);
                        if (putFails) return json({ error: 'not allowed now' }, 409);
                        op = { ...op, enabled: body.enabled }; return json(op);
                    }
                    return json(op);
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
        assert.equal(await ev(`${box}.checked`), false, 'off by default');
        assert.match(await ev(`document.getElementById('settings-cloud-hub-operator-desc').textContent`), /Off by default.*Operator.*coral\.log.*disconnect/s, 'the plain warning');

        await ev(`(() => { const b = ${box}; b.checked = true; b.dispatchEvent(new Event('change')); return true; })()`);
        await waitFor(`window.__opPuts.length === 1`, 'the PUT');
        assert.deepEqual(await ev(`window.__opPuts[0]`), { enabled: true });
        await waitFor(`${box}.checked === true && ${msg}.hidden`, 'turned on');
        console.log('   ✓ shown while linked, off by default, turning it on PUTs {enabled:true}');

        await ev(`window.__ctl.putFails(true); (() => { const b = ${box}; b.checked = false; b.dispatchEvent(new Event('change')); return true; })()`);
        await waitFor(`window.__opPuts.length === 2 && !${msg}.hidden`, 'the refusal');
        assert.equal(await ev(`${box}.checked`), true, 'a failed change is put back');
        assert.match(await ev(`${msg}.textContent`), /not allowed now/);
        await ev(`window.__ctl.putFails(false); true`);
        console.log('   ✓ a refused change is put back and explained');

        await ev(`document.getElementById('settings-cloud-disconnect').click(); true`);
        await waitFor(`${row}.hidden`, 'the row after disconnect (the server clears it with the link)');
        console.log('   ✓ disconnecting reads it again: no link, off, hidden');

        await ev(`(async () => { window.__ctl.cloud({ state: 'connected' }); window.__ctl.op({ enabled: false, linked: true }); window.__ctl.opStatus(404);
            (await import(${S}+'cloud_connect.js')).loadCloudStatus(); return true; })()`);
        await waitFor(`${row}.hidden && document.getElementById('settings-cloud-status').textContent.length > 0`, 'status loaded');
        assert.equal(await ev(`${row}.hidden`), true, 'a 404 (through the hub) hides it');
        console.log('   ✓ hidden where the route answers 404');
        console.log('PASS hub_user_ui_browser');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
