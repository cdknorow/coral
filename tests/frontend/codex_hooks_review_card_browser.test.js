// The chat's "needs input" card for Codex's hooks review (#306, QA's #278 row 15): it read "Hooks need review 2 hooks are new
// or changed…" as one line. The dialog's heading is now the card's heading (an h3, under the session's h2), and its text a line
// under it. A scripted codex prints the dialog as Codex 0.161 draws it (the pointer as ">", which the parser takes like "›"),
// so the whole path runs: the agent's real screen, the server's parse, the chat reply, the card. Run against an isolated server.
'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const DIALOG = ['  Hooks need review', '  2 hooks are new or changed.', '  Hooks can run outside the sandbox after you trust them.', '',
    '> 1. Review hooks', '  2. Trust all and continue', "  3. Continue without trusting (hooks won't run)", '', '  Press enter to confirm or esc to go back'];

// The scripted codex: answers the version and sign-in probes like the CLI, and otherwise draws the dialog and waits.
function writeCodex(dir) {
    if (process.platform === 'win32') {
        const cli = path.join(dir, 'codex.cmd');
        const esc = l => (l === '' ? 'echo.' : 'echo ' + l.replace(/([&|<>^()])/g, '^$1').replace(/%/g, '%%'));
        fs.writeFileSync(cli, ['@echo off', 'if "%~1"=="--version" (echo codex-cli 0.161.0& exit /b 0)', 'if "%~1"=="login" (echo Logged in using ChatGPT& exit /b 0)',
            ...DIALOG.map(esc), 'ping -n 600 127.0.0.1 >nul', ''].join('\r\n'));
        return cli;
    }
    const cli = path.join(dir, 'codex');
    fs.writeFileSync(cli, ['#!/bin/sh', 'case "$1" in --version) echo "codex-cli 0.161.0"; exit 0;; login) echo "Logged in using ChatGPT"; exit 0;; esac',
        `printf '%s\\n' ${DIALOG.map(l => `'${l.replace(/'/g, "'\\''")}'`).join(' ')}`, 'exec sleep 600', ''].join('\n'), { mode: 0o755 });
    return cli;
}

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-codex-dialog-'));
    const work = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-codex-work-'));
    const api = (method, p, body) => ev(`fetch(${JSON.stringify(p)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }${body ? `, body: ${JSON.stringify(JSON.stringify(body))}` : ''} })
        .then(async r => ({ status: r.status, data: await r.json().catch(() => null) }))`);
    const guard = await require('./helpers/settings_guard').guardSettings(); // cli_path_codex is put back
    let launch = null;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        const put = await api('PUT', '/api/settings', { cli_path_codex: writeCodex(dir) });
        assert.equal(put.status, 200, 'cli_path_codex set: ' + JSON.stringify(put.data));
        launch = await api('POST', '/api/sessions/launch', { working_dir: work, agent_type: 'codex', display_name: 'Codex Dialog' });
        assert.ok(launch.data && launch.data.session_id, 'launched: ' + JSON.stringify(launch));
        const { session_id: sid, session_name: name } = launch.data;
        await ev(`(async () => { await window.selectLiveSession(${JSON.stringify(name)}, 'codex', ${JSON.stringify(sid)}, 'local'); return true; })()`);
        await waitFor(`!!document.querySelector('#live-history-messages > .chat-needs-input h3.cni-question')`, 'the card with its heading', 30000,
            `(() => { const c = document.querySelector('#live-history-messages > .chat-needs-input'); return c ? c.innerText.slice(0, 300) : 'no card'; })()`);
        const card = await ev(`(() => { const c = document.querySelector('#live-history-messages > .chat-needs-input');
            const h = c.querySelector('h3.cni-question'), d = c.querySelector('.cni-question-detail');
            return { heading: h && h.textContent, detail: d && d.textContent, headings: [...c.querySelectorAll('h1,h2,h3,h4')].map(e => e.tagName + ':' + e.textContent),
                options: [...c.querySelectorAll('.cni-answer-label')].map(e => e.textContent) }; })()`);
        assert.equal(card.heading, 'Hooks need review', 'the heading on its own line: ' + JSON.stringify(card));
        assert.equal(card.detail, '2 hooks are new or changed. Hooks can run outside the sandbox after you trust them.', 'its text under it');
        assert.deepEqual(card.headings, ['H3:Hooks need review'], 'one heading, an h3 under the session name (h2)');
        assert.ok(card.options.includes('Review hooks') && card.options.includes('Trust all and continue'), 'the options: ' + JSON.stringify(card.options));
        console.log('PASS codex hooks-review card: the heading on its own line (h3), the text under it, the options');
    } finally {
        const d = launch && launch.data;
        if (d && d.session_id) await api('POST', `/api/sessions/live/${encodeURIComponent(d.session_name)}/kill`, { agent_type: 'codex', session_id: d.session_id }).catch(() => {});
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
        fs.rmSync(dir, { recursive: true, force: true });
        fs.rmSync(work, { recursive: true, force: true });
    }
})().catch(e => { console.error(e.stack || String(e)); process.exit(1); });
