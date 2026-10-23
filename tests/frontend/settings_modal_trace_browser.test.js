// The Settings dialog, as it opens (#4653): showSettingsModal (modals.js) is a 245-line function that fills about a hundred controls from the saved settings, the browser's own settings, and a handful of
// requests (privacy status, themes, the model list, the API key, the version). Splitting it must change nothing. This opens it with the settings empty, with every kind of value set, with custom themes and a
// poll interval no preset has, with the requests failing, asking for a group, on a local Coral and on the Coral Hub, and records what every control holds (value, checked, disabled, read-only, placeholder,
// its options), the text of the status lines, which group is open, and what was fetched. Compared with fixtures/settings_modal_trace.json (sha256 per scenario, with readable lines). CORAL_UPDATE_GOLDEN=1
// rewrites it. Drives Chrome over CDP through agent_view_harness.js; the hub case is served as the hub serves it (boot flag mode "cloud", as in hub_nav_browser).
const assert = require('node:assert/strict');
const { safePaused } = require('./helpers/paused');
const fs = require('node:fs');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
// Windows has its own: Coral runs there on pty with no tmux choice, so the Terminal section (and its sha) is not the one recorded where tmux is the default.
const GOLDEN = path.join(__dirname, 'fixtures', process.platform === 'win32' ? 'settings_modal_trace.windows.json' : 'settings_modal_trace.json');
const sha = t => require('node:crypto').createHash('sha256').update(t).digest('hex');
// The default folder's placeholder is the server's home folder. Elsewhere the runner points HOME into the run folder (so it reads
// <run folder>/home); on Windows the home folder is USERPROFILE, this computer's own (C:\Users\<name>), which the server and this
// suite share. It is written <home>, so the Windows golden holds on any computer, not only the one that recorded it.
const HOME_JSON = JSON.stringify(require('node:os').homedir()).slice(1, -1); // as it appears inside the trace's JSON text
const atHome = json => (process.platform === 'win32' && HOME_JSON ? json.split(HOME_JSON).join('<home>') : json);

const FULL = { theme: 'light', default_renderer: 'xterm', default_agent_type: 'codex', default_permission_mode: 'acceptEdits', default_working_dir: '/work/dir', cli_path_claude: '/bin/claude', cli_path_codex: '/bin/codex', cli_path_agy: '/bin/agy', cli_path_pi: '/bin/pi',
    default_model_claude: 'opus', default_model_codex: 'gpt', default_model_agy: 'gem', default_model_pi: 'p1', terminal_font_size: '16', terminal_scrollback: '50000', fit_pane_width: true, notify_needs_input: true, show_scrollbars: false,
    file_search_mode: 'project', file_search_limit: '1000', git_diff_mode: 'head', git_poll_interval_s: '60', agent_priority: 'normal', telemetry_enabled: 'false', board_auto_attach: 'true', auto_resume_after_api_error: 'true', auto_goals: 'false',
    debug_input_log: 'true', remote_access_enabled: 'true' };
