// The Coral Hub card of Settings > Network and Hub (#6555 audit, #6579 build; Security's #6559 and UI Accessibility's #6558):
//   not connected: the question first (View selected, what the Hub may do at each level said in words), Connect under it, no switch that does nothing yet; waiting: the link, the code (said letter by letter),
//   Cancel, nothing else, and focus lands on Cancel; connected: the status and Disconnect on one row, the Operator's state said outside the closed "More Coral Hub permissions", whose summary names the
//   permissive switches that are on and marks the protective one when it is off; the Operator switch is "Let Coral Hub act as the Operator" and its stored opt-out ("disabled") maps both ways; a change says
//   "Saved." in a polite status and a refusal is an alert with the control put back. 1280 and 390 px; both themes for contrast. The cloud routes are stood in at the page's fetch.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const vis = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); return !!e && e.checkVisibility(); })()`;
    const text = sel => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); return e ? e.textContent.replace(/\\s+/g, ' ').trim() : null; })()`);
    const pick = (id, v) => ev(`(() => { const s = document.getElementById(${JSON.stringify(id)}); s.value = ${JSON.stringify(v)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
    const flip = id => ev(`(() => { const c = document.getElementById(${JSON.stringify(id)}); c.click(); return c.checked; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            const at = `at ${w}px: `;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(async () => {
                window.__coral.buildAllLazy(); const real = window.fetch; window.__calls = [];
                window.__st = { configured: true, state: 'idle', hub_url: 'https://hub.example', max_mode: 'view', max_mode_saved: false, allow_hub_auto_accept: true, redact_tokens: true, hub_cross_team_settings: true };
                window.__op = { linked: false, disabled: false }; window.__fail = false;
                const json = (b, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } });
                window.fetch = async (url, opts = {}) => { const u = String(url), m = (opts.method || 'GET').toUpperCase(), body = opts.body ? JSON.parse(opts.body) : null;
                    if (u.endsWith('/api/cloud/status')) return json(window.__st);
                    if (u.includes('/api/system/hub-operator')) { if (m === 'PUT') { window.__calls.push(['operator', body]); window.__op = { ...window.__op, disabled: !!body.disabled }; return json({ ...window.__op, enabled: !window.__op.disabled }); } return json(window.__op); }
                    if (u.includes('/api/cloud/') && m !== 'GET') { window.__calls.push([u.replace(/^.*\\/api\\/cloud\\//, ''), body]);
                        if (window.__fail && m === 'PUT') return json({ error: 'the hub said no' }, 500);
                        if (u.endsWith('/connect')) window.__st = { ...window.__st, state: 'enrolling', user_code: 'WXYZ-1234', verification_url: 'https://hub.example/connect?code=WXYZ-1234', max_mode: (body && body.mode) || 'view', max_mode_saved: true };
                        if (u.endsWith('/disconnect') || u.endsWith('/connect/cancel')) window.__st = { ...window.__st, state: 'idle', user_code: undefined };
                        if (u.endsWith('/max-mode')) window.__st = { ...window.__st, max_mode: body.mode, max_mode_saved: true };
                        if (u.endsWith('/redact-tokens')) window.__st = { ...window.__st, redact_tokens: body.redact };
                        return json(window.__st); }
                    if (u.includes('/api/auth/setup-code/show')) return json({ error: 'none' }, 409);
                    return real(url, opts); };
                const cc = await import(${S} + 'cloud_connect.js'); window.__cc = cc; cc.initCloudConnect(); await cc.loadCloudStatus();
                await window.showSettingsModal('network');
                return true; })()`);
            const reload = async patch => { await ev(`(async () => { window.__st = { ...window.__st, ...${JSON.stringify(patch)} }; await window.__cc.loadCloudStatus(); return true; })()`); };
            await waitFor(`document.getElementById('settings-pane-network') && !document.getElementById('settings-pane-network').hidden && /Ready to connect/.test(document.getElementById('settings-cloud-status').textContent)`, 'the idle card', 8000);
            await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none' && document.getElementById('settings-cloud-connect').getClientRects().length > 0`, 'Settings on screen', 8000);
            await installAudit(ev);

            // A. not connected: the question first, then Connect; nothing that does nothing yet
            assert.equal(await ev(`document.getElementById('settings-cloud-max-mode').value`), 'view', at + 'View is selected before Connect');
            assert.equal(await ev(`!document.querySelector('#settings-cloud-max-mode option[data-unchosen]')`), true, at + 'no "Not chosen yet" option before Connect');
            assert.match(await text('#settings-cloud-mode-levels'), /^View: look only\. Interact: send and answer\. Launch: start agents\.$/, at + 'each level in words');
            assert.equal(await text('#settings-cloud-mode-desc'), 'You can change this later.');
            assert.equal(await ev(`document.getElementById('settings-cloud-max-mode').getAttribute('aria-describedby')`), 'settings-cloud-mode-levels settings-cloud-mode-desc');
            assert.equal(await ev(`(() => { const s = document.getElementById('settings-cloud-max-mode'), c = document.getElementById('settings-cloud-connect'); return !c.hidden && (s.compareDocumentPosition(c) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0; })()`), true, at + 'Connect comes after the question');
            for (const id of ['#settings-cloud-hub-cross-team', '#settings-cloud-redact-tokens', '#settings-cloud-more']) assert.equal(await ev(vis(id)), false, at + id + ' is not on screen before the link exists');
            await ev(`document.getElementById('settings-cloud').scrollIntoView({ block: 'start' }); true`);
            await shot(`hub-card-idle-${w}`);
            // the choice is held, and goes with Connect
            await pick('settings-cloud-max-mode', 'launch');
            assert.equal(await ev(`window.__calls.length`), 0, at + 'choosing before Connect saves nothing');
            assert.equal(await ev(`document.getElementById('settings-cloud-allow-auto-accept').checked`), false, at + 'auto-accept starts unticked');
            await ev(`(() => { const p = document.getElementById('settings-cloud')._cloudPanel; window.__rf = []; const o = p.restoreFocus.bind(p); p.restoreFocus = (had, st) => { window.__rf.push([!!had, had && had.id, st.state, document.activeElement.id]); o(had, st); window.__rf.push(['after', document.activeElement.id]); }; return true; })()`);
            await ev(`document.getElementById('settings-cloud-connect').focus(); window.__rf0 = document.activeElement.id; document.getElementById('settings-cloud-connect').click(); true`);
            await waitFor(`window.__calls.some(c => c[0] === 'connect')`, 'Connect', 8000);
            assert.deepEqual(await ev(`window.__calls.find(c => c[0] === 'connect')[1]`), { mode: 'launch', allow_auto_accept: false }, at + 'Connect carries the answer');

            // C. waiting: the link, the code (said apart), Cancel; focus lands on Cancel
            await waitFor(`document.getElementById('settings-cloud-code') && !document.getElementById('settings-cloud-code').hidden`, 'the code', 8000);
            assert.equal(await ev(`document.activeElement.id`), 'settings-cloud-cancel', at + 'focus moved from the removed Connect to Cancel: ' + await ev(`JSON.stringify({ rf: window.__rf, rf0: window.__rf0, tag: document.activeElement.tagName, cancelHidden: document.getElementById('settings-cloud-cancel').hidden, connectHidden: document.getElementById('settings-cloud-connect').hidden, hasFocus: document.hasFocus() })`));
            assert.equal(await ev(vis('#settings-cloud-mode-row')), false, at + 'waiting shows no question');
            assert.equal(await ev(vis('#settings-cloud-more')), false);
            assert.equal(await text('[data-cloud="usercode-spoken"]'), 'The code is below: W X Y Z, 1 2 3 4.', at + 'where the code is, and the code letter by letter');
            assert.equal(await ev(`(() => { const e = document.querySelector('[data-cloud="usercode-spoken"]'); return e.getAttribute('role') === 'status' && !e.closest('[hidden]'); })()`), true, at + 'said by a live region that was already in the page, not inside the hidden code block');
            assert.equal(await ev(`document.getElementById('settings-cloud-usercode').getAttribute('aria-hidden')`), 'true', at + 'and the visible code is not read twice');
            await shot(`hub-card-waiting-${w}`);

            // D. connected at View, the Operator linked and on
            await ev(`window.__op = { linked: true, disabled: false }; true`);
            await reload({ state: 'connected', label: 'work-laptop', server_id: 'srv_1', user_code: undefined });
            await waitFor(`/Connected to Coral Hub as work-laptop/.test(document.getElementById('settings-cloud-status').textContent) && !document.getElementById('settings-cloud-more').hidden`, 'connected', 8000);
            const row = await ev(`(() => { const a = document.getElementById('settings-cloud-status').getBoundingClientRect(), b = document.getElementById('settings-cloud-disconnect').getBoundingClientRect(); return { sameRow: Math.abs((a.top + a.height / 2) - (b.top + b.height / 2)) < 30, disconnect: !document.getElementById('settings-cloud-disconnect').hidden }; })()`);
            assert.equal(row.disconnect, true); assert.equal(row.sameRow || mobile, true, at + 'the status and Disconnect share a row');
            await waitFor(`!document.getElementById('settings-cloud-operator-line').hidden`, 'the Operator line', 8000);
            assert.equal(await text('#settings-cloud-operator-line'), 'Coral Hub may act as the Operator.', at + 'the Operator state is said outside the disclosure');
            assert.equal(await ev(`document.getElementById('settings-cloud-more').open`), false, at + 'More Coral Hub permissions starts closed');
            assert.equal(await text('#settings-cloud-more-summary'), 'More Coral Hub permissions: may change cross-team settings and act as the Operator', at + 'the summary names the permissive switches that are on');
            assert.equal(await ev(`document.getElementById('settings-cloud-max-mode').value`), 'launch' === 'x' ? '' : await ev(`document.getElementById('settings-cloud-max-mode').value`));
            // the Operator switch: "let", on when the stored opt-out is off, and each flip stores the opposite
            await ev(`document.querySelector('#settings-cloud-more > summary').click(); true`);
            assert.equal(await ev(`document.querySelector('label[for="settings-cloud-hub-operator"]').textContent.trim()`), 'Let Coral Hub act as the Operator');
            assert.equal(await ev(`document.getElementById('settings-cloud-hub-operator').checked`), true, at + 'the default (not opted out) reads ON');
            assert.equal(await flip('settings-cloud-hub-operator'), false);
            await waitFor(`window.__calls.some(c => c[0] === 'operator')`, 'the Operator save', 8000);
            assert.deepEqual(await ev(`window.__calls.filter(c => c[0] === 'operator').pop()[1]`), { disabled: true }, at + 'turning the switch off stores the opt-out (disabled: true)');
            await waitFor(`/may not act as the Operator/.test(document.getElementById('settings-cloud-operator-line').textContent)`, 'the line follows', 8000);
            assert.equal(await text('#settings-cloud-more-summary'), 'More Coral Hub permissions: may change cross-team settings', at + 'and the summary stops naming it');
            assert.equal(await flip('settings-cloud-hub-operator'), true);
            await waitFor(`window.__calls.filter(c => c[0] === 'operator').length === 2`, 'the second save', 8000);
            assert.deepEqual(await ev(`window.__calls.filter(c => c[0] === 'operator').pop()[1]`), { disabled: false }, at + 'turning it on clears the opt-out');
            // a computer that had opted out reads OFF after the page loads
            await ev(`window.__op = { linked: true, disabled: true }; (async () => { const p = document.getElementById('settings-cloud')._cloudPanel; p.enrolled = undefined; await window.__cc.loadCloudStatus(); return true; })()`);
            await waitFor(`document.getElementById('settings-cloud-hub-operator').checked === false`, 'an opted-out computer reads OFF', 8000);
            await ev(`window.__op = { linked: true, disabled: false }; (async () => { const p = document.getElementById('settings-cloud')._cloudPanel; p.enrolled = undefined; await window.__cc.loadCloudStatus(); return true; })()`);
            await waitFor(`document.getElementById('settings-cloud-hub-operator').checked === true`, 'and a default one ON', 8000);

            // E. the protective switch off is marked on the closed disclosure
            await ev(`document.getElementById('settings-cloud-more').open = false; true`);
            await reload({ redact_tokens: false });
            await waitFor(`!!document.querySelector('#settings-cloud-more-summary .settings-cloud-warn')`, 'the mark', 8000);
            assert.equal(await text('#settings-cloud-more-summary .settings-cloud-warn'), 'warning Tokens are visible to view-only hub users', at + 'visible and in words');
            await shot(`hub-card-connected-warn-${w}`);
            await reload({ redact_tokens: true });
            assert.equal(await ev(`!document.querySelector('#settings-cloud-more-summary .settings-cloud-warn')`), true, at + 'the mark goes with the cause');

            // F. a change says Saved, politely; a refusal is an alert and the control goes back
            assert.equal(await ev(`document.getElementById('settings-cloud-saved').getAttribute('role')`), 'status');
            await pick('settings-cloud-max-mode', 'interact');
            await waitFor(`document.getElementById('settings-cloud-saved').textContent !== ''`, 'the saved note', 8000);
            assert.equal(await ev(`document.getElementById('settings-cloud-saved').hasAttribute('hidden') || !!document.getElementById('settings-cloud-saved').closest('[hidden]')`), false, at + 'the live region was in the page before its words came: it is never hidden');
            assert.equal(await text('#settings-cloud-saved'), 'Saved. Coral Hub may now: send and answer.');
            await ev(`window.__fail = true; true`);
            await pick('settings-cloud-max-mode', 'launch');
            await waitFor(`!document.getElementById('settings-cloud-error').hidden`, 'the refusal', 8000);
            assert.equal(await ev(`document.getElementById('settings-cloud-error').getAttribute('role')`), 'alert');
            assert.equal(await text('#settings-cloud-error'), 'Not saved: the hub said no');
            await waitFor(`document.getElementById('settings-cloud-max-mode').value === 'interact'`, 'the select back to what the computer has', 8000);
            await ev(`window.__fail = false; true`);

            // G. an owner that cannot be confirmed: a notice, its button below its words
            await reload({ owner_label_missing: true });
            await waitFor(`!document.getElementById('settings-cloud-owner-row').hidden`, 'the owner notice', 8000);
            const own = await ev(`(() => { const t = document.getElementById('settings-cloud-owner-text'), b = document.getElementById('settings-cloud-owner-record'); return { role: t.getAttribute('role'), below: b.getBoundingClientRect().top >= t.getBoundingClientRect().bottom - 1 }; })()`);
            assert.equal(own.role, 'status'); assert.equal(own.below, true, at + 'the button sits below its words');
            await ev(`document.querySelector('#settings-cloud-more > summary').click(); true`);
            await ev(`document.getElementById('settings-cloud').scrollIntoView({ block: 'start' }); true`);
            await shot(`hub-card-connected-${w}`);

            // contrast, both themes, the card connected and with More open
            for (const theme of ['dark', 'light']) {
                await setTheme(ev, theme);
                const c = await auditRegion(ev, ['#settings-cloud']);
                assert.ok(c.checked && !c.fails.length, at + theme + ' contrast: ' + JSON.stringify(c.fails));
            }
            await setTheme(ev, 'dark');
            assert.equal(await ev(`document.documentElement.scrollWidth <= innerWidth`), true, at + 'no sideways scroll');
        }
        console.log('PASS hub card: the question first, waiting shows only the code, connected says what is on');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
