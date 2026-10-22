// First-run guard (#5842, from UI/UX's #5835 report): what a brand-new person does on a brand-new install, walked on a server of its OWN that has an empty data folder and an empty
// HOME (the shared runner server has a password, stub CLIs and settings, so it cannot show a first run).
//
//   1. Happy path, a stand-in `claude` that is installed and ready: the dashboard asks for a password (signed out: no data without it), the person creates one, the home screen
//      offers "Launch agent", they pick a folder and press Launch -> THE NEW AGENT OPENS (#5835 finding 1: today the dialog closes, a toast shows and the page stays on the home
//      screen), they type a first prompt and it reaches the CLI.
//   2. Signed-out CLI, a stand-in that prints Claude Code's login menu and waits: the agent opens the same way, and its terminal tab is there to sign in with.
//   3. CLI missing (nothing named claude on PATH): the New Agent dialog says so once under the folder (install page, command, Check again) with Launch off (#5990), and starts no agent.
//
// No real agent CLI runs: the server's PATH holds only this suite's stand-in folder (and /usr/bin:/bin), HOME is empty, telemetry is off, no cloud. Scenario 3 needs "no claude" on the
// machine's fixed install paths too (Coral looks in /usr/local/bin and others after PATH); when one is there the scenario is SKIPPED, never run against it.
//
// Expects CDP_PORT and CORAL_TEST_BIN (the built coral, from run_cdp_suites.mjs). Drives Chrome over CDP. Exit 0 / 1.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const net = require('node:net');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const CDP = require('chrome-remote-interface');

if (process.platform === 'win32') { console.log('first_run_browser: skipped on Windows (sh stand-ins, /usr/bin PATH)'); process.exit(0); }
const BIN = process.env.CORAL_TEST_BIN;
if (!BIN || !fs.existsSync(BIN)) { console.error('CORAL_TEST_BIN is not set to a built coral (run this through run_cdp_suites.mjs)'); process.exit(2); }

const PASSWORD = 'first-run-pass-5842';
const PROMPT = 'hello from the first run';
const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: server start-up poll spacing

const freePort = () => new Promise((res, rej) => { const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); }); s.on('error', rej); });

