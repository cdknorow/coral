// How much text each Settings group shows before anything is opened (the usability pass, #5985: "help people get to the setting, then expand it with more info if necessary"). The words a person
// can see in a group are counted (text nodes whose whole path is shown; a select's options are not text); a group that has been cut has a budget, so a paragraph that creeps back fails here. The
// long explanations are behind each row's (i): a button with aria-expanded that controls its text, and the text still describes its control. Read as a computer connected to Coral Hub at Launch, with
// every Hub row shown (the largest the Network and Hub group gets). Drives Chrome over CDP against the isolated dev server; the cloud routes are stood in for the page's fetch.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

// words a person can see in a group now, per the audit (artifact 141071b6); a group with no number has not been cut yet
const BUDGET = { 'Network and Hub': 80, 'Agents': 64, 'Storage and cleanup': 65, 'Terminal': 55, 'General': 40, 'Notifications': 6, 'Files and Git': 16, 'Security and privacy': 43 }; // (Security: +3 for #6047's install ID row, "Install ID", "Copy" and its hint; "No prompts, transcripts, or secrets are sent." stays visible beside the Usage analytics switch: it is what a person reads where they decide) // (Storage: 47 on a fresh server; a server where an earlier suite turned automatic cleanup on shows its two status lines, up to 62) // (Agents: +3 for #6583's switch "Coral artifact reminders": the group was already at its 61 words without the row, so any visible label raises it; the help is behind the (i) and is not counted)

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await client.Emulation.setFocusEmulationEnabled({ enabled: true });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 1000, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { window.__coral.buildAllLazy(); const real = window.fetch; const json = b => new Response(JSON.stringify(b), { headers: { 'Content-Type': 'application/json' } });
            window.fetch = async (u, o = {}) => { const p = String(u); if (p.endsWith('/api/cloud/status')) return json({ configured: true, state: 'connected', hub_url: 'https://app.coralhub.dev', label: 'my-mac', server_id: 'srv_x', max_mode: 'launch', max_mode_saved: true, allow_hub_auto_accept: true, redact_tokens: true, hub_relaunch_updates: true, hub_cross_team_settings: true, allow_hub_auto_accept_changed: { by: 'hub', user: 'hub-user-3f2a', at: '2026-10-21T20:15:00Z' } }); if (/hub-operator/.test(p)) return json({ linked: true, disabled: false, recent_actions: [] }); return real(u, o); };
            const cc = await import((window.__coralStatic || '/static/') + 'cloud_connect.js'); cc.initCloudConnect(); await cc.loadCloudStatus(); return true; })()`);
        await ev(`window.showSettingsModal(); true`);
        await waitFor(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'the dialog open', 8000);
        await waitFor(`!!document.getElementById('settings-tab-network')`, 'the Settings dialog', 8000);
        await waitFor(`document.querySelectorAll('#settings-modal .settings-info-btn').length > 5`, 'the (i) buttons', 8000);
        // General opens on what people change first: Theme, then the default agent. The render engine is engine jargon (#6383 rank 12): it is behind a disclosure of its own.
        await ev(`document.getElementById('settings-tab-general').click(); true`);
        await waitFor(`!!document.querySelector('#settings-pane-general:not([hidden])')`, 'General open', 6000);
        const seen = id => ev(`(() => { const e = document.getElementById(${JSON.stringify(id)}); return !!e && e.checkVisibility({ checkVisibilityCSS: true }); })()`);
        assert.deepEqual(await ev(`[...document.querySelectorAll('#settings-pane-general select')].filter(e => e.checkVisibility({ checkVisibilityCSS: true })).slice(0, 2).map(e => e.id)`), ['settings-theme', 'settings-agent-type'], 'Theme, then Default agent type');
        assert.equal(await seen('settings-renderer-select'), false, 'the render engine is not shown until its disclosure opens');
        await ev(`document.querySelector('#settings-pane-general .settings-engine-more > summary').click(); true`);
        assert.equal(await seen('settings-renderer-select'), true, 'and is there when it does');
        await ev(`document.querySelector('#settings-pane-general .settings-engine-more').open = false; true`);
        const groups = await ev(`[...document.querySelectorAll('#settings-tabs [role=tab]')].map(b => ({ id: b.id, name: b.querySelector('.settings-tab-name').textContent.trim() }))`);
        for (const g of groups) {
            await ev(`document.getElementById(${JSON.stringify(g.id)}).click(); true`);
            await waitFor(`(() => { const p = document.querySelector('#settings-modal .settings-pane:not([hidden])'); return !!p && p.dataset.group === ${JSON.stringify(g.id.replace('settings-tab-', ''))}; })()`, `${g.name} shown`, 4000);
            await waitFor(`!/Loading…|Couldn't read/.test(document.querySelector('#settings-modal .settings-pane:not([hidden])').textContent)`, `${g.name} loaded`, 6000).catch(() => {}); // the count is of the settled pane (a pane that never settles is counted as it is)
            await ev(`(() => { const r = document.getElementById('settings-tools-folder-row'); if (r) r.hidden = true; return true; })()`); // the normal state has no warning row: a Windows runner shows it for real (its tools folder is a temp folder), and the 64-word state with it is checked on its own
            const words = await ev(`(() => { const pane = document.querySelector('#settings-modal .settings-pane:not([hidden])'); let n = 0;
                const shown = e => { for (let p = e; p; p = p.parentElement) { const s = getComputedStyle(p); if (s.display === 'none' || s.visibility === 'hidden' || p.hidden) return false; const d = p.parentElement; if (d && d.tagName === 'DETAILS' && !d.open && p.tagName !== 'SUMMARY') return false; /* a closed disclosure hides what is in it */ } return true; };
                const w = document.createTreeWalker(pane, NodeFilter.SHOW_TEXT);
                for (let t = w.nextNode(); t; t = w.nextNode()) { const e = t.parentElement; if (!e || e.closest('select, option, script, style, .material-icons') || !shown(e)) continue; n += t.nodeValue.trim().split(/\\s+/).filter(Boolean).length; }
                return n; })()`);
            (global.__tb = global.__tb || []).push(`${g.name}: ${words}`);
            const seenText = words > (BUDGET[g.name] ?? Infinity) ? await ev(`(() => { const pane = document.querySelector('#settings-modal .settings-pane:not([hidden])'); const out = []; const shown = e => { for (let p = e; p; p = p.parentElement) { const s = getComputedStyle(p); if (s.display === 'none' || s.visibility === 'hidden' || p.hidden) return false; const d = p.parentElement; if (d && d.tagName === 'DETAILS' && !d.open && p.tagName !== 'SUMMARY') return false; } return true; }; const w = document.createTreeWalker(pane, NodeFilter.SHOW_TEXT); for (let t = w.nextNode(); t; t = w.nextNode()) { const e = t.parentElement; if (!e || e.closest('select, option, script, style, .material-icons') || !shown(e)) continue; const v = t.nodeValue.trim(); if (v) out.push(v.slice(0, 80)); } return out; })()`) : null;
            if (BUDGET[g.name] !== undefined) assert.ok(words <= BUDGET[g.name], `${g.name} shows ${words} words, budget ${BUDGET[g.name]}: ${JSON.stringify(seenText)}`);
        }

        if (process.env.TB_DUMP) assert.fail('TABLE ' + global.__tb.join(' | '));
        // the (i) of every Hub row: a button that controls its text, closed at first, the text describing the control, opens by keyboard
        await ev(`document.getElementById('settings-tab-network').click(); true`);
        const rows = await ev(`[...document.querySelectorAll('#settings-cloud .settings-cloud-switch-row:not([hidden]), #settings-cloud .settings-cloud-mode:not([hidden])')].map(r => { const b = r.querySelector('.settings-info-btn'); const box = b && document.getElementById(b.getAttribute('aria-controls')); const input = r.querySelector('input[type=checkbox], select'); const d = input && input.getAttribute('aria-describedby'); return { id: r.id, btn: !!b, name: b && b.getAttribute('aria-label'), expanded: b && b.getAttribute('aria-expanded'), boxHidden: box && box.hidden, words: box ? box.textContent.trim().split(/\\s+/).length : 0, describes: !!(d && box && d.split(' ').some(x => box.querySelector('#' + x) || box.id === x)) || !input || input.tagName === 'SELECT' }; })`);
        assert.ok(rows.length >= 5, 'the Hub rows are shown: ' + JSON.stringify(rows.map(r => r.id)));
        for (const r of rows) {
            assert.equal(r.btn, true, `${r.id}: has an (i)`); assert.equal(r.expanded, 'false'); assert.equal(r.boxHidden, true, `${r.id}: its text is closed`); assert.match(r.name, /^About /); assert.ok(r.words > 3);
            assert.ok(r.describes, `${r.id}: the control is still described by the text`);
        }
        await ev(`document.querySelector('#settings-cloud-redact-row .settings-info-btn').focus(); true`);
        await client.Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' }); await client.Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
        await waitFor(`document.querySelector('#settings-cloud-redact-row .settings-info-btn').getAttribute('aria-expanded') === 'true' && !document.getElementById(document.querySelector('#settings-cloud-redact-row .settings-info-btn').getAttribute('aria-controls')).hidden`, 'the redaction text open by keyboard', 3000);
        console.log('PASS Settings shows little text up front, with the rest behind each row\'s (i)');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
