// Settings > Terminal on a system with only pty (Windows, or no tmux): the Terminal group keeps to its word budget there too. The one paragraph that said what pty-only means was 52 words up front (the Windows
// run counted 132 against the budget of 60, #6155): it is one short line now, with the detail behind Learn more. The terminal-backend answer is stood in as pty-only (choice false), so it reads the same on any
// machine. Drives Chrome over CDP against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

// words a person can see in a group now, per the audit (artifact 141071b6); a group with no number has not been cut yet
const TERMINAL_BUDGET = 60;
const WITH_WARNING_BUDGET = 64; // 60, and the tools folder warning's label, line and button when that row shows // the same as settings_text_budget_browser's, now on a pty-only system // (Security: +3 for #6047's install ID row, "Install ID", "Copy" and its hint; "No prompts, transcripts, or secrets are sent." stays visible beside the Usage analytics switch: it is what a person reads where they decide) // (Storage: 47 on a fresh server; a server where an earlier suite turned automatic cleanup on shows its two status lines, up to 62)

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await client.Emulation.setFocusEmulationEnabled({ enabled: true });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 1000, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { window.__coral.buildAllLazy(); const real = window.fetch; const json = b => new Response(JSON.stringify(b), { headers: { 'Content-Type': 'application/json' } });
            window.fetch = async (u, o = {}) => { const p = String(u); if (p.endsWith('/api/system/terminal-backend')) return json({ saved: '', running: 'pty', forced: false, choice: false, restart_required: false, can_restart: true, running_agents: 0, leftover_tmux_agents: 0, next: 'pty' }); if (p.endsWith('/api/cloud/status')) return json({ configured: true, state: 'connected', hub_url: 'https://app.coralhub.dev', label: 'my-mac', server_id: 'srv_x', max_mode: 'launch', max_mode_saved: true, allow_hub_auto_accept: true, redact_tokens: true, hub_relaunch_updates: true, hub_cross_team_settings: true, allow_hub_auto_accept_changed: { by: 'hub', user: 'hub-user-3f2a', at: '2026-10-21T20:15:00Z' } }); if (/hub-operator/.test(p)) return json({ linked: true, disabled: false, recent_actions: [] }); return real(u, o); };
            const cc = await import((window.__coralStatic || '/static/') + 'cloud_connect.js'); cc.initCloudConnect(); await cc.loadCloudStatus(); return true; })()`);
        await ev(`window.showSettingsModal(); true`);
        await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'the dialog open', 8000);
        await waitFor(`!!document.getElementById('settings-tab-network')`, 'the Settings dialog', 8000);
        await waitFor(`document.querySelectorAll('#settings-modal .settings-info-btn').length > 5`, 'the (i) buttons', 8000);
        await ev(`document.getElementById('settings-tab-terminal').click(); true`);
        await waitFor(`(() => { const p = document.querySelector('#settings-modal .settings-pane:not([hidden])'); return !!p && p.dataset.group === 'terminal' && !document.getElementById('settings-terminal-backend').hidden; })()`, 'the Terminal group, loaded', 8000);
        await ev(`(() => { const r = document.getElementById('settings-tools-folder-row'); if (r) r.hidden = true; return true; })()`); // the normal state has no warning row: a Windows runner shows it for real (its tools folder is a temp folder), and the 64-word state with it is checked on its own
        const seen = await ev(`(() => { const pane = document.querySelector('#settings-modal .settings-pane:not([hidden])'); const out = [];
            const shown = e => { for (let p = e; p; p = p.parentElement) { const s = getComputedStyle(p); if (s.display === 'none' || s.visibility === 'hidden' || p.hidden) return false; } return true; };
            const w = document.createTreeWalker(pane, NodeFilter.SHOW_TEXT);
            for (let t = w.nextNode(); t; t = w.nextNode()) { const e = t.parentElement; if (!e || e.closest('select, option, script, style, .material-icons') || !shown(e)) continue; const v = t.nodeValue.trim(); if (v) out.push(v); }
            return out; })()`);
        const words = seen.join(' ').split(/\s+/).filter(Boolean).length;
        assert.ok(words <= TERMINAL_BUDGET, `Terminal shows ${words} words on a pty-only system, budget ${TERMINAL_BUDGET}: ${JSON.stringify(seen)}`);
        // With the agent tools folder warning showing too (a portable install in the profile: the row's label, its one line and the "How to fix it" button, 16 words), the group stays within 64 words: the 60 of the budget plus that row, which is no longer left out of the count (its line carries its own subject because a status is announced alone)
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.renderToolsFolderStatus({ tools_folder: { path: 'C:\\\\Users\\\\someone\\\\Coral\\\\tools', writable_by: ['Users', 'Everyone'] } }); return true; })()`);
        const withRow = await ev(`(() => { const pane = document.querySelector('#settings-modal .settings-pane:not([hidden])'); const out = [];
            const shown = e => { for (let p = e; p; p = p.parentElement) { const s = getComputedStyle(p); if (s.display === 'none' || s.visibility === 'hidden' || p.hidden) return false; } return true; };
            const w = document.createTreeWalker(pane, NodeFilter.SHOW_TEXT);
            for (let t = w.nextNode(); t; t = w.nextNode()) { const e = t.parentElement; if (!e || e.closest('select, option, script, style, .material-icons') || !shown(e)) continue; const v = t.nodeValue.trim(); if (v) out.push(v); }
            return out; })()`);
        const wordsWithRow = withRow.join(' ').split(/\s+/).filter(Boolean).length;
        assert.ok(withRow.some(t => /agent tools folder can be changed without administrator rights/.test(t)), 'the tools folder line shows: ' + JSON.stringify(withRow));
        assert.ok(wordsWithRow <= WITH_WARNING_BUDGET, `Terminal shows ${wordsWithRow} words with the tools folder warning too, budget ${WITH_WARNING_BUDGET}: ${JSON.stringify(withRow)}`);
        await ev(`document.getElementById('settings-tools-folder-row').hidden = true; true`);
        const note = await ev(`(() => { const p = document.getElementById('tb-fixed'), b = document.getElementById('tb-learn-fixed'), d = document.getElementById('tb-d-fixed'); const vis = e => e.getClientRects().length > 0;
            return { noteShown: vis(p), note: p.textContent.trim(), learnShown: vis(b), expanded: b.getAttribute('aria-expanded'), controls: b.getAttribute('aria-controls'), detailHidden: d.hidden, detail: d.textContent.trim() }; })()`);
        assert.ok(note.noteShown && note.learnShown, JSON.stringify(note));
        assert.match(note.note, /pty/); assert.match(note.note, /wake next time/);
        assert.equal(note.expanded, 'false'); assert.equal(note.controls, 'tb-d-fixed'); assert.equal(note.detailHidden, true);
        assert.match(note.detail, /no tmux on this system/); assert.match(note.detail, /resume their conversation/);
        await ev(`document.getElementById('tb-learn-fixed').click(); true`);
        const open = await ev(`({ expanded: document.getElementById('tb-learn-fixed').getAttribute('aria-expanded'), hidden: document.getElementById('tb-d-fixed').hidden, label: document.getElementById('tb-learn-fixed').textContent.trim() })`);
        assert.deepEqual(open, { expanded: 'true', hidden: false, label: 'Show less' });
        console.log('PASS Settings > Terminal on a pty-only system: a short line, the rest behind Learn more, within the word budget');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
