// Coral Hub on first launch and on the home screen (#3221).
//   - Off unless the server offers it (#3459, status offer_at_launch, which the Go tests derive
//     from the flags): with only the built-in default URL neither a prod-tier server (not
//     enrolled) nor a dev one (no hub) opens it, and Coral Hub stays on the home screen. An
//     explicit URL or --cloud-connect prompt opens it (the flows below); a dev build with the
//     flag still does not (no hub).
//   - The server marks a new install welcome_pending. The dashboard opens "Connect to Coral
//     Hub" once, with the real connect flow inline (cloud_connect.js): Connect → link and
//     code → live status → connected → how much the hub may do → Done. The answer is stored
//     on the server, so another load does not ask again.
//   - "Skip for now" and Escape keep Coral local; "Run your own hub instead" shows the
//     coral --hub explainer. A build with no hub configured never opens it.
//   - From another computer (the cloud routes answer 403) it says to connect on the
//     computer running Coral.
//   - Settings > About reopens it without changing the stored answer.
//   - The home section sits under the launch cards, opens the screen or the explainer,
//     and its X hides it for good (server setting).
// The /api/cloud/* routes are stubbed in the page (a scripted hub), so no hub is needed.
// Desktop and phone. The test runners set CORAL_NO_WELCOME=1, so this suite marks the
// first launch itself.
//
// Expects CORAL_TEST_URL + CDP_PORT from tests/frontend/run.sh. Exit 0 / 1.

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}

