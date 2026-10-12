#!/usr/bin/env node
// Runs the CDP/Chrome frontend suites against an ISOLATED Coral dev server and a headless Chrome.
//
//   node run_cdp_suites.mjs [--only substring[,substring...]] [--timeout seconds] [--chrome /path/to/chrome] [--shards N]
//
// It builds coral with -tags "dev fts5" into a temp dir, starts it with a temp CORAL_DATA_DIR on a
// free non-default port (never ~/.coral, never :8420), starts headless Chrome with a temp profile and
// a debugging port, runs every test that talks CDP (they import chrome-remote-interface) one at a time
// with a timeout, and prints pass/fail per suite. Exit status 1 if any suite failed.
//
// --shards N (default 1) starts N such server+Chrome pairs, each with its own data dir, ports and profile,
// and runs N suites at once: each shard takes the next suite from one shared queue, so within a shard
// suites still run one at a time. Output is printed per suite as it finishes.
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const goDir = path.resolve(here, '../../coral-go');
const arg = (name, def) => { const i = process.argv.indexOf(name); return i > 0 ? process.argv[i + 1] : def; };
const only = arg('--only', '');
const timeoutS = Number(arg('--timeout', '120'));
const shards = Number(arg('--shards', '1'));
if (!Number.isInteger(shards) || shards < 1) { console.error('--shards must be a positive integer'); process.exit(2); }
const chromePath = arg('--chrome', process.env.CHROME || [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser',
].find(p => fs.existsSync(p)));
if (!chromePath) { console.error('no Chrome found; pass --chrome or set CHROME'); process.exit(2); }

const freePort = () => new Promise((res, rej) => {
  const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); }); s.on('error', rej);
});
const sleep = ms => new Promise(r => setTimeout(r, ms));
async function waitHttp(url, ms = 30000) {
  const end = Date.now() + ms;
  while (Date.now() < end) { try { const r = await fetch(url); if (r.status < 500) return; } catch { /* retry */ } await sleep(200); }
  throw new Error('timeout waiting for ' + url);
}

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-cdp-'));
const procs = [];
const cleanup = () => { for (const p of procs) { try { p.kill('SIGTERM'); } catch { /* gone */ } } };
process.on('exit', cleanup); process.on('SIGINT', () => process.exit(130));

console.log('building coral (dev) ...');
const bin = path.join(tmp, 'coral');
const b = spawnSync('go', ['build', '-tags', 'dev fts5', '-o', bin, './cmd/coral'], { cwd: goDir, stdio: 'inherit' });
if (b.status !== 0) process.exit(2);

// Same isolation as run.sh: no inherited production pointers, isolated Codex home, no telemetry.
const cleanEnv = Object.fromEntries(Object.entries(process.env).filter(([k]) => !['CORAL_PORT', 'CORAL_URL', 'CORAL_DATA_DIR', 'CORAL_DIR', 'TMUX', 'TMUX_PANE'].includes(k)));