// A fresh install: its own data folder, HOME, stand-in folder and project folder under one short temp dir.
async function freshServer(standIn) {
    const root = fs.mkdtempSync(path.join('/tmp', 'coral-firstrun-'));
    const dirs = { root, home: path.join(root, 'home'), data: path.join(root, 'data'), bin: path.join(root, 'bin'), work: path.join(root, 'project') };
    for (const d of [dirs.home, dirs.data, dirs.bin, dirs.work]) fs.mkdirSync(d);
    const ran = path.join(root, 'ran'), got = path.join(root, 'got');
    if (standIn) {
        const banner = standIn === 'login'
            ? "printf 'Welcome to Claude Code\\n\\nSelect login method:\\n\\n 1. Claude account with subscription\\n 2. Anthropic Console account\\n\\n'"
            : "printf 'stand-in claude ready\\n> '";
        fs.writeFileSync(path.join(dirs.bin, 'claude'), [
            '#!/bin/sh',
            // --version and Coral's sign-in check ("auth status --json", asked once after a launch, #5838) are answered like the CLI does and never taken for a launch ("ran").
            'case "$1" in --version) echo "2.0.0 (stand-in)"; exit 0;; auth) echo \'' + (standIn === 'login' ? '{"loggedIn": false}\'; exit 1' : '{"loggedIn": true}\'; exit 0') + ';; esac',
            `: > '${ran}'`,
            banner,
            `while IFS= read -r line; do printf '%s\\n' "$line" >> '${got}'; done`,
            'exec sleep 600', '',
        ].join('\n'), { mode: 0o755 });
    }
    const port = await freePort();
    assert.notEqual(port, 8420);
    const log = path.join(root, 'coral.log');
    const env = { PATH: `${dirs.bin}:/usr/bin:/bin`, HOME: dirs.home, TMPDIR: root, CORAL_DATA_DIR: dirs.data, CORAL_TELEMETRY_DISABLED: '1', CORAL_NO_DEMO_SEED: '1' };
    const proc = spawn(BIN, ['--home', dirs.data, '--host', '127.0.0.1', '--port', String(port), '--no-browser', '--cloud-url', 'off', '--backend', 'pty'],
        { env, stdio: ['ignore', fs.openSync(log, 'w'), fs.openSync(log, 'a')], detached: true });
    let exited = null; proc.on('exit', (c, s) => { exited = `exit ${c} ${s}`; }); proc.on('error', e => { exited = String(e); });
    const base = `http://127.0.0.1:${port}`;
    for (let i = 0; i < 200; i++) {
        if (exited) throw new Error(`the fresh server did not start: ${exited}\n${fs.readFileSync(log, 'utf8').slice(-600)}`);
        try { if ((await fetch(`${base}/api/health`)).ok) break; } catch { /* not up yet */ }
        await sleep(100);
    }
    const stop = async () => {
        try { process.kill(-proc.pid, 'SIGTERM'); } catch { /* gone */ }
        for (let i = 0; i < 50 && !exited; i++) await sleep(100);
        try { process.kill(-proc.pid, 'SIGKILL'); } catch { /* gone */ }
        try { fs.chmodSync(root, 0o700); } catch { /* ok */ }
        if (!process.env.QA_KEEP_FIRST_RUN) fs.rmSync(root, { recursive: true, force: true });
    };
    return { base, dirs, ran, got, log, stop };
}

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Input } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const servers = [];
    // A scenario that fails does not hide the next one: each starts its own fresh install, and the run reports every failure at the end.
    const failures = [];
    const scenario = async fn => {
        try { await fn(); } catch (e) { failures.push(e); console.log('   ✗ ' + String((e && e.message) || e).split('\n')[0]); }
        for (const s of servers.splice(0)) await s.stop().catch(() => {});
    };
    const fresh = async standIn => { const s = await freshServer(standIn); servers.push(s); return s; };
    const screen = () => ev(`(() => { const vis = e => !!e && e.offsetParent !== null; return { url: location.pathname, welcome: vis(document.querySelector('.welcome-shell')), composer: vis(document.getElementById('command-input')), modals: [...document.querySelectorAll('.modal')].filter(m => getComputedStyle(m).display !== 'none').map(m => (m.querySelector('h3,h2') || {}).textContent || m.id), toast: [...document.querySelectorAll('.toast')].map(t => t.textContent.trim()).slice(-2) }; })()`);

    // Signed out, then create the password the first-run page asks for, which signs this browser in.
    const signUp = async s => {
        await Page.navigate({ url: s.base + '/' }); await Page.loadEventFired();
        await waitFor(`!!document.getElementById('create-form')`, 'the "Create a password" page', { timeout: 15000, diag: `document.body.innerText.slice(0, 300)` });
        assert.match(await ev(`document.body.innerText`), /Create a password/, 'a fresh install asks for a password before it shows anything');
        const anon = await ev(`fetch('/api/sessions/live').then(r => r.status)`);
        assert.ok(anon === 401 || anon === 403, `signed out, the agent list is refused (got ${anon})`);
        await ev(`(() => { for (const id of ['password', 'confirm']) { const i = document.getElementById(id); i.value = ${JSON.stringify(PASSWORD)}; i.dispatchEvent(new Event('input', { bubbles: true })); } document.getElementById('submit').click(); return true; })()`);
        await waitFor(`!document.getElementById('create-form')`, 'leaving the password page after Create', { timeout: 15000, diag: `document.body.innerText.slice(0, 300)` });
        await coralReady();
    };

    // Home -> Launch agent -> a folder -> Launch. Returns the new agent's {session_id, session_name}.
    const launchFromHome = async s => {
        await waitFor(`/What do you want to work on\\?/.test(document.body.innerText)`, 'the home screen', { timeout: 15000, diag: `document.body.innerText.slice(0, 300)` });
        const before = await ev(`fetch('/api/sessions/live').then(r => r.json()).then(l => (Array.isArray(l) ? l : l.sessions || []).map(x => x.session_id))`);
        await ev(`document.querySelector('.welcome-launch[data-arg="agent"]').click(); true`);
        await waitFor(`(() => { const e = document.getElementById('launch-step-agent'); return !!e && getComputedStyle(e).display !== 'none' && !!document.getElementById('launch-dir'); })()`, 'the Launch dialog (agent step)', { timeout: 10000, diag: `document.body.innerText.slice(0, 300)` });
        await ev(`(() => { const d = document.getElementById('launch-dir'); d.value = ${JSON.stringify(s.dirs.work)}; d.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        await ev(`document.querySelector('#launch-step-agent [data-on-click="m-launch-session"]').click(); true`);
        return { before };
    };
    // Whatever stands between Launch and the launch itself (the permission question) is answered the way a first-timer would: the recommended button.
    const answerQuestions = async () => {
        for (let i = 0; i < 40; i++) {
            const btn = await ev(`(() => { const b = document.querySelector('body > .modal:not([id]) [data-action="enable"]'); if (b) { b.click(); return true; } return false; })()`);
            if (btn) return true;
            await sleep(50);
        }
        return false;
    };
    const agentNewIn = async before => {
        let found = null;
        const until = Date.now() + 20000;
        while (Date.now() < until && !found) {
            const l = await ev(`fetch('/api/sessions/live').then(r => r.json()).then(l => (Array.isArray(l) ? l : l.sessions || []).map(x => ({ session_id: x.session_id, name: x.name || x.session_name || x.tmux_session })))`);
            found = l.find(x => !before.includes(x.session_id)) || null;
            if (!found) await sleep(100);
        }
        return found;
    };

    try {
        await Promise.all([Page.enable(), Runtime.enable()]);

        // ---------------------------------------------------------------- 1
        console.log('1. A new person with a ready claude: password, home, Launch, the agent OPENS, a first prompt reaches the CLI');
        await scenario(async () => {
            const s = await fresh('ready');
            await signUp(s);
            console.log('   ✓ signed out until a password is created; the home screen follows');
            const { before } = await launchFromHome(s);
            await answerQuestions();
            const agent = await agentNewIn(before);
            assert.ok(agent, `Launch started no agent (screen: ${JSON.stringify(await screen())}; server log tail: ${fs.readFileSync(s.log, 'utf8').slice(-300)})`);
            console.log('   ✓ Launch started an agent');
            const opened = await ev(`Promise.race([window.__coralReady.agentSelected(${JSON.stringify(agent.session_id)}).then(() => true), new Promise(r => setTimeout(() => r(false), 10000))])`);
            assert.ok(opened, `after Launch the new agent is NOT opened: the page stays where it was (#5835 finding 1). Screen: ${JSON.stringify(await screen())}`);
            const composer = await waitFor(`(() => { const c = document.getElementById('command-input'); return !!c && c.offsetParent !== null && !c.disabled; })()`, 'the message box of the opened agent', { timeout: 10000, diag: `document.body.innerText.slice(0, 300)` });
            assert.ok(composer);
            console.log('   ✓ the new agent is open, with its message box');
            await ev(`document.getElementById('command-input').focus(); true`);
            await Input.insertText({ text: PROMPT });
            const k = { key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 };
            await Input.dispatchKeyEvent({ type: 'keyDown', text: '\r', ...k }); await Input.dispatchKeyEvent({ type: 'keyUp', ...k });
            const until = Date.now() + 30000; let gotText = '';
            while (Date.now() < until) { try { gotText = fs.readFileSync(s.got, 'utf8'); } catch { gotText = ''; } if (gotText.includes(PROMPT)) break; await sleep(100); }
            assert.ok(gotText.includes(PROMPT), `the first prompt never reached the CLI (it received ${JSON.stringify(gotText)}; stand-in started: ${fs.existsSync(s.ran)})`);
            console.log('   ✓ the first prompt reached the CLI');
            await s.stop(); servers.splice(servers.indexOf(s), 1);
        });

        // ---------------------------------------------------------------- 2
        console.log('2. A claude that is not signed in: the agent opens, and its terminal tab is there to sign in with');
        await scenario(async () => {
            const s = await fresh('login');
            await signUp(s);
            const { before } = await launchFromHome(s);
            await answerQuestions();
            const agent = await agentNewIn(before);
            assert.ok(agent, `Launch started no agent (screen: ${JSON.stringify(await screen())})`);
            const opened = await ev(`Promise.race([window.__coralReady.agentSelected(${JSON.stringify(agent.session_id)}).then(() => true), new Promise(r => setTimeout(() => r(false), 10000))])`);
            assert.ok(opened, `after Launch the new agent is NOT opened (#5835 finding 1). Screen: ${JSON.stringify(await screen())}`);
            const tab = await waitFor(`(() => { const t = document.getElementById('live-view-btn-terminal'); if (!t || t.offsetParent === null) return null; t.click(); return true; })()`, 'a visible Terminal tab on the new agent', { timeout: 10000, diag: `[...document.querySelectorAll('#live-session-view .live-view-toggle-btn')].map(b => b.id + '|' + b.textContent.trim())` });
            assert.ok(tab);
            await waitFor(`!!document.querySelector('.xterm, #terminal-container .xterm, .terminal-container')`, 'the terminal view mounting', { timeout: 10000 });
            assert.ok(fs.existsSync(s.ran), 'the stand-in claude was started by the launch');
            console.log('   ✓ the agent opened and its terminal is one click away (the login menu is in it)');
            await s.stop(); servers.splice(servers.indexOf(s), 1);
        });

        // ---------------------------------------------------------------- 3
        console.log('3. claude is not installed: the dialog says so once, Launch is off, nothing starts');
        await scenario(async () => {
            const fixed = ['/usr/local/bin/claude', '/opt/homebrew/bin/claude', '/snap/bin/claude'].filter(p => fs.existsSync(p));
            if (fixed.length) console.log(`   SKIPPED: a claude exists at ${fixed.join(', ')}, where Coral looks after PATH; this scenario would launch it`);
            else {
                const s = await fresh(null);
                await signUp(s);
                const { before } = await launchFromHome(s);
                // #5990: it is said once, under the folder, with Launch off: no second dialog
                await waitFor(`!!document.querySelector('#launch-step-agent [data-blocks-launch]')`, 'the missing CLI said under the folder', { timeout: 15000, diag: `document.body.innerText.slice(0, 400)` });
                const d = await ev(`(() => { const w = document.querySelector('#launch-step-agent [data-blocks-launch]'); const go = document.getElementById('launch-agent-go'); return { text: w.textContent.replace(/\\s+/g, ' '), code: [...w.querySelectorAll('code')].map(c => c.textContent), link: [...w.querySelectorAll('a')].map(a => a.href), disabled: go.disabled, describedBy: go.getAttribute('aria-describedby') === w.id, dialog: !!document.querySelector('body > .modal:not([id]) [data-action="recheck"]') }; })()`);
                assert.match(d.text, /claude CLI was not found/); assert.ok(d.code.some(c => c.length > 8), `an install command is offered (${JSON.stringify(d.code)})`);
                assert.ok(d.link.some(u => /^https:\/\//.test(u)), `the vendor's install page is linked (${JSON.stringify(d.link)})`);
                assert.deepEqual({ disabled: d.disabled, describedBy: d.describedBy, dialog: d.dialog }, { disabled: true, describedBy: true, dialog: false }, 'Launch is off with the message as its reason, and no second dialog');
                const after = await ev(`fetch('/api/sessions/live').then(r => r.json()).then(l => (Array.isArray(l) ? l : l.sessions || []).length)`);
                assert.equal(after, before.length, 'no agent was started');
                // What the server answers if a launch gets there anyway (an older page, the API), for the record: an error that names the CLI, never a success.
                const raw = await ev(`fetch('/api/sessions/launch', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ working_dir: ${JSON.stringify(s.dirs.work)}, agent_type: 'claude' }) }).then(async r => ({ status: r.status, body: await r.text() }))`);
                assert.ok(raw.status >= 400, `the launch of a missing CLI is an error (got ${raw.status})`);
                assert.match(raw.body, /not found/i);
                console.log(`   ✓ said once under the folder with the install page and command; Launch off; no agent; the server answers HTTP ${raw.status}`);
                await s.stop(); servers.splice(servers.indexOf(s), 1);
            }
        });
        if (failures.length) throw new Error(`${failures.length} of 3 first-run scenarios failed:\n - ` + failures.map(e => String((e && e.message) || e).split('\n')[0]).join('\n - '));
        console.log('first_run_browser: ok');
    } finally {
        for (const s of servers) await s.stop().catch(() => {});
        await client.close();
    }
})().catch(e => { console.error('FAIL:', e && e.stack || e); process.exit(1); });
