// Workflow presets through Coral Hub (#4285, with #4274): the Hub refuses a preset's PUT and its reset, so on a Hub session Save (for an
// existing preset) and Reset built-in are not drawn, the preset reads as read-only with the reason, and Enter in the name box sends nothing;
// a NEW preset can still be saved (its POST is allowed). On a local dashboard both buttons work as before. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input, Fetch, Network } = client;
    const ok = name => console.log(`   ✓ ${name}`);
    const TEAM = 'preset-hub-' + Date.now().toString(36);
    const state = () => ev(`(() => { const q = s => document.querySelector(s); const vis = el => !!el && !el.hidden && el.checkVisibility({ checkVisibilityCSS: true });
        return { save: vis(q('.preset-save')), reset: vis(q('.preset-reset')), saveText: q('.preset-save').textContent, nameRO: q('.working-mode-form [name=preset_name]')?.readOnly ?? null,
        textRO: q('.working-mode-form [name=preset_instructions]').readOnly, status: q('.preset-state').textContent }; })()`);
    const open = async () => {
        await ev(`document.getElementById('team-settings-workspace')?.remove(); import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
        await waitFor(`!!document.querySelector('.preset-save') && !!document.querySelector('.preset-select, [name=preset]')`, 'the preset controls', 15000);
        await waitFor(`document.querySelector('.working-mode-form [type=submit]') && !document.querySelector('.working-mode-form [type=submit]').disabled`, 'the dialog to load', 15000);
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });

        // Local: Save preset shows, Reset shows once a built-in is overridden.
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install(); await ev(`window.__coralReady.then(() => true)`);
        assert.equal(await ev(`JSON.parse(document.getElementById('coral-boot').textContent).mode`), 'local');
        for (const [id, title] of [['Boss', 'Orchestrator'], ['Dev One', 'Developer']]) {
            const r = await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: ${JSON.stringify(id)}, job_title: ${JSON.stringify(title)}, session_name: ${JSON.stringify(id.toLowerCase().replace(' ', '-') + '-session')} }) }).then(r => r.status)`);
            assert.ok(r < 300, `subscribe ${id}: ${r}`);
        }
        await open();
        let st = await state();
        assert.equal(st.save, true, 'local: Save shows'); assert.equal(st.textRO, false, 'local: instructions editable');
        await ev(`(() => { const s = document.querySelector('.preset-select, [name=preset]'); s.value = 'worktrees'; s.dispatchEvent(new Event('change', { bubbles: true })); })()`);
        const ins = await ev(`(() => { const t = document.querySelector('.working-mode-form [name=preset_instructions]'); t.value = t.value + ' (edited)'; document.querySelector('.preset-save').click(); return true; })()`);
        await waitFor(`/saved/i.test(document.querySelector('.working-mode-status, .preset-status')?.textContent || document.body.textContent)`, 'the save', 8000);
        await waitFor(`!document.querySelector('.preset-reset').hidden`, 'Reset built-in appears after an override');
        st = await state();
        assert.equal(st.reset, true, 'local: Reset built-in shows for an overridden built-in');
        ok('local dashboard: Save preset and Reset built-in are there and work');

        // Hub session.
        await Network.enable(); await Network.setBypassServiceWorker({ bypass: true }); // the first load's service worker would answer the page itself
        // The page is the local server's own with the Hub's boot flag and a Hub sign-in answered, as hub_readonly_browser does it.
        await Fetch.enable({ patterns: [{ urlPattern: '*', resourceType: 'Document', requestStage: 'Response' }, { urlPattern: '*/auth/session', requestStage: 'Request' }] });
        safePaused(client, async e => {
            if (e.request.url.endsWith('/auth/session')) {
                return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }],
                    body: Buffer.from(JSON.stringify({ authenticated: true, email: 'qa@example.com', csrf: 'qa-csrf' })).toString('base64') });
            }
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            const html = (base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body).replace(/"mode":\s*"local"/, '"mode":"cloud"');
            const headers = (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name));
            return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: headers, body: Buffer.from(html).toString('base64') });
        });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install(); // the Hub page does not signal __coralReady the same way (hub_readonly_browser does not wait for it)
        assert.equal(await ev(`JSON.parse(document.getElementById('coral-boot').textContent).mode`), 'cloud', 'the page believes it is on the Hub');
        await open();
        await ev(`(() => { const s = document.querySelector('.preset-select, [name=preset]'); s.value = 'worktrees'; s.dispatchEvent(new Event('change', { bubbles: true })); })()`);
        st = await state();
        assert.equal(st.save, false, 'hub: no Save for an existing preset'); assert.equal(st.reset, false, 'hub: no Reset built-in even when overridden');
        assert.equal(st.textRO, true); assert.match(st.status, /Through Coral Hub a preset can be added but not changed/);
        await ev(`window.__puts = 0; { const w = window.fetch; window.fetch = (u, o = {}) => { if (/presets/.test(String(u)) && (o.method || 'GET') !== 'GET') window.__puts++; return w(u, o); }; } document.querySelector('.working-mode-form [name=preset_name]').focus(); true`);
        for (const type of ['keyDown', 'keyUp']) await Input.dispatchKeyEvent({ type, key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: type === 'keyDown' ? '\r' : undefined });
        assert.equal(await ev(`window.__puts`), 0, 'Enter in the name box sends nothing');
        ok('Hub session: Save and Reset built-in are not drawn for an existing preset, the text is read-only with the reason, Enter sends nothing');

        // A new preset can still be saved on the Hub (POST is allowed).
        await ev(`document.querySelector('.preset-new').click(); true`);
        st = await state();
        assert.equal(st.save, true, 'hub: a new preset has Save'); assert.equal(st.textRO, false);
        ok('Hub session: a new preset has Save and editable text');
    } catch (e) {
        console.error(e);
        process.exitCode = 1;
    } finally {
        await client.close();
    }
})();