// startShard starts one isolated Coral server and one headless Chrome. Each shard has its own data dir,
// Codex home, ports and Chrome profile, so shards share nothing but the built binary.
async function startShard(n) {
  const dir = shards > 1 ? path.join(tmp, `shard${n}`) : tmp;
  fs.mkdirSync(dir, { recursive: true });
  const port = await freePort();
  const data = path.join(dir, 'data'); fs.mkdirSync(data);
  const codexHome = path.join(dir, 'codex'); fs.mkdirSync(path.join(codexHome, 'sessions'), { recursive: true });
  const testEnv = { ...cleanEnv, CODEX_HOME: codexHome, CORAL_TELEMETRY_DISABLED: '1' };
  const log = path.join(dir, 'coral.log');
  const coral = spawn(bin, ['--home', data, '--host', '127.0.0.1', '--port', String(port), '--no-browser', ...(process.env.CORAL_BACKEND ? ['--backend', process.env.CORAL_BACKEND] : [])],
    { env: { ...testEnv, CORAL_DATA_DIR: data }, stdio: ['ignore', fs.openSync(log, 'w'), fs.openSync(log, 'a')] });
  let coralExit = null;
  coral.on('exit', (code, sig) => { coralExit = `exit code ${code} signal ${sig}`; });
  procs.push(coral);
  // A port that answers is not proof that this shard owns it, so wait for coral's own log to say it
  // bound the port (it prints the dashboard line only after net.Listen succeeds), or for it to exit.
  const bound = `Coral dashboard: http://localhost:${port}\n`;
  for (const end = Date.now() + 30000; !coralExit && !fs.readFileSync(log, 'utf8').includes(bound); await sleep(100)) {
    if (Date.now() > end) { console.error(`coral did not bind :${port} within 30s; see ${log}`); process.exit(2); }
  }
  if (coralExit) { console.error(`coral exited during startup (${coralExit}); see ${log}`); process.exit(2); }
  await waitHttp(`http://127.0.0.1:${port}/api/health`);
  // On CI runners (Ubuntu 24.04) unprivileged user namespaces are restricted, so Chrome's sandbox cannot start; the runner is a
  // throwaway VM running only our own test pages, so the sandbox is switched off there and only there.
  // Port 0: Chrome picks a free DevTools port itself and writes it to <profile>/DevToolsActivePort, so the
  // port read back is the one this Chrome bound (no race with other shards or processes).
  const profile = path.join(dir, 'profile');
  const chromeArgs = ['--headless=new', '--remote-debugging-port=0', `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--window-size=1400,900',
    ...(process.env.CI ? ['--no-sandbox', '--disable-dev-shm-usage'] : []), 'about:blank'];
  const chrome = spawn(chromePath, chromeArgs, { stdio: ['ignore', 'ignore', 'pipe'] });
  let chromeErr = '';
  chrome.stderr.on('data', d => { chromeErr = (chromeErr + d).slice(-8000); });
  let chromeExit = null;
  chrome.on('exit', (code, sig) => { chromeExit = `exit code ${code} signal ${sig}`; });
  procs.push(chrome);
  let cdp = 0;
  try {
    const portFile = path.join(profile, 'DevToolsActivePort');
    for (const end = Date.now() + 60000; !cdp; await sleep(100)) {
      if (chromeExit || Date.now() > end) throw new Error(chromeExit ? 'Chrome exited' : `no ${portFile} after 60s`);
      try { cdp = Number(fs.readFileSync(portFile, 'utf8').split('\n')[0]) || 0; } catch { /* not written yet */ }
    }
    await waitHttp(`http://127.0.0.1:${cdp}/json/version`, 60000);
  } catch (e) {
    // The DevTools port never opened: say why (this used to be invisible, stdio was swallowed).
    console.error(`Chrome did not open its DevTools port: ${e.message}\n  chrome: ${chromePath}\n  state: ${chromeExit || 'still running'}\n  args: ${chromeArgs.join(' ')}\n  stderr (last 8 KB):\n${chromeErr}`);
    process.exit(2);
  }
  console.log(`${shards > 1 ? `shard ${n}: ` : ''}coral http://127.0.0.1:${port}  chrome cdp :${cdp}  tmp ${dir}`);
  return { n, log, env: { ...testEnv, CORAL_URL: `http://127.0.0.1:${port}`, CDP_PORT: String(cdp), CORAL_CDP_PORT: String(cdp) } };
}
// Shards start one after another: freePort() releases the port before coral binds it, so two shards
// starting at once could be handed the same port. Each shard also waits for coral's own "bound" log line.
const shardsUp = [];
for (let i = 1; i <= shards; i++) shardsUp.push(await startShard(i));
console.log('');

const tests = fs.readdirSync(here).filter(f => /\.test\.(js|mjs)$/.test(f))
  .filter(f => fs.readFileSync(path.join(here, f), 'utf8').includes('chrome-remote-interface'))
  .filter(f => !only || only.split(',').some(o => f.includes(o))).sort();
const results = [];
const runStarted = Date.now();
let next = 0;
async function runQueue(shard) {
  while (next < tests.length) {
    const t = tests[next++];
    const started = Date.now();
    const r = await new Promise(resolve => {
      const p = spawn('node', [t], { cwd: here, env: shard.env });
      let out = ''; p.stdout.on('data', d => out += d); p.stderr.on('data', d => out += d);
      const timer = setTimeout(() => { p.kill('SIGKILL'); resolve({ code: 'timeout', out }); }, timeoutS * 1000);
      p.on('exit', code => { clearTimeout(timer); resolve({ code, out }); });
    });
    const ok = r.code === 0;
    const res = { test: t, ok, code: r.code, secs: ((Date.now() - started) / 1000).toFixed(1), tail: r.out.trim().split('\n').slice(-6).join('\n') };
    if (shards > 1) res.shard = shard.n;
    results.push(res);
    console.log(`${ok ? 'PASS' : 'FAIL'}  ${t}  (${res.secs}s)${shards > 1 ? `  [shard ${shard.n}]` : ''}${ok ? '' : '  exit=' + r.code}`);
    // CDP_FULL=1 prints a failing suite's whole output (the default shows only the last 30 lines, which can hide the failing check).
    if (!ok) console.log(r.out.trim().split('\n').slice(process.env.CDP_FULL ? 0 : -30).map(l => '      ' + l).join('\n'));
  }
}
await Promise.all(shardsUp.map(runQueue));
results.sort((a, b) => a.test.localeCompare(b.test));
const failed = results.filter(r => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} suites passed in ${((Date.now() - runStarted) / 1000).toFixed(1)}s${shards > 1 ? ` on ${shards} shards` : ''}`);
fs.writeFileSync(path.join(tmp, 'results.json'), JSON.stringify(results, null, 1));
console.log('results:', path.join(tmp, 'results.json'), ' coral log:', shardsUp.map(sh => sh.log).join(' '));
process.exit(failed.length ? 1 : 0);
