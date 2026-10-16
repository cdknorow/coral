// The dialogs load on first use (#3140, perf phase 3b): modals.js (launch, settings,
// resume, info, team dialogs) is not requested by a cold load; each dialog opens on its
// first click through its window stub; what runs at startup stays eager (settings and
// theme in settings_core.js, Escape/Tab/backdrop handling for every dialog in
// modal_chrome.js); a failed load shows a toast and the next click retries.
//
// Drives Chrome over CDP (chrome-remote-interface); run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Fetch } = client;
    const ev = async x => {
        const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const reqs = [];
    let block = 0; // fail this many requests for modals.js
    let blockMember = 0; // fail this many requests for launch_models.js, a module modals.js loads (#3836 family)
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setCacheDisabled({ cacheDisabled: true });
        Network.requestWillBeSent(e => reqs.push(e.request.url));
        await Fetch.enable({ patterns: [{ urlPattern: '*/modals.js*', requestStage: 'Request' }, { urlPattern: '*/launch_models.js*', requestStage: 'Request' }] });
        Fetch.requestPaused(e => {
            const member = /\/launch_models\.js/.test(e.request.url);
            if (member ? blockMember > 0 : block > 0) { if (member) blockMember--; else block--; return Fetch.failRequest({ requestId: e.requestId, errorReason: 'InternetDisconnected' }); }
            return Fetch.continueRequest({ requestId: e.requestId });
        });
        const count = name => reqs.filter(u => new RegExp(`/${name}(\\?|$)`).test(new URL(u).pathname + (new URL(u).search ? '?' : ''))).length;
        const shown = id => ev(`(() => { const m = document.getElementById(${JSON.stringify(id)}); return !!m && m.style.display !== 'none' && m.getClientRects().length > 0; })()`);
        const escape = () => ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);

        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady('startup', 'liveList', 'liveListLoaded', 'settings');

        // 1. Cold load: no modals.js; settings and theme applied anyway.
        assert.equal(count('modals.js'), 0, 'a cold load does not request modals.js');
        assert.equal(await ev(`typeof window.showSettingsModal`), 'function', 'the dialog functions exist (stubs)');
        assert.ok(await ev(`!!document.documentElement.getAttribute('data-theme')`), 'the theme is applied at startup (settings_core)');
        assert.equal(await ev(`(async () => (await import((window.__coralStatic || '/static/') + 'state.js')).state.settings?.file_search_mode)()`), 'directory', 'loadSettings ran at startup');
        console.log('   ✓ cold load: modals.js not requested; settings and theme applied at startup');

        // 2. Page-wide modal handling works before modals.js: Escape closes a confirm dialog.
        await ev(`window.showConfirmModal('Sure?', 'Test', () => {}); true`);
        assert.ok(await shown('confirm-modal'), 'confirm dialog open');
        await escape();
        assert.ok(!(await shown('confirm-modal')), 'Escape closes it (modal_chrome.js, no modals.js needed)');
        assert.equal(count('modals.js'), 0, 'still no modals.js');
        console.log('   ✓ Escape closes a non-modals.js dialog before modals.js loads');

        // 3. The home view's Launch buttons: showLaunchModal + _selectLaunchType, both stubs on the first click.
        await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`);
        await waitFor(`(() => { const m = document.getElementById('launch-modal'); return m && m.style.display !== 'none'; })()`, 'the launch dialog', 5000);
        // Both stubs wait for the same load and run in call order: open (which resets to the chooser), then 'team'.
        await waitFor(`document.getElementById('launch-step-team').style.display === 'flex' && document.getElementById('launch-step-chooser').style.display === 'none'`, 'the team step selected after the dialog opened', 3000);
        assert.equal(count('modals.js'), 1, 'modals.js loaded once, on first use');
        assert.ok(!/=>\s*load\(\)/.test(await ev(`String(window.showLaunchModal)`)), 'the stub was replaced by the real function');
        await escape();
        assert.ok(!(await shown('launch-modal')), 'Escape closes the launch dialog');
        console.log('   ✓ launch dialog opens on first click (stub), modals.js loaded once, Escape closes it');

        // 4. Every other dialog opens; no refetch.
        for (const [open, id] of [['window.showSettingsModal()', 'settings-modal'], ['window._showMobileConnectModal()', 'mobile-connect-modal']]) {
            await ev(`(${open}, true)`);
            await waitFor(`(() => { const m = document.getElementById(${JSON.stringify(id)}); return m && m.style.display !== 'none'; })()`, id, 5000);
            await escape();
            await waitFor(`(() => { const m = document.getElementById(${JSON.stringify(id)}); return !m || m.style.display === 'none'; })()`, id + ' closed', 3000);
        }
        assert.equal(count('modals.js'), 1, 'no refetch of modals.js');
        console.log('   ✓ settings and mobile QR dialogs open and close; no refetch');

        // 4b. The Default Prompts and Resume dialogs are modules of their own (#3836): opening one loads that module only.
        assert.equal(count('default_prompts_dialog.js'), 0, 'not requested before it is opened');
        assert.equal(count('resume_dialog.js'), 0, 'not requested before it is opened');
        await ev(`(window.showDefaultPromptsModal(), true)`);
        await waitFor(`(() => { const m = document.getElementById('default-prompts-modal'); return m && m.style.display !== 'none'; })()`, 'default-prompts-modal', 5000);
        await escape();
        await waitFor(`(() => { const m = document.getElementById('default-prompts-modal'); return !m || m.style.display === 'none'; })()`, 'default-prompts-modal closed', 3000);
        assert.equal(count('default_prompts_dialog.js'), 1, 'default_prompts_dialog.js loaded once, on first use');
        assert.equal(count('resume_dialog.js'), 0, 'opening Default Prompts does not load the Resume dialog');
        assert.equal(count('modals.js'), 1, 'and does not refetch modals.js');
        await ev(`(async () => { const { state } = await import((window.__coralStatic || '/static/') + 'state.js'); state.currentSession = { type: 'history', name: 'rs-hist', agent_type: 'claude' }; state.historySessionsList = [{ session_id: 'rs-hist', source_type: 'claude' }]; window.showResumeModal(); return true; })()`);
        await waitFor(`(() => { const m = document.getElementById('resume-modal'); return m && m.style.display !== 'none'; })()`, 'resume-modal', 5000);
        await escape();
        await waitFor(`(() => { const m = document.getElementById('resume-modal'); return !m || m.style.display === 'none'; })()`, 'resume-modal closed', 3000);
        assert.equal(count('resume_dialog.js'), 1, 'resume_dialog.js loaded once, on first use');
        assert.equal(count('modals.js'), 1, 'modals.js still loaded once');
        console.log('   ✓ Default Prompts and Resume load their own module on first use, each once, without touching modals.js');

        // 5. A fresh page where the first load fails: toast, then the next click retries (?retry=1) and opens.
        reqs.length = 0; block = 1;
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady('startup', 'liveList', 'liveListLoaded', 'settings');
        await ev(`window.showSettingsModal(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Could not load this dialog/.test(t.textContent))`, 'the failure toast', 5000);
        assert.ok(!(await shown('settings-modal')), 'no dialog after the failure');
        await ev(`window.showSettingsModal(); true`);
        await waitFor(`(() => { const m = document.getElementById('settings-modal'); return m && m.style.display !== 'none'; })()`, 'the settings dialog after the retry', 5000);
        const urls = reqs.filter(u => /\/modals\.js/.test(u)).map(u => u.split('/').pop());
        assert.deepEqual(urls, ['modals.js', 'modals.js?retry=1'], 'failed once, then retried under a new URL: ' + JSON.stringify(urls));
        await escape();
        console.log('   ✓ failed load -> toast, next click retries (?retry=1) and opens');

        // 6. Restart (opened by controls.js) loads the dialogs first, so its config form renders.
        reqs.length = 0;
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady('startup', 'liveList', 'liveListLoaded', 'settings');
        await ev(`(async () => {
            const { state } = await import((window.__coralStatic || '/static/') + 'state.js');
            state.currentSession = { type: 'live', name: 'rs', session_id: 'rs-id', agent_type: 'claude', display_name: 'Restart me', prompt: 'p' };
            (await import((window.__coralStatic || '/static/') + 'controls.js')).restartSession();
            return true;
        })()`);
        await waitFor(`(() => { const m = document.getElementById('restart-modal'); return !!m && m.style.display !== 'none' && !!document.querySelector('#restart-acf *'); })()`,
            'the restart dialog and its config form', { timeout: 5000, soft: true });
        const restart = await ev(`({ shown: document.getElementById('restart-modal').style.display !== 'none', form: !!document.querySelector('#restart-acf *'), config: typeof window.getAgentConfig })`);
        assert.deepEqual(restart, { shown: true, form: true, config: 'function' }, 'restart dialog shows its config form: ' + JSON.stringify(restart));
        assert.equal(count('modals.js'), 1, 'modals.js loaded for the restart dialog');
        await escape();
        console.log('   ✓ restart dialog (controls.js) loads the dialogs first: its agent config form renders');

        // 7. The pieces modals.js loads with it (launch_models.js, ...) are one family: a cold load requests none of them, and if one
        // fails the dialog toasts and the next click retries the piece (?retry=1) and the dialog.
        reqs.length = 0; blockMember = 1;
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady('startup', 'liveList', 'liveListLoaded', 'settings');
        for (const m of ['launch_models.js', 'launch_state.js', 'agent_config_form.js', 'persona_store.js']) assert.equal(count(m), 0, 'a cold load does not request ' + m);
        await ev(`window.showSettingsModal(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /Could not load this dialog/.test(t.textContent))`, 'the failure toast for a failed piece', 5000);
        assert.ok(!(await shown('settings-modal')), 'no dialog after a piece failed');
        await ev(`window.showSettingsModal(); true`);
        await waitFor(`(() => { const m = document.getElementById('settings-modal'); return m && m.style.display !== 'none'; })()`, 'the settings dialog after the retry', 5000);
        const memberUrls = reqs.filter(u => /\/launch_models\.js/.test(u)).map(u => u.split('/').pop());
        assert.deepEqual(memberUrls, ['launch_models.js', 'launch_models.js?retry=1'], 'the piece failed once, then retried under a new URL: ' + JSON.stringify(memberUrls));
        await escape();
        console.log('   ✓ a failed piece of modals.js -> toast, next click retries it (?retry=1) and the dialog opens');
        console.log('PASS modals_lazy_browser');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
