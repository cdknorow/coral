// Settings > Network and Hub, the groups (#6579 part 2; Security's #6559, UI Accessibility's #6558, the Orchestrator's fold of #6629 LOW 4):
//   the groups are sections with an h3 (the headings list reads Remote access, [Setup code], Coral Hub, Manage other computers), Coral Hub first on a phone;
//   Remote access turning on is an explicit act (it waits for Save and says what it then lets in); the setup code is there only while Remote access is on or a Coral Hub link is being made, is not on the screen
//   until "Show setup code" (a button that says whether it is open), is spelled for a screen reader, is copied with "Copy setup code", and hides itself after a minute; "Manage other computers from this one" says
//   a hub has full control of those computers; an Operator opt-out is still said on the card when the computer is not linked. 1280 and 390 px; both themes for contrast. Routes are stood in at the page's fetch.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const text = sel => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); return e ? e.textContent.replace(/\\s+/g, ' ').trim() : null; })()`);
    const vis = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && e.checkVisibility(); })()`;
    const headings = () => ev(`[...document.querySelectorAll('#settings-pane-network h3')].filter(h => h.checkVisibility()).map(h => h.textContent.replace(/\\s+/g, ' ').trim())`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            const at = `at ${w}px: `;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(async () => {
                window.__coral.buildAllLazy(); const real = window.fetch; window.__timers = [];
                const so = window.setTimeout; window.setTimeout = (f, ms, ...a) => { if (ms === 60000) window.__timers.push(f); return so(f, ms, ...a); };
                Object.defineProperty(navigator, 'clipboard', { value: { writeText: () => Promise.resolve() }, configurable: true });
                window.__st = { configured: true, state: 'idle', hub_url: 'https://hub.example', max_mode: 'view', max_mode_saved: true, redact_tokens: true, hub_cross_team_settings: true };
                window.__op = { linked: false, disabled: false }; window.__setup = { status: 200, body: { code: 'H2XR-HS16', expires_in: 3540 } };
                const json = (b, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } });
                window.fetch = async (url, opts = {}) => { const u = String(url);
                    if (u.endsWith('/api/cloud/status')) return json(window.__st);
                    if (u.includes('/api/system/hub-operator')) return json(window.__op);
                    if (u.includes('/api/auth/setup-code/show')) return json(window.__setup.body, window.__setup.status);
                    return real(url, opts); };
                const cc = await import(${S} + 'cloud_connect.js'); window.__cc = cc; cc.initCloudConnect(); await cc.loadCloudStatus();
                await window.showSettingsModal('network'); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none' && document.getElementById('settings-cloud-connect').getClientRects().length > 0`, 'Settings on screen', 8000);
            await waitFor(`document.getElementById('settings-remote-access-enabled').dataset.saved !== undefined`, 'the access state read', 8000);
            await installAudit(ev);
            const remote = () => ev(`(() => { const c = document.getElementById('settings-remote-access-enabled'); c.click(); return c.checked; })()`);

            // 1. the groups: headings, and the order (Coral Hub first on a phone)
            assert.deepEqual(await headings(), ['Remote access', 'Coral Hub', 'Manage other computers from this one'], at + 'the headings list, with nothing that could use a setup code yet');
            const order = await ev(`(() => { const t = id => document.getElementById(id).getBoundingClientRect().top; return { hubFirst: t('net-group-hub') < t('net-group-access'), accessFirst: t('net-group-access') < t('net-group-hub'), manageLast: t('net-group-manage') > t('net-group-hub') && t('net-group-manage') > t('net-group-access') }; })()`);
            if (mobile) assert.equal(order.hubFirst, true, at + 'Coral Hub comes first on a phone'); else assert.equal(order.accessFirst, true, at + 'Remote access first on a desktop');
            assert.equal(order.manageLast, true, at + 'Manage other computers last');
            assert.equal(await ev(`document.getElementById('settings-remote-access-enabled').getAttribute('aria-labelledby')`), 'net-access-title', at + 'the switch is named by its heading');
            assert.equal(await ev(vis('#net-group-setup')), false, at + 'no Setup code group while Remote access is off and no link is being made');

            // 2. Manage other computers: its wording
            assert.equal(await text('#settings-hub-mode-line'), 'Adds a Servers tab, with full control of those computers.', at + 'a hub has full control of what it manages, said where the switch is');
            assert.equal(await ev(`document.getElementById('settings-hub-mode-enabled').getAttribute('aria-labelledby')`), 'settings-hub-mode-label');
            assert.match(await ev(`document.getElementById('settings-hub-mode-enabled').getAttribute('aria-describedby')`), /settings-hub-mode-line/);

            // 3. Remote access turning on: a note, it waits for Save
            assert.equal(await text('#net-remote-note'), '', at + 'no note while nothing is changed');
            assert.equal(await remote(), true);
            assert.match(await text('#net-remote-note'), /^Applies when you press Save settings\. Other devices on your network can then reach Coral; they need your password or a setup code\.$/, at + 'turning it on says what it lets in and that it waits for Save');
            assert.equal(await ev(`document.getElementById('net-remote-note').getAttribute('role')`), 'status');
            assert.equal(await ev(`document.getElementById('net-remote-note').hasAttribute('hidden')`), false, at + 'the live region is always in the page');
            await waitFor(`!document.getElementById('net-group-setup').hidden`, 'the Setup code group once Remote access is on', 8000);
            assert.deepEqual(await headings(), ['Remote access', 'Setup code', 'Coral Hub', 'Manage other computers from this one'].filter(x => true), at + 'Setup code is a heading under Remote access');
            await ev(`document.getElementById('settings-remote-access-enabled').click(); true`);
            assert.equal(await text('#net-remote-note'), '', at + 'back to what is saved: no note');
            assert.equal(await ev(vis('#net-group-setup')), false, at + 'and the group goes');
            await remote();
            await waitFor(`!document.getElementById('net-group-setup').hidden`, 'the group again', 8000);

            // 4. the code is not on the screen until asked for, then spelled, copied, and hidden by itself
            assert.equal(await ev(vis('#settings-setup-code-box')), false);
            assert.equal(await ev(`document.getElementById('settings-pane-network').innerText.includes('H2XR')`), false, at + 'the code is nowhere on the screen');
            assert.equal(await ev(`document.getElementById('settings-setup-code').textContent`), '', at + 'nor in the page');
            assert.equal(await ev(`document.getElementById('settings-setup-code-reveal').getAttribute('aria-expanded')`), 'false');
            await ev(`document.getElementById('settings-setup-code-reveal').focus(); document.getElementById('settings-setup-code-reveal').click(); true`);
            assert.equal(await ev(`document.getElementById('settings-setup-code-reveal').getAttribute('aria-expanded')`), 'true');
            assert.equal(await text('#settings-setup-code-reveal'), 'Hide setup code');
            assert.equal(await ev(`document.activeElement.id`), 'settings-setup-code-reveal', at + 'focus stays on the button');
            assert.equal(await text('#settings-setup-code'), 'H2XR-HS16');
            assert.equal(await text('#settings-setup-code-spoken'), 'Setup code: H 2 X R, H S 1 6', at + 'the code is spelled for a screen reader');
            assert.equal(await ev(`document.getElementById('settings-setup-code').getAttribute('aria-hidden')`), 'true', at + 'and the visible code is not read twice');
            assert.equal(await ev(`document.getElementById('settings-setup-code-copy').getAttribute('aria-label')`), 'Copy setup code');
            assert.match(await text('#settings-setup-code-hint'), /^The code is below\. .*Valid for about 59 more minutes; it works once\. It hides itself in a minute\./);
            await ev(`document.getElementById('settings-setup-code-copy').click(); true`);
            await waitFor(`document.getElementById('settings-setup-code-hint').textContent === 'Copied.'`, 'Copied', 5000);
            await ev(`document.getElementById('settings-setup-code-reveal').scrollIntoView({ block: 'center' }); true`);
            await shot(`net-pane-setup-shown-${w}`);
            for (const theme of ['dark', 'light']) { await setTheme(ev, theme); const c = await auditRegion(ev, ['#settings-pane-network']); assert.ok(c.checked && !c.fails.length, at + theme + ' contrast: ' + JSON.stringify(c.fails)); }
            await setTheme(ev, 'dark');
            assert.equal(await ev(`window.__timers.length >= 1`), true, at + 'a 60 s timer was set when the code opened');
            await ev(`window.__timers[window.__timers.length - 1](); true`); // the minute is over
            assert.equal(await ev(vis('#settings-setup-code-box')), false, at + 'the code hid itself');
            assert.equal(await ev(`document.getElementById('settings-setup-code').textContent`), '', at + 'and left the page');
            assert.equal(await text('#settings-setup-code-hint'), 'Setup code hidden.');
            assert.equal(await text('#settings-setup-code-reveal'), 'Show setup code');
            assert.equal(await ev(`document.getElementById('settings-setup-code-reveal').getAttribute('aria-expanded')`), 'false');

            // 5. a code that is open goes when the group goes
            await ev(`document.getElementById('settings-setup-code-reveal').click(); true`);
            assert.equal(await ev(vis('#settings-setup-code-box')), true);
            await ev(`document.getElementById('settings-remote-access-enabled').click(); true`);
            assert.equal(await ev(vis('#net-group-setup')), false);
            assert.equal(await ev(`document.getElementById('settings-setup-code').textContent`), '', at + 'the group went, the code with it');
            // ... but a Coral Hub link being made offers it even with Remote access off
            await ev(`(async () => { window.__st = { ...window.__st, state: 'enrolling', user_code: 'WXYZ-1234', verification_url: 'https://hub.example/c' }; await window.__cc.loadCloudStatus(); return true; })()`);
            await waitFor(`!document.getElementById('net-group-setup').hidden`, 'the group while a link is being made', 8000);
            await ev(`(async () => { window.__st = { ...window.__st, state: 'idle', user_code: undefined }; await window.__cc.loadCloudStatus(); return true; })()`);
            await waitFor(`document.getElementById('net-group-setup').hidden`, 'and gone when it is over', 8000);
            // no code for this page (a password is set, another computer ...): no group, whatever the switch says
            await ev(`window.__setup = { status: 409, body: { code: 'password_set' } }; document.getElementById('settings-remote-access-enabled').click(); document.dispatchEvent(new CustomEvent('coral-settings-group', { detail: { id: 'network' } })); true`);
            await waitFor(`document.getElementById('net-group-setup').hidden`, 'no group without a code', 8000);
            await ev(`window.__setup = { status: 200, body: { code: 'H2XR-HS16', expires_in: 3540 } }; document.getElementById('settings-remote-access-enabled').click(); true`);

            // 6. an Operator opt-out stays said on the card when the computer is not linked
            await ev(`(async () => { window.__op = { linked: false, disabled: true }; const p = document.getElementById('settings-cloud')._cloudPanel; p.enrolled = undefined; await window.__cc.loadCloudStatus(); return true; })()`);
            await waitFor(`document.getElementById('settings-cloud-operator-line').textContent === 'Coral Hub may not act as the Operator.' && !document.getElementById('settings-cloud-operator-line').hidden`, 'the opt-out said while unlinked', 8000);
            await ev(`(async () => { window.__op = { linked: false, disabled: false }; const p = document.getElementById('settings-cloud')._cloudPanel; p.enrolled = undefined; await window.__cc.loadCloudStatus(); return true; })()`);
            await waitFor(`document.getElementById('settings-cloud-operator-line').hidden`, 'and nothing said when not opted out', 8000);
            await ev(`window.scrollTo(0, 0); document.getElementById('settings-pane-network').scrollTop = 0; true`);
            await shot(`net-pane-${w}`);
            assert.equal(await ev(`document.documentElement.scrollWidth <= innerWidth`), true, at + 'no sideways scroll');
        }
        console.log('PASS net pane: groups with headings, Remote access explicit, the setup code on request');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
