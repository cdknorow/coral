// The setup code where a person is (#6410, Security's #6408 rank 1 option A; #6579 part 2): Settings > Network and Hub offers the one-time code (the one `coral setup-code` prints) to a dashboard the server says is signed
// in on this computer, as a "Show setup code" button in a group that is there only while Remote access is on; the page reads the code only while the pane is in view, shows it only when asked, and keeps it nowhere; the group is
// not there when the server has none for this page (a password is set, another computer, through Coral Hub, not signed in). At 1280 and 390 px. The route's answer is stood in at the page's fetch (its handler and refusals
// are in internal/auth); what the group shows and does is net_pane_browser's.
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(() => { if (!window.__innerFetch) window.__innerFetch = window.fetch; const real = window.__innerFetch; window.__show = { status: 200, body: { code: 'H2XR-HS16', expires_in: 3540 }, reads: 0 };
                window.fetch = (u, o = {}) => { if (String(u).includes('/api/auth/setup-code/show')) { window.__show.reads++; return Promise.resolve(new Response(JSON.stringify(window.__show.body), { status: window.__show.status, headers: { 'content-type': 'application/json' } })); } return real(u, o); };
                return true; })()`);
            const group = () => ev(`(() => { const g = document.getElementById('net-group-setup'); const code = document.getElementById('settings-setup-code'); return { shown: !!g && !g.hidden && g.getClientRects().length > 0, code: code ? code.textContent : null, boxShown: !!document.getElementById('settings-setup-code-box') && !document.getElementById('settings-setup-code-box').hidden, hint: (document.getElementById('settings-setup-code-hint') || {}).textContent }; })()`);
            const open = async () => { await ev(`(async () => { await window.showSettingsModal('network'); return true; })()`); await waitFor(`document.getElementById('settings-pane-network') && !document.getElementById('settings-pane-network').hidden && document.getElementById('settings-remote-access-enabled').dataset.saved !== undefined`, 'the Network and Hub pane', 8000); };
            const remoteOn = on => ev(`(() => { const c = document.getElementById('settings-remote-access-enabled'); if (c.checked !== ${on}) c.click(); return true; })()`);
            const reveal = () => ev(`(() => { const b = document.getElementById('settings-setup-code-reveal'); if (b.getAttribute('aria-expanded') !== 'true') b.click(); return true; })()`);
            // read only while the Network and Hub pane is in view: Settings opened on another pane asks nothing; the pane's tab asks
            await ev(`(async () => { await window.showSettingsModal('terminal'); return true; })()`); await sleep(500); // sleep-ok: the dialog fills over a few answers
            check(`${tag}: Settings opened on another pane does not ask for the code`, (await ev('window.__show.reads')) === 0);
            await ev(`(() => { const sel = document.getElementById('settings-group-select'); if (sel && sel.offsetParent) { sel.value = 'network'; sel.dispatchEvent(new Event('change', { bubbles: true })); } else document.getElementById('settings-tab-network').click(); return true; })()`);
            await waitFor(`window.__show.reads === 1`, 'the read when the pane comes into view', 6000);
            check(`${tag}: the pane coming into view reads it once`, (await ev('window.__show.reads')) === 1);
            await ev(`window.hideSettingsModal ? window.hideSettingsModal() : document.getElementById('settings-modal').style.display = 'none'; true`);
            await open(); await remoteOn(true);
            await waitFor(`!document.getElementById('net-group-setup').hidden`, 'the group with Remote access on', 6000);
            let a = await group();
            check(`${tag}: with Remote access on there is a group, and the code is not on the screen or in the page`, a.shown && a.code === '' && !a.boxShown, JSON.stringify(a));
            await reveal();
            a = await group();
            check(`${tag}: Show setup code shows it with how long it lasts`, a.code === 'H2XR-HS16' && a.boxShown && /about 59 more minutes;/.test(a.hint) && /^The code is below\./.test(a.hint), JSON.stringify(a));
            // read again each time Settings opens; not kept by the page
            await ev(`window.hideSettingsModal ? window.hideSettingsModal() : document.getElementById('settings-modal').style.display = 'none'; window.__show.reads = 0; window.__show.body = { code: 'ABCD-2345', expires_in: 60 }; true`);
            await open(); await remoteOn(true);
            await waitFor(`window.__show.reads >= 1 && !document.getElementById('net-group-setup').hidden`, 'the code read again', 6000);
            await reveal();
            a = await group();
            check(`${tag}: it is read again when Settings opens (the new code, one minute)`, a.code === 'ABCD-2345' && /about 1 more minute;/.test(a.hint), JSON.stringify(a));
            for (const [what, status, body] of [['a password is set (409)', 409, { code: 'password_set' }], ['not this computer (403)', 403, { code: 'this_computer_only' }], ['not signed in (403)', 403, { code: 'password_session_required' }], ['a broken answer', 200, { nope: 1 }]]) {
                await ev(`window.hideSettingsModal ? window.hideSettingsModal() : document.getElementById('settings-modal').style.display = 'none'; window.__show.status = ${status}; window.__show.body = ${JSON.stringify(body)}; true`);
                await open(); await remoteOn(true); await sleep(400); // sleep-ok: the read answers
                const c = await group();
                check(`${tag}: ${what}: the group is not there`, !c.shown, JSON.stringify(c));
            }
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} setup code checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
