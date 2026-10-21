// First run (#5835): a CLI that is not installed is said where the person is looking, and can be checked again. Before, the check ("claude CLI was not found", Check again) sat inside the
// collapsed More options of the New Agent dialog, so the first sign was the "CLI Not Found" dialog after pressing Launch, which could not check again: after installing, OK, Launch, and a Coral
// that was started before the install may not see it. Now the line is under the folder, and the Not Found dialog has Check again: found -> it closes and launches; not found -> it says so.
// Drives Chrome over CDP against the isolated dev server; the CLI check and the launch are answered by the test.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(() => { window.__found = false; window.__launches = []; window.__checks = [];
            const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href), m = o.method || 'GET';
                if (p.pathname === '/api/system/cli-check') { window.__checks.push(p.search); return window.__found ? json({ found: true, version: '1', status: 'available' }) : json({ found: false, status: 'missing', install_command: 'npm install -g @anthropic-ai/claude-code', install_url: 'https://code.claude.com/docs/en/overview' }); }
                if (p.pathname === '/api/sessions/launch' && m === 'POST') { window.__launches.push(1); return window.__found ? json({ session_name: 'first', session_id: 'sid-first' }) : json({ error: 'claude CLI not found. Install it: npm install -g @anthropic-ai/claude-code' }, 500); }
                if (p.pathname === '/api/board/projects') return json([]);
                return real(u, o); };
            return true; })()`);

        console.log('1. the missing CLI is said under the folder, with More options closed');
        await ev(`(async () => { await window.showLaunchModal(); window._selectLaunchType('agent'); return true; })()`);
        await waitFor(`(() => { const s = document.getElementById('launch-cli-slot'); const w = s && s.querySelector('.acf-cli-warning'); return !!w && w.offsetParent !== null && /claude CLI was not found/.test(w.textContent); })()`, 'the line under the folder', 8000);
        const line = await ev(`(() => { const s = document.getElementById('launch-cli-slot'); const w = s.querySelector('.acf-cli-warning'); const more = document.getElementById('launch-agent-more'); const dir = document.getElementById('launch-dir').getBoundingClientRect(), r = w.getBoundingClientRect();
            return { moreHidden: more.hidden, below: r.top >= dir.bottom, button: [...w.querySelectorAll('button')].map(b => b.textContent), command: /npm install -g @anthropic-ai\\/claude-code/.test(w.textContent), role: w.getAttribute('role'), inForm: !!document.querySelector('#launch-agent-acf .acf-cli-warning') }; })()`);
        assert.deepEqual(line, { moreHidden: true, below: true, button: ['Check again'], command: true, role: 'status', inForm: false });

        await installAudit(ev);
        for (const theme of ['dark', 'light']) {
            await setTheme(ev, theme);
            const audit = await auditRegion(ev, ['#launch-cli-slot']);
            assert.ok(audit.checked > 0 && !audit.fails.length, `${theme}: the line holds 4.5:1: ` + JSON.stringify(audit.fails).slice(0, 300));
        }
        await setTheme(ev, 'dark');

        console.log('2. its Check again looks again, and the line goes when the CLI is there');
        await ev(`window.__found = true; document.querySelector('#launch-cli-slot .acf-cli-warning button').click(); true`);
        await waitFor(`getComputedStyle(document.querySelector('#launch-cli-slot .acf-cli-warning')).display === 'none'`, 'the line gone', 8000);
        assert.ok((await ev(`window.__checks`)).some(q => /source=cli_recheck/.test(q)), 'the recheck says it is one');

        console.log('3. the Not Found dialog: Check again says so when it is still missing, and launches when it is found');
        await ev(`window.__found = false; window.__launches.length = 0; document.getElementById('launch-dir').value = '/tmp'; window.launchSession(); true`);
        await waitFor(`!!document.querySelector('body > .modal:not([id]) [data-action="recheck"]')`, 'the Not Found dialog', 8000);
        assert.deepEqual(await ev(`[...document.querySelectorAll('body > .modal:not([id]) .modal-actions button')].map(b => b.dataset.action)`), ['settings', 'close', 'recheck']);
        await ev(`document.querySelector('body > .modal:not([id]) [data-action="recheck"]').click(); true`);
        await waitFor(`/Still not found/.test((document.querySelector('body > .modal:not([id]) .cli-recheck-status') || {}).textContent || '')`, 'the still-missing words', 8000);
        assert.equal(await ev(`window.__launches.length`), 1, 'no second launch while it is missing');
        await ev(`window.__found = true; document.querySelector('body > .modal:not([id]) [data-action="recheck"]').click(); true`);
        await waitFor(`window.__launches.length === 2`, 'the launch after the CLI was found', 8000);
        assert.equal(await ev(`!!document.querySelector('body > .modal:not([id]) [data-action="recheck"]')`), false, 'the dialog closed');
        console.log('PASS the missing CLI is said early and can be checked again');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
