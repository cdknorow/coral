// The Servers tab's "Updating" badge for a hub relaunch (#5355; coral-hub's spec #71333 with the section 4 correction #71360), behind the update flag. The hub's typed status reply is read for the machines that
// are online, and only the strict top-level `relaunching: true` shows the word Updating (constants of the page: no text from the reply is ever shown, a hostile machine name in the same payload never reaches the
// badge). Fifteen replies (fixtures/relaunch_status/): the badge shows for 04, 05 and 14 (an older last_result does not end it), not for the other twelve (the hub refusing as a whole, a string, a number, null, an
// array, a nested flag, the flag dropped with this relaunch's own last_result). Failing reads (401, 403, 404, 503, a network error) show nothing and no error. Reads happen when the tab opens and when a machine
// comes back online, never for an offline machine and not on every poll; the dashboard that sent the Relaunch keeps the badge across the machine's offline gap for at most five minutes and drops it at the first
// read with no flag. The badge is not a control, is named by the spec's sentence after the visible word, adds no height to the row, and does not animate. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const DIR = path.join(__dirname, 'fixtures', 'relaunch_status');
const CASES = fs.readdirSync(DIR).filter(f => f.endsWith('.json')).sort().map(f => ({ name: f.replace(/\.json$/, ''), ...JSON.parse(fs.readFileSync(path.join(DIR, f), 'utf8')) }));
const TIP = 'Coral on this machine is restarting into an update. It reconnects on its own.';

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the offline gap and the return are each seen by one poll tick of the list (5 s), and a poll that has not yet run leaves nothing to wait for: the two waits in section 5 stay fixed (a wait on the read itself returned before the tick had seen the state)
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await installAudit(ev);
        await ev(`(() => { const R = window.fetch; window.__calls = []; window.__reply = {}; window.__servers = [];
            const json = (b, s = 200) => Promise.resolve(new Response(typeof b === 'string' ? b : JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin);
                if (url.pathname === '/api/servers') return json({ servers: window.__servers });
                const m = /^\\/api\\/remote\\/([^/]+)\\/api\\/system\\/update$/.exec(url.pathname);
                if (m) { window.__calls.push(m[1]); const r = window.__reply[m[1]]; if (r === 'network') return Promise.reject(new TypeError('network')); if (!r) return json({}, 404); return json(r.body, r.status); }
                const rl = /^\\/api\\/remote\\/([^/]+)\\/api\\/system\\/update\\/relaunch$/.exec(url.pathname);
                if (rl) { window.__calls.push('POST ' + rl[1]); return json({ ok: true, operation: 'op' }, 202); }
                return R(u, o); };
            const host = document.createElement('div'); host.id = 'rb-host'; document.body.appendChild(host); return true; })()`);
        const srv = (id, label, online) => ({ id, label, online, access_mode: 'launch', local_mode: 'launch', created_at: '2026-10-01T00:00:00Z', last_seen: '2026-10-20T08:00:00Z' });
        const open = async (servers, replies) => {
            await ev(`window.__calls.length = 0; window.__servers = ${JSON.stringify(servers)}; window.__reply = ${JSON.stringify(replies)}; true`);
            await ev(`import(${S} + 'cloud_servers.js').then(m => m.renderCloudServersInto(document.getElementById('rb-host')))`);
        };
        const badges = () => ev(`[...document.querySelectorAll('#rb-host .cs-row')].map(r => ({ id: r.dataset.id, badge: (r.querySelector('.server-relaunch-badge') || {}).textContent || null }))`);
        const close = () => ev(`import(${S} + 'cloud_servers.js').then(m => { m.hideCloudServers(); return true; })`);

        console.log('1. flag off: nothing is read, nothing shown');
        await ev(`localStorage.removeItem('coral-updates-ui'); true`);
        await open([srv('m1', 'Alpha', true)], { m1: { status: 200, body: { relaunching: true } } });
        check('flag off: no read, no badge', (await ev('window.__calls.length')) === 0 && (await badges())[0].badge === null);
        await close();

        await ev(`localStorage.setItem('coral-updates-ui', 'on'); true`);
        console.log('2. the fifteen hub replies');
        for (const c of CASES) {
            await open([srv('m1', 'Alpha', true)], { m1: { status: c.status, body: c.body } });
            await waitFor(`window.__calls.length >= 1`, `the read for ${c.name}`, 4000);
            await ev(`new Promise(r => setTimeout(r, 60))`);
            const b = (await badges())[0].badge;
            check(`${c.name}: the badge is ${c.badge ? 'shown as "Updating"' : 'not shown'} (${b})`, c.badge ? b === 'Updating' : b === null);
            if (c.badge) {
                const d = await ev(`(() => { const e = document.querySelector('#rb-host .server-relaunch-badge'); return { role: e.getAttribute('role'), name: e.getAttribute('aria-label'), title: e.title, html: e.outerHTML, tab: e.tabIndex, focusable: e.matches('a, button, [tabindex]:not([tabindex="-1"])'), anim: getComputedStyle(e).animationName }; })()`);
                check(`${c.name}: named by the spec's sentence, not a control, no animation, nothing of the payload in it`, d.name === 'Updating: ' + TIP && d.name.startsWith('Updating') && d.title === TIP && !d.focusable && d.anim === 'none' && !/<img|onerror|Dana|rows/.test(d.html));
            }
            check(`${c.name}: no error shown and no script ran from the payload`, (await ev(`!document.querySelector('#rb-host [role=alert]') && !window.__pwned`)));
            await close();
        }

        console.log('3. failing reads show nothing, no error');
        for (const [what, reply] of [['401', { status: 401, body: { error: 'sign in' } }], ['403', { status: 403, body: { error: 'no' } }], ['404', { status: 404, body: { error: 'x' } }], ['503', { status: 503, body: { relaunching: true } }], ['502', { status: 502, body: { relaunching: true } }], ['a network error', 'network'], ['no body', { status: 200, body: '' }], ['not JSON', { status: 200, body: 'relaunching: true' }]]) {
            await open([srv('m1', 'Alpha', true)], { m1: reply });
            await waitFor(`window.__calls.length >= 1`, `the read (${what})`, 4000); await ev(`new Promise(r => setTimeout(r, 60))`);
            check(`${what}: no badge, no alert`, (await badges())[0].badge === null && (await ev(`!document.querySelector('#rb-host [role=alert]')`)));
            await close();
        }

        console.log('4. which machines are read, and when');
        await open([srv('on1', 'Online one', true), srv('off1', 'Offline one', false), srv('on2', 'Online two', true)], { on1: { status: 200, body: { relaunching: true } }, off1: { status: 200, body: { relaunching: true } }, on2: { status: 200, body: { relaunching: false } } });
        await waitFor(`window.__calls.length >= 2`, 'the reads', 4000); await ev(`new Promise(r => setTimeout(r, 60))`);
        const calls1 = (await ev('window.__calls.slice().sort()'));
        check(`only the online machines are read, once each (${JSON.stringify(calls1)})`, JSON.stringify(calls1) === '["on1","on2"]');
        check(`the badge is on the one the hub flagged (${JSON.stringify(await badges())})`, JSON.stringify(await badges()) === JSON.stringify([{ id: 'off1', badge: null }, { id: 'on1', badge: 'Updating' }, { id: 'on2', badge: null }]) /* the table is sorted by name */);
        // the list polls every 5 s: a machine that comes back online is read, the others are not read again
        await ev(`window.__servers = ${JSON.stringify([srv('on1', 'Online one', true), srv('off1', 'Offline one', true), srv('on2', 'Online two', true)])}; true`);
        await waitFor(`JSON.stringify(window.__calls.slice().sort()) === '["off1","on1","on2"]' && ((document.querySelector('#rb-host .cs-row[data-id="off1"] .server-relaunch-badge') || {}).textContent === 'Updating')`, 'the poll to read the machine that came back online and its badge to show', 9000); // the list polls every 5 s: wait for the read, not for 5.6 s
        const calls2 = (await ev('window.__calls.slice().sort()'));
        check(`a poll later: only the machine that came back online was read (${JSON.stringify(calls2)})`, JSON.stringify(calls2) === '["off1","on1","on2"]');
        check('and its badge is on', (await badges()).find(b => b.id === 'off1').badge === 'Updating');
        await close();

        console.log('5. the dashboard that sent the Relaunch keeps the badge across the offline gap, for at most five minutes');
        await open([srv('r1', 'Relaunched', true)], { r1: { status: 200, body: { relaunching: true } } });
        await waitFor(`document.querySelector('#rb-host .server-relaunch-badge')`, 'the badge', 4000);
        // a relaunch sent from here (202), the machine then goes offline: the hub cannot be read, the badge stays
        await ev(`window.__calls.length = 0; window.__reply = { r1: { status: 200, body: {} } }; import(${S} + 'update_api.js').then(m => m.requestRelaunch('ack-1', { server: 'r1' }))`);
        await waitFor(`window.__calls.includes('POST r1')`, 'the 202', 4000); await ev(`new Promise(r => setTimeout(r, 80))`);
        const afterSend = (await badges())[0].badge;
        check(`a 202 for a machine behind the hub: the read at once said no flag, so the badge went (a mirror of the read, ${afterSend})`, afterSend === null);
        await ev(`window.__reply = { r1: { status: 200, body: { relaunching: true } } }; true`);
        await ev(`import(${S} + 'relaunch_badge.js').then(m => { m.noteRelaunchSent('r1'); return true; })`);
        await waitFor(`!!document.querySelector('#rb-host .server-relaunch-badge')`, 'the badge after a send with a flag', 4000);
        await ev(`window.__servers = ${JSON.stringify([srv('r1', 'Relaunched', false)])}; window.__reply = {}; true`); await settle(5600);
        check('the machine is offline (cannot be read): the sender still shows Updating', (await badges())[0].badge === 'Updating' && (await ev(`window.__calls.filter(c => c === 'r1').length`)) >= 1);
        const past = await ev(`import(${S} + 'relaunch_badge.js').then(m => [m.badgeShown('r1', Date.now() + 4 * 60000), m.badgeShown('r1', Date.now() + 5 * 60000 + 1000)])`);
        check(`shown at four minutes, gone at five (${JSON.stringify(past)})`, past[0] === true && past[1] === false);
        // back online with no flag on its first read: gone
        await ev(`window.__calls.length = 0; window.__reply = { r1: { status: 200, body: { enabled: true } } }; window.__servers = ${JSON.stringify([srv('r1', 'Relaunched', true)])}; true`); await settle(5600);
        check('back online, no flag on the first read: the badge is gone', (await badges())[0].badge === null);
        await close();

        console.log('6. layout and contrast');
        await open([srv('l1', 'Alpha', true), srv('l2', 'Beta', true)], { l1: { status: 200, body: { relaunching: true } }, l2: { status: 200, body: { relaunching: false } } });
        await waitFor(`!!document.querySelector('#rb-host .server-relaunch-badge')`, 'the badge', 4000);
        const hts = await ev(`[...document.querySelectorAll('#rb-host .cs-title')].map(e => Math.round(e.getBoundingClientRect().height))`);
        check(`the title line is as tall with the badge as without (${hts})`, Math.abs(hts[0] - hts[1]) <= 1);
        for (const theme of ['dark', 'light']) {
            await setTheme(ev, theme);
            const au = await auditRegion(ev, ['#rb-host .cs-title']);
            check(`${theme}: 4.5:1 text contrast (${JSON.stringify((au.fails || []).slice(0, 2))})`, (au.fails || []).length === 0);
        }
        await close();
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await client.close(); process.exit(failed ? 1 : 0);
    } catch (e) { console.error('FAIL', e && e.stack || e); await client.close().catch(() => {}); process.exit(1); }
})();
