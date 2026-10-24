// First-time single-agent friction (#5990, QA's walk A1, A5, A7): New Agent does not start on the home folder (the person chooses; choosing the home folder says what that means), an unnamed agent
// is called by its folder or its CLI (not "Agent"), and a phone's Home, with nothing running, says what Coral is for and offers the same Launch agent as the desktop's welcome.
// (The missing CLI keeping Launch off is in cli_first_run_browser; the first page's quiet links are in the Go golden of internal/auth.) Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const open = async (w, h, mobile) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });
            await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            await ev(`(() => { const real = window.fetch; const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
                window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href); if (p.pathname === '/api/system/cli-check') return json({ found: true, version: '1', status: 'available' }); if (p.pathname === '/api/board/projects') return json([]); return real(u, o); }; return true; })()`);
        };

        console.log('1. New Agent opens with no folder chosen; the home folder, once chosen, says what it means');
        await open(1280, 900, false);
        await ev(`(async () => { await window.showLaunchModal(); window._selectLaunchType('agent'); return true; })()`);
        await waitFor(`!!document.getElementById('launch-step-agent') && getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none'`, 'the agent step');
        const first = await ev(`(() => { const d = document.getElementById('launch-dir'), n = document.getElementById('launch-dir-note'); return { value: d.value, placeholder: d.placeholder, noteHidden: n.hidden, root: d.dataset.coralRoot }; })()`);
        assert.equal(first.value, '', 'no folder is chosen for the person'); assert.equal(first.placeholder, 'Choose a folder'); assert.equal(first.noteHidden, true); assert.ok(first.root, 'the server has a root to compare with');
        const typed = v => ev(`(() => { const d = document.getElementById('launch-dir'); d.value = ${JSON.stringify(v)}; d.dispatchEvent(new Event('input', { bubbles: true })); return !document.getElementById('launch-dir-note').hidden; })()`);
        assert.equal(await typed(first.root), true, 'the home folder: the note shows');
        assert.deepEqual(await ev(`(() => { const d = document.getElementById('launch-dir'), n = document.getElementById('launch-dir-note'); return { described: d.getAttribute('aria-describedby'), role: n.getAttribute('role') }; })()`), { described: 'launch-dir-note', role: null }, 'the note describes the Folder field while it applies and is not a live region');
        assert.equal(await ev(`document.getElementById('launch-dir-note').textContent`), 'This is your home folder: the agent can change any file in it.');
        assert.equal(await typed(first.root + '/project'), false, 'a folder in it is a choice: no note');
        assert.equal(await ev(`document.getElementById('launch-dir').hasAttribute('aria-describedby')`), false, 'and the field is described by nothing when it does not apply');
        assert.equal(await typed('~'), true, '"~" is the home folder too');
        assert.equal(await typed(''), false);

        console.log('2. an unnamed agent is called by its folder, else its CLI; never the home folder');
        const names = await ev(`import((window.__coralStatic || '/static/') + 'session_state.js').then(m => [m.defaultAgentName('/work/my-app', 'claude'), m.defaultAgentName('/work/my-app/', 'codex'), m.defaultAgentName(document.body.dataset.coralRoot, 'claude'), m.defaultAgentName('', 'agy'), m.defaultAgentName('~', 'pi'), m.defaultAgentName('', ''), m.resolveSessionIdentity({ agent_type: 'claude', working_directory: '/work/my-app' }), m.resolveSessionIdentity({ agent_type: 'terminal', working_directory: '/work/my-app' }), m.resolveSessionIdentity({ display_name: 'Ada', working_directory: '/work/my-app' })])`);
        assert.deepEqual(names, ['my-app', 'my-app', 'Claude', 'Antigravity', 'Pi', 'Agent', 'my-app', 'Terminal', 'Ada']);

        console.log('3. a phone with nothing running: a welcome and Launch agent; the desktop keeps its plain line');
        await open(390, 844, true);
        const phone = await ev(`(() => { const vis = e => !!e && e.checkVisibility({ checkVisibilityCSS: true }); const w = document.querySelector('.session-list li.empty-welcome'), p = document.querySelector('.session-list li.empty-plain');
            return { welcome: vis(w), plain: vis(p), title: w && w.querySelector('h2').textContent, buttons: w ? [...w.querySelectorAll('button')].map(b => b.querySelector('strong').textContent) : [], target: w ? [...w.querySelectorAll('button')].map(b => Math.round(b.getBoundingClientRect().height)) : [] }; })()`);
        assert.equal(phone.welcome, true); assert.equal(phone.plain, false); assert.equal(phone.title, 'What do you want to work on?'); assert.deepEqual(phone.buttons, ['New agent', 'New team']);
        assert.ok(phone.target.every(h => h >= 44), 'the cards are touch targets: ' + phone.target);
        await ev(`document.querySelector('.session-list li.empty-welcome button').click(); true`);
        await waitFor(`!!document.getElementById('launch-step-agent') && getComputedStyle(document.getElementById('launch-step-agent')).display !== 'none'`, 'the same New Agent dialog the desktop opens'); // (the dialog is a lazy template: it is not in the page until the click has built it)
        assert.equal(await ev(`document.getElementById('launch-agent-title').textContent.replace(/\\s+/g, ' ').trim().replace(/^←\\s*/, '')`), 'New agent');
        await open(1280, 900, false);
        const desk = await ev(`(() => { const shown = e => !!e && getComputedStyle(e).display !== 'none'; return { welcome: shown(document.querySelector('.session-list li.empty-welcome')), plain: shown(document.querySelector('.session-list li.empty-plain')) }; })()`); // (the sidebar's section may itself be folded away on a desktop with nothing running: the rule is what is checked)
        assert.deepEqual(desk, { welcome: false, plain: true });
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
