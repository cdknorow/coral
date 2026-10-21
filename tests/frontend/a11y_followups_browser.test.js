// Accessibility follow-ups to the 146-148 pass (#5681):
//   - every focusable control of Team settings (Messaging), the artifact viewer, Settings and the Launch dialog shows a focus ring that is PROVED, light and
//     dark: the contrast helper focuses each control with a keyboard-style focus and checks the ring's colour against what is behind it (3:1); a control with
//     no ring is flagged (checked on a control that has none);
//   - the Team settings dialog's Close button closes it, and "Rename board" does not (the dialog's close handler was on the first button of its header);
//   - focus returns to the opener when the Team settings dialog, the artifact viewer, Settings and the Launch dialog close, by Escape and by their button;
//   - at 390 px with a mouse (no touch pointer) the close buttons are 44 px and the "Who can read this" summary is 44 px high;
//   - the Linked teams heading holds its words only: the "Manage in Coral Hub" link is beside it, not inside it.
// Drives Chrome over CDP against the isolated dev server through agent_view_harness.js; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { auditRegion, setTheme } = require('./helpers/contrast');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const TEAM = 'a11yfu-' + Date.now().toString(36);
    const HEX = 'a'.repeat(64);
    const key = async (k, code, vk) => { await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code, windowsVirtualKeyCode: vk }); await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code, windowsVirtualKeyCode: vk }); };
    const FOCUSABLE = `a[href], button:not([disabled]), input:not([disabled]):not([type=hidden]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])`;
    const SURFACES = {
        'Team settings, Messaging': { root: '#team-working-mode-dialog', open: `import((window.__coralStatic || '/static/') + 'team_working_mode.js').then(m => m.showTeamWorkingMode('${TEAM}', { workspace: false })); true`,
            ready: `!!document.querySelector('[data-settings-tab=messaging]')`, then: `document.querySelector('[data-settings-tab=messaging]').click(); true`, ready2: `!!document.querySelector('.xt-primary') && !!document.querySelector('.xt-links')`,
            isOpen: `!!document.querySelector('#team-working-mode-dialog')?.open`, esc: true, closeBtn: `document.querySelector('#team-working-mode-dialog .modal-close-btn')` },
        'Artifact viewer': { root: '.artifact-viewer', open: `import((window.__coralStatic || '/static/') + 'artifact_viewer.js').then(m => m.openArtifactViewer('${HEX}', {})); true`,
            ready: `!!document.querySelector('.artifact-readers > summary')`, then: `document.querySelector('.artifact-readers > summary').click(); true`, ready2: `document.querySelector('.artifact-readers').open`,
            isOpen: `!!document.querySelector('.artifact-viewer') && !document.querySelector('.artifact-viewer').hidden`, closeBtn: `document.querySelector('.artifact-viewer-close')` },
        'Settings': { root: '#settings-modal', open: `(async () => { await window.showSettingsModal('network'); return true; })()`, ready: `getComputedStyle(document.getElementById('settings-modal')).display !== 'none' && !!document.getElementById('settings-cloud-hub-cross-team')`,
            isOpen: `getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, closeBtn: `[...document.querySelectorAll('#settings-modal .modal-actions .btn')].find(x => x.textContent.trim() === 'Cancel')` },
        'Launch': { root: '#launch-modal', open: `(async () => { await window.showLaunchModal(); window._selectLaunchType('team'); return true; })()`, ready: `getComputedStyle(document.getElementById('launch-step-team')).display !== 'none'`,
            isOpen: `getComputedStyle(document.getElementById('launch-modal')).display !== 'none'`, closeBtn: `[...document.querySelectorAll('#launch-step-team .modal-actions .btn')].find(x => x.textContent.trim() === 'Cancel')` },
    };
    const fresh = async (w = 1280, h = 900, mobile = false) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(() => { const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href);
                if (p.pathname.endsWith('/readers')) return json({ machine_owner: 'op', owners: [{ kind: 'operator', id: 'x', records: 1, teams: [{ label: 't' }] }], note: 'A Coral control, not an operating-system boundary.' });
                if (p.pathname === '/api/board/${TEAM}/cross-team-state') return json({ status: { state: 'ready', text: 'A team on another Coral can message this team.' }, links: [{ server_label: 'Laptop', server_id: 'srv_abc12', team_label: 'research', refreshed: '2026-10-21T10:00:00Z' }], hub_may_change: true, can_change: true, owner_path_live: true, last_changed: {} });
                if (p.pathname === '/api/cloud/status') return json({ configured: true, connected: true, state: 'connected', max_mode: 'view', hub_cross_team_settings: true, server_id: 'srv_demo' });
                return real(u, o); };
            const t = document.createElement('button'); t.id = 'opener'; t.textContent = 'opener'; t.style.cssText = 'position:fixed;left:4px;bottom:4px;z-index:1'; document.body.append(t); })(); true`);
    };
    const show = async (s) => { await ev(s.open); await waitFor(s.ready, 'the surface', 15000); if (s.then) { await ev(s.then); await waitFor(s.ready2, 'its content', 15000); } };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await client.Emulation.setFocusEmulationEnabled({ enabled: true }); // :focus-visible and a ring need a focused page
        await fresh();
        assert.ok(await ev(`fetch('/api/board/${TEAM}/subscribe', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: 'boss-session' }) }).then(r => r.status < 300)`), 'a team');

        console.log('1. A focus ring on every focusable control, proved, light and dark');
        for (const theme of ['dark', 'light']) for (const [name, s] of Object.entries(SURFACES)) {
            await fresh(); await setTheme(ev, theme); await show(s);
            const n = await ev(`[...document.querySelector('${s.root}').querySelectorAll('${FOCUSABLE}')].filter(e => e.checkVisibility({ checkVisibilityCSS: true, contentVisibilityAuto: true }) && e.getClientRects().length).map((e, i) => { e.setAttribute('data-ring-probe', i); return i; }).length`);
            assert.ok(n >= 3, `${name}: its controls were found (${n})`);
            const r = await auditRegion(ev, [s.root], { focus: Array.from({ length: n }, (_, i) => `[data-ring-probe="${i}"]`) });
            const bad = r.fails.filter(f => f.what === 'focus ring');
            assert.deepEqual(bad, [], `${theme}, ${name}: every control has a ring of 3:1 (${n} checked): ` + JSON.stringify(bad));
        }
        // the check can fail: a control with its ring switched off is flagged
        await fresh(); await ev(`(() => { const b = document.createElement('button'); b.id = 'noring'; b.textContent = 'no ring'; b.style.cssText = 'outline:none;box-shadow:none;position:fixed;top:4px;left:4px'; document.body.append(b); })(); true`);
        const neg = await auditRegion(ev, ['body'], { focus: ['#noring'] });
        assert.ok(neg.fails.some(f => f.what === 'focus ring' && /no ring/.test(f.text)), 'a button with no ring is flagged: ' + JSON.stringify(neg.fails.filter(f => f.what === 'focus ring')));

        console.log('2. Team settings: Close closes it, Rename board does not');
        await fresh(); await show(SURFACES['Team settings, Messaging']);
        await ev(`document.querySelector('#team-working-mode-dialog .board-rename-toggle').click(); true`);
        assert.equal(await ev(SURFACES['Team settings, Messaging'].isOpen), true, 'Rename board leaves the dialog open');
        await ev(`document.querySelector('#team-working-mode-dialog .modal-close-btn').click(); true`);
        await waitFor(`!document.querySelector('#team-working-mode-dialog')`, 'Close closes the dialog');

        console.log('3. Focus returns to the opener when each dialog closes');
        for (const [name, s] of Object.entries(SURFACES)) for (const how of ['Escape', 'its button']) {
            await fresh(); await ev(`document.getElementById('opener').focus(); true`); await show(s);
            if (how === 'Escape') await key('Escape', 'Escape', 27); else await ev(`(${s.closeBtn}).click(); true`);
            await waitFor(`!(${s.isOpen})`, `${name} closes by ${how}`, 8000);
            await waitFor(`document.activeElement && document.activeElement.id === 'opener'`, `${name}, closed by ${how}: the focus is back on the opener`, 5000).catch(async () => assert.fail(`${name}, closed by ${how}: focus is on ${await ev(`document.activeElement.tagName + '#' + document.activeElement.id + '.' + document.activeElement.className`)}`));
        }

        console.log('4. 390 px with a mouse: 44px close buttons and disclosure');
        await fresh(390, 844, true); await show(SURFACES['Team settings, Messaging']);
        const c1 = await ev(`(() => { const r = document.querySelector('#team-working-mode-dialog .modal-close-btn').getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; })()`);
        assert.ok(c1[0] >= 44 && c1[1] >= 44, 'the dialog\'s close button is 44px: ' + c1);
        await ev(`document.getElementById('team-working-mode-dialog').close(); true`);
        await show(SURFACES['Artifact viewer']);
        const c2 = await ev(`(() => { const q = s => { const r = document.querySelector(s).getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; }; return { close: q('.artifact-viewer-close'), summary: q('.artifact-readers > summary') }; })()`);
        assert.ok(c2.close[0] >= 44 && c2.close[1] >= 44, 'the viewer\'s close button is 44px: ' + c2.close); assert.ok(c2.summary[1] >= 44, 'the disclosure is 44px high: ' + c2.summary);

        console.log('5. The Linked teams heading holds its words only');
        await fresh(); await show(SURFACES['Team settings, Messaging']);
        const h = await ev(`(() => { const h = document.querySelector('.xt-links h3.xt-h'); const a = document.querySelector('.xt-links a.update-link'); return { text: h.textContent.trim(), linkInHeading: !!a.closest('h1, h2, h3, h4, h5, h6'), linkText: a.textContent.trim(), sameRow: Math.abs(h.getBoundingClientRect().top - a.getBoundingClientRect().top) < 30 }; })()`);
        assert.equal(h.text, 'Linked teams that can message this one'); assert.equal(h.linkInHeading, false); assert.equal(h.linkText, 'Manage in Coral Hub'); assert.ok(h.sameRow, 'the link sits beside the heading');
        console.log('   ✓');
    } finally { await removeBoards(ev, [TEAM]); await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
