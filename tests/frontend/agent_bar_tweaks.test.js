// End-to-end acceptance test for the Agents sidebar tweaks
// (specs/AGENT_BAR_TWEAKS.md D1–D7, badge-only D5):
//   - identity chain display_name -> auto_name -> board_job_title -> Agent on
//     row label, avatar initials, header, terminal label and Sending-to
//     placeholder (summary and first_prompt are goal text only, never names or
//     initials); passive "No goal yet" label, sparkle-only
//     goal trigger; generic spread-merge of WS diffs (omitted preserves,
//     explicit null/empty/value overwrites)
//   - mobile-only banner/meta rows hidden on desktop
//   - context bar labelled "ctx N%" / "ctx full"; informational only, no
//     clickable Compact control at any level (users type /compact in chat)
//   - Mode quick action shows Default/Plan/Accept (or "Mode" when unknown)
//   - Agents nav badge counts rows needing attention without reordering rows
//   - density sizes and the Transcript tab rename
//
// Drives a running Coral server via headless Chrome + Chrome DevTools Protocol.
//
// Expects:
//   CORAL_URL  — base URL of a running Coral dev server (default http://127.0.0.1:8462)
//   CDP_PORT   — port of a running headless Chrome with --remote-debugging-port
//                (default 9222)
//
// Exits 0 on success, 1 on any failing scenario, 2 on harness error.

const CDP = require('chrome-remote-interface');

const BASE = process.env.CORAL_URL || 'http://127.0.0.1:8462';

// Safety: shells spawned by a running Coral inherit CORAL_URL=http://127.0.0.1:8420
// (production). This suite mutates server state, so refuse the production port
// unless explicitly overridden. tests/frontend/run.sh always sets CORAL_URL to
// its isolated server.
if (/:8420(\/|$)/.test(BASE) && !process.env.CORAL_TEST_ALLOW_PROD) {
    console.error(`Refusing to run against ${BASE}: that is the production Coral port. Use tests/frontend/run.sh.`);
    process.exit(2);
}
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const verdict = cond ? 'PASS' : 'FAIL';
    const line = `[${verdict}] ${label}${detail ? ' — ' + detail : ''}`;
    results.push({ verdict, label, line });
    console.log(line);
}

const SESSIONS = [
    // 0: named agent with summary, high context -> ctx-error label, no action
    { name: 'coral-go', display_name: 'Lead Dev', agent_type: 'claude', session_id: 'sid-a',
      summary: 'Refactoring the store layer', context_pct: 87, context_window: 200000,
      working: true, working_directory: '/repo/coral-go' },
    // 1: unnamed agent, no summary, first_prompt fallback, waiting for input
    { name: 'coral-go', display_name: '', agent_type: 'claude', session_id: 'sid-b',
      summary: '', first_prompt: 'Please fix the flaky\n  websocket reconnect test in xterm_renderer.js',
      context_pct: 42, waiting_for_input: true, working_directory: '/repo/coral-go' },
    // 2: unnamed agent, nothing at all -> "No goal yet"; not_started; full context
    { name: 'coral-go', display_name: '', agent_type: 'claude', session_id: 'sid-c',
      summary: '', first_prompt: '', context_pct: 100, not_started: true,
      working_directory: '/repo/coral-go' },
    // 3: terminal, nothing -> no goal affordance
    { name: 'coral-go', display_name: '', agent_type: 'terminal', session_id: 'sid-d',
      summary: '', first_prompt: '', context_pct: 0, working_directory: '/repo/coral-go' },
    // 4: unnamed, board job title, path-style first prompt (screenshot case)
    { name: 'coral-go', display_name: '', agent_type: 'claude', session_id: 'sid-g',
      board_job_title: 'Release Wrangler', summary: '',
      first_prompt: '/Users/me/Software/coral/coral-go fix the release workflow', context_pct: 33,
      working_directory: '/repo/coral-go' },
    // 5: named agent in another folder, low context, healthy
    { name: 'other-proj', display_name: 'Zed', agent_type: 'claude', session_id: 'sid-e',
      summary: 'Writing docs', context_pct: 12, working_directory: '/repo/other-proj' },
    // 6: unnamed, summary only (no first_prompt) -> initials fall back to folder
    { name: 'other-proj', display_name: '', agent_type: 'claude', session_id: 'sid-f',
      summary: 'Investigate slow startup on Windows', first_prompt: '', context_pct: 63,
      working_directory: '/repo/other-proj' },
];
const ORDER = SESSIONS.map(s => s.session_id).join(',');

// Installed before any app script runs (Page.addScriptToEvaluateOnNewDocument).
// - GET /api/sessions/live returns the current fixture, so app startup/init
//   loads (and any later refresh) cannot overwrite injected sessions.
// - POST .../send is recorded (and short-circuited) only while
//   window.__sendCalls is an array, so we can prove the context bar sends
//   nothing when clicked.
// - window.WebSocket is wrapped so /ws/coral never opens a real socket
//   (Network.setBlockedURLs does not block WebSocket upgrades). Other sockets
//   (e.g. /ws/terminal) pass through untouched. Attempts are counted in
//   window.__coralWsAttempts so the test can prove the stub intercepted them.
const PRE_NAV_STUB = `
    window.__fixture = ${JSON.stringify(SESSIONS)};
    window.__sendCalls = null;
    window.__coralWsAttempts = 0;
    (function () {
        const RealWS = window.WebSocket;
        function StubCoralWS(url) {
            this.url = String(url);
            this.readyState = 0; // CONNECTING forever: never opens, never closes
            this.onopen = this.onmessage = this.onclose = this.onerror = null;
        }
        StubCoralWS.prototype.send = function () {};
        StubCoralWS.prototype.close = function () {};
        StubCoralWS.prototype.addEventListener = function () {};
        StubCoralWS.prototype.removeEventListener = function () {};
        function WrappedWS(url, protocols) {
            if (/\\/ws\\/coral(\\?|$)/.test(String(url))) { window.__coralWsAttempts++; return new StubCoralWS(url); }
            return protocols === undefined ? new RealWS(url) : new RealWS(url, protocols);
        }
        WrappedWS.prototype = RealWS.prototype;
        WrappedWS.CONNECTING = 0; WrappedWS.OPEN = 1; WrappedWS.CLOSING = 2; WrappedWS.CLOSED = 3;
        window.__RealWebSocket = RealWS;
        window.WebSocket = WrappedWS;
    })();
    window.__origFetch = window.fetch.bind(window);
    window.fetch = (url, opts) => {
        const u = String(url);
        const path = u.replace(/^https?:\\/\\/[^/]+/, '').split('?')[0];
        const method = ((opts && opts.method) || 'GET').toUpperCase();
        const json = (body) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } }));
        if (path === '/api/sessions/live' && method === 'GET') return json(window.__fixture);
        if (Array.isArray(window.__sendCalls) && /\\/send$/.test(path)) { window.__sendCalls.push({ url: u, body: opts && opts.body }); return json({}); }
        return window.__origFetch(url, opts);
    };
`;

