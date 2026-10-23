// The Connect screen's one question, "What may Coral Hub do on this computer?" (#5915), for the keyboard and for contrast, at 1280 and 390 px in both themes: the radios are a group named by the
// question with each choice described; the arrow keys move the choice and the keyboard's focus is SEEN on the radio and on the auto-accept checkbox (an outline of the accent, 3:1; the modal's
// own text-field rule had taken it away and left a 15% glow, 1.2:1); the words on the screen, "Run your own hub instead" included, hold 4.5:1; the auto-accept switch is there only with Launch;
// and "Nothing is sent until you connect" is said once. The cloud routes are stubbed.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const key = async (k, code, vk) => { const b = { key: k, code, windowsVirtualKeyCode: vk }; await Input.dispatchKeyEvent({ type: 'keyDown', ...b }); await Input.dispatchKeyEvent({ type: 'keyUp', ...b }); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await client.Emulation.setFocusEmulationEnabled({ enabled: true });
        for (const [w, mobile] of [[1280, false], [390, true]]) for (const theme of ['dark', 'light']) {
            const tag = `${w}px ${theme}`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: theme }] });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`); await installAudit(ev); await setTheme(ev, theme);
            await ev(`(() => { const R = window.fetch; window.__cloud = { configured: true, state: 'idle', hub_url: 'https://app.coralhub.dev', max_mode: 'view', max_mode_saved: false, allow_hub_auto_accept: true, offer_at_launch: true };
                const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
                window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href).pathname; if (!p.startsWith('/api/cloud/')) return R(u, o); return json({ ...window.__cloud }); }; return true; })()`);
            await ev(`import((window.__coralStatic || '/static/') + 'editions.js').then(m => m.showHubSetup('connect')).then(() => true)`);
            await waitFor(`!!document.querySelector('#editions-modal input[data-cloud-mode]') && !document.querySelector('#editions-modal fieldset.editions-ask').hidden`, `${tag}: the question`, 8000);

            const s = await ev(`(() => { const m = document.getElementById('editions-modal'); const fs = m.querySelector('fieldset.editions-ask'); const radios = [...m.querySelectorAll('input[data-cloud-mode]')]; const aa = m.querySelector('[data-cloud="auto-accept-row"]');
                return { legend: fs.querySelector('legend').textContent, checked: radios.filter(r => r.checked).map(r => r.value), described: radios.every(r => { const d = document.getElementById(r.getAttribute('aria-describedby')); return !!d && d.textContent.trim().length > 10; }), autoShown: !aa.hidden && aa.getClientRects().length > 0, nothingSent: (m.textContent.match(/Nothing is sent until you connect/g) || []).length }; })()`);
            assert.deepEqual(s.checked, ['view'], `${tag}: View is preselected, the lowest mode (#6370)`);
            assert.equal(await ev(`(() => { const a = document.activeElement; return a.type === 'radio' && a.checked ? a.value : (a.dataset && a.dataset.cloud) || a.tagName; })()`), 'view', `${tag}: the first focus is on the chosen answer, so the question is read before Connect`);
            assert.equal(s.legend, 'What may Coral Hub do on this computer?', `${tag}: the group is named by the question`);
            assert.ok(s.described, `${tag}: each choice says what it lets Coral Hub do`);
            assert.ok(!s.autoShown, `${tag}: no auto-accept switch with View (it is for Launch, and starts unticked)`);
            assert.equal(s.nothingSent, 1, `${tag}: "Nothing is sent until you connect" is said once`);

            const au = await auditRegion(ev, ['#editions-modal .modal-content']);
            assert.ok(au.checked > 0 && !au.fails.length, `${tag}: the screen holds 4.5:1: ` + JSON.stringify(au.fails).slice(0, 300));

            // the keyboard: an arrow moves the choice (the switch goes with Launch), and the focus is seen on the radio and on the checkbox
            await ev(`document.querySelector('#editions-modal input[data-cloud-mode][value="launch"]').focus(); true`);
            await key('ArrowUp', 'ArrowUp', 38);
            await waitFor(`document.querySelector('#editions-modal input[data-cloud-mode][value="interact"]').checked`, `${tag}: ArrowUp moves to Interact`, 3000);
            const ring = sel => ev(`(() => { const r = document.querySelector(${JSON.stringify(sel)}); const cs = getComputedStyle(r); return { vis: r === document.activeElement && r.matches(':focus-visible'), style: cs.outlineStyle, width: parseFloat(cs.outlineWidth), color: cs.outlineColor }; })()`);
            let r = await ring('#editions-modal input[data-cloud-mode][value="interact"]');
            assert.ok(r.vis && r.style !== 'none' && r.width >= 2, `${tag}: the focus on the radio is an outline: ${JSON.stringify(r)}`);
            if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/connect-focus-${w}-${theme}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
            assert.equal(await ev(`(() => { const aa = document.querySelector('#editions-modal [data-cloud="auto-accept-row"]'); return !aa.hidden && aa.getClientRects().length > 0; })()`), false, `${tag}: no auto-accept switch below Launch`);
            await key('ArrowDown', 'ArrowDown', 40);
            await waitFor(`document.querySelector('#editions-modal input[data-cloud-mode][value="launch"]').checked`, `${tag}: ArrowDown back to Launch`, 3000);
            await key('Tab', 'Tab', 9);
            r = await ring('#editions-modal [data-cloud="allow-auto-accept"]');
            assert.ok(r.vis && r.style !== 'none' && r.width >= 2, `${tag}: the focus on the auto-accept checkbox is an outline: ${JSON.stringify(r)}`);
            console.log(`   ok ${tag}`);
        }
        console.log('PASS the Connect question works from the keyboard and reads in both themes');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
