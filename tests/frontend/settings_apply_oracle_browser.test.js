// What Save does in the Settings dialog, setting by setting (#4896, from #4893 proposal A): applySettings (settings_dialog.js) reads about forty controls, builds the payload, sends only what changed,
// saves it and then applies each setting to the page. Splitting it into reading, saving and applying must change nothing. This opens the real dialog (showSettingsModal) with the settings empty,
// changes ONE control (or a few), calls applySettings, and records for each case: the request body (or that none was sent), the toast, whether the dialog closed, the theme (data-theme, the saved
// preference, the inline variables of a custom theme), the scrollbar class, the terminal's font size and scrollback, the browser's update-check flag, state.settings, the privacy status, and what was
// fetched. Compared exactly with fixtures/settings_apply_oracle.json, one entry per case, so a change names the setting. A floor per rule: every key the payload can hold must appear in a request of
// some case (a rule that is never exercised is not guarded). The save is stubbed (nothing reaches the server's settings), the rest goes to the isolated server. CORAL_UPDATE_GOLDEN=1 rewrites it.
const assert = require('node:assert/strict');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { framesJs } = require('./helpers/wait');
const { compareGolden } = require('./helpers/builder_golden');
const BASE = require('./test_base').BASE;
const GOLDEN = path.join(__dirname, 'fixtures', 'settings_apply_oracle.json');

// Every key the payload can hold (settings_dialog.js applySettings), the floor of the oracle.
const PAYLOAD_KEYS = ['theme', 'custom_theme', 'default_renderer', 'default_agent_type', 'default_permission_mode', 'default_working_dir', 'fit_pane_width', 'notify_needs_input', 'terminal_font_size', 'terminal_scrollback',
    'show_scrollbars', 'cli_path_claude', 'cli_path_codex', 'cli_path_agy', 'cli_path_gemini', 'cli_path_pi', 'default_model_claude', 'default_model_codex', 'default_model_agy', 'default_model_gemini', 'default_model_pi',
    'file_search_mode', 'file_search_limit', 'git_diff_mode', 'git_poll_interval_s', 'agent_priority', 'telemetry_enabled', 'board_auto_attach', 'board_health_monitor', 'auto_resume_after_api_error', 'auto_goals',
    'artifact_store_max_bytes', 'received_artifacts_max_bytes', 'artifact_retention_days', 'artifact_retention_kinds', 'debug_input_log', 'remote_access_enabled'];

