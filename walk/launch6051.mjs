// Launch agents on a Windows runner and record each step to a first prompt (trial branch; not for landing).
import CDP from 'chrome-remote-interface';
import fs from 'node:fs';
import { spawn } from 'node:child_process';
const out = process.env.WALK_OUT;
const phase = process.argv[2] || 'phase1';
const chromePath = ['C:/Program Files/Google/Chrome/Application/chrome.exe', 'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe'].find(fs.existsSync);
const base = 'http://127.0.0.1:8450';
const chrome = spawn(chromePath, ['--remote-debugging-port=9222', '--user-data-dir=C:/walk/chrome-profile', '--headless=new', '--no-first-run', '--window-size=1280,900', 'about:blank'], { stdio: 'ignore' });
let client;
for (let i = 0; i < 30 && !client; i++) { await new Promise(r => setTimeout(r, 1000)); try { client = await CDP({ port: 9222 }); } catch {} }
const { Page, Runtime, Input } = client;
await Page.enable();
let n = 200;
const sleep = ms => new Promise(r => setTimeout(r, ms));
const ev = async (expr) => { const r = await Runtime.evaluate({ expression: expr, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
const redact = () => ev(`(() => { window.__undo = []; const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT); let t; while ((t = w.nextNode())) { if (t.nodeValue.includes('runneradmin')) { const o = t.nodeValue, x = t; window.__undo.push(() => { x.nodeValue = o; }); t.nodeValue = t.nodeValue.replaceAll('runneradmin', 'user-name'); } } return 1; })()`);
const unredact = () => ev(`(() => { (window.__undo || []).forEach(f => f()); window.__undo = []; return 1; })()`);
const shot = async (name) => { await redact(); const r = await Page.captureScreenshot({ format: 'png' }); await unredact(); fs.writeFileSync(`${out}/${++n}-${name}.png`, Buffer.from(r.data, 'base64')); console.log('SHOT', n, name); };
const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(async r => JSON.stringify({ status: r.status, body: (await r.text()).slice(0, 4000) }))`).then(JSON.parse);
const clean = s => (s || '').replace(/\x1b\[[0-9;?]*[ -\/]*[@-~]/g, '').replace(/[^\S\n]+/g, ' ').trim();

// a person's first page: create a password (phase 1); phase 2 reuses the browser profile and its session
await Page.navigate({ url: base + '/' }); await sleep(3500);
const pos = async (re) => ev(`(() => { const re = ${re}; const e = [...document.querySelectorAll('button')].find(b => re.test(b.innerText.trim())); if (!e) return null; const r = e.getBoundingClientRect(); return JSON.stringify({ x: r.x + r.width / 2, y: r.y + r.height / 2 }); })()`);
if (phase === 'phase1') {
    await ev(`document.querySelector('input[name=new-password], input[autocomplete=new-password]').focus()`); await Input.insertText({ text: 'walk-pass-Correct-Horse-9' });
    await ev(`document.querySelector('input[name=confirm-password]').focus()`); await Input.insertText({ text: 'walk-pass-Correct-Horse-9' });
    const bp = JSON.parse(await pos(/^Create password and open Coral$/));
    await Input.dispatchMouseEvent({ type: 'mousePressed', x: bp.x, y: bp.y, button: 'left', clickCount: 1 });
    await Input.dispatchMouseEvent({ type: 'mouseReleased', x: bp.x, y: bp.y, button: 'left', clickCount: 1 });
    await sleep(4000);
} else { console.log('phase 2 page: ' + (await ev('location.href'))); }
async function experiment(label, agentType, settings, seconds, workDir = 'C:\\walk\\work') {
    console.log(`\n##### ${label}`);
    if (settings) { const r = await api('PUT', '/api/settings', settings); console.log('PUT settings', r.status, r.body.slice(0, 160)); }
    const t0 = Date.now(); const at = () => ((Date.now() - t0) / 1000).toFixed(1);
    const r = await api('POST', '/api/sessions/launch', { agent_type: agentType, working_dir: workDir, backend: 'pty' });
    console.log(`[${at()}s] launch answered ${r.status}: ${r.body.slice(0, 400)}`);
    let res; try { res = JSON.parse(r.body); } catch {}
    if (r.status !== 200 || !res) { console.log('LAUNCH FAILED', label); return; }
    const name = res.session_name, id = res.session_id;
    await Page.navigate({ url: `${base}/agents/${id}` }); await sleep(2500);
    let lastCap = '', lastState = '', shots = new Set(), saidAt = null, firstLive = true;
    for (let s = 0; s <= seconds; s += 2) {
        const cap = await api('GET', `/api/sessions/live/${encodeURIComponent(name)}/capture`);
        let text = ''; try { const j = JSON.parse(cap.body); text = typeof j === 'string' ? j : (j.output || j.text || j.capture || JSON.stringify(j)); } catch { text = cap.body; }
        const c = clean(text).slice(-420);
        if (c !== lastCap) { console.log(`[${at()}s] SCREEN: ${c.replace(/\n/g, ' | ').slice(0, 420)}`); lastCap = c; }
        const lst = await api('GET', '/api/sessions/live');
        let st = ''; try { const arr = JSON.parse(lst.body); const it = (Array.isArray(arr) ? arr : arr.sessions || []).find(x => (x.name || x.session_name) === name || x.session_id === id); st = it ? JSON.stringify({ state: it.state, status: it.status, activity: it.activity, waiting: it.waiting, working: it.working, agent_state: it.agentic_state, last: it.last_action }) : 'not in the live list'; } catch { st = lst.body.slice(0, 120); }
        const said = await ev(`(() => { const t = document.body.innerText; const m = t.match(/SIGN-IN NEEDED|PERMISSION NEEDED|Needs input|Check terminal|Terminal gone|not found|Trust|Welcome to Codex/); return m ? m[0] : ''; })()`);
        if (said && saidAt === null) { saidAt = at(); console.log(`[${saidAt}s] COREAL SAYS (the page shows): ${said}`); }
        if (firstLive) { try { const arr = JSON.parse(lst.body); const it = (Array.isArray(arr) ? arr : arr.sessions || [])[0]; console.log('LIVE ITEM KEYS ' + JSON.stringify(it).slice(0, 700)); firstLive = false; } catch {} }
        if (st !== lastState) { console.log(`[${at()}s] LIVE: ${st}`); lastState = st; }
        for (const t of [6, 30, 70]) if (s >= t && !shots.has(t)) { shots.add(t); await shot(`${label.replace(/\W+/g, '-')}-${t}s`); }
        await sleep(2000);
    }
    console.log(`[${at()}s] end of watch; Coral said something on the page at: ${saidAt === null ? 'NEVER' : saidAt + ' s'}; killing`);
    await api('POST', `/api/sessions/live/${encodeURIComponent(name)}/kill`);
    await sleep(2500);
}

if (phase === 'phase1') {
    await experiment('E1 stand-in claude (baseline)', 'claude', { cli_path_claude: 'C:\\walk\\bin\\claude.exe' }, 24);
    await experiment('E2 real claude, signed out', 'claude', { cli_path_claude: '' }, 60);
    await experiment('E3 real codex 0.163, signed out', 'codex', { cli_path_codex: '' }, 60);
    await experiment('E5 stand-in in a folder with spaces and non-ASCII letters', 'claude', { cli_path_claude: 'C:\\walk\\bin\\claude.exe' }, 20, 'C:\\walk\\Ordner mit Leerzeichen und \u00dcmlauten \u00c4\u00d6');
    await experiment('E6 claude path set to a program that is not there', 'claude', { cli_path_claude: 'C:\\walk\\nowhere\\claude.exe' }, 12);
} else {
    await experiment('E7 stand-in with Windows PowerShell 5.1 only (no PowerShell 7 on the PATH)', 'claude', { cli_path_claude: 'C:\\walk\\bin\\claude.exe' }, 24);
    await experiment('E4 real claude, Git for Windows (its bash) not on the PATH', 'claude', { cli_path_claude: '' }, 60);
}
await client.close(); chrome.kill();
