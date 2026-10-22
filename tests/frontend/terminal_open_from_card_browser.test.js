// The terminal of an agent that needs input (#4711): the needs-input card's "Open terminal" shows the Terminal view for that agent without
// changing the saved Chat choice, and the terminal attaches and draws, as it does from the Terminal toggle. It used to stay blank on
// "Connecting to terminal…": the terminal attaches only for a view that wants it (#3719) and the card's one-time switch was not asked about.
// Real isolated server and a real agent: the claude CLI is a script that prints a permission prompt and waits for a key, so the server sees an
// agent that needs input. The page's terminal socket is wrapped only to record its events.
//
// Expects CORAL_TEST_URL + CDP_PORT from the runner. Exit 0 / 1.
'use strict';
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const CDP_PORT = parseInt(process.env.CDP_PORT || '9222', 10);

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}
async function httpJson(method, p, body) {
    const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}
// What the page's terminal sockets did: window.__ws, one record per socket with its events.
const WRAP = `(function(){ const Real = window.WebSocket; window.__ws = [];
  function W(u, p) { const s = p === undefined ? new Real(u) : new Real(u, p); if (/\\/ws\\/terminal/.test(String(u))) { const rec = { events: ['new'] }; window.__ws.push(rec);
    s.addEventListener('open', () => rec.events.push('open')); s.addEventListener('close', () => rec.events.push('close')); } return s; }
  W.prototype = Real.prototype; W.CONNECTING = 0; W.OPEN = 1; W.CLOSING = 2; W.CLOSED = 3; window.WebSocket = W; })();`;

