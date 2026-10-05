#!/usr/bin/env bash
# Assignment editor regression against the caller's isolated, built Coral server.
# Usage: CORAL_URL=http://127.0.0.1:8474 bash tests/stress/run_assignment_browser.sh
# Optional: CORAL_CHROME_BIN=/path/to/chrome. Installs no dependencies.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if ! command -v node >/dev/null 2>&1; then
    echo '[assignment-browser] ERROR: Node.js is required' >&2
    exit 1
fi
exec node - "$SCRIPT_DIR/../frontend/task_assignment_ui.test.js" <<'JS'
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const {spawn, spawnSync} = require('node:child_process');
const {createRequire} = require('node:module');
const testFile = path.resolve(process.argv[2]);
const log = message => console.log(`[assignment-browser] ${message}`);
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
let chrome, test, tempDir, interrupted = false;
let chromeError, chromeLog = '';
function running(child) {
    return child && child.pid && child.exitCode === null && child.signalCode === null;
}
function signalOwned(child, signal) {
    if (!running(child)) return;
    try { child.kill(signal); }
    catch (error) { if (error.code !== 'ESRCH') throw error; }
}
async function stop(child) {
    if (!running(child)) return;
    const closed = new Promise(resolve => child.once('close', resolve));
    signalOwned(child, 'SIGTERM');
    // Bound shutdown even if Chrome or Node ignores SIGTERM.
    const deadline = Date.now() + 2000;
    while (running(child) && Date.now() < deadline) await delay(50);
    if (running(child)) signalOwned(child, 'SIGKILL');
    await Promise.race([closed, delay(1000)]);
}
for (const signal of ['SIGINT', 'SIGTERM']) {
    process.on(signal, () => {
        interrupted = true;
        process.exitCode = signal === 'SIGINT' ? 130 : 143;
        if (running(test)) signalOwned(test, 'SIGTERM');
        if (running(chrome)) signalOwned(chrome, 'SIGTERM');
    });
}
function executable(file) {
    try { fs.accessSync(file, fs.constants.X_OK); return fs.statSync(file).isFile(); } catch { return false; }
}
function findChrome() {
    if (process.env.CORAL_CHROME_BIN) {
        const configured = path.resolve(process.env.CORAL_CHROME_BIN);
        if (!executable(configured)) throw Error(`CORAL_CHROME_BIN is not executable: ${configured}`);
        return configured;
    }
    const candidates = process.platform === 'darwin'
        ? ['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
           path.join(os.homedir(), 'Applications/Google Chrome.app/Contents/MacOS/Google Chrome'),
           '/Applications/Chromium.app/Contents/MacOS/Chromium'] : [];
    for (const dir of (process.env.PATH || '').split(path.delimiter).filter(Boolean)) {
        for (const name of ['google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser']) candidates.push(path.join(dir, name));
    }
    const found = candidates.find(executable);
    if (!found) throw Error('Chrome/Chromium is required; set CORAL_CHROME_BIN to its executable path');
    return found;
}
(async () => {
    try {
        const base = process.env.CORAL_URL || '';
        if (!/^http:\/\/(127\.0\.0\.1|localhost):\d+\/?$/.test(base) ||
            !Number(new URL(base).port) || Number(new URL(base).port) === 8420) {
            throw Error('CORAL_URL must name an isolated local server, e.g. http://127.0.0.1:8474 (not port 8420)');
        }
        if (!fs.existsSync(testFile)) throw Error(`Regression test missing: ${testFile}`);
        try { createRequire(testFile)('chrome-remote-interface'); }
        catch { throw Error('Missing chrome-remote-interface; provision tests/frontend dependencies before running this helper'); }
        const chromeBin = findChrome();
        tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-assignment-browser-'));
        chrome = spawn(chromeBin, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
            '--remote-debugging-address=127.0.0.1', '--remote-debugging-port=0', `--user-data-dir=${tempDir}`, 'about:blank'],
            {stdio:['ignore', 'ignore', 'pipe'], detached:true});
        chrome.on('error', error => { chromeError = error; });
        chrome.stderr.on('data', data => { chromeLog = (chromeLog + data).slice(-8000); });
        const portFile = path.join(tempDir, 'DevToolsActivePort');
        const deadline = Date.now() + 20000;
        let port;
        while (Date.now() < deadline) {
            if (interrupted) throw Error('Interrupted');
            if (chromeError) throw chromeError;
            if (!running(chrome)) throw Error('Chrome exited before its debug port was ready');
            try {
                const lines = fs.readFileSync(portFile, 'utf8').trim().split('\n');
                const candidate = Number(lines[0]);
                if (Number.isInteger(candidate) && candidate > 0 && candidate <= 65535 && lines[1]?.startsWith('/devtools/browser/')) {
                    port = candidate; break;
                }
            } catch { /* Chrome has not written the private port file yet. */ }
            await delay(100);
        }
        if (!port) throw Error('Chrome startup exceeded 20 seconds');
        log(`Running assignment regression against ${base}, private CDP port ${port}`);
        test = spawn(process.execPath, [testFile], {
            env:{...process.env, CORAL_URL:base, CDP_PORT:String(port)}, stdio:'inherit',
        });
        let timedOut = false;
        const timer = setTimeout(() => { timedOut = true; if (running(test)) test.kill('SIGKILL'); }, 60000);
        let result;
        try {
            result = await new Promise((resolve, reject) => {
                test.once('error', reject);
                test.once('close', (code, signal) => resolve({code, signal}));
            });
        } finally { clearTimeout(timer); }
        if (timedOut) throw Error('Assignment regression exceeded 60 seconds');
        if (interrupted) throw Error('Interrupted');
        if (result.code !== 0) throw Error(`Assignment regression failed (${result.signal || result.code})`);
        log('PASS assignment editor regression');
    } catch (error) {
        console.error(`[assignment-browser] ERROR: ${error.message}`);
        if (chromeLog) console.error(chromeLog);
        process.exitCode = process.exitCode || 1;
    } finally {
        try {
            await stop(test);
            await stop(chrome);
            if (tempDir) {
                // Do not signal an exited leader's process group: macOS may
                // reject that signal. Verify graceful Chrome shutdown instead.
                const deadline = Date.now() + 2000;
                while (true) {
                    const result = spawnSync('ps', ['-axo', 'pid=,args='], {encoding:'utf8', timeout:2000, maxBuffer:8*1024*1024});
                    if (result.error || result.status !== 0) throw Error('Could not verify Chrome profile process cleanup');
                    const survivors = result.stdout.split('\n').filter(line => line.includes(`--user-data-dir=${tempDir}`));
                    if (!survivors.length) break;
                    if (Date.now() >= deadline) throw Error(`Chrome profile processes still running; retained ${tempDir}: ${survivors.join('; ')}`);
                    await delay(100);
                }
                fs.rmSync(tempDir, {recursive:true, force:true});
                log('Owned Chrome/profile cleanup verified');
            }
        } catch (error) {
            console.error(`[assignment-browser] ERROR: cleanup: ${error.message}`);
            process.exitCode = process.exitCode || 1;
        }
    }
})();
JS
