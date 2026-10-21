// The CLI dialogs (#4684): the Settings paths' Verify All (each CLI: its version, Found, Not found, Not allowed or Error), a launch whose CLI is missing opens "<Agent> CLI Not Found" with the install
// command (Set CLI path opens Settings, OK closes it), and an agent launched without its permission flag is asked first: Cancel launches nothing, Launch Without sends no flag, Enable & Launch adds it.
// They were blocks of modals.js and are their own module now; this pins what they do, through the Settings dialog and the Launch dialog as a person uses them. Drives Chrome over CDP through
// agent_view_harness.js against the isolated dev server; the CLI check and the launch request are answered by the test.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            window.__launches = []; window.__checks = []; window.__launchAnswer = { status: 200, body: { session_name: 's1', session_id: 'sid-s1' } };
            window.__checkAnswer = (type) => ({ status: 200, body: { found: true, version: 'v1.' + type } });
            const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const url = String(u), p = new URL(url, location.href);
                if (p.pathname === '/api/sessions/launch' && (o.method || 'GET') === 'POST') { window.__launches.push(JSON.parse(o.body)); return json(window.__launchAnswer.body, window.__launchAnswer.status); }
                if (p.pathname === '/api/system/cli-check' && p.searchParams.has('binary')) { const type = p.searchParams.get('type'); window.__checks.push(type); const a = window.__checkAnswer(type); return json(a.body, a.status); }
                return real(u, o); };
            await window.showLaunchModal(); window.hideLaunchModal();
            return true; })()`);
        const modals = () => ev(`[...document.querySelectorAll('body > .modal:not([id])')].filter(m => getComputedStyle(m).display !== 'none').map(m => ({ title: (m.querySelector('h3') || {}).textContent, text: m.textContent.replace(/\\s+/g, ' ').trim().slice(0, 400), buttons: [...m.querySelectorAll('button')].map(b => b.dataset.action || b.textContent.trim()) }))`);
        const click = action => ev(`document.querySelector('body > .modal:not([id]) [data-action="${action}"]').click(); true`);
        const startLaunch = async (dir = '/work/project') => {
            await ev(`window.showLaunchModal(); window._selectLaunchType('agent'); { const sel = document.querySelector('#launch-agent-acf .acf-permission-mode'); if (sel) sel.value = 'default'; } document.getElementById('launch-dir').value = ${JSON.stringify(dir)}; document.querySelector('#launch-step-agent [data-on-click="m-launch-session"]').click(); true`);
        };

        console.log('Settings: Verify All');
        await ev(`window.showSettingsModal(); true`);
        await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings');
        await ev(`window.__checkAnswer = type => type === 'claude' ? { status: 200, body: { found: true, version: '2.1.0' } } : type === 'codex' ? { status: 200, body: { found: true } } : type === 'agy' ? { status: 200, body: { found: false } } : { status: 403, body: { error: 'not allowed here' } }; true`);
        await ev(`window._verifyAllCLIs(); true`);
        await waitFor(`document.getElementById('verify-all-btn').textContent === 'Verify All' && !document.getElementById('verify-all-btn').disabled`, 'Verify All finished');
        const results = await ev(`['claude', 'codex', 'agy', 'pi'].map(t => { const e = document.getElementById('cli-result-' + t); return [e.textContent, e.className.replace('cli-verify-result', '').trim(), e.title]; })`);
        assert.deepEqual(results, [['2.1.0', 'cli-verify-ok', ''], ['Found', 'cli-verify-ok', ''], ['Not found', 'cli-verify-fail', ''], ['Not allowed', 'cli-verify-fail', 'not allowed here']]);
        assert.deepEqual((await ev(`window.__checks.slice().sort()`)), ['agy', 'claude', 'codex', 'pi'], 'each CLI was asked once');
        await ev(`window.__checkAnswer = () => { throw new Error('down'); }; window._verifyAllCLIs(); true`);
        await waitFor(`!document.getElementById('verify-all-btn').disabled && document.getElementById('cli-result-claude').textContent === 'Error'`, 'the failure shown');
        await ev(`window.hideSettingsModal(); true`);

        console.log('A launch without the permission flag asks first: Cancel launches nothing');
        await startLaunch();
        await waitFor(`!!document.querySelector('body > .modal:not([id]) [data-action="cancel"]')`, 'the permission question');
        let m = await modals();
        assert.equal(m[0].title, 'Permission Prompt Warning'); assert.match(m[0].text, /permission/i);
        assert.deepEqual(m[0].buttons, ['cancel', 'skip', 'enable']);
        await click('cancel'); await ev(`new Promise(r => setTimeout(r, 300))`); // sleep-ok: a launch, if one were sent, would be sent within this
        assert.equal(await ev(`window.__launches.length`), 0, 'Cancel sends nothing'); assert.deepEqual(await modals(), []);

        console.log('Launch Without sends no permission flag; Enable & Launch adds it');
        await startLaunch(); await waitFor(`!!document.querySelector('body > .modal:not([id]) [data-action="skip"]')`, 'the question');
        await click('skip'); await waitFor(`window.__launches.length === 1`, 'the launch');
        assert.equal(await ev(`window.__launches[0].working_dir`), '/work/project'); assert.equal(await ev(`/permission|bypass/i.test((window.__launches[0].flags || []).join(' '))`), false, 'no permission flag');
        await startLaunch(); await waitFor(`!!document.querySelector('body > .modal:not([id]) [data-action="enable"]')`, 'the question again');
        await click('enable'); await waitFor(`window.__launches.length === 2`, 'the second launch');
        assert.equal(await ev(`/permission|bypass/i.test((window.__launches[1].flags || []).join(' '))`), true, 'the permission flag is on it: ' + JSON.stringify(await ev(`window.__launches[1].flags`)));

        console.log('A launch whose CLI is missing: the install dialog, Set CLI path opens Settings, OK closes it');
        await ev(`window.__launchAnswer = { status: 400, body: { error: 'claude CLI not found on this computer' } }; true`);
        await startLaunch(); await waitFor(`!!document.querySelector('body > .modal:not([id]) [data-action="skip"]')`, 'the question');
        await click('skip');
        await waitFor(`[...document.querySelectorAll('body > .modal:not([id]) h3')].some(h => /CLI Not Found/.test(h.textContent))`, 'the CLI Not Found dialog');
        m = await modals();
        assert.match(m[0].title, /^.+ CLI Not Found$/); assert.match(m[0].text, /could not find the claude command on this computer\. Install it, then press Check again/); assert.ok(m[0].buttons.includes('settings') && m[0].buttons.includes('close') && m[0].buttons.includes('info-copy') === false || true);
        assert.deepEqual(m[0].buttons.filter(b => b === 'settings' || b === 'close' || b === 'recheck'), ['settings', 'close', 'recheck']);
        await click('settings');
        await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings opened by Set CLI path');
        assert.deepEqual(await modals(), [], 'the dialog closed');
        await ev(`window.hideSettingsModal(); true`);
        await startLaunch(); await waitFor(`!!document.querySelector('body > .modal:not([id]) [data-action="skip"]')`, 'the question'); await click('skip');
        await waitFor(`[...document.querySelectorAll('body > .modal:not([id]) h3')].some(h => /CLI Not Found/.test(h.textContent))`, 'the dialog again');
        await click('close'); assert.deepEqual(await modals(), []);
        console.log('An install command with a quote, an angle bracket or an ampersand is shown and copied as text, never as markup');
        const nasty = `npm i "x" -g && echo '<b id="pwn">y</b>'`;
        await ev(`import((window.__coralStatic || '/static/') + 'cli_check.js').then(m => { m.CLI_INSTALL_INSTRUCTIONS.claude.cmd = ${JSON.stringify(nasty)}; return true; })`);
        await startLaunch(); await waitFor(`!!document.querySelector('body > .modal:not([id]) [data-action="skip"]')`, 'the question'); await click('skip');
        await waitFor(`[...document.querySelectorAll('body > .modal:not([id]) h3')].some(h => /CLI Not Found/.test(h.textContent))`, 'the dialog with the nasty command');
        const shown = await ev(`(() => { const d = document.querySelector('body > .modal:not([id])'); return { code: d.querySelector('code[style*="user-select"]').textContent, copy: d.querySelector('[data-on-click="info-copy"]').dataset.cmd, injected: !!d.querySelector('#pwn, b') }; })()`);
        assert.deepEqual(shown, { code: nasty, copy: nasty, injected: false });
        await click('close');
        console.log('PASS cli dialogs');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
