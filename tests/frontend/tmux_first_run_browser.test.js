// First run (#6046): tmux not installed is the top cause of failed first launches. The New Agent dialog says it once, under the folder, with the one install command for this OS
// and a Check again; Launch stays off with that line as its reason; nothing is installed. Where the backend does not need tmux (pty), or tmux is there, the dialog says nothing.
// Drives Chrome over CDP against the isolated dev server; the system status, the CLI check and the launch are answered by the test.
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
        await ev(`(() => { window.__tmux = { tmux_available: false, tmux_required: true, effective_backend: 'tmux', tmux_install_command: 'brew install tmux' };
            window.__launches = []; window.__status = [];
            const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            window.fetch = async (u, o = {}) => { const p = new URL(String(u), location.href), m = o.method || 'GET';
                if (p.pathname === '/api/system/status') { window.__status.push(o.cache || 'default'); const r = await real(u, o); const b = await r.json(); return json({ ...b, ...window.__tmux }); }
                if (p.pathname === '/api/system/cli-check') return json({ found: true, version: '1', status: 'available' });
                if (p.pathname === '/api/sessions/launch' && m === 'POST') { window.__launches.push(1); return json({ session_name: 'first', session_id: 'sid-first' }); }
                if (p.pathname === '/api/sessions/launch-team' && m === 'POST') { window.__launches.push(1); return json({ board: 'tmuxteam', sessions: [], failures: [] }); }
                if (p.pathname === '/api/board/projects') return json([]);
                return real(u, o); };
            return true; })()`);
        const open = `(async () => { await window.showLaunchModal(); window._selectLaunchType('agent'); document.getElementById('launch-dir').value = '/tmp'; return true; })()`;
        const slot = `document.getElementById('launch-tmux-slot')`;

        console.log('1. tmux missing: the line is under the folder with the one command and Check again; Launch is off and the line is its reason');
        await new Promise(r => setTimeout(r, 5200)); // sleep-ok: the status answer is shared for 5 s
        await ev(`document.getElementById('tmux-missing-banner').style.display = ''; true`); // the app-wide banner is showing
        await ev(open);
        await waitFor(`(() => { const s = ${slot}; return !!s && s.offsetParent !== null && /tmux is not installed/.test(s.textContent); })()`, 'the tmux line', 8000);
        const line = await ev(`(() => { const s = ${slot}, go = document.getElementById('launch-agent-go'), dir = document.getElementById('launch-dir').getBoundingClientRect();
            return { below: s.getBoundingClientRect().top >= dir.bottom, command: s.querySelector('code').textContent, buttons: [...s.querySelectorAll('button')].map(b => b.textContent), role: s.getAttribute('role'),
                disabled: go.disabled, describedBy: go.getAttribute('aria-describedby') === s.id, blocks: s.dataset.blocksLaunch, moreHidden: document.getElementById('launch-agent-more').hidden }; })()`);
        assert.deepEqual(line, { below: true, command: 'brew install tmux', buttons: ['Check again'], role: 'status', disabled: true, describedBy: true, blocks: '1', moreHidden: true });
        assert.equal(await ev(`getComputedStyle(document.getElementById('tmux-missing-banner')).display`), 'none', 'said once: the app-wide banner steps aside while the dialog has the line');
        await ev(`window.launchSession(); true`);
        assert.equal(await ev(`window.__launches.length`), 0, 'a launch is not sent while tmux is missing');

        await installAudit(ev);
        for (const theme of ['dark', 'light']) {
            await setTheme(ev, theme);
            const audit = await auditRegion(ev, ['#launch-tmux-slot']);
            assert.ok(audit.checked > 0 && !audit.fails.length, `${theme}: the line holds 4.5:1: ` + JSON.stringify(audit.fails).slice(0, 300));
        }
        await setTheme(ev, 'dark');

        console.log('2. Check again looks again (a fresh status); still missing: the line stays, Launch stays off');
        await ev(`window.__status.length = 0; ${slot}.querySelector('button').click(); true`);
        await waitFor(`window.__status.includes('no-store')`, 'a fresh status request', 8000);
        assert.equal(await ev(`${slot}.dataset.blocksLaunch`), '1');
        assert.equal(await ev(`document.getElementById('launch-agent-go').disabled`), true);

        console.log('3. installed: Check again clears the line, Launch comes back and works');
        await ev(`window.__tmux = { tmux_available: true, tmux_required: true, effective_backend: 'tmux', tmux_install_command: 'brew install tmux' }; ${slot}.querySelector('button').click(); true`);
        await waitFor(`getComputedStyle(${slot}).display === 'none' && !document.getElementById('launch-agent-go').disabled`, 'the line gone and Launch on', 8000);
        assert.equal(await ev(`document.getElementById('launch-agent-go').hasAttribute('aria-describedby')`), false);
        await ev(`document.getElementById('launch-agent-go').click(); true`);
        await waitFor(`window.__launches.length === 1`, 'the launch once tmux is there', 8000);

        await new Promise(r => setTimeout(r, 5200)); // sleep-ok: the status answer is shared for 5 s
        console.log('4. the pty backend does not need tmux: nothing is said, Launch is on');
        await ev(`window.__tmux = { tmux_available: false, tmux_required: false, effective_backend: 'pty', tmux_install_command: 'brew install tmux' }; window._selectLaunchType('back'); true`);
        await ev(`window.__status.length = 0; true`);
        await ev(`window._selectLaunchType('agent'); true`);
        await waitFor(`window.__status.length > 0 && !!document.getElementById('launch-agent-go')`, 'the status read');
        await new Promise(r => setTimeout(r, 400)); // sleep-ok: nothing is said: there is no event to wait for
        assert.deepEqual(await ev(`({ shown: ${slot}.offsetParent !== null, blocks: ${slot}.dataset.blocksLaunch || '', disabled: document.getElementById('launch-agent-go').disabled })`), { shown: false, blocks: '', disabled: false });

        await new Promise(r => setTimeout(r, 5200)); // sleep-ok: the status answer is shared for 5 s
        console.log('5. a status that cannot be read says nothing about tmux and does not hold the launch');
        await ev(`window.__tmux = { tmux_available: 'unknown' }; window._selectLaunchType('back'); window._selectLaunchType('agent'); true`);
        await new Promise(r => setTimeout(r, 600)); // sleep-ok: nothing is said: there is no event to wait for
        assert.deepEqual(await ev(`({ shown: ${slot}.offsetParent !== null, disabled: document.getElementById('launch-agent-go').disabled })`), { shown: false, disabled: false });
        await new Promise(r => setTimeout(r, 5200)); // sleep-ok: the status answer is shared for 5 s
        console.log('6. Launch Team: the same line, Launch Team off and no launch sent; the banner is back when the dialog closes');
        await ev(`window.__tmux = { tmux_available: false, tmux_required: true, effective_backend: 'tmux', tmux_install_command: 'brew install tmux' }; window._selectLaunchType('back'); window._selectLaunchType('team'); document.getElementById('team-board-name').value = 'tmuxteam'; document.getElementById('launch-team-dir').value = '/tmp'; window._validateTeamName(); if (!document.querySelector('#team-agents-list .team-agent-row')) window._addTeamAgent('worker', 'do the work'); true`); // a team that is otherwise ready to launch
        const team = `document.getElementById('team-tmux-slot')`;
        await waitFor(`/tmux is not installed/.test(${team}.textContent) && document.getElementById('launch-team-go').disabled`, 'the team line and Launch Team off', 8000);
        assert.deepEqual(await ev(`({ command: ${team}.querySelector('code').textContent, describedBy: document.getElementById('launch-team-go').getAttribute('aria-describedby') === ${team}.id })`), { command: 'brew install tmux', describedBy: true });
        const before = await ev(`window.__launches.length`);
        await ev(`window.launchTeam(); true`);
        assert.equal(await ev(`window.__launches.length`), before, 'no member launch is sent while tmux is missing');
        await ev(`window.hideLaunchModal(); true`);
        assert.notEqual(await ev(`getComputedStyle(document.getElementById('tmux-missing-banner')).display`), 'none', 'the banner is back once the dialog is closed');
        console.log('7. a launch on another server (a local multi-server hub) skips this computer\'s tmux check: no line, no block, no status request; coming back to this computer shows it again');
        await ev(`window.__tmux = { tmux_available: false, tmux_required: true, effective_backend: 'tmux', tmux_install_command: 'brew install tmux' }; window._selectLaunchType('back'); window._selectLaunchType('agent'); true`);
        await waitFor(`/tmux is not installed/.test(${slot}.textContent) && document.getElementById('launch-agent-go').disabled`, 'the line on this computer', 8000);
        await ev(`window.__status.length = 0; window.__remote = true; import((window.__coralStatic || '/static/') + 'prerequisites.js').then(m => m.checkTmuxForLaunch(${slot}, { isLocal: () => !window.__remote })); true`);
        await waitFor(`!document.getElementById('launch-agent-go').disabled`, 'Launch on for a remote server', 8000);
        assert.deepEqual(await ev(`({ shown: ${slot}.offsetParent !== null, blocks: ${slot}.dataset.blocksLaunch || '', status: window.__status.length })`), { shown: false, blocks: '', status: 0 });
        await ev(`window.__remote = false; import((window.__coralStatic || '/static/') + 'prerequisites.js').then(m => m.checkTmuxForLaunch(${slot}, { isLocal: () => !window.__remote })); true`);
        await waitFor(`/tmux is not installed/.test(${slot}.textContent) && document.getElementById('launch-agent-go').disabled`, 'the line again on this computer', 8000);
        await ev(`document.getElementById('launch-modal').style.display = 'none'; true`);
        console.log('PASS tmux missing is said once in the launch form and holds Launch until it is there');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