const settings = async () => (await (await fetch(BASE + '/api/settings')).json()).settings || {};
const put = body => fetch(BASE + '/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
const VERIFY = 'https://app.coralhub.dev/enroll?code=ABCD-EFGH';

// A scripted connector: window.__cloud is the status (window.__cloudStart seeds it before the
// app runs); window.__cloudCalls records actions.
// The connect panel polls the status every 3 s (cloud_connect.js schedule): every step that waits for the next poll cost 3 s, six times in
// this suite (desktop and phone). The suite shortens exactly that timer to 0.3 s, so a step waits for the poll, not for the clock; the
// app code and every assertion are unchanged.
const STUB = `
  { const realSetTimeout = window.setTimeout.bind(window); window.setTimeout = (fn, ms, ...rest) => realSetTimeout(fn, ms === 3000 ? 300 : ms, ...rest); }
  window.__cloudForbidden = !!window.__cloudStartForbidden; window.__cloudCalls = [];
  window.__cloudForbiddenBody = window.__cloudStartForbiddenBody || { error: 'Cloud connector settings are only available from the Coral dashboard on this computer' };
  window.__cloud = Object.assign({ state: 'idle', hub_url: 'https://app.coralhub.dev', configured: true, max_mode: 'view', allow_hub_auto_accept: true, offer_at_launch: true }, window.__cloudStart || {});
  const realFetch = window.fetch.bind(window);
  const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } }));
  window.fetch = (url, opts = {}) => {
    const path = String(url).replace(/^https?:\\/\\/[^/]+/, '').split('?')[0];
    if (!path.startsWith('/api/cloud/')) return realFetch(url, opts);
    const m = (opts.method || 'GET').toUpperCase();
    if (window.__cloudForbidden) return json(window.__cloudForbiddenBody, 403);
    if (m !== 'GET') window.__cloudCalls.push(m + ' ' + path + (opts.body && opts.body !== '{}' ? ' ' + opts.body : ''));
    if (path === '/api/cloud/connect/cancel') { Object.assign(window.__cloud, { state: 'idle', user_code: '', verification_url: '' }); return json({ ok: true }); }
    if (path === '/api/cloud/disconnect') Object.assign(window.__cloud, { state: 'idle', user_code: '', verification_url: '' });
    if (path === '/api/cloud/connect') {
      const b = opts.body ? JSON.parse(opts.body) : {};
      if (b.mode) Object.assign(window.__cloud, { max_mode: b.mode, max_mode_saved: true });
      if ('allow_auto_accept' in b) window.__cloud.allow_hub_auto_accept = b.allow_auto_accept;
      Object.assign(window.__cloud, { state: 'enrolling', user_code: 'ABCD-EFGH', verification_url: '${VERIFY}' });
    }
    if (path === '/api/cloud/max-mode') window.__cloud.max_mode = JSON.parse(opts.body).mode;
    if (path === '/api/cloud/allow-auto-accept') window.__cloud.allow_hub_auto_accept = JSON.parse(opts.body).allow;
    // loading (#3285): the first window.__cloudLoadingFor status reads say loading, then __cloudAfterLoad applies.
    if (path === '/api/cloud/status' && window.__cloudStart && window.__cloudStart.loadingFor) {
      window.__cloudReads = (window.__cloudReads || 0) + 1;
      if (window.__cloudReads <= window.__cloudStart.loadingFor) return json({ ...window.__cloud, state: 'idle', loading: true });
      Object.assign(window.__cloud, window.__cloudStart.afterLoad || {});
    }
    return json({ ...window.__cloud });
  };`;

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Emulation, Input } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    const exceptions = [];
    Runtime.exceptionThrown(e => exceptions.push((((e.exceptionDetails || {}).exception || {}).description || 'exception').slice(0, 200)));
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (x, what, timeout = 5000) => waitFor(x, what, { soft: true, timeout });
    // Load the dashboard with the scripted connector in `cloud` state from the first request.
    // coralReady() includes settings, and the first-launch check runs before settings are marked ready.
    let seed = null;
    const load = async (cloud = {}, forbidden = false, forbiddenBody = null) => {
        if (seed) await Page.removeScriptToEvaluateOnNewDocument({ identifier: seed });
        ({ identifier: seed } = await Page.addScriptToEvaluateOnNewDocument({ source: `window.__coralEditionsLoadingWaitMs = 20; window.__cloudStart = ${JSON.stringify(cloud)}; window.__cloudStartForbidden = ${forbidden}; window.__cloudStartForbiddenBody = ${JSON.stringify(forbiddenBody)};` + STUB }));
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady();
    };
    const open = () => ev(`(() => { const m = document.getElementById('editions-modal'); return !!m && m.style.display !== 'none'; })()`);
    const screen = () => ev(`(() => { const m = document.getElementById('editions-modal'); if (!m || m.style.display === 'none') return null;
        const q = s => m.querySelector(s); const vis = s => { const e = q(s); return !!e && !e.hidden && e.offsetParent !== null; };
        const c = q('.modal-content').getBoundingClientRect();
        return { title: q('#editions-title').textContent, status: (q('[data-cloud="status"]') || {}).textContent || null,
            connect: vis('[data-cloud="connect"]'), cancel: vis('[data-cloud="cancel"]'), command: vis('[data-cloud="command-row"]') ? q('[data-cloud="command"]').textContent : null, code: vis('[data-cloud="code"]') ? q('[data-cloud="usercode"]').textContent : null,
            link: q('[data-cloud="link"]') ? q('[data-cloud="link"]').getAttribute('href') : null,
            mode: vis('[data-cloud="ask-row"]'), modeValue: (q('[data-cloud-mode]:checked') || {}).value || null, asked: (q('#editions-ask-q') || {}).textContent || null,
            autoRow: vis('[data-cloud="auto-accept-row"]'), autoOn: !!(q('[data-cloud="allow-auto-accept"]') || {}).checked,
            skip: vis('[data-editions="skip"]'), done: vis('[data-editions="done"]'), own: vis('[data-editions="own-hub"]'),
            role: m.getAttribute('role'), modal: m.getAttribute('aria-modal'), labelled: !!document.getElementById(m.getAttribute('aria-labelledby')),
            focus: !!document.activeElement && m.contains(document.activeElement),
            inView: c.left >= 0 && c.right <= innerWidth + 1 && c.top >= 0 && c.bottom <= innerHeight + 1 }; })()`);
    const click = sel => ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e) return false; e.click(); return true; })()`);
    const escape = async () => {
        await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    };
    const savedIs = (key, value) => `(async () => (await (await fetch('/api/settings')).json()).settings[${JSON.stringify(key)}] === ${JSON.stringify(value)})()`;
    const home = () => ev(`(() => { const e = document.getElementById('editions-home'); const launch = document.querySelector('#welcome-screen .welcome-launch-list');
        return { hidden: e.hidden, shown: !e.hidden && e.getBoundingClientRect().height > 0, below: launch.getBoundingClientRect().bottom <= e.getBoundingClientRect().top + 1,
            items: [...e.querySelectorAll('.editions-home-item strong')].map(s => s.textContent) }; })()`);
    const showHome = async (mobile) => {
        if (mobile) await ev(`(async () => { (await import((window.__coralStatic||'/static/')+'utils.js')).showView('welcome-screen'); return true; })()`);
        await until(`getComputedStyle(document.getElementById('welcome-screen')).display !== 'none'`, 'the home view');
    };

    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
    try {
        for (const [w, h, mobile, label] of [[1280, 800, false, 'desktop'], [390, 844, true, 'phone']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile });

            // ── Without the flag (#3459): no popup on a fresh prod-tier or dev install ──
            const PROD = { state: 'idle', hub_url: 'https://app.coralhub.dev', configured: true };
            const DEV = { state: 'off', hub_url: '', configured: false };
            for (const [tier, cloud] of [['prod tier', PROD], ['dev', DEV]]) {
                await put({ welcome_pending: 'True', welcome_choice: '', home_editions_hidden: 'False' });
                await load({ ...cloud, offer_at_launch: false });
                check(`${label}: fresh ${tier} install without the flag: no Coral Hub popup`, !(await open()));
                const st0 = await settings();
                check(`${label}: ${tier}: nothing is answered for the user (welcome_pending stays)`, st0.welcome_pending === 'True' && !st0.welcome_choice, JSON.stringify({ p: st0.welcome_pending, c: st0.welcome_choice }));
                check(`${label}: ${tier}: no connect was started`, (await ev('window.__cloudCalls')).length === 0, JSON.stringify(await ev('window.__cloudCalls')));
            }
            await showHome(mobile);
            const hm0 = await home();
            check(`${label}: Coral Hub stays reachable from the home screen`, hm0.shown && hm0.items.length > 0, JSON.stringify(hm0));
            const ccLoaded = () => ev(`performance.getEntriesByType('resource').filter(e => /\\/cloud_connect\\.js/.test(e.name)).length`);
            const ccBefore = await ccLoaded();
            await click('#editions-home [data-editions="home-hub"]');
            await until(`(() => { const m = document.getElementById('editions-modal'); return !!m && m.style.display !== 'none'; })()`, 'the screen from home');
            check(`${label}: the connect flow (cloud_connect.js) loads when the screen is opened, once, not with the page (#5818)`, ccBefore <= 1 && (await ccLoaded()) === 1, `${ccBefore} -> ${await ccLoaded()}`);
            check(`${label}: the home card still opens Connect to Coral Hub`, ((await screen()) || {}).title === 'Connect to Coral Hub');
            await escape();
            // An explicit URL (--cloud-url / CORAL_CLOUD_URL): the server offers it, as with the flag.
            await put({ welcome_pending: 'True', welcome_choice: '' });
            await load({ ...PROD, hub_url: 'https://hub.example.com', offer_at_launch: true });
            await until(`!!document.querySelector('#editions-modal [data-cloud="connect"]:not([hidden])')`, 'the screen for an explicit URL');
            check(`${label}: an explicit URL: the first-launch screen opens`, ((await screen()) || {}).title === 'Connect to Coral Hub');
            await escape();
            await until(`document.getElementById('editions-modal').style.display === 'none'`, 'closed');
            // With the flag, a dev build (no hub configured) still asks nothing.
            await put({ welcome_pending: 'True', welcome_choice: '' });
            await load({ ...DEV, offer_at_launch: true });
            check(`${label}: dev with the flag: no popup (no hub to connect to)`, !(await open()));

            // ── With the flag: first launch, Connect to Coral Hub, the whole flow ──
            await put({ welcome_pending: 'True', welcome_choice: '', home_editions_hidden: 'False' });
            await load();
            await until(`!!document.querySelector('#editions-modal [data-cloud="connect"]:not([hidden])')`, 'the Connect button');
            let s = await screen();
            check(`${label}: first launch opens "Connect to Coral Hub", ready to connect, asking what Coral Hub may do (Launch preselected, auto-accept on)`, !!s && s.title === 'Connect to Coral Hub' && s.connect && /Nothing is sent until you connect/.test(s.status) && s.skip && !s.done && s.own && s.mode && s.asked === 'What may Coral Hub do on this computer?' && s.modeValue === 'launch' && s.autoRow && s.autoOn, JSON.stringify(s));
            // The question's words hold 4.5:1 in both themes (and are looked at: CORAL_SCREENSHOT_DIR keeps the shots).
            { const { setTheme, installAudit, auditRegion } = require('./helpers/contrast'); const fs = require('node:fs');
              for (const theme of ['dark', 'light']) {
                await installAudit(ev); await setTheme(ev, theme);
                const au = await auditRegion(ev, ['#editions-modal .editions-ask']);
                check(`${label}: the question holds 4.5:1 (${theme})`, au.checked > 0 && !au.fails.length, JSON.stringify(au.fails).slice(0, 300));
                if (process.env.CORAL_SCREENSHOT_DIR) { fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true }); fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/connect-question-${label}-${theme}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }
              }
              await setTheme(ev, 'dark'); }
            check(`${label}: an accessible dialog, focused on the question's chosen answer (Launch), on screen`, !!s && s.role === 'dialog' && s.modal === 'true' && s.labelled && s.focus && s.inView && (await ev(`(() => { const a = document.activeElement; return a.type === 'radio' && a.checked && a.value === 'launch' && a.hasAttribute('data-cloud-mode'); })()`)) === true, JSON.stringify(s));
            await click('#editions-modal [data-cloud="connect"]');
            await until(`!!document.querySelector('#editions-modal [data-cloud="code"]:not([hidden])')`, 'the code');
            s = await screen();
            check(`${label}: Connect shows the hub's link (code prefilled) and the code, and waits`, !!s && s.code === 'ABCD-EFGH' && s.link === VERIFY && s.status === 'Waiting for you to approve…' && !s.connect && s.skip, JSON.stringify(s));
            check(`${label}: Connect went to this server's /api/cloud/connect`, JSON.stringify(await ev('window.__cloudCalls')) === '["POST /api/cloud/connect {\\"mode\\":\\"launch\\",\\"allow_auto_accept\\":true}"]', JSON.stringify(await ev('window.__cloudCalls')));
            check(`${label}: while the code is shown, Cancel is offered`, s.cancel, JSON.stringify(s));
            // Cancel (#3285): stops the enrollment; Connect comes back.
            await click('#editions-modal [data-cloud="cancel"]');
            await until(`!!document.querySelector('#editions-modal [data-cloud="connect"]:not([hidden])')`, 'Connect again');
            s = await screen();
            check(`${label}: Cancel calls /api/cloud/connect/cancel and offers Connect again`, (await ev('window.__cloudCalls')).includes('POST /api/cloud/connect/cancel') && s.connect && !s.code && !s.cancel, JSON.stringify({ s, calls: await ev('window.__cloudCalls') }));
            await ev(`window.__cloudCalls = []; true`);
            // The answer is held until Connect: choosing Interact saves nothing and hides the auto-accept switch (it is for Launch); choosing Launch again and turning auto-accept off goes with Connect.
            await click('#editions-modal [data-cloud-mode][value="interact"]');
            s = await screen();
            check(`${label}: before Connect a choice is held (no request), and auto-accept is shown only with Launch`, s.modeValue === 'interact' && !s.autoRow && (await ev('window.__cloudCalls')).length === 0, JSON.stringify({ s, calls: await ev('window.__cloudCalls') }));
            await click('#editions-modal [data-cloud-mode][value="launch"]');
            await click('#editions-modal [data-cloud="allow-auto-accept"]');
            // The switch means something only with Launch: away from Launch it is gone, and its saved value comes back with Launch.
            await click('#editions-modal [data-cloud-mode][value="view"]');
            check(`${label}: the auto-accept switch is shown only with Launch`, !(await screen()).autoRow);
            await click('#editions-modal [data-cloud-mode][value="launch"]');
            check(`${label}: back on Launch the switch is there, still as it was (off)`, (await screen()).autoRow && !(await screen()).autoOn);
            check(`${label}: the auto-accept switch is held too`, (await ev('window.__cloudCalls')).length === 0 && !(await screen()).autoOn);
            await click('#editions-modal [data-cloud="connect"]');
            check(`${label}: Connect carries the answer in the same call`, JSON.stringify(await ev('window.__cloudCalls')) === '["POST /api/cloud/connect {\\"mode\\":\\"launch\\",\\"allow_auto_accept\\":false}"]', JSON.stringify(await ev('window.__cloudCalls')));
            await until(`!!document.querySelector('#editions-modal [data-cloud="code"]:not([hidden])')`, 'the code again');
            // The hub approves: the panel's own status poll picks it up.
            await ev(`Object.assign(window.__cloud, { state: 'connected', label: 'My Laptop', user_code: '', verification_url: '' }); true`);
            await until(`!!document.querySelector('#editions-modal [data-editions="done"]:not([hidden])')`, 'connected', 6000);
            s = await screen();
            check(`${label}: connected: the status, the answer kept, Done instead of Skip`, !!s && s.status === 'Connected to Coral Hub as My Laptop.' && s.mode && s.modeValue === 'launch' && !s.autoOn && s.done && !s.skip && !s.code, JSON.stringify(s));
            await click('#editions-modal [data-cloud-mode][value="interact"]');
            await until(`window.__cloud.max_mode === 'interact'`, 'the mode saved');
            check(`${label}: the mode is set through /api/cloud/max-mode`, (await ev('window.__cloudCalls')).includes('PUT /api/cloud/max-mode {"mode":"interact"}'), JSON.stringify(await ev('window.__cloudCalls')));
            await click('#editions-modal [data-editions="done"]');
            await until(savedIs('welcome_choice', 'connected'), 'the answer saved');
            let st = await settings();
            check(`${label}: Done closes it and the answer is stored on the server`, !(await open()) && st.welcome_choice === 'connected' && st.welcome_pending === 'False', JSON.stringify({ c: st.welcome_choice, p: st.welcome_pending }));
            await load({ state: 'connected', label: 'My Laptop' });
            check(`${label}: another load does not ask again`, !(await open()));

            // ── The hub approves, then its tunnel fails (QA's repro): enrolled, so connected (reconnecting…) ──
            await put({ welcome_pending: 'True', welcome_choice: '' });
            await load();
            await until(`!!document.querySelector('#editions-modal [data-cloud="connect"]:not([hidden])')`, 'the Connect button');
            await click('#editions-modal [data-cloud="connect"]');
            await until(`!!document.querySelector('#editions-modal [data-cloud="code"]:not([hidden])')`, 'the code');
            // Not enrolled yet (no server id, no label): retrying is still 'not connected'.
            await ev(`Object.assign(window.__cloud, { state: 'retrying', last_error: 'the hub is unreachable; retrying', user_code: '', verification_url: '' }); true`);
            await until(`/Not connected right now/.test(document.querySelector('#editions-modal [data-cloud="status"]').textContent)`, 'not connected', 6000);
            s = await screen();
            check(`${label}: retrying without an enrollment: not connected, Skip, the question still answerable`, !!s && /^Not connected right now/.test(s.status) && s.skip && !s.done && s.mode && s.modeValue === 'launch', JSON.stringify(s));
            await ev(`Object.assign(window.__cloud, { label: 'e2e-box', server_id: 'srv-e2e' }); true`);
            await until(`!!document.querySelector('#editions-modal [data-editions="done"]:not([hidden])')`, 'connected (reconnecting)', 6000);
            s = await screen();
            check(`${label}: enrolled but the tunnel is retrying: 'Connected to Coral Hub as e2e-box (reconnecting…)', the access choice and Done`,
                !!s && /^Connected to Coral Hub as e2e-box \(reconnecting…\)/.test(s.status) && s.mode && s.done && !s.skip && !s.code, JSON.stringify(s));
            await click('#editions-modal [data-editions="done"]');
            await until(savedIs('welcome_choice', 'connected'), 'the answer saved');
            check(`${label}: Done answers it as connected`, !(await open()) && (await settings()).welcome_pending === 'False');

            // ── Skip for now, Escape, and Run your own hub ──
            await put({ welcome_pending: 'True' });
            await load();
            await until(`!!document.querySelector('#editions-modal [data-editions="skip"]')`, 'the screen');
            await click('#editions-modal [data-editions="skip"]');
            await until(savedIs('welcome_choice', 'skipped'), 'skip saved');
            check(`${label}: Skip for now closes it, sends nothing, and is remembered`, !(await open()) && (await settings()).welcome_choice === 'skipped' && (await ev('window.__cloudCalls')).length === 0);

            await put({ welcome_pending: 'True', welcome_choice: '' });
            await load();
            await until(`!!document.querySelector('#editions-modal [data-editions="skip"]')`, 'the screen');
            await escape();
            await until(savedIs('welcome_pending', 'False'), 'Escape saved');
            check(`${label}: Escape is "Skip for now"`, !(await open()) && (await settings()).welcome_choice === 'skipped');

            // Escape answers wherever the focus is (#5818 opens the screen a little later than other start-up steps that move the focus; the generic modal close would hide it without saving the answer)
            await put({ welcome_pending: 'True', welcome_choice: '' });
            await load();
            await until(`!!document.querySelector('#editions-modal [data-editions="skip"]')`, 'the screen, for Escape with the focus outside it');
            await ev(`(() => { if (document.activeElement && document.activeElement.blur) document.activeElement.blur(); document.body.focus(); return !document.getElementById('editions-modal').contains(document.activeElement); })()`);
            await escape();
            await until(savedIs('welcome_pending', 'False'), 'Escape saved with the focus outside');
            check(`${label}: Escape is "Skip for now" with the focus outside the screen`, !(await open()) && (await settings()).welcome_choice === 'skipped');

            // a dialog opened over the screen has the Escape (the topmost one closes, as everywhere else); the screen stays open and unanswered until the next Escape
            await put({ welcome_pending: 'True', welcome_choice: '' });
            await load();
            await until(`!!document.querySelector('#editions-modal [data-editions="skip"]')`, 'the screen, for a dialog over it');
            await ev(`import((window.__coralStatic || '/static/') + 'cli_dialogs.js').then(m => { m._showCLINotFoundModal('claude', 'local'); return true; })`);
            await until(`!!document.querySelector('body > .modal:not([id])')`, 'the dialog over the screen');
            await escape();
            await until(`!document.querySelector('body > .modal:not([id])')`, 'Escape closed the dialog over the screen');
            check(`${label}: Escape closes the dialog over the screen first; the screen is still open and unanswered`, (await open()) && (await settings()).welcome_pending === "True", JSON.stringify([await open(), await settings()]));
            await escape();
            await until(savedIs('welcome_pending', 'False'), 'the second Escape answers the screen');
            check(`${label}: the next Escape is "Skip for now"`, !(await open()) && (await settings()).welcome_choice === 'skipped');

            await put({ welcome_pending: 'True' });
            await load();
            await until(`!!document.querySelector('#editions-modal [data-editions="own-hub"]')`, 'the screen');
            await click('#editions-modal [data-editions="own-hub"]');
            const steps = await ev(`[...document.querySelectorAll('#editions-modal .editions-steps li')].map(l => l.textContent)`);
            check(`${label}: Run your own hub shows the coral --hub explainer`, ((await screen()) || {}).title === 'Run your own hub' && steps.length === 3 && /coral --hub/.test(steps[0]), JSON.stringify(steps));
            await click('#editions-modal [data-editions="back"]');
            check(`${label}: Back returns to Connect to Coral Hub`, ((await screen()) || {}).title === 'Connect to Coral Hub');
            await click('#editions-modal [data-editions="own-hub"]');
            await click('#editions-modal [data-editions="done-own"]');
            await until(savedIs('welcome_choice', 'own-hub'), 'own-hub saved');
            check(`${label}: Go to the dashboard closes it and is remembered`, !(await open()) && (await settings()).welcome_choice === 'own-hub');

            // ── The connector still loading (#3285): wait for it, then decide ──
            await put({ welcome_pending: 'True', welcome_choice: '' });
            await load({ loadingFor: 2, afterLoad: { state: 'connected', label: 'CLI Box' } });
            check(`${label}: loading, then already connected (coral cloud connect): no screen, answered as connected`, !(await open()) && (await settings()).welcome_choice === 'connected' && (await settings()).welcome_pending === 'False');
            await put({ welcome_pending: 'True', welcome_choice: '' });
            await load({ loadingFor: 2, afterLoad: { state: 'idle' } });
            await until(`!!document.querySelector('#editions-modal [data-cloud="connect"]:not([hidden])')`, 'the screen after loading');
            check(`${label}: loading, then not connected: the screen opens ready to connect`, ((await screen()) || {}).connect === true);
            await click('#editions-modal [data-editions="skip"]');
            // The skip's own save must land before the next case marks the launch pending again.
            await until(savedIs('welcome_choice', 'skipped'), 'skipped saved');
            // Still loading after the first-launch check gives up: no screen now, asked next launch.
            await put({ welcome_pending: 'True', welcome_choice: '' });
            await load({ loadingFor: 1000, afterLoad: { state: 'idle' } });
            // coralReady() (settings) waits for the first-launch check, so it has given up by now.
            check(`${label}: still loading: the check asked 10 times, then gave up`, (await ev(`window.__cloudReads || 0`)) >= 10);
            check(`${label}: still loading: no screen this launch, still asked next time`, !(await open()) && (await settings()).welcome_pending === 'True');
            await put({ welcome_choice: 'own-hub', welcome_pending: 'False' });

            // ── The code expired before it was approved (#3285): say why it may have, link to access ──
            for (const [how, st] of [['last_error_code', { state: 'idle', last_error_code: 'code_expired', last_error: "The code expired before it was approved. If you couldn't sign in at app.coralhub.dev, Coral Hub is in closed beta: request access at https://coralhub.dev/hub.html" }]]) {
                await put({ welcome_pending: 'True', welcome_choice: '' });
                await load(st);
                await until(`!!document.querySelector('#editions-modal [data-cloud="access-link"]')`, 'the access link');
                const ex = await ev(`(() => { const m = document.getElementById('editions-modal'); const s = m.querySelector('[data-cloud="status"]'); const a = s.querySelector('a');
                    return { text: s.textContent, href: a && a.getAttribute('href'), target: a && a.target, rel: a && a.rel, connect: !m.querySelector('[data-cloud="connect"]').hidden }; })()`);
                check(`${label}: code expired (${how}): the closed-beta text with a real link to request access, and Connect to retry`,
                    /^The code expired before it was approved\. If you couldn't sign in at app\.coralhub\.dev, Coral Hub is in closed beta: request access at https:\/\/coralhub\.dev\/hub\.html$/.test(ex.text)
                    && ex.href === 'https://coralhub.dev/hub.html' && ex.target === '_blank' && /noopener/.test(ex.rel) && ex.connect, JSON.stringify(ex));
                await click('#editions-modal [data-editions="skip"]');
                await until(savedIs('welcome_choice', 'skipped'), 'skipped saved');
            }
            await put({ welcome_choice: 'own-hub', welcome_pending: 'False' });

            // ── No hub configured (a dev build): never opens. From another computer: says where ──
            await put({ welcome_pending: 'True' });
            await load({ state: 'off', configured: false });
            check(`${label}: a build with no hub configured does not open it`, !(await open()) && (await settings()).welcome_pending === 'True');
            const offText = await ev(`(async () => (await import((window.__coralStatic||'/static/')+'cloud_connect.js')).describe({ state: 'off' }))()`);
            check(`${label}: not configured: says how to configure it (same text as the CLI)`, /^Coral Hub isn't configured on this server\. Start Coral with --cloud-url https:\/\/app\.coralhub\.dev .*then run: coral cloud connect$/.test(offText), offText);
            await put({ welcome_pending: 'False' });
            await load({}, true);
            await ev(`(async () => (await import((window.__coralStatic||'/static/')+'editions.js')).showHubSetup())()`);
            await until(`/computer running Coral/.test((document.querySelector('#editions-modal [data-cloud="status"]') || {}).textContent || '')`, 'the elsewhere message');
            s = await screen();
            check(`${label}: from another computer (a 403 without the server's code): connect on the computer running Coral, with the command to copy`, !!s && s.status === 'Connecting has to happen on the computer running Coral. On that computer, run: coral cloud connect' && !s.connect && !s.code && s.command === 'coral cloud connect', JSON.stringify(s));
            // Headless Chrome has no clipboard: record what would be copied.
            await ev(`Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async t => { window.__copied = t; } } }); true`);
            await click('#editions-modal [data-cloud="copy"]');
            await until(`document.querySelector('#editions-modal [data-cloud="copy"]').textContent === 'Copied'`, 'the copy feedback');
            check(`${label}: Copy copies the command`, (await ev(`window.__copied`)) === 'coral cloud connect' && (await ev(`document.querySelector('#editions-modal [data-cloud="copy"]').textContent`)) === 'Copied');
            await click('#editions-modal [data-editions="skip"]');
            // A server with #3285 says it itself (code local_dashboard_required): shown as is.
            const own = { error: 'Connecting has to happen on the computer running Coral. On that computer, open this dashboard, or run: coral cloud connect', code: 'local_dashboard_required', command: 'coral cloud connect' };
            await load({}, true, own);
            await ev(`(async () => (await import((window.__coralStatic||'/static/')+'editions.js')).showHubSetup())()`);
            await until(`/open this dashboard/.test((document.querySelector('#editions-modal [data-cloud="status"]') || {}).textContent || '')`, 'the server message');
            s = await screen();
            check(`${label}: from another computer (#3285 server): its message as is, and its command`, !!s && s.status === own.error && s.command === 'coral cloud connect', JSON.stringify(s));
            await click('#editions-modal [data-editions="skip"]');

            // ── Settings > About reopens it; closing keeps the stored answer ──
            await load();
            await ev(`window.showSettingsModal(); true`);
            await until(`!!document.querySelector('#about-section [data-editions="open"]') && document.querySelector('#about-section [data-editions="open"]').offsetParent !== null`, 'About > Connect to Coral Hub');
            await click('#about-section [data-editions="open"]');
            s = await screen();
            check(`${label}: Settings > About reopens it (settings closed)`, !!s && s.title === 'Connect to Coral Hub' && (await ev(`getComputedStyle(document.getElementById('settings-modal')).display === 'none'`)), JSON.stringify(s));
            await escape();
            await until(`document.getElementById('editions-modal').style.display === 'none'`, 'closed');
            check(`${label}: closing a reopened screen keeps the stored answer`, (await settings()).welcome_choice === 'own-hub');

            // ── The home section ──
            await showHome(mobile);
            let hm = await home();
            check(`${label}: home section under the launch cards: Coral Hub and Run your own hub`, hm.shown && hm.below && JSON.stringify(hm.items) === '["Coral Hub","Run your own hub"]', JSON.stringify(hm));
            await click('#editions-home [data-editions="home-hub"]');
            check(`${label}: home Coral Hub opens Connect to Coral Hub`, ((await screen()) || {}).title === 'Connect to Coral Hub');
            await click('#editions-modal [data-editions="skip"]');
            await click('#editions-home [data-editions="home-own"]');
            check(`${label}: home Run your own hub opens the explainer`, ((await screen()) || {}).title === 'Run your own hub');
            await click('#editions-modal [data-editions="done-own"]');
            check(`${label}: and closing it keeps the stored answer`, !(await open()) && (await settings()).welcome_choice === 'own-hub');
            await click('#editions-home [data-editions="hide-home"]');
            await until(`(async () => document.getElementById('editions-home').hidden && (await (await fetch('/api/settings')).json()).settings.home_editions_hidden === 'True')()`, 'the section hidden and saved');
            check(`${label}: X hides the home section and saves it`, (await home()).hidden && (await settings()).home_editions_hidden === 'True');
            await load();
            await showHome(mobile);
            check(`${label}: still hidden after reload`, (await home()).hidden);
        }
        check('no page exceptions', exceptions.length === 0, JSON.stringify(exceptions));
    } finally {
        await put({ welcome_pending: 'False', home_editions_hidden: 'False' }).catch(() => {});
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
}

run().then(() => {
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
}).catch(err => { console.error(err); process.exit(1); });
