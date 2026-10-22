// Confirm owner in Settings > Network and Hub (#6108): a computer linked before it recorded who owns it shows a row. The last owner the Hub showed (and how long ago) is looked up again every time Settings
// opens (the panel outlives the dialog). Pressing Confirm owner posts {} to the arm route and counts down a minute while the person opens an owner-only page through Coral Hub; when the label is recorded the
// row goes and "Owner confirmed <time>" says so; when the minute ends first it says "Time is up". The server's answers are stubbed in the page; drives Chrome over CDP through agent_view_harness.js.
// (Adapted from QA's refresh check for the earlier record-an-account design.)
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const stub = async () => ev(`(() => { const real = window.fetch; window.__missing = true; window.__adopted = ''; window.__cand = { label_missing: true, hub: 'hub.example', candidate: '', candidate_seen_at: '', several: false }; window.__arms = []; window.__armSeconds = 60; window.__gets = 0;
        window.fetch = async (u, o = {}) => { const url = String(u), m = (o.method || 'GET').toUpperCase();
            const json = (b, s = 200) => new Response(JSON.stringify(b), { status: s, headers: { 'Content-Type': 'application/json' } });
            if (url.endsWith('/api/cloud/status') && m === 'GET') { const r = await real(u, o); const st = await r.json(); Object.assign(st, { configured: true, state: 'connected', hub_url: 'https://hub.example', label: 'My Laptop', max_mode: 'interact', server_id: 'srv_demo', user_code: '', verification_url: '' }); st.owner_label_missing = window.__missing; if (window.__adopted) st.owner_adopted_at = window.__adopted; return json(st); }
            if (url.endsWith('/api/cloud/owner-label') && m === 'GET') { window.__gets++; return json(window.__cand); }
            if (url.endsWith('/api/cloud/owner-label/arm') && m === 'POST') { window.__arms.push(o.body); return json({ ok: true, armed_until: new Date(Date.now() + window.__armSeconds * 1000).toISOString(), seconds: window.__armSeconds, hub: 'hub.example', candidate: window.__cand.candidate }); }
            return real(u, o); }; return true; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
        await stub();
        const open = async () => { await ev(`(async () => { await window.showSettingsModal('network'); return true; })()`); await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings', 8000); };
        const close = async () => { await ev(`(() => { window.hideSettingsModal(); return true; })()`); await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display === 'none'`, 'Settings closed', 8000); };
        const row = () => ev(`(() => { const q = id => document.getElementById(id); const vis = e => !!e && !e.hidden && e.getClientRects().length > 0; return { row: vis(q('settings-cloud-owner-row')), cand: vis(q('settings-cloud-owner-candidate')) ? q('settings-cloud-owner-candidate').textContent : null, btn: vis(q('settings-cloud-owner-record')), btnOff: !!q('settings-cloud-owner-record').disabled, say: q('settings-cloud-owner-status').textContent, confirmed: vis(q('settings-cloud-owner-confirmed')) ? q('settings-cloud-owner-confirmed').textContent : null, text: q('settings-cloud-owner-text').textContent }; })()`);

        await open();
        await waitFor(`!document.getElementById('settings-cloud-owner-row').hidden`, 'the owner row', 8000);
        let r = await row();
        check('a linked computer with no recorded owner shows the row with the Confirm owner button, the plain refusal-free words, and (the Hub showing no owner yet) only the hub host', r.row && r.btn && /Confirm owner/.test(r.text) && /link this computer again/i.test(r.text) && /hub\.example/.test(r.cand || ''), JSON.stringify(r));

        // the owner uses Coral Hub, and Settings is reopened: the account is looked up again, with how long ago it was seen
        await ev(`window.__cand = { label_missing: true, hub: 'hub.example', candidate: 'hub-user-0a1b2c3d', candidate_seen_at: new Date(Date.now() - 120000).toISOString(), several: false }; true`);
        const before = await ev('window.__gets');
        await close(); await open();
        await waitFor(`window.__gets > ${before}`, 'a new look for the owner when Settings reopens', 6000).catch(() => {});
        await waitFor(`/hub-user-0a1b2c3d/.test(document.getElementById('settings-cloud-owner-candidate').textContent)`, 'the candidate', 6000).catch(() => {});
        r = await row();
        check('reopening Settings looks again and shows the last owner, the hub, and "seen 2 min ago"', /hub-user-0a1b2c3d/.test(r.cand || '') && /hub\.example/.test(r.cand) && /seen 2 min ago/.test(r.cand), JSON.stringify(r));

        // the dialog stays open and the owner shown changes: a status load more than 30 s after the last look refreshes it too
        await ev(`window.__cand = { label_missing: true, hub: 'hub.example', candidate: 'hub-user-0a1b2c3d', candidate_seen_at: new Date().toISOString(), several: false }; (async () => { const p = document.getElementById('settings-cloud')._cloudPanel; p.ownerSeenAt = Date.now() - 31000; await p.load(); return true; })()`);
        await waitFor(`/seen 0 min ago/.test(document.getElementById('settings-cloud-owner-candidate').textContent)`, 'the refresh after 30 s with the dialog open', 6000).catch(() => {});
        r = await row();
        check('a status load more than 30 s after the last look refreshes the last owner while Settings stays open', /seen 0 min ago/.test(r.cand || ''), JSON.stringify(r));

        // press Confirm owner: exactly {} is posted, the button waits, the countdown tells the person what to do
        await ev(`document.getElementById('settings-cloud-owner-record').click(); true`);
        await waitFor(`window.__arms.length === 1`, 'the arm post', 6000);
        check('the button posts exactly {} to the arm route', (await ev(`window.__arms[0]`)) === '{}', await ev(`window.__arms[0]`));
        r = await row();
        check('while armed the button is off and the line says to open Artifacts or Saved through Coral Hub with the seconds left', r.btnOff && /open Artifacts or Saved through Coral Hub/.test(r.say) && /s left/.test(r.say), JSON.stringify(r));

        // the owner opens an owner-only page: the label is recorded; the next poll hides the row and says it was confirmed
        await ev(`window.__missing = false; window.__adopted = new Date().toISOString(); true`);
        await waitFor(`document.getElementById('settings-cloud-owner-row').hidden`, 'the row goes once the owner is recorded', 9000);
        await waitFor(`!document.getElementById('settings-cloud-owner-confirmed').hidden`, 'Owner confirmed', 5000);
        r = await row();
        check('once the owner is recorded the row is gone and "Owner confirmed <time>" says so', r.row === false && /^Owner confirmed /.test(r.confirmed || ''), JSON.stringify(r));

        // the minute ends first: "Time is up", and the button works again
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');
        await stub();
        await ev(`window.__armSeconds = 3; true`);
        await open();
        await waitFor(`!document.getElementById('settings-cloud-owner-row').hidden`, 'the owner row (second page)', 8000);
        await ev(`document.getElementById('settings-cloud-owner-record').click(); true`);
        await waitFor(`/Time is up/.test(document.getElementById('settings-cloud-owner-status').textContent)`, 'Time is up', 12000);
        r = await row();
        check('when the minute ends before an owner page is opened it says "Time is up" and the button is back', r.row && r.btn && !r.btnOff && /Press Confirm owner/.test(r.say), JSON.stringify(r));
    } finally { await client.close(); }
    const failed = results.filter(x => !x).length;
    console.log(`hub_owner_label_row: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