// [case name, edits ([control id, 'value'|'check'|'next'|'input', value]), how the save answers]
const sel = id => [id, 'next'];
const val = (id, v) => [id, 'input', v];
const chk = id => [id, 'check'];
const CASES = [
    ['opened with no saved settings: every field is sent', [], 'ok', 'empty'],
    ['nothing changed', [], 'ok'],
    ['theme: another built-in theme', [sel('settings-theme')], 'ok'],
    ['theme: a custom theme', [[ 'settings-theme', 'pick', 'custom:Mine' ]], 'ok'],
    ['renderer', [sel('settings-renderer-select')], 'ok'],
    ['agent type', [sel('settings-agent-type')], 'ok'],
    ['permission mode', [sel('settings-permission-mode')], 'ok'],
    ['working directory (trimmed)', [val('settings-working-dir', '  /work/new dir  ')], 'ok'],
    ['claude path', [val('settings-cli-path-claude', ' /bin/claude2 ')], 'ok'],
    ['codex path', [val('settings-cli-path-codex', '/bin/codex2')], 'ok'],
    ['agy path (gemini follows)', [val('settings-cli-path-agy', '/bin/agy2')], 'ok'],
    ['pi path', [val('settings-cli-path-pi', '/bin/pi2')], 'ok'],
    ['claude model', [val('settings-default-model-claude', ' opus ')], 'ok'],
    ['codex model', [val('settings-default-model-codex', 'gpt-5')], 'ok'],
    ['agy model (gemini follows)', [val('settings-default-model-agy', 'gem-2')], 'ok'],
    ['pi model', [val('settings-default-model-pi', 'p2')], 'ok'],
    ['fit pane width', [chk('settings-fit-pane-width')], 'ok'],
    ['notify when input is needed', [chk('settings-notify-needs-input')], 'ok'],
    ['terminal font size', [sel('settings-terminal-font-size')], 'ok'],
    ['terminal font size that is not a number', [['settings-terminal-font-size', 'addopt', 'abc']], 'ok'],
    ['terminal scrollback', [sel('settings-terminal-scrollback')], 'ok'],
    ['check for updates off', [chk('settings-check-updates')], 'ok'],
    ['scrollbars hidden', [chk('settings-show-scrollbars')], 'ok'],
    ['file search mode', [sel('settings-file-search-mode')], 'ok'],
    ['file search limit', [sel('settings-file-search-limit')], 'ok'],
    ['git diff mode', [sel('settings-git-diff-mode')], 'ok'],
    ['git diff mode, with an agent open (its changed files are asked for again)', [sel('settings-git-diff-mode')], 'ok', '', true],
    ['fit pane width, with an agent open (the pane is told its width)', [chk('settings-fit-pane-width')], 'ok', '', true],
    ['git poll interval', [sel('settings-git-poll-interval')], 'ok'],
    ['agent priority normal', [chk('settings-agent-low-priority')], 'ok'],
    ['telemetry off', [chk('settings-telemetry-enabled')], 'ok'],
    ['board auto attach', [chk('settings-board-auto-attach')], 'ok'],
    ['auto resume after an API error', [chk('settings-auto-resume')], 'ok'],
    ['auto goals off', [chk('settings-auto-goals')], 'ok'],
    ['team health checks off (all teams)', [chk('settings-board-health-monitor')], 'ok'],
    ['debug input log on', [chk('settings-debug-input-log')], 'ok'],
    ['debug input log forced on by the server', [['settings-debug-input-log', 'forced']], 'ok'],
    ['remote access on', [chk('settings-remote-access-enabled')], 'ok'],
    ['artifact store limit', [['settings-artifact-store-max', 'loaded', '7']], 'ok'],
    ['artifact store limit left empty', [['settings-artifact-store-max', 'loaded', '']], 'ok'],
    ['received files limit', [['settings-received-max', 'loaded', '3']], 'ok'],
    ['retention days and kind', [['settings-retention-days', 'loaded', '30'], ['settings-retention-kind', 'next']], 'ok'],
    ['retention days over the limit', [['settings-retention-days', 'loaded', '99999']], 'ok'],
    ['several settings at once', [sel('settings-theme'), chk('settings-show-scrollbars'), sel('settings-terminal-font-size'), val('settings-working-dir', '/x'), chk('settings-auto-goals')], 'ok'],
    ['the server refuses (400 with a reason)', [val('settings-cli-path-claude', '/not/an/executable')], '400'],
    ['the server fails (500)', [chk('settings-auto-goals')], '500'],
    ['the server does not answer', [chk('settings-auto-goals')], 'reject'],
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const cases = {};
    const keys = new Set();
    const S = `(window.__coralStatic||'/static/')`;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(async () => { const st = ${S};
            window.ST = (await import(st + 'state.js')).state; window.MD = await import(st + 'modals.js'); window.SD = await import(st + 'settings_dialog.js'); window.XT = await import(st + 'xterm_renderer.js'); window.LM = await import(st + 'launch_models.js');
            await (await import(st + 'vendor_loader.js')).loadXterm();
            const host = document.createElement('div'); host.id = 'oracle-term'; host.style.cssText = 'position:fixed;left:0;top:0;width:600px;height:300px;z-index:99999'; document.body.appendChild(host); window.XT.createTerminal(host);
            // every assignment to the terminal's options (fitTerminal may move the font size afterwards, so the end value alone hides what Save set)
            { const T = window.XT.getTerminal(); window.__termSets = []; Object.defineProperty(T, 'options', { value: new Proxy(T.options, { set(t, k, v) { window.__termSets.push([k, v]); t[k] = v; return true; } }) }); }
            window.__fetched = []; window.__puts = []; window.__inflight = 0; window.__putMode = 'ok';
            const real = window.fetch; const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href).pathname, m = (o.method || 'GET').toUpperCase(); let pr;
                if (p !== '/api/system/auto-wake') window.__fetched.push(m + ' ' + p);
                if (p === '/api/settings' && m === 'PUT') { window.__puts.push(o.body); pr = window.__putMode === 'ok' ? json({ ok: true }) : window.__putMode === '400' ? json({ error: 'cli_path_claude must be an executable file' }, 400) : window.__putMode === '500' ? json({}, 500) : Promise.reject(new TypeError('Failed to fetch')); }
                else if (p === '/api/system/privacy') pr = json({ remote_access_enabled: false, remote_access_effective: false, debug_input_log: { enabled: false, forced: false, path: '' }, bind_host: '127.0.0.1' });
                else if (p === '/api/themes') pr = json({ themes: [{ name: 'Mine' }] });
                else if (p === '/api/themes/Mine') pr = json({ theme: { base: 'light', variables: { '--accent': '#123456', '--bg-primary': '#fafafa' } } });
                else if (p === '/api/agent-models') pr = json({ claude: ['opus'], codex: ['gpt'], agy: ['gem'], pi: ['p1'] });
                else if (p === '/api/system/api-key') pr = json({ key: '' });
                else if (p === '/api/system/status') pr = json({ version: '9.9.9' });
                else if (p.startsWith('/api/sessions/live/oracle-agent/')) pr = json({ files: [] });
                else if (p === '/api/system/terminal-backend' || p === '/api/system/auto-wake') pr = json({});
                else pr = real(u, o);
                window.__inflight++; Promise.resolve(pr).then(() => { window.__inflight--; }, () => { window.__inflight--; }); return pr; };
            return true; })()`);
        let saved = {}; // what the dialog shows by default, as settings: Save sends only what differs from it
        for (const [name, edits, answer, from, withAgent] of CASES) {
            const out = await ev(`(async () => { const st = window.ST, T = window.XT.getTerminal();
                st.settings = ${JSON.stringify(from || "")} === 'empty' ? {} : JSON.parse(${JSON.stringify(JSON.stringify(saved))}); window.__puts.length = 0; window.__fetched.length = 0; window.__putMode = ${JSON.stringify(answer)};
                for (const t of document.querySelectorAll('.toast')) t.remove();
                localStorage.removeItem('coral-update-check-enabled'); localStorage.removeItem('coral-theme'); document.body.classList.remove('no-scrollbars');
                { const s = document.documentElement.style; for (const p of [...s]) if (p.startsWith('--')) s.removeProperty(p); } document.documentElement.setAttribute('data-theme', 'dark');
                st.currentSession = ${withAgent ? JSON.stringify({ type: 'live', name: 'oracle-agent', session_id: 'oracle-sid' }) : 'null'}; st.selectedTeam = null;
                await window.MD.showSettingsModal();
                { const t0 = performance.now(); while (performance.now() - t0 < 1500 && window.__inflight > 0) await ${framesJs(1)}; await ${framesJs(4, 1000)}; }
                const missing = [];
                for (const [id, kind, v] of ${JSON.stringify(edits)}) {
                    const e = document.getElementById(id); if (!e) { missing.push(id); continue; }
                    if (kind === 'check') e.checked = !e.checked;
                    else if (kind === 'forced') { e.checked = true; e.disabled = true; }
                    else if (kind === 'loaded') { e.dataset.loaded = '1'; e.value = v; }
                    else if (kind === 'addopt') { if (![...e.options].some(o => o.value === v)) e.add(new Option(v, v)); e.value = v; }
                    else if (kind === 'pick') { if (![...e.options].some(o => o.value === v)) missing.push(id + '=' + v); e.value = v; }
                    else if (kind === 'next') { const cur = e.value; const o = [...e.options].find(o => o.value !== cur && !o.disabled); if (o) e.value = o.value; else missing.push(id + ' (no other option)'); }
                    else e.value = v;
                    e.dispatchEvent(new Event('input', { bubbles: true })); e.dispatchEvent(new Event('change', { bubbles: true }));
                }
                window.__dm0 = window.LM._getDefaultModels();
                const before = window.__fetched.length;
                T.options.fontSize = 11; T.options.scrollback = 1000; window.__termSets.length = 0; // here, not at the start of the case: the terminal's own layout sync (_applyFitWidth puts the Settings font back on a size change) can land while the dialog opens and add a stray fontSize before the theme (2 of 4 runs, #5110); what is recorded is what applySettings assigns
                await window.SD.applySettings();
                await ${framesJs(6, 1000)};
                const fetchedNow = [...new Set(window.__fetched.slice(before))].sort();
                window.__modelsRefetched = window.__dm0 !== window.LM._getDefaultModels(); // the saved defaults' cache: a new promise means Save emptied it (launch_models.js _invalidateDefaultModels)
                const body = window.__puts.length ? JSON.parse(window.__puts[window.__puts.length - 1]) : null;
                const sorted = o => o && Object.fromEntries(Object.keys(o).sort().map(k => [k, o[k]]));
                const toasts = [...document.querySelectorAll('.toast')].map(t => (t.querySelector('.toast-message') || t).textContent + (t.classList.contains('error') ? ' (error)' : ''));
                const vars = {}; { const s = document.documentElement.style; for (const p of [...s]) if (p.startsWith('--')) vars[p] = s.getPropertyValue(p); }
                const pv = st.privacyStatus || {};
                const dlg = document.getElementById('settings-modal');
                return { missing, puts: window.__puts.length, request: sorted(body), toasts, dialogOpen: dlg.style.display !== 'none',
                    theme: { dataTheme: document.documentElement.getAttribute('data-theme'), saved: localStorage.getItem('coral-theme'), vars },
                    noScrollbars: document.body.classList.contains('no-scrollbars'), terminal: { fontSize: T.options.fontSize, scrollback: T.options.scrollback, assigned: window.__termSets.slice() },
                    updateCheck: localStorage.getItem('coral-update-check-enabled'), settings: sorted(st.settings), privacy: { remote: pv.remote_access_enabled, restart: pv.remote_access_restart_required, debugInput: pv.debug_input_log },
                    fetched: fetchedNow, defaultModelsCacheEmptied: window.__modelsRefetched }; })()`);
            if (from === 'empty') saved = out.request;
            if (out.request) for (const k of Object.keys(out.request)) keys.add(k);
            cases[name] = JSON.stringify(out, null, 1).replace(/[^"\s|,]*coral-cdp-[A-Za-z0-9]+((?:\\\\|\/)shard\d+(-\d+)?)?/g, '<run folder>').replace(/<run folder>(?:\\\\|\/)mock-agent(?:\.exe)?/g, '<run folder>/mock-agent'); // the run's own temporary folder is in the CLI path the server fills in
        }
        // the floor: every key the payload can hold is sent by some case
        const unseen = PAYLOAD_KEYS.filter(k => !keys.has(k));
        assert.deepEqual(unseen, [], 'payload keys no case exercises: ' + unseen.join(', '));
        const problems = compareGolden(GOLDEN, cases, 'What Save did for each setting in the Settings dialog (request, toast, theme, terminal, storage, state), for settings_apply_oracle_browser.test.js. CORAL_UPDATE_GOLDEN=1 rewrites it.');
        if (problems.length) { console.error(problems.join('\n')); process.exitCode = 1; }
        else console.log(`PASS settings apply oracle: ${Object.keys(cases).length} cases identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