const SCEN = [
    ['empty settings', {}, {}, undefined, {}],
    ['every setting set', FULL, {}, undefined, {}],
    ['custom themes and an odd poll interval', { ...FULL, custom_theme: 'Mine', git_poll_interval_s: '45', cli_path_agy: '', cli_path_gemini: '/bin/gemini', default_model_agy: '', default_model_gemini: 'g-old' }, { themes: true }, undefined, {}],
    ['requests fail', { ...FULL }, { privacy: 500, themes: 500, models: 500, key: 401, status: 500 }, undefined, {}],
    ['the api key and models answer', FULL, { key: 'sk-test-123' }, undefined, {}],
    ['a group asked for', FULL, {}, 'terminal', {}],
    ['browser settings', { ...FULL }, {}, undefined, { 'coral-group-by-team': 'false', 'coral-update-check-enabled': 'false' }],
];

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network, Fetch, Emulation } = client;
    const ev = async expression => { const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 500)); return r.result.value; };
    const { waitFor, settle } = require('./helpers/wait').bind(ev);
    const { framesJs } = require('./helpers/wait');
    const trace = {};
    let cloud = false;
    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Network.setCacheDisabled({ cacheDisabled: true });
        await Network.setBypassServiceWorker({ bypass: true }); // the service worker would answer the document before Fetch sees it
        await Fetch.enable({ patterns: [{ urlPattern: `${BASE}/`, resourceType: 'Document', requestStage: 'Response' }, { urlPattern: `${BASE}/auth/session`, requestStage: 'Request' }] });
        safePaused(client, async e => {
            if (e.request.url.endsWith('/auth/session')) return Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: 'application/json' }], body: Buffer.from(JSON.stringify({ authenticated: true, email: 'tester@example.com', csrf: 'x' })).toString('base64') });
            if (e.responseStatusCode !== 200) return Fetch.continueRequest({ requestId: e.requestId });
            const { body, base64Encoded } = await Fetch.getResponseBody({ requestId: e.requestId });
            let html = base64Encoded ? Buffer.from(body, 'base64').toString('utf8') : body;
            if (cloud) html = html.replace('"mode":"local"', '"mode":"cloud"');
            await Fetch.fulfillRequest({ requestId: e.requestId, responseCode: 200, responseHeaders: (e.responseHeaders || []).filter(h => !/^content-length$/i.test(h.name)), body: Buffer.from(html).toString('base64') });
        });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
        const SNAP = `(() => {
            const m = document.getElementById('settings-modal');
            const controls = [...m.querySelectorAll('input, select, textarea')].map(e => [e.id || e.name || e.className, e.type, e.type === 'checkbox' || e.type === 'radio' ? e.checked : e.value, e.disabled, e.readOnly, e.placeholder || '', e.hidden || !!e.closest('[hidden]'), e.tagName === 'SELECT' ? [...e.options].map(o => o.value + (o.disabled ? '!' : '')).join('|') : '', e.dataset.filled || '']);
            const lists = [...m.querySelectorAll('datalist')].map(d => [d.id, [...d.options].map(o => o.value).join('|')]);
            const text = id => { const e = document.getElementById(id); return e ? e.textContent.replace(/\\\\s+/g, ' ').trim() : null; };
            return { display: m.style.display, controls, lists, notice: text('settings-default-models-notice'), version: text('app-version-label'), apiKey: (document.getElementById('settings-api-key') || {}).value + '|' + (document.getElementById('settings-api-key') || {}).placeholder,
                openGroups: [...m.querySelectorAll('[data-settings-group], .settings-group')].filter(e => e.classList.contains('active') || e.classList.contains('open') || e.getAttribute('aria-selected') === 'true' || e.getAttribute('aria-expanded') === 'true' || (e.hidden === false && e.tagName !== 'BUTTON')).map(e => e.id || e.dataset.settingsGroup || e.className).slice(0, 12),
                hiddenRows: [...m.querySelectorAll('.settings-row, .setting-row, label')].filter(e => getComputedStyle(e).display === 'none').length,
                fetches: [...new Set(window.__fetched)].sort() };
        })()`;
        const run = async (name, settings, answers, group, local) => {
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
            await waitFor(`!!window.T || !!window.__coralStatic || document.readyState === 'complete'`, 'the page', { timeout: 15000 });
            await settle(2500); // the page's own startup loads (and, on the hub, its sign-in check) settle over frames; settle caps at 2500 ms
            assert.equal(await ev(`document.body.classList.contains('cloud-mode')`), cloud, 'the page is served as the hub or as a local Coral: ' + name + ' wants cloud=' + cloud);
            const out = await ev(`(async () => {
                const st = window.__coralStatic || '/static/';
                const { state } = await import(st + 'state.js'); const modals = await import(st + 'modals.js');
                localStorage.clear(); for (const [k, v] of Object.entries(${JSON.stringify(local)})) localStorage.setItem(k, v);
                window.__fetched = [];
                const real = window.fetch; const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
                const A = ${JSON.stringify(answers)};
                window.__inflight = 0; // requests the dialog has started and not yet finished (the data-ready signal)
                window.fetch = (u, o = {}) => {
                    const url = String(u), p = new URL(url, location.href).pathname; if (p !== '/api/system/auto-wake') window.__fetched.push(((o.method) || 'GET').toUpperCase() + ' ' + p); // the page's own auto-wake poll comes when it comes, not from the dialog: it is left out of the fetches
                    let pr;
                    if (p === '/api/system/privacy') pr = A.privacy === 500 ? json({}, 500) : json({ remote_access_enabled: true, debug_input_log: true, bind_host: '127.0.0.1' });
                    else if (p === '/api/themes') pr = A.themes === 500 ? json({}, 500) : json({ themes: A.themes ? [{ name: 'Mine' }, { name: 'Other' }] : [] });
                    else if (p === '/api/agent-models') pr = A.models === 500 ? json({}, 500) : json({ claude: ['opus', 'sonnet'], codex: ['gpt'], agy: ['gem'], pi: ['p1'] });
                    else if (p === '/api/system/api-key') pr = A.key === 401 ? json({}, 401) : json({ key: typeof A.key === 'string' ? A.key : '' });
                    else if (p === '/api/system/status') pr = A.status === 500 ? json({}, 500) : json({ version: '9.9.9' });
                    else pr = real(u, o);
                    window.__inflight++;
                    Promise.resolve(pr).then(() => { window.__inflight--; }, () => { window.__inflight--; }); // count it done whether it answers or errors
                    return pr;
                };
                state.settings = ${JSON.stringify(settings)};
                try { await modals.showSettingsModal(${JSON.stringify(group)}); await (async () => { const start = performance.now();
                    while (performance.now() - start < 1200 && window.__inflight > 0) { await ${framesJs(1)}; } // the data is in once no request is in flight
                    let prev = -1, since = performance.now(); // then let the dialog's geometry settle before reading, within the same 1200 ms cap
                    while (performance.now() - start < 1200) { await ${framesJs(1)}; let s = 0; for (const e of document.querySelectorAll('#settings-modal *')) { const b = e.getBoundingClientRect(); s += b.left + b.top + b.width + b.height; } s = Math.round(s); if (s === prev) { if (performance.now() - since >= 120) break; } else { prev = s; since = performance.now(); } }
                })(); /* wait until in-flight requests are 0, then settle; capped at 1200 ms -- no fixed sleep */ } finally { window.fetch = real; }
                return true;
            })()`);
            void out;
            const snap = await ev(SNAP);
            trace[name] = JSON.parse(atHome(JSON.stringify(snap)).replace(/[^"\s|,]*coral-cdp-[A-Za-z0-9]+((?:\\\\|\/)shard\d+(-\d+)?)?/g, '<run folder>').replace(/<run folder>(?:\\\\|\/)mock-agent(?:\.exe)?/g, '<run folder>/mock-agent')); // the run's own temporary folder is in some placeholders, with the shard's folder under it (shard1, or shard1-1 once a shard was replaced) when a path runs through it
        };
        for (const [name, settings, answers, group, local] of SCEN) await run(name, settings, answers, group, local);
        cloud = true;
        await run('hub: every setting set', FULL, {}, undefined, {});
        await run('hub: empty settings', {}, {}, undefined, {});
        cloud = false;
        // The same twice.
        await run('again', {}, {}, undefined, {});
        for (const f of Object.keys(trace['again'])) if (JSON.stringify(trace['again'][f]) !== JSON.stringify(trace['empty settings'][f])) {
            const a = trace['again'][f], b = trace['empty settings'][f];
            const detail = Array.isArray(a) ? a.map((x, i) => JSON.stringify(x) === JSON.stringify(b[i]) ? null : [JSON.stringify(b[i]), JSON.stringify(x)]).filter(Boolean).slice(0, 3) : [JSON.stringify(b), JSON.stringify(a)];
            assert.fail('not the same twice: ' + f + ' ' + JSON.stringify(detail).slice(0, 600));
        }
        assert.equal(sha(JSON.stringify(trace['again'])), sha(JSON.stringify(trace['empty settings'])), 'the same trace twice');
        delete trace['again'];
        if (process.env.TRACE_DUMP) fs.writeFileSync(process.env.TRACE_DUMP, JSON.stringify(trace));
        const hashes = {}, lines = {};
        for (const [name, s] of Object.entries(trace)) {
            hashes[name] = sha(JSON.stringify(s));
            lines[name] = `${s.controls.length} controls | notice ${s.notice} | version ${s.version} | apiKey ${s.apiKey} | hidden rows ${s.hiddenRows} | fetches ${[...new Set(s.fetches)].sort().join(' ; ')} | values ${s.controls.filter(c => c[2] !== '' && c[2] !== false).slice(0, 12).map(c => c[0] + '=' + c[2]).join(', ')}`;
        }
        if (process.env.CORAL_UPDATE_GOLDEN) {
            fs.mkdirSync(path.dirname(GOLDEN), { recursive: true });
            fs.writeFileSync(GOLDEN, JSON.stringify({ about: 'what showSettingsModal filled in, scenario by scenario, for settings_modal_trace_browser.test.js: sha256 of every control, list, status line and request, and a readable line', hashes, lines }, null, 1) + '\n');
            console.log('golden written: ' + Object.keys(hashes).length + ' scenarios');
            return;
        }
        const want = JSON.parse(fs.readFileSync(GOLDEN, 'utf8'));
        assert.deepEqual(Object.keys(hashes), Object.keys(want.hashes), 'the same scenarios');
        for (const k of Object.keys(want.hashes)) if (hashes[k] !== want.hashes[k]) assert.fail(`${k}: changed.\n  now:  ${lines[k]}\n  was:  ${want.lines[k]}`);
        console.log(`PASS settings modal trace: ${Object.keys(want.hashes).length} scenarios identical`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
