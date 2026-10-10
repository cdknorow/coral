#!/usr/bin/env node
// Runs the CDP/Chrome frontend suites against an ISOLATED Coral dev server and a headless Chrome.
//
//   node run_cdp_suites.mjs [--only substring[,substring...]] [--timeout seconds] [--chrome /path/to/chrome]
//
// It builds coral with -tags "dev fts5" into a temp dir, starts it with a temp CORAL_DATA_DIR on a
// free non-default port (never ~/.coral, never :8420), starts headless Chrome with a temp profile and
// a debugging port, runs every test that talks CDP (they import chrome-remote-interface) one at a time
// with a timeout, and prints pass/fail per suite. Exit status 1 if any suite failed.
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

const port = await freePort(), cdp = await freePort();
const data = path.join(tmp, 'data'); fs.mkdirSync(data);
const codexHome = path.join(tmp, 'codex'); fs.mkdirSync(path.join(codexHome, 'sessions'), { recursive: true });
// Same isolation as run.sh: no inherited production pointers, isolated Codex home, no telemetry.
const cleanEnv = Object.fromEntries(Object.entries(process.env).filter(([k]) => !['CORAL_PORT', 'CORAL_URL', 'CORAL_DATA_DIR', 'CORAL_DIR'].includes(k)));
const testEnv = { ...cleanEnv, CODEX_HOME: codexHome, CORAL_TELEMETRY_DISABLED: '1' };
const coral = spawn(bin, ['--home', data, '--host', '127.0.0.1', '--port', String(port), '--no-browser', ...(process.env.CORAL_BACKEND ? ['--backend', process.env.CORAL_BACKEND] : [])],
  { env: { ...testEnv, CORAL_DATA_DIR: data }, stdio: ['ignore', fs.openSync(path.join(tmp, 'coral.log'), 'w'), fs.openSync(path.join(tmp, 'coral.log'), 'a')] });
procs.push(coral);
await waitHttp(`http://127.0.0.1:${port}/api/health`);
const chrome = spawn(chromePath, ['--headless=new', `--remote-debugging-port=${cdp}`, `--user-data-dir=${path.join(tmp, 'profile')}`,
  '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--window-size=1400,900', 'about:blank'], { stdio: 'ignore' });
procs.push(chrome);
await waitHttp(`http://127.0.0.1:${cdp}/json/version`);
console.log(`coral http://127.0.0.1:${port}  chrome cdp :${cdp}  tmp ${tmp}\n`);

const tests = fs.readdirSync(here).filter(f => /\.test\.(js|mjs)$/.test(f))
  .filter(f => fs.readFileSync(path.join(here, f), 'utf8').includes('chrome-remote-interface'))
  .filter(f => !only || only.split(',').some(o => f.includes(o))).sort();
const results = [];
for (const t of tests) {
  const started = Date.now();
  const r = await new Promise(resolve => {
    const p = spawn('node', [t], { cwd: here, env: { ...testEnv, CORAL_URL: `http://127.0.0.1:${port}`, CDP_PORT: String(cdp), CORAL_CDP_PORT: String(cdp) } });
    let out = ''; p.stdout.on('data', d => out += d); p.stderr.on('data', d => out += d);
    const timer = setTimeout(() => { p.kill('SIGKILL'); resolve({ code: 'timeout', out }); }, timeoutS * 1000);
    p.on('exit', code => { clearTimeout(timer); resolve({ code, out }); });
  });
  const ok = r.code === 0;
  results.push({ test: t, ok, code: r.code, secs: ((Date.now() - started) / 1000).toFixed(1), tail: r.out.trim().split('\n').slice(-6).join('\n') });
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${t}  (${results.at(-1).secs}s)${ok ? '' : '  exit=' + r.code}`);
  if (!ok) console.log(r.out.trim().split('\n').slice(-30).map(l => '      ' + l).join('\n'));
}
const failed = results.filter(r => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} suites passed`);
fs.writeFileSync(path.join(tmp, 'results.json'), JSON.stringify(results, null, 1));
console.log('results:', path.join(tmp, 'results.json'), ' coral log:', path.join(tmp, 'coral.log'));
process.exit(failed.length ? 1 : 0);