async function run() {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cp-'));
    const cli = path.join(dir, 'fake-claude.sh');
    fs.writeFileSync(cli, `#!/bin/bash\nif [ "$1" = auth ]; then echo '{"loggedIn": true}'; exit 0; fi\nprintf '\\n  Bash command\\n\\n    rm -rf /tmp/x\\n\\n Do you want to proceed?\\n \\xe2\\x9d\\xaf 1. Yes\\n   2. No, and tell Claude what to do differently (esc)\\n'\nwhile true; do read -r -n1 k; done\n`, { mode: 0o755 });
    const launched = [];
    const client = await CDP({ port: CDP_PORT });
    const { Page, Runtime } = client;
    await Promise.all([Page.enable(), Runtime.enable()]);
    await Page.addScriptToEvaluateOnNewDocument({ source: WRAP });
    const ev = async (x) => { const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true }); if (r.exceptionDetails) throw new Error('page eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expression, what, timeout = 10000) => waitFor(expression, what, { soft: true, timeout });
    const XT = `(await import((window.__coralStatic||'/static/')+'xterm_renderer.js'))`;
    const screen = () => ev(`(async () => { const t = ${XT}.getTerminal(); if (!t) return ''; const b = t.buffer.active; let s = ''; for (let i = 0; i < b.length; i++) { const l = b.getLine(i); if (l) s += l.translateToString(true) + '\\n'; } return s; })()`);
    const bar = () => ev(`(document.getElementById('terminal-statusbar') || { dataset: {} }).dataset.state || ''`);
    const open = () => ev(`window.__ws.filter(w => w.events.includes('open') && !w.events.includes('close')).length`);
    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
    try {
        await Page.navigate({ url: BASE + '/' });
        await coralReady();
        const set = await httpJson('PUT', '/api/settings', { cli_path_claude: cli });
        if (set.status !== 200) throw new Error('settings: ' + set.status);
        const mk = async name => {
            const r = await httpJson('POST', '/api/sessions/launch', { working_dir: dir, agent_type: 'claude', display_name: name });
            if (!r.data || !r.data.session_id) throw new Error(`launch: ${r.status} ${JSON.stringify(r.data)}`);
            launched.push(r.data);
            await until(`(window._coralGetLiveSessions ? window._coralGetLiveSessions() : []).some(x => x.session_id === ${JSON.stringify(r.data.session_id)} && x.waiting_for_input)`, `${name} listed and waiting for input`, 20000);
            return r.data;
        };
        const a = await mk('qa-needs-input-a'), b = await mk('qa-needs-input-b');
        const select = s => ev(`window.selectLiveSession(${JSON.stringify(s.session_name)}, 'claude', ${JSON.stringify(s.session_id)}, ''); true`);

        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        await select(a);
        await until(`!!document.querySelector('[data-on-click="terminal-view-open"]')`, 'the needs-input card with Open terminal');
        check('in Chat view no terminal socket is open', (await open()) === 0, String(await open()));
        await ev(`document.querySelector('[data-on-click="terminal-view-open"]').click(); true`);
        check('Open terminal: the terminal connects', await until(`(() => (document.getElementById('terminal-statusbar') || { dataset: {} }).dataset.state === 'connected')()`, 'the bar says connected'), await bar());
        check('and draws the prompt', await until(`(async () => { const t = ${XT}.getTerminal(); if (!t) return false; const b = t.buffer.active; for (let i = 0; i < b.length; i++) { const l = b.getLine(i); if (l && /Do you want to proceed/.test(l.translateToString(true))) return true; } return false; })()`, 'the prompt in the terminal'), JSON.stringify((await screen()).slice(-120)));
        check('one socket is open', (await open()) === 1, String(await open()));
        check('the saved choice is still Chat', (await ev(`localStorage.getItem('coral-live-view-mode')`)) === 'chat');

        // Chat from the Terminal view that Open terminal opened: the view is Chat and no socket stays attached (the one-time switch must not outlive a choice).
        await ev(`document.getElementById('live-view-btn-chat').click(); true`);
        check('the Chat toggle after Open terminal shows Chat', await until(`document.getElementById('capture-wrapper').classList.contains('chat-mode')`, 'Chat view'));
        check('and no terminal socket stays attached', await until(`window.__ws.filter(w => w.events.includes('open') && !w.events.includes('close')).length === 0`, 'the socket closed'), String(await open()));
        check('and the saved choice is Chat', (await ev(`localStorage.getItem('coral-live-view-mode')`)) === 'chat');
        await ev(`document.querySelector('[data-on-click="terminal-view-open"]').click(); true`);
        check('Open terminal works again after that', await until(`(() => (document.getElementById('terminal-statusbar') || { dataset: {} }).dataset.state === 'connected')()`, 'the bar says connected'), await bar());

        // Another agent starts from the saved choice again: Chat, no terminal socket.
        await select(b);
        await until(`!!document.querySelector('[data-on-click="terminal-view-open"]')`, "the other agent's card");
        // Switching agents closes the previous agent's terminal socket (selectLiveSession -> _leaveCurrentAgent ->
        // disconnectTerminalWs -> ws.close()), but the WebSocket 'close' event this test records fires a tick later,
        // so wait for it rather than reading the count immediately (#4866, the #3719 contract still holds: a leaked
        // socket would never close and this wait would time out).
        check('the next agent is in Chat view with no socket', await until(`window.__ws.filter(w => w.events.includes('open') && !w.events.includes('close')).length === 0`, "the previous agent's terminal socket closed on switch") && (await ev(`document.getElementById('capture-wrapper').classList.contains('chat-mode')`)), String(await open()));

        // The toggle does the same for an agent that needs input, and saves the choice.
        await ev(`document.getElementById('live-view-btn-terminal').click(); true`);
        check('the Terminal toggle connects too', await until(`(() => (document.getElementById('terminal-statusbar') || { dataset: {} }).dataset.state === 'connected')()`, 'the bar says connected again'), await bar());
        check('and saves the choice', (await ev(`localStorage.getItem('coral-live-view-mode')`)) === 'terminal');
    } finally {
        for (const s of launched) await httpJson('POST', '/api/sessions/live/' + encodeURIComponent(s.session_name) + '/kill', { session_id: s.session_id, agent_type: 'claude' }).catch(() => {});
        fs.rmSync(dir, { recursive: true, force: true });
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(failed ? `\n${failed} FAILED` : '\nALL PASS');
    process.exit(failed ? 1 : 0);
}
run().catch(e => { console.error(e); process.exit(1); });
