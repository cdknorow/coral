// The setup code where a person is (#6410, Security's #6408 rank 1 option A): Settings > Network and Hub shows the one-time code (the one `coral setup-code` prints) and how long it lasts, with a Copy button, to a dashboard the server
// says is signed in on this computer; the row is not there when the server has none for this page (a password is set, another computer, through Coral Hub, not signed in), and the page keeps the code nowhere: it is read each time Settings
// opens. At 1280 and 390 px. The route's answer is stood in at the page's fetch (its handler and refusals are in internal/auth). Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
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
            await ev(`(() => { const inner = window.__innerFetch || (window.__innerFetch = window.fetch); window.__show = { status: 200, body: { code: 'H2XR-HS16', expires_in: 3540 }, reads: 0 };
                window.fetch = (u, o = {}) => { if (String(u).includes('/api/auth/setup-code/show')) { window.__show.reads++; return Promise.resolve(new Response(JSON.stringify(window.__show.body), { status: window.__show.status, headers: { 'content-type': 'application/json' } })); } return inner(u, o); };
                window.__copied = []; Object.defineProperty(navigator, 'clipboard', { value: { writeText: t => { window.__copied.push(t); return Promise.resolve(); } }, configurable: true }); return true; })()`);
            const open = async () => { await ev(`(async () => { await window.showSettingsModal('network'); return true; })()`); await waitFor(`document.getElementById('settings-pane-network') && !document.getElementById('settings-pane-network').hidden`, 'the Network pane', 8000); await sleep(500); }; // sleep-ok: the section loads its status rows over a few answers
            const row = () => ev(`(() => { const r = document.getElementById('settings-setup-code-row'); return { shown: !!r && r.getClientRects().length > 0, code: (document.getElementById('settings-setup-code') || {}).textContent, hint: (document.getElementById('settings-setup-code-hint') || {}).textContent, label: (document.getElementById('settings-setup-code-label') || {}).textContent, copyName: (document.getElementById('settings-setup-code-copy') || {}).getAttribute && document.getElementById('settings-setup-code-copy').getAttribute('aria-label') }; })()`);
            // read only while the Network and Hub pane is in view: Settings opened on another pane asks nothing; the pane's tab asks
            await ev(`(async () => { await window.showSettingsModal('terminal'); return true; })()`); await sleep(500); // sleep-ok: the dialog fills over a few answers
            check(`${tag}: Settings opened on another pane does not ask for the code`, (await ev('window.__show.reads')) === 0 && !(await row()).shown);
            await ev(`(() => { const sel = document.getElementById('settings-group-select'); if (sel && sel.offsetParent) { sel.value = 'network'; sel.dispatchEvent(new Event('change', { bubbles: true })); } else document.getElementById('settings-tab-network').click(); return true; })()`);
            await waitFor(`!!document.getElementById('settings-setup-code-row')`, 'the row once the pane is shown', 6000);
            check(`${tag}: the Network and Hub pane coming into view reads it`, (await ev('window.__show.reads')) === 1 && (await row()).code === 'H2XR-HS16');
            await ev(`window.hideSettingsModal ? window.hideSettingsModal() : document.getElementById('settings-modal').style.display = 'none'; true`);
            await open();
            const a = await row();
            check(`${tag}: the code, what it is for and how long it lasts show in Settings > Network and Hub`, a.shown && a.code === 'H2XR-HS16' && /another device/.test(a.hint) && /about 59 more minutes/.test(a.hint) && a.label === 'Setup code', JSON.stringify(a));
            await ev(`document.getElementById('settings-setup-code-copy').click(); true`);
            await waitFor(`/Copied/.test(document.getElementById('settings-setup-code-hint').textContent)`, 'the copy answer', 4000);
            check(`${tag}: Copy has a name and copies the code as shown`, a.copyName === 'Copy the setup code' && (await ev('window.__copied')).join() === 'H2XR-HS16', a.copyName);
            const fit = await ev(`document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1`);
            check(`${tag}: no sideways scroll`, fit);
            // read again each time Settings opens; not kept by the page
            await ev(`window.hideSettingsModal ? window.hideSettingsModal() : document.getElementById('settings-modal').style.display = 'none'; window.__show.reads = 0; window.__show.body = { code: 'ABCD-2345', expires_in: 60 }; true`);
            await open();
            const b = await row();
            check(`${tag}: it is read again when Settings opens (the new code, one minute)`, b.code === 'ABCD-2345' && /about 1 more minute;/.test(b.hint) && (await ev('window.__show.reads')) >= 1, JSON.stringify(b));
            for (const [what, status, body] of [['a password is set (409)', 409, { code: 'password_set' }], ['not this computer (403)', 403, { code: 'this_computer_only' }], ['not signed in (403)', 403, { code: 'password_session_required' }], ['a broken answer', 200, { nope: 1 }]]) {
                await ev(`window.hideSettingsModal ? window.hideSettingsModal() : document.getElementById('settings-modal').style.display = 'none'; window.__show.status = ${status}; window.__show.body = ${JSON.stringify(body)}; true`);
                await open();
                const c = await row();
                check(`${tag}: ${what}: the row is not there`, !c.shown, JSON.stringify(c));
            }
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} setup code checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