async function run() {
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime, Emulation, Network } = client;
    await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
    // Belt and braces against fixture overwrite: stub the HTTP list endpoint
    // (startup + refresh loads) AND stub the live-update socket constructor.
    await Page.addScriptToEvaluateOnNewDocument({ source: PRE_NAV_STUB });
    // Independent proof from the network layer: count real WebSocket upgrades.
    const wsCreated = [];
    Network.webSocketCreated(({ url }) => wsCreated.push(url));
    await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });

    async function evalInPage(expr) {
        const r = await Runtime.evaluate({ expression: expr, awaitPromise: true, returnByValue: true });
        if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails));
        return r.result.value;
    }
    const sleep = (ms) => new Promise(r => setTimeout(r, ms));
    // Keep the HTTP stub and the rendered list in lockstep.
    const setFixture = (sessions) => evalInPage(`window.__fixture = ${JSON.stringify(sessions)}; window._coralSetLiveSessions(window.__fixture); true`);

    try {
        await Page.navigate({ url: BASE + '/' });
        await Page.loadEventFired();
        // Wait for the app module to expose its globals.
        for (let i = 0; i < 50; i++) {
            if (await evalInPage(`typeof window._coralSetLiveSessions === 'function'`)) break;
            await sleep(100);
        }
        check('app exposes _coralSetLiveSessions hook',
            await evalInPage(`typeof window._coralSetLiveSessions === 'function'`));

        // Switch to Agents tab / desktop layout and render our fixture.
        check('fetch stub is installed before app start', await evalInPage(`typeof window.__origFetch === 'function' && Array.isArray(window.__fixture)`));
        await evalInPage(`localStorage.setItem('coral-group-by-team', 'false'); window.switchNavTab('agents'); true`);
        await setFixture(SESSIONS);
        // A startup-style reload through the real API path must keep the fixture.
        await evalInPage(`window._coralLoadLiveSessions()`);
        await sleep(150);
        check('startup /api/sessions/live load cannot overwrite fixture',
            await evalInPage(`document.querySelectorAll('#live-sessions-list .session-group-item').length`) === SESSIONS.length);

        // WebSocket isolation: the app tried to open /ws/coral, the stub caught
        // it, and the browser opened zero real coral sockets.
        const wsAttempts = await evalInPage(`window.__coralWsAttempts`);
        check('app attempted /ws/coral and hit the stub', wsAttempts >= 1, `attempts=${wsAttempts}`);
        check('zero real /ws/coral sockets created', !wsCreated.some(u => /\/ws\/coral/.test(u)), JSON.stringify(wsCreated));
        check('WebSocket constants preserved for app code', await evalInPage(`WebSocket.OPEN === 1 && WebSocket.CLOSED === 3`));
        // Fixture stability: nothing can tick in from the server over time.
        const before = await evalInPage(`Array.from(document.querySelectorAll('#live-sessions-list .session-group-item')).map(li => li.dataset.sessionId).join(',')`);
        await sleep(1500);
        const after = await evalInPage(`Array.from(document.querySelectorAll('#live-sessions-list .session-group-item')).map(li => li.dataset.sessionId).join(',')`);
        check('fixture stable over time with no live socket', before === after && after === ORDER, after);
        check('still zero real /ws/coral sockets after settling', !wsCreated.some(u => /\/ws\/coral/.test(u)), JSON.stringify(wsCreated));

        const rows = await evalInPage(`
            Array.from(document.querySelectorAll('#live-sessions-list .session-group-item')).map(li => {
                const q = (sel) => li.querySelector(sel);
                const cs = (el) => el ? getComputedStyle(el).display : null;
                const goal = q('.session-goal');
                return {
                    sid: li.dataset.sessionId,
                    label: q('.session-label') ? q('.session-label').textContent.trim() : null,
                    labelTitle: q('.session-label') ? q('.session-label').getAttribute('title') : null,
                    emptyLabelOnclick: q('.session-goal-empty-label') ? q('.session-goal-empty-label').getAttribute('onclick') : null,
                    emptyLabelTitle: q('.session-goal-empty-label') ? q('.session-goal-empty-label').getAttribute('title') : null,
                    emptyLabelCursor: q('.session-goal-empty-label') ? getComputedStyle(q('.session-goal-empty-label')).cursor : null,
                    rowCursor: getComputedStyle(li).cursor,
                    sparkleW: q('.sidebar-goal-btn-inline') ? q('.sidebar-goal-btn-inline').getBoundingClientRect().width : null,
                    sparkleH: q('.sidebar-goal-btn-inline') ? q('.sidebar-goal-btn-inline').getBoundingClientRect().height : null,
                    sparkleAria: q('.sidebar-goal-btn-inline') ? q('.sidebar-goal-btn-inline').getAttribute('aria-label') : null,
                    goalText: goal ? goal.textContent.replace(/\\s+/g, ' ').trim() : null,
                    goalEmpty: !!q('.session-goal-empty'),
                    goalBtnVisible: q('.sidebar-goal-btn') ? parseFloat(getComputedStyle(q('.sidebar-goal-btn')).opacity) > 0 : false,
                    hasGoal: !!goal,
                    initials: q('.agent-avatar-initials') ? q('.agent-avatar-initials').textContent.trim() : null,
                    avatarW: q('.agent-avatar') ? q('.agent-avatar').getBoundingClientRect().width : null,
                    avatarRgb: q('.agent-avatar') ? (getComputedStyle(q('.agent-avatar')).backgroundColor.match(/\\d+/g) || []).slice(0, 3).join(',') : null,
                    avatarEmoji: q('.agent-avatar-emoji') ? q('.agent-avatar-emoji').textContent.trim() : null,
                    initialsFont: q('.agent-avatar-initials') ? getComputedStyle(q('.agent-avatar-initials')).fontSize : null,
                    ctxLabel: q('.context-bar-label') ? q('.context-bar-label').textContent.trim() : null,
                    compactBtn: !!q('.context-compact-btn'),
                    ctxClickables: q('.session-context-bar') ? q('.session-context-bar').querySelectorAll('button, a, [onclick], [role="button"]').length : 0,
                    ctxHasOnclick: q('.session-context-bar') ? q('.session-context-bar').hasAttribute('onclick') : false,
                    bannerDisplay: cs(q('.session-mobile-banner-row')),
                    metaDisplay: cs(q('.session-mobile-meta')),
                    bannerText: q('.session-mobile-banner') ? q('.session-mobile-banner').textContent.trim() : null,
                    padTop: getComputedStyle(li).paddingTop,
                    height: li.getBoundingClientRect().height,
                    attention: li.classList.contains('needs-attention'),
                };
            })
        `);

        check('renders all fixture rows', rows.length === SESSIONS.length, `got ${rows.length}`);
        const bySid = Object.fromEntries(rows.map(r => [r.sid, r]));

        // D5 badge-only: ordering preserved exactly (folders are grouped by
        // insertion order; within a folder the payload order is kept).
        const order = rows.map(r => r.sid).join(',');
        check('session ordering preserved (no attention sort)',
            order === ORDER, order);

        // D1 identity
        check('summary shown on non-active row', bySid['sid-a'].goalText === 'Refactoring the store layer', bySid['sid-a'].goalText);
        check('first_prompt fallback is one-line', bySid['sid-b'].goalText === 'Please fix the flaky websocket reconnect test in xterm_renderer.js', bySid['sid-b'].goalText);
        check('empty goal shows "No goal yet"', bySid['sid-c'].goalEmpty && /No goal yet/.test(bySid['sid-c'].goalText), bySid['sid-c'].goalText);
        check('generate-goal button is always visible when no goal', bySid['sid-c'].goalBtnVisible);
        check('terminal without goal shows no goal affordance', !bySid['sid-d'].hasGoal);
        // Screenshot cases (task #85): a sentence or a path in first_prompt is
        // goal text, never a name or initials.
        check('sentence prompt is not the row label (Agent)', bySid['sid-b'].label === 'Agent', bySid['sid-b'].label);
        check('sentence prompt still shows on the goal line', bySid['sid-b'].goalText.startsWith('Please fix the flaky websocket'), bySid['sid-b'].goalText);
        check('row label carries a title with the resolved identity', bySid['sid-b'].labelTitle === 'Agent' && bySid['sid-g'].labelTitle === 'Release Wrangler', `${bySid['sid-b'].labelTitle} / ${bySid['sid-g'].labelTitle}`);
        check('path prompt with board_job_title uses the job title as label', bySid['sid-g'].label === 'Release Wrangler', bySid['sid-g'].label);
        check('path prompt shows on the goal line', bySid['sid-g'].goalText === '/Users/me/Software/coral/coral-go fix the release workflow', bySid['sid-g'].goalText);
        check('initials from board_job_title, not path words', bySid['sid-g'].initials === 'RW', bySid['sid-g'].initials);
        check('row label with no identity falls back to Agent', bySid['sid-c'].label === 'Agent', bySid['sid-c'].label);
        check('summary is goal text only, never the row label', bySid['sid-f'].label === 'Agent', bySid['sid-f'].label);
        check('terminal row label falls back to Terminal', bySid['sid-d'].label === 'Terminal', bySid['sid-d'].label);
        // Passive "No goal yet" label: no click handler, no action title, no pointer.
        check('"No goal yet" label has no onclick', bySid['sid-c'].emptyLabelOnclick === null, bySid['sid-c'].emptyLabelOnclick);
        check('"No goal yet" label has no action title', bySid['sid-c'].emptyLabelTitle === null, bySid['sid-c'].emptyLabelTitle);
        check('"No goal yet" label has no cursor styling of its own (inherits the row)', bySid['sid-c'].emptyLabelCursor === bySid['sid-c'].rowCursor, `${bySid['sid-c'].emptyLabelCursor} vs row ${bySid['sid-c'].rowCursor}`);
        check('sparkle hit target is at least 24x24', bySid['sid-c'].sparkleW >= 24 && bySid['sid-c'].sparkleH >= 24, `${bySid['sid-c'].sparkleW}x${bySid['sid-c'].sparkleH}`);
        check('sparkle has an aria-label naming the agent', bySid['sid-c'].sparkleAria === 'Generate goal for Agent', bySid['sid-c'].sparkleAria);
        check('initials never derive from prompt words (folder fallback)', bySid['sid-b'].initials === 'CO', bySid['sid-b'].initials);
        // Avatar colour is keyed on the folder: named + unnamed agents in one
        // folder share a hue; another folder gets a different one.
        check('same-folder agents share an avatar hue', bySid['sid-a'].avatarRgb && bySid['sid-a'].avatarRgb === bySid['sid-b'].avatarRgb && bySid['sid-b'].avatarRgb === bySid['sid-g'].avatarRgb, `${bySid['sid-a'].avatarRgb} / ${bySid['sid-b'].avatarRgb} / ${bySid['sid-g'].avatarRgb}`);
        check('different folder gets a different avatar hue', bySid['sid-e'].avatarRgb !== bySid['sid-b'].avatarRgb, `${bySid['sid-e'].avatarRgb} vs ${bySid['sid-b'].avatarRgb}`);
        check('same-folder agents keep distinct identity marks (emoji / CO / RW)',
            bySid['sid-a'].avatarEmoji && !bySid['sid-a'].initials && bySid['sid-b'].initials === 'CO' && bySid['sid-g'].initials === 'RW',
            JSON.stringify([bySid['sid-a'].avatarEmoji, bySid['sid-b'].initials, bySid['sid-g'].initials]));
        check('initials never derive from mutable summary (folder fallback)', bySid['sid-f'].initials === 'OT', bySid['sid-f'].initials);
        check('summary-only row still shows summary as goal', bySid['sid-f'].goalText === 'Investigate slow startup on Windows', bySid['sid-f'].goalText);
        check('initials from display_name when present', bySid['sid-e'].initials === 'ZE', bySid['sid-e'].initials);
        check('role keyword in display_name still yields emoji avatar', bySid['sid-a'].initials === null);

        // D2 desktop hides mobile-only rows
        check('mobile banner row hidden on desktop', bySid['sid-c'].bannerDisplay === 'none', bySid['sid-c'].bannerDisplay);
        check('mobile meta row hidden on desktop', bySid['sid-c'].metaDisplay === 'none', bySid['sid-c'].metaDisplay);
        check('banner markup still emitted for mobile clone', bySid['sid-c'].bannerText === 'Nothing yet — open the terminal', bySid['sid-c'].bannerText);

        // D3 context bar
        check('ctx label is "ctx N%"', bySid['sid-b'].ctxLabel === 'ctx 42%', bySid['sid-b'].ctxLabel);
        check('high context keeps its label', bySid['sid-a'].ctxLabel === 'ctx 87%', bySid['sid-a'].ctxLabel);
        check('100% reads "ctx full"', bySid['sid-c'].ctxLabel === 'ctx full', bySid['sid-c'].ctxLabel);
        // Operator decision (task #75): the context bar is informational only.
        check('no Compact control at >= 80%', !bySid['sid-a'].compactBtn && bySid['sid-a'].ctxClickables === 0 && !bySid['sid-a'].ctxHasOnclick);
        check('no Compact control at 100%', !bySid['sid-c'].compactBtn && bySid['sid-c'].ctxClickables === 0 && !bySid['sid-c'].ctxHasOnclick);
        check('no Compact control below 80%', !bySid['sid-b'].compactBtn && bySid['sid-b'].ctxClickables === 0);
        check('no Compact control anywhere in the agent list',
            await evalInPage(`document.querySelectorAll('#live-sessions-list .context-compact-btn, #live-sessions-list .session-context-bar button').length`) === 0);
        check('compactSession plumbing removed', await evalInPage(`typeof window.compactSession`) === 'undefined');

        // D6 density
        check('avatar is 28px', Math.round(bySid['sid-a'].avatarW) === 28, `${bySid['sid-a'].avatarW}`);
        check('initials font is 11px', bySid['sid-e'].initialsFont === '11px', bySid['sid-e'].initialsFont);
        check('row vertical padding is 9px', bySid['sid-a'].padTop === '9px', bySid['sid-a'].padTop);
        // name row + goal line + context bar; previously ~90px at 14px padding / 36px avatar
        check('row with goal + ctx bar is compact (<= 70px)', bySid['sid-a'].height <= 70, `${bySid['sid-a'].height}`);
        check('row with goal but no ctx bar is compact (<= 54px)', bySid['sid-d'].height <= 54, `${bySid['sid-d'].height}`);

        // D5 badge
        const badge = await evalInPage(`
            (() => { const b = document.getElementById('nav-tab-agents-badge'); return b ? { text: b.textContent, display: getComputedStyle(b).display } : null; })()
        `);
        check('Agents nav badge exists', !!badge);
        check('badge counts rows needing attention (2)', badge && badge.text === '2' && badge.display !== 'none', JSON.stringify(badge));
        const attn = rows.filter(r => r.attention).map(r => r.sid).join(',');
        check('needs-attention rows are sid-b and sid-c', attn === 'sid-b,sid-c', attn);

        await setFixture(SESSIONS.map(s => ({ ...s, waiting_for_input: false, not_started: false })));
        const badgeAfter = await evalInPage(`(() => { const b = document.getElementById('nav-tab-agents-badge'); return { text: b.textContent, display: getComputedStyle(b).display }; })()`);
        check('badge hides when nothing needs attention', badgeAfter.text === '' && badgeAfter.display === 'none', JSON.stringify(badgeAfter));
        await setFixture(SESSIONS);

        // D7 naming
        const tab = await evalInPage(`(() => { const t = document.getElementById('agentic-tab-history'); return t ? { title: t.title, aria: t.getAttribute('aria-label') } : null; })()`);
        check('live panel tab renamed to Transcript', tab && tab.aria === 'Transcript' && /^Transcript/.test(tab.title), JSON.stringify(tab));

        // D1 header + placeholder + D4 mode label via selectLiveSession on the
        // unnamed first_prompt row.
        await evalInPage(`window.selectLiveSession('coral-go', 'claude', 'sid-b'); true`);
        await sleep(300);
        const header = await evalInPage(`(() => ({
            name: document.getElementById('session-name').textContent,
            term: (document.getElementById('terminal-header-label') || {}).textContent || '',
            placeholder: document.getElementById('command-input').placeholder,
            modeLabel: (document.querySelector('#btn-mode-toggle .btn-label') || {}).textContent || null,
            modeTip: (document.getElementById('btn-mode-toggle') || { getAttribute(){ return null; } }).getAttribute('data-tooltip'),
        }))()`);
        check('header is Agent for a prompt-only session (prompt is not identity)', header.name === 'Agent', header.name);
        check('terminal header label uses the same identity', header.term === 'Agent -- sid-b', header.term);
        check('placeholder "Sending to:" uses resolved identity',
            /^Sending to: Agent \(claude\)/.test(header.placeholder), header.placeholder);
        // The fixture session has no real terminal, so the poll returns an
        // error placeholder rather than a buffer -> label must fall back.
        check('Mode label falls back to "Mode" when undetectable', header.modeLabel === 'Mode', header.modeLabel);
        check('Mode aria-label explains unknown state', /^Mode: unknown\. Click to cycle Default, Plan, Accept Edits/.test(await evalInPage(`document.getElementById('btn-mode-toggle').getAttribute('aria-label')`)));

        // D4: feed a fake terminal buffer and trigger the rescan hook.
        await evalInPage(`const pc = document.getElementById('pane-capture'); pc.dataset.captureState = 'ok'; pc.textContent = '⏸ plan mode on (shift+tab to cycle)'; document.dispatchEvent(new CustomEvent('coral:terminal-updated')); true`);
        const plan = await evalInPage(`(() => ({ label: document.querySelector('#btn-mode-toggle .btn-label').textContent, tip: document.getElementById('btn-mode-toggle').getAttribute('data-tooltip') }))()`);
        check('Mode label tracks buffer: Plan', plan.label === 'Plan', plan.label);
        check('Mode tooltip names current and next mode', plan.tip === 'Current: Plan. Click to switch to Default (Shift+Tab).', plan.tip);
        check('Mode aria-label names current and next mode', await evalInPage(`document.getElementById('btn-mode-toggle').getAttribute('aria-label')`) === 'Mode: Plan. Click to switch to Default (Shift+Tab).');
        await evalInPage(`document.getElementById('pane-capture').textContent = '⏵⏵ accept edits on (shift+tab to cycle)'; document.dispatchEvent(new CustomEvent('coral:terminal-updated')); true`);
        check('Mode label tracks buffer: Accept', await evalInPage(`document.querySelector('#btn-mode-toggle .btn-label').textContent`) === 'Accept');
        await evalInPage(`document.getElementById('pane-capture').textContent = '> some prompt'; document.dispatchEvent(new CustomEvent('coral:terminal-updated')); true`);
        const def = await evalInPage(`(() => ({ label: document.querySelector('#btn-mode-toggle .btn-label').textContent, tip: document.getElementById('btn-mode-toggle').getAttribute('data-tooltip') }))()`);
        check('Mode label tracks buffer: Default', def.label === 'Default', def.label);
        check('Default tooltip points at Accept Edits next', def.tip === 'Current: Default. Click to switch to Accept Edits (Shift+Tab).', def.tip);

        // Regression: a live coral_diff tick that omits first_prompt (and has no
        // display_name/summary) must not blank the row goal, initials, header
        // or placeholder of the selected unnamed session.
        check('WS handler hook exposed', await evalInPage(`typeof window._coralHandleWsMessage === 'function'`));

        // Regression: explicit null context fields from the server must clear
        // stale values retained by the generic spread-merge and remove the bar
        // without a page reload.
        await evalInPage(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [
            { name: 'coral-go', agent_type: 'claude', session_id: 'sid-c',
              context_pct: null, context_window: null }
        ], removed: [] }); true`);
        await sleep(100);
        const clearedContext = await evalInPage(`(() => {
            const li = document.querySelector('#live-sessions-list [data-session-id="sid-c"]');
            const session = window._coralGetLiveSessions().find(s => s.session_id === 'sid-c');
            return {
                hasBar: !!(li && li.querySelector('.session-context-bar')),
                pct: session ? session.context_pct : undefined,
                window: session ? session.context_window : undefined,
            };
        })()`);
        check('explicit null context fields overwrite stale values',
            clearedContext.pct === null && clearedContext.window === null, JSON.stringify(clearedContext));
        check('explicit null context removes stale ctx full bar without reload',
            !clearedContext.hasBar, JSON.stringify(clearedContext));

        await evalInPage(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [
            { name: 'coral-go', display_name: '', agent_type: 'claude', session_id: 'sid-b', summary: '',
              context_pct: 45, waiting_for_input: true, working: false, working_directory: '/repo/coral-go' }
        ], removed: [] }); true`);
        await sleep(100);
        const afterDiff = await evalInPage(`(() => {
            const li = document.querySelector('#live-sessions-list [data-session-id="sid-b"]');
            return {
                goal: li ? li.querySelector('.session-goal').textContent.replace(/\\s+/g, ' ').trim() : null,
                initials: li ? (li.querySelector('.agent-avatar-initials') || {}).textContent : null,
                ctx: li ? li.querySelector('.context-bar-label').textContent.trim() : null,
                header: document.getElementById('session-name').textContent,
                term: (document.getElementById('terminal-header-label') || {}).textContent || '',
                placeholder: document.getElementById('command-input').placeholder,
                order: Array.from(document.querySelectorAll('#live-sessions-list .session-group-item')).map(li => li.dataset.sessionId).join(','),
            };
        })()`);
        check('diff without first_prompt keeps row goal', afterDiff.goal === 'Please fix the flaky websocket reconnect test in xterm_renderer.js', afterDiff.goal);
        check('diff without first_prompt keeps folder initials', afterDiff.initials === 'CO', afterDiff.initials);
        check('diff still applies changed fields (ctx 45%)', afterDiff.ctx === 'ctx 45%', afterDiff.ctx);
        check('diff without first_prompt keeps header identity', afterDiff.header === 'Agent', afterDiff.header);
        check('diff without first_prompt keeps terminal label identity', afterDiff.term === 'Agent -- sid-b', afterDiff.term);
        check('diff without first_prompt keeps Sending-to placeholder', /^Sending to: Agent/.test(afterDiff.placeholder), afterDiff.placeholder);
        check('diff does not reorder sessions', afterDiff.order === ORDER, afterDiff.order);

        // display_name / summary updates in a diff must still win.
        await evalInPage(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [
            { name: 'coral-go', display_name: 'Renamed Bot', agent_type: 'claude', session_id: 'sid-b', summary: 'Now writing tests',
              context_pct: 45, waiting_for_input: true, working_directory: '/repo/coral-go' }
        ] }); true`);
        await sleep(100);
        const afterRename = await evalInPage(`(() => {
            const li = document.querySelector('#live-sessions-list [data-session-id="sid-b"]');
            return { label: li.querySelector('.session-label').textContent.trim(), goal: li.querySelector('.session-goal').textContent.trim(),
                     initials: (li.querySelector('.agent-avatar-initials') || {}).textContent, header: document.getElementById('session-name').textContent,
                     placeholder: document.getElementById('command-input').placeholder };
        })()`);
        check('diff display_name update applies to row + header', afterRename.label === 'Renamed Bot' && afterRename.header === 'Renamed Bot', JSON.stringify(afterRename));
        check('diff summary update applies to goal line', afterRename.goal === 'Now writing tests', afterRename.goal);
        check('initials follow display_name after rename', afterRename.initials === 'RB', afterRename.initials);
        check('placeholder follows display_name after rename', /^Sending to: Renamed Bot/.test(afterRename.placeholder), afterRename.placeholder);
        await setFixture(SESSIONS);

        // ── Passive label vs sparkle ──────────────────────────────────────
        // With sid-a active, clicking "No goal yet" on sid-c must select sid-c
        // and issue no /send and no requestGoal.
        await evalInPage(`window.selectLiveSession('coral-go', 'claude', 'sid-a'); true`);
        await sleep(200);
        await evalInPage(`window.__goalCalls = 0; const _rg = window.requestGoal; window.__origRequestGoal = _rg; window.requestGoal = (...a) => { window.__goalCalls++; return _rg(...a); }; window.__sendCalls = []; true`);
        await evalInPage(`document.querySelector('#live-sessions-list [data-session-id="sid-c"] .session-goal-empty-label').click(); true`);
        await sleep(250);
        const afterLabel = await evalInPage(`({ active: document.querySelector('#live-sessions-list .session-group-item.active')?.dataset.sessionId, sends: window.__sendCalls.length, goals: window.__goalCalls, header: document.getElementById('session-name').textContent })`);
        check('passive label click selects the row', afterLabel.active === 'sid-c' && afterLabel.header === 'Agent', JSON.stringify(afterLabel));
        check('passive label click sends zero requests', afterLabel.sends === 0, `${afterLabel.sends}`);
        check('passive label click does not request a goal', afterLabel.goals === 0, `${afterLabel.goals}`);
        // Sparkle is the sole trigger, and it does not change the selection.
        await evalInPage(`window.selectLiveSession('coral-go', 'claude', 'sid-a'); true`);
        await sleep(200);
        await evalInPage(`window.__sendCalls = []; document.querySelector('#live-sessions-list [data-session-id="sid-c"] .sidebar-goal-btn-inline').click(); true`);
        await sleep(250);
        const afterSparkle = await evalInPage(`({ active: document.querySelector('#live-sessions-list .session-group-item.active')?.dataset.sessionId, goals: window.__goalCalls, sends: window.__sendCalls.map(c => c.url) })`);
        check('sparkle is the sole goal trigger', afterSparkle.goals === 1, `${afterSparkle.goals}`);
        check('sparkle click does not change the active session', afterSparkle.active === 'sid-a', afterSparkle.active);
        check('sparkle request targets the clicked row, not the active one', afterSparkle.sends.every(u => /\/api\/sessions\/live\/coral-go\//.test(u)) && afterSparkle.sends.length <= 1, JSON.stringify(afterSparkle.sends));
        await evalInPage(`window.__sendCalls = null; window.requestGoal = window.__origRequestGoal; true`);

        // ── auto_name precedence + generic merge semantics ────────────────
        check('live-sessions read hook exposed', await evalInPage(`typeof window._coralGetLiveSessions === 'function'`));
        const snapshotF = await evalInPage(`JSON.stringify(window._coralGetLiveSessions().find(s => s.session_id === 'sid-f'))`);
        // A minimal diff carrying only auto_name: every omitted field must survive.
        await evalInPage(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [
            { name: 'other-proj', agent_type: 'claude', session_id: 'sid-f', auto_name: 'Store Refactorer' }
        ] }); true`);
        await sleep(100);
        const rowF = () => evalInPage(`(() => {
            const li = document.querySelector('#live-sessions-list [data-session-id="sid-f"]');
            const g = li.querySelector('.session-goal');
            return { label: li.querySelector('.session-label').textContent.trim(), initials: (li.querySelector('.agent-avatar-initials') || {}).textContent || null,
                     goal: g ? g.textContent.replace(/\\s+/g, ' ').trim() : null, ctx: (li.querySelector('.context-bar-label') || {}).textContent || null,
                     order: Array.from(document.querySelectorAll('#live-sessions-list .session-group-item')).map(li => li.dataset.sessionId).join(',') };
        })()`);
        let f = await rowF();
        check('auto_name becomes the row label when no display_name', f.label === 'Store Refactorer', f.label);
        check('auto_name drives avatar initials', f.initials === 'SR', f.initials);
        check('auto_name diff keeps omitted summary as goal', f.goal === 'Investigate slow startup on Windows', f.goal);
        check('auto_name diff keeps omitted context_pct', f.ctx === 'ctx 63%', f.ctx);
        check('auto_name diff does not reorder', f.order === ORDER, f.order);
        const mergedF = JSON.parse(await evalInPage(`JSON.stringify(window._coralGetLiveSessions().find(s => s.session_id === 'sid-f'))`));
        const prevF = JSON.parse(snapshotF);
        const lostKeys = Object.keys(prevF).filter(k => JSON.stringify(mergedF[k]) !== JSON.stringify(prevF[k]));
        check('generic merge preserves every omitted field', lostKeys.length === 0 && mergedF.auto_name === 'Store Refactorer', `changed=${JSON.stringify(lostKeys)}`);
        check('merge does not alias the previous object', await evalInPage(`window.__fixture.find(s => s.session_id === 'sid-f').auto_name === undefined`));

        // Header / terminal label / placeholder follow auto_name.
        await evalInPage(`window.selectLiveSession('other-proj', 'claude', 'sid-f'); true`);
        await sleep(250);
        const hdrF = await evalInPage(`({ name: document.getElementById('session-name').textContent, term: (document.getElementById('terminal-header-label') || {}).textContent || '', ph: document.getElementById('command-input').placeholder })`);
        check('header uses auto_name', hdrF.name === 'Store Refactorer', hdrF.name);
        check('terminal label uses auto_name', hdrF.term === 'Store Refactorer -- sid-f', hdrF.term);
        check('placeholder uses auto_name', /^Sending to: Store Refactorer \(claude\)/.test(hdrF.ph), hdrF.ph);

        // Summary-only update: goal changes, identity does not.
        await evalInPage(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [
            { name: 'other-proj', agent_type: 'claude', session_id: 'sid-f', summary: 'Now profiling startup' }
        ] }); true`);
        await sleep(100);
        f = await rowF();
        const hdrF2 = await evalInPage(`({ name: document.getElementById('session-name').textContent, ph: document.getElementById('command-input').placeholder })`);
        check('summary-only diff updates the goal line', f.goal === 'Now profiling startup', f.goal);
        check('summary-only diff leaves label + initials unchanged', f.label === 'Store Refactorer' && f.initials === 'SR', JSON.stringify(f));
        check('summary-only diff leaves header + placeholder unchanged', hdrF2.name === 'Store Refactorer' && /^Sending to: Store Refactorer/.test(hdrF2.ph), JSON.stringify(hdrF2));

        // Explicit values overwrite: null clears auto_name, "" clears summary, a number replaces context_pct.
        await evalInPage(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [
            { name: 'other-proj', agent_type: 'claude', session_id: 'sid-f', auto_name: null, summary: '', context_pct: 71 }
        ] }); true`);
        await sleep(100);
        f = await rowF();
        const hdrF3 = await evalInPage(`({ name: document.getElementById('session-name').textContent, ph: document.getElementById('command-input').placeholder })`);
        check('explicit null auto_name falls back to Agent', f.label === 'Agent' && hdrF3.name === 'Agent' && /^Sending to: Agent/.test(hdrF3.ph), JSON.stringify({ f, hdrF3 }));
        check('initials fall back to folder after auto_name cleared', f.initials === 'OT', f.initials);
        check('explicit empty summary clears the goal line', /No goal yet/.test(f.goal || ''), f.goal);
        check('explicit context_pct value overwrites', f.ctx === 'ctx 71%', f.ctx);

        // display_name always beats auto_name.
        await evalInPage(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [
            { name: 'other-proj', agent_type: 'claude', session_id: 'sid-e', auto_name: 'Somebody Else' }
        ] }); true`);
        await sleep(100);
        const rowE = await evalInPage(`(() => { const li = document.querySelector('#live-sessions-list [data-session-id="sid-e"]'); return { label: li.querySelector('.session-label').textContent.trim(), initials: (li.querySelector('.agent-avatar-initials') || {}).textContent }; })()`);
        check('display_name beats auto_name', rowE.label === 'Zed' && rowE.initials === 'ZE', JSON.stringify(rowE));

        // New session arriving via diff is taken intact (no merge partner).
        await evalInPage(`window.__newSessionPayload = { name: 'coral-go', agent_type: 'claude', session_id: 'sid-new', display_name: '', summary: '', first_prompt: '/repo/coral-go/.github/workflows/release.yml audit this', context_pct: 5, working_directory: '/repo/coral-go' };
            window._coralHandleWsMessage({ type: 'coral_diff', changed: [window.__newSessionPayload] }); true`);
        await sleep(100);
        const rowNew = await evalInPage(`(() => { const li = document.querySelector('#live-sessions-list [data-session-id="sid-new"]'); return li ? { label: li.querySelector('.session-label').textContent.trim(), initials: (li.querySelector('.agent-avatar-initials') || {}).textContent, count: window._coralGetLiveSessions().length } : null; })()`);
        check('new session via diff renders intact (path prompt -> Agent, folder initials)', rowNew && rowNew.label === 'Agent' && rowNew.initials === 'CO' && rowNew.count === SESSIONS.length + 1, JSON.stringify(rowNew));
        check('new session merge does not alias incoming payload', await evalInPage(`window._coralGetLiveSessions().find(s => s.session_id === 'sid-new') !== window.__newSessionPayload`));

        // A full update follows the same contract: omitted fields survive,
        // explicit empty/null/value fields overwrite, and the incoming object
        // is not retained by reference.
        await evalInPage(`window.__fullSessionPayload = { name: 'coral-go', agent_type: 'claude', session_id: 'sid-new', summary: null, context_pct: 0 };
            window._coralHandleWsMessage({ type: 'coral_update', sessions: [window.__fullSessionPayload] }); true`);
        const fullMerged = await evalInPage(`(() => { const s = window._coralGetLiveSessions()[0]; return { count: window._coralGetLiveSessions().length, first: s.first_prompt, summary: s.summary, pct: s.context_pct, distinct: s !== window.__fullSessionPayload }; })()`);
        check('full update preserves omitted fields', fullMerged.count === 1 && fullMerged.first === '/repo/coral-go/.github/workflows/release.yml audit this', JSON.stringify(fullMerged));
        check('full update applies explicit null and zero', fullMerged.summary === null && fullMerged.pct === 0, JSON.stringify(fullMerged));
        check('full update merge does not alias incoming payload', fullMerged.distinct);
        await setFixture(SESSIONS);

        // board_job_title identity agrees across header / terminal label / placeholder.
        await evalInPage(`window.selectLiveSession('coral-go', 'claude', 'sid-g'); true`);
        await sleep(250);
        const hdrG = await evalInPage(`({ name: document.getElementById('session-name').textContent, term: (document.getElementById('terminal-header-label') || {}).textContent || '', ph: document.getElementById('command-input').placeholder })`);
        check('header uses board_job_title, not the path prompt', hdrG.name === 'Release Wrangler', hdrG.name);
        check('terminal label uses board_job_title', hdrG.term === 'Release Wrangler -- sid-g', hdrG.term);
        check('placeholder uses board_job_title', /^Sending to: Release Wrangler \(claude\)/.test(hdrG.ph), hdrG.ph);
        // auto_name outranks board_job_title.
        await evalInPage(`window._coralHandleWsMessage({ type: 'coral_diff', changed: [ { name: 'coral-go', agent_type: 'claude', session_id: 'sid-g', auto_name: 'Ship Bot' } ] }); true`);
        await sleep(100);
        const rowG = await evalInPage(`(() => { const li = document.querySelector('#live-sessions-list [data-session-id="sid-g"]'); return { label: li.querySelector('.session-label').textContent.trim(), initials: (li.querySelector('.agent-avatar-initials') || {}).textContent, header: document.getElementById('session-name').textContent }; })()`);
        check('auto_name beats board_job_title', rowG.label === 'Ship Bot' && rowG.initials === 'SB' && rowG.header === 'Ship Bot', JSON.stringify(rowG));
        await setFixture(SESSIONS);

        // Header follows a named session too.
        await evalInPage(`window.selectLiveSession('other-proj', 'claude', 'sid-e'); true`);
        await sleep(200);
        check('header uses display_name when present',
            await evalInPage(`document.getElementById('session-name').textContent`) === 'Zed');

        // Row with no identity at all resolves to "Agent" in the header.
        await evalInPage(`window.selectLiveSession('coral-go', 'claude', 'sid-c'); true`);
        await sleep(200);
        const bare = await evalInPage(`(() => ({ name: document.getElementById('session-name').textContent, ph: document.getElementById('command-input').placeholder }))()`);
        check('header falls back to "Agent" with no identity', bare.name === 'Agent', bare.name);
        check('placeholder falls back to "Agent"', /^Sending to: Agent \(claude\)/.test(bare.ph), bare.ph);

        // Clicking the high-context bar itself must not send anything: the
        // context display is informational only. (It still bubbles to the row
        // click, which selects that session like any other click on the row.)
        await evalInPage(`window.__sendCalls = []; true`);
        await evalInPage(`document.querySelector('#live-sessions-list [data-session-id="sid-a"] .context-bar-label').click(); true`);
        await sleep(200);
        const calls = await evalInPage(`window.__sendCalls`);
        await evalInPage(`window.__sendCalls = null; true`);
        check('clicking the context bar sends no /send request', calls.length === 0, JSON.stringify(calls));
        const toast = await evalInPage(`Array.from(document.querySelectorAll('.toast')).map(t => t.textContent).join(' | ')`);
        check('clicking the context bar shows no compact toast', !/compact/i.test(toast), toast);

        // ── 390px mobile: empty-goal affordance stays one line; banner/meta visible ──
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: false });
        await evalInPage(`window.dispatchEvent(new Event('resize')); true`);
        await sleep(150);
        await setFixture(SESSIONS); // re-render -> syncMobileAgentList() clones into #mobile-session-list
        await evalInPage(`const al = document.getElementById('mobile-agent-list'); if (al) al.style.display = 'flex'; true`);
        await sleep(150);
        const mobile = await evalInPage(`(() => {
            const list = document.getElementById('mobile-session-list');
            if (!list) return { list: false };
            const li = list.querySelector('[data-session-id="sid-c"]');
            const empty = li.querySelector('.session-goal-empty');
            const btn = li.querySelector('.sidebar-goal-btn-inline');
            const lbl = li.querySelector('.session-goal-empty-label');
            const br = btn.getBoundingClientRect(), lr = lbl.getBoundingClientRect(), er = empty.getBoundingClientRect();
            const banner = li.querySelector('.session-mobile-banner-row'), meta = li.querySelector('.session-mobile-meta');
            return { list: true, emptyDisplay: getComputedStyle(empty).display, emptyHeight: er.height, labelHeight: lr.height,
                     centerDelta: Math.abs((br.top + br.height / 2) - (lr.top + lr.height / 2)), sideBySide: br.right <= lr.left + 1,
                     sparkle: [br.width, br.height], bannerDisplay: getComputedStyle(banner).display, bannerText: (li.querySelector('.session-mobile-banner') || {}).textContent || '',
                     metaDisplay: getComputedStyle(meta).display, metaVisibleH: meta.getBoundingClientRect().height, viewport: innerWidth };
        })()`);
        check('mobile clone list rendered at 390px', mobile.list && mobile.viewport === 390, JSON.stringify(mobile));
        check('mobile empty-goal affordance is a flex row (clamp overridden)', mobile.emptyDisplay === 'flex', mobile.emptyDisplay);
        check('mobile sparkle + label sit on one line', mobile.sideBySide && mobile.centerDelta <= 6 && mobile.labelHeight <= 26 && mobile.emptyHeight <= 30, JSON.stringify(mobile));
        check('mobile sparkle hit target is at least 24x24', mobile.sparkle[0] >= 24 && mobile.sparkle[1] >= 24, JSON.stringify(mobile.sparkle));
        check('mobile banner row visible', mobile.bannerDisplay === 'block' && mobile.bannerText === 'Nothing yet — open the terminal', JSON.stringify([mobile.bannerDisplay, mobile.bannerText]));
        check('mobile meta row visible', mobile.metaDisplay === 'flex' && mobile.metaVisibleH > 0, JSON.stringify([mobile.metaDisplay, mobile.metaVisibleH]));
        // Back to desktop; the desktop rules must still hide the mobile rows.
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await evalInPage(`window.dispatchEvent(new Event('resize')); true`);
        await setFixture(SESSIONS);
        check('desktop still hides mobile rows after switching back', await evalInPage(`getComputedStyle(document.querySelector('#live-sessions-list [data-session-id="sid-c"] .session-mobile-meta')).display`) === 'none');
    } finally {
        await client.close();
    }
}

run().then(() => {
    const failed = results.filter(r => r.verdict === 'FAIL');
    console.log(`\n${results.length - failed.length}/${results.length} checks passed`);
    process.exit(failed.length ? 1 : 0);
}).catch(err => {
    console.error('HARNESS ERROR:', err);
    process.exit(2);
});
