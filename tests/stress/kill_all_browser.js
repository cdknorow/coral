// Real-dashboard half of run_kill_all.sh (#3058): starts a private headless Chrome,
// opens the isolated server's dashboard, sets Group by Team, clicks the folder
// group's "Kill all" and confirms. Prints one JSON line with the confirm text.
//
// usage: node kill_all_browser.js <base-url> <folder-key> <group-by-team: on|off> <expect-session-ids-csv>
// The listed session ids must all be visible in the sidebar before it clicks.
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn, spawnSync } = require('node:child_process');
const { createRequire } = require('node:module');

const [base, folder, grouping, idsCsv] = process.argv.slice(2);
const log = m => console.error(`[kill-all-browser] ${m}`);
const delay = ms => new Promise(r => setTimeout(r, ms));
if (!/^http:\/\/(127\.0\.0\.1|localhost):\d+\/?$/.test(base || '') || Number(new URL(base).port) === 8420) {
    console.error('base must be an isolated local server URL with a non-8420 port'); process.exit(2);
}
const CDP = createRequire(path.join(__dirname, '../frontend/package.json'))('chrome-remote-interface');

function findChrome() {
    if (process.env.CORAL_CHROME_BIN) return process.env.CORAL_CHROME_BIN;
    const c = process.platform === 'darwin'
        ? ['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', path.join(os.homedir(), 'Applications/Google Chrome.app/Contents/MacOS/Google Chrome'), '/Applications/Chromium.app/Contents/MacOS/Chromium'] : [];
    for (const dir of (process.env.PATH || '').split(path.delimiter).filter(Boolean)) for (const n of ['google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser']) c.push(path.join(dir, n));
    const found = c.find(f => { try { fs.accessSync(f, fs.constants.X_OK); return true; } catch { return false; } });
    if (!found) throw Error('Chrome/Chromium is required; set CORAL_CHROME_BIN');
    return found;
}

(async () => {
    const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-killall-chrome-'));
    const chrome = spawn(findChrome(), ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
        '--remote-debugging-address=127.0.0.1', '--remote-debugging-port=0', `--user-data-dir=${profile}`, 'about:blank'],
        { stdio: ['ignore', 'ignore', 'ignore'] });
    let client;
    try {
        let port;
        for (const end = Date.now() + 20000; !port && Date.now() < end; await delay(100)) {
            try { const l = fs.readFileSync(path.join(profile, 'DevToolsActivePort'), 'utf8').trim().split('\n'); if (Number(l[0]) > 0) port = Number(l[0]); } catch { /* not yet */ }
        }
        if (!port) throw Error('Chrome debug port not ready in 20s');
        client = await CDP({ port });
        const { Page, Runtime } = client;
        const ev = async expression => {
            const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
            if (r.exceptionDetails) throw Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
            return r.result.value;
        };
        const waitFor = async (expr, what, ms = 20000) => {
            for (const end = Date.now() + ms; Date.now() < end; await delay(200)) if (await ev(expr)) return;
            throw Error(`timed out waiting for ${what}`);
        };
        await Page.enable();
        await Page.navigate({ url: base });
        await Page.loadEventFired();
        await ev(`localStorage.setItem('coral-group-by-team', ${JSON.stringify(grouping === 'off' ? 'false' : 'true')}); true`);
        await Page.navigate({ url: base });
        await Page.loadEventFired();
        const ids = idsCsv.split(',').filter(Boolean);
        // Every agent of the folder must be loaded (sleeping/archived rows may sit in collapsed sections,
        // so wait on the live list rather than on rows).
        await waitFor(`(async () => { const { state } = await import('/static/state.js'); const have = new Set((state.liveSessions || []).map(s => s.session_id)); return ${JSON.stringify(ids)}.every(id => have.has(id)); })()`, 'agents in the dashboard list');
        const button = `[...document.querySelectorAll('#live-sessions-list .overflow-menu-item')].find(b => (b.getAttribute('onclick') || '').includes("killGroup('${folder.replace(/'/g, "\\'")}')"))`;
        await waitFor(`!!${button}`, `the "${folder}" group's Kill all`);
        await ev(`${button}.click(); true`);
        await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirm dialog', 5000);
        const confirm = await ev(`document.getElementById('confirm-modal-message').textContent`);
        await ev(`document.getElementById('confirm-modal-yes').click(); true`);
        await delay(1500); // the per-agent kill POSTs run after confirm
        console.log(JSON.stringify({ confirm }));
    } finally {
        try { if (client) await client.close(); } catch { /* closing */ }
        chrome.kill('SIGTERM');
        for (const end = Date.now() + 3000; Date.now() < end; await delay(100)) {
            const ps = spawnSync('ps', ['-axo', 'args='], { encoding: 'utf8' });
            if (!ps.stdout.includes(`--user-data-dir=${profile}`)) break;
        }
        fs.rmSync(profile, { recursive: true, force: true });
    }
})().catch(e => { log(`ERROR: ${e.message}`); process.exit(1); });
