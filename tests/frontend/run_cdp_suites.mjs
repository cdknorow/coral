#!/usr/bin/env node
// Runs the CDP/Chrome frontend suites against an ISOLATED Coral dev server and a headless Chrome.
//
//   node run_cdp_suites.mjs [--only substring[,substring...]] [--timeout seconds] [--chrome /path/to/chrome]
//                           [--shards N] [--retries N] [--allow-flaky] [--keep] [--stress [--runs N] [--cpu-throttle X]]
//                           [--sequences] [--strict | --no-strict]
//
// It builds coral with -tags "dev fts5" into a temp dir, starts it with a temp CORAL_DATA_DIR on a
// free non-default port (never ~/.coral, never :8420), starts headless Chrome with a temp profile and
// a debugging port, runs every CDP and server suite (see suites.mjs) one at a time with a timeout, and
// prints pass/fail per suite. It refuses to start (exit 2) while any *.test.js/mjs file here has no runner
// or more than one, so a suite can't be silently skipped.
//
// --shards N (default 1) starts N such server+Chrome pairs, each with its own data dir, ports and profile,
// and runs N suites at once: each shard takes the next suite from one shared queue, so within a shard
// suites still run one at a time. Output is printed per suite as it finishes.
//
// --timeout S (default 120) is a hard wall-clock limit per suite attempt. When it fires (typically a hung
// CDP socket), the suite's whole process group is killed, which closes its CDP client, and that shard's
// Chrome and Coral server are replaced with fresh ones before its next suite, so a wedged browser can't
// fail the suites after it. The attempt is reported as TIMEOUT. A shard whose Chrome or server died
// between suites is replaced the same way.
//
// Linux: the whole run is in a private network namespace with only loopback when the user may create
// one (unshare), so the host's address changes can't cancel Chrome's requests (#3466); CDP_NETNS=0 turns
// that off. CDP_NETLOG=1 has each shard's Chrome write its network log to <shard>/netlog.json.
//
// --retries N (default 1): a failed or timed-out suite is run again, up to N more times, each time on a
// fresh Coral server and Chrome (its shard is replaced first). A suite that
// passes only on a retry is FLAKY: it is listed separately in the summary and is NOT counted as passed.
// --retries 0 disables retries.
//
// Exit status: 0 when every suite passed on its first attempt, 1 when any suite failed every attempt,
// 3 when the only problems are FLAKY suites (--allow-flaky turns that 3 into 0), 2 on a setup error,
// 4 when everything else passed but a shard's tmux server outlived the run (#3550; it is killed and listed).
//
// SEQUENCES (#3464) are groups of suites whose order matters: one can leave state behind that breaks
// the next (terminal_scroll right after terminal_pane_size once did). A full run (no --only) also runs
// each group back to back on a fresh shard, so that case is tested whatever the shard split; the result
// is one line, "sequence: a > b". --sequences runs only the groups. No retries: an order problem is the
// point.
//
// --stress runs the selected suites --runs N times (default 3) on --shards shards (default 3 in this
// mode) with every page's CPU throttled --cpu-throttle X times (default 4) through CDP
// Emulation.setCPUThrottlingRate, without retries, and prints each suite's pass count (e.g. 2/3) so the
// flaky list is measurable. Exit status 1 unless every suite passed every run.
//
// results.json (path printed at the end) has every suite with its status and every attempt (with the
// log of the Coral server it ran against); a failed attempt keeps its complete output, untruncated.
//
// Disk: a run's temp folder holds the built binary and every shard's data dir and Chrome profile (about
// 250 MB). On exit it is deleted when every suite passed on its first attempt; otherwise (a failure, a
// flaky suite, an interrupt) only results.json and the coral.log files are kept, for diagnosis. --keep
// keeps the whole folder.
//
// Before every suite attempt, the shard's Chrome gets one fresh about:blank tab (every other page is
// closed) and the Coral origin's browser storage (localStorage, sessionStorage via the fresh tab,
// IndexedDB, cookies, caches, service workers) is cleared, so suites can't leak state into each other
// through the browser. Server-side state (agents, boards, settings) is not reset; suites own that.
//
// Leftovers: suites, Coral servers and Chromes run in their own process groups, killed on exit, Ctrl-C
// and SIGTERM. A runner killed with SIGKILL can't clean up, so each runner records its groups in
// <tmpdir>/coral-cdp-runner-<runner pid>.json. At start, a runner kills the groups listed by any runner
// that is no longer alive (runners running at the same time are left alone), and only groups whose
// command line still names that run's temp directory, so a reused pid is never hit.
// Never run bare 'tmux' from an agent shell: $TMUX there points at production Coral's ~/.coral/tmux.sock.
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { createRequire } from 'node:module';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { classifySuites, problems } from './suites.mjs';

// Linux: the run goes in a private network namespace with only loopback (#3466). Chrome cancels
// every request in flight, even to 127.0.0.1, when the host's addresses change (ERR_NETWORK_CHANGED;
// on epoch, IPv6 temporary-address renewals every few minutes), so a page loading its modules then
// failed with "Failed to fetch dynamically imported module". In its own namespace no change reaches
// Chrome. The runner runs itself again under unshare (same user, no root: --map-current-user), brings
// loopback up, and drops the namespace's capabilities before node starts. Where namespaces aren't
// allowed (CI's Ubuntu 24.04 restricts them) it says so and runs as before. CDP_NETNS=0 turns it off.
if (process.platform === 'linux' && !process.env.CDP_NETNS_INNER && process.env.CDP_NETNS !== '0') {
  const ns = ['--net', '--map-current-user', '--keep-caps'];
  const probe = spawnSync('unshare', [...ns, 'ip', 'link', 'set', 'lo', 'up'], { stdio: 'ignore' });
  if (probe.status === 0) {
    // The namespace has no network, and the run builds Coral inside it: fetch the Go modules first
    // (nothing to do when they're already in the module cache).
    spawnSync('go', ['mod', 'download'], { cwd: path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../coral-go'), stdio: 'inherit' });
    const inner = spawn('unshare', [...ns, 'sh', '-c', 'ip link set lo up && exec setpriv --inh-caps=-all --ambient-caps=-all -- "$@"',
      'sh', process.execPath, ...process.argv.slice(1)], { stdio: 'inherit', env: { ...process.env, CDP_NETNS_INNER: '1' } });
    for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => inner.kill(sig));
    const code = await new Promise(resolve => inner.on('exit', (c, sig) => resolve(c ?? 128 + (os.constants.signals[sig] || 0))));
    process.exit(code);
  }
  console.log('note: no private network namespace (unshare --net --map-current-user failed); a change of the host\'s ' +
    'addresses can fail page loads (ERR_NETWORK_CHANGED, #3466). CDP_NETNS=0 hides this note.');
}
if (process.env.CDP_NETNS_INNER) console.log('network: a private namespace with only loopback (#3466; CDP_NETNS=0 turns it off)');

const here = path.dirname(fileURLToPath(import.meta.url));
const goDir = path.resolve(here, '../../coral-go');
const CDP = createRequire(import.meta.url)('chrome-remote-interface');
const argv = process.argv.slice(2);
const flag = name => argv.includes(name);
const arg = (name, def) => { const i = argv.indexOf(name); return i >= 0 ? argv[i + 1] : def; };
const intArg = (name, def, min) => {
  const v = Number(arg(name, String(def)));
  if (!Number.isInteger(v) || v < min) { console.error(`${name} must be an integer >= ${min}`); process.exit(2); }
  return v;
};
const stress = flag('--stress');
const only = arg('--only', '');
const timeoutS = intArg('--timeout', 120, 1);
const shards = intArg('--shards', stress ? 3 : 1, 1);
const retries = stress ? 0 : intArg('--retries', 1, 0);
const runs = stress ? intArg('--runs', 3, 1) : 1;
const throttle = stress ? Number(arg('--cpu-throttle', '4')) : 1;
if (!(throttle >= 1)) { console.error('--cpu-throttle must be a number >= 1'); process.exit(2); }
const allowFlaky = flag('--allow-flaky');
const sequencesOnly = flag('--sequences');
const SEQUENCES = [
  ['terminal_pane_size_browser.test.js', 'terminal_scroll.test.js'],
];
const keep = flag('--keep');
// Strict local auth (#3527, Security's #3509 item 6): --strict runs STRICT_SUITES on shards whose Coral has
// --strict-local-auth, each suite starting in a tab signed in through 'coral open --print'. A full run (no
// --only, no --stress) adds one such shard at the end; --no-strict leaves it out.
const strictOnly = flag('--strict');
const STRICT_SUITES = [
  'strict_mode_browser.test.js', 'server_switcher_browser.test.js', 'agent_state.test.js',
  'team_view_consolidation.test.js', 'team_view_responsive_rows.test.js', 'a11y_dashboard_browser.test.js',
];
const strictShard = strictOnly || (!flag('--no-strict') && !only && !stress && !flag('--sequences'));
const PROD_PORT = 8420;
const chromePath = arg('--chrome', process.env.CHROME || [
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser',
].find(p => fs.existsSync(p)));
if (!chromePath) { console.error('no Chrome found; pass --chrome or set CHROME'); process.exit(2); }

// Every suite file must have exactly one runner before anything starts (#3405).
const suites = classifySuites(here);
{
  const bad = problems(suites);
  console.log(`suites: ${suites.cdp.length} CDP (${suites.cdpByName} named *_browser), ${suites.server.length} server-only, ${suites.node.length} node (npm run test:cloud)`);
  if (bad.length) { console.error(bad.join('\n')); process.exit(2); }
}

const freePort = async () => {
  for (;;) {
    const p = await new Promise((res, rej) => {
      const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const q = s.address().port; s.close(() => res(q)); }); s.on('error', rej);
    });
    if (p !== PROD_PORT) return p; // never the production Coral port, even by chance
  }
};
const sleep = ms => new Promise(r => setTimeout(r, ms));
async function waitHttp(url, ms = 30000) {
  const end = Date.now() + ms;
  while (Date.now() < end) { try { const r = await fetch(url); if (r.status < 500) return; } catch { /* retry */ } await sleep(200); }
  throw new Error('timeout waiting for ' + url);
}
const alive = p => p && p.exitCode === null && p.signalCode === null;
const killTree = (p, sig = 'SIGKILL') => {
  if (!p || !p.pid) return;
  try { process.kill(-p.pid, sig); } catch { try { p.kill(sig); } catch { /* gone */ } }
};

// Each shard's Coral runs its agents in its own tmux server (-S <shard>/data/tmux.sock). tmux
// daemonizes out of Coral's process group, so killing the group leaves it running (#3550: orphans
// alive for hours on epoch). It is stopped through its own socket, with TMUX unset: never bare
// 'tmux', which from an agent shell means production Coral's server.
const tmuxEnv = Object.fromEntries(Object.entries(process.env).filter(([k]) => k !== 'TMUX' && k !== 'TMUX_PANE'));
const killTmux = sock => {
  if (!sock || !fs.existsSync(sock)) return;
  spawnSync('tmux', ['-S', sock, 'kill-server'], { env: tmuxEnv, stdio: 'ignore', timeout: 5000 });
};
const tmuxSocks = new Set(); // the sockets of the shards this run started

// Kill what a previous runner that was SIGKILLed left behind (see "Leftovers" above).
const pidDir = os.tmpdir();
const pidFile = path.join(pidDir, `coral-cdp-runner-${process.pid}.json`);
const isAlive = pid => { try { process.kill(pid, 0); return true; } catch (e) { return e.code === 'EPERM'; } };
function sweepLeftovers() {
  let n = 0;
  for (const f of fs.readdirSync(pidDir)) {
    const m = /^coral-cdp-runner-(\d+)\.json$/.exec(f);
    if (!m || Number(m[1]) === process.pid || isAlive(Number(m[1]))) continue; // ours, or a runner still running
    let groups = [];
    try { groups = JSON.parse(fs.readFileSync(path.join(pidDir, f), 'utf8')); } catch { /* unreadable: just remove it */ }
    for (const { pid, tmux: sock, tmp: t } of groups) {
      // A recorded tmux socket: only one under that runner's own folder.
      if (sock) { if (t && sock.startsWith(t + path.sep)) { killTmux(sock); n++; } continue; }
      const r = spawnSync('ps', ['-o', 'command=', '-p', String(pid)], { encoding: 'utf8' });
      if (r.status !== 0 || !t || !r.stdout.includes(t)) continue; // gone, or the pid now belongs to something else
      try { process.kill(-pid, 'SIGKILL'); n++; } catch { /* already gone */ }
    }
    try { fs.rmSync(path.join(pidDir, f)); } catch { /* fine */ }
  }
  if (n) console.log(`killed ${n} leftover process group(s) from an earlier run that did not exit cleanly`);
}
sweepLeftovers();

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-cdp-'));
const procs = new Set(); // every process group we started, killed on exit
const recordPids = () => {
  try {
    fs.writeFileSync(pidFile, JSON.stringify([...procs].filter(p => p.pid).map(p => ({ pid: p.pid, tmp }))
      .concat([...tmuxSocks].map(sock => ({ tmux: sock, tmp })))));
  } catch { /* best effort */ }
};
const track = p => { procs.add(p); recordPids(); return p; };
const untrack = p => { procs.delete(p); recordPids(); };
let allPassed = false; // set at the end: every suite passed on its first attempt
// pruneTmp deletes the run's folder, or (unless all passed) everything in it but results.json and the
// coral.log files. Chrome profiles go whole.
const pruneTmp = () => {
  if (keep) return;
  try {
    if (allPassed) { fs.rmSync(tmp, { recursive: true, force: true }); return; }
    const prune = dir => {
      for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const p = path.join(dir, e.name);
        if (e.isDirectory() && e.name === 'profile') fs.rmSync(p, { recursive: true, force: true });
        else if (e.isDirectory()) { prune(p); try { fs.rmdirSync(p); } catch { /* not empty: it has a log */ } }
        else if (e.name !== 'results.json' && e.name !== 'coral.log') fs.rmSync(p, { force: true });
      }
    };
    prune(tmp);
  } catch { /* best effort */ }
};
const cleanup = () => {
  for (const p of procs) killTree(p);
  for (const sock of tmuxSocks) killTmux(sock);
  try { fs.rmSync(pidFile); } catch { /* fine */ }
  pruneTmp();
};

// The tmux servers still alive whose socket is under this run's folder: { pid, command }.
function leakedTmux() {
  const r = spawnSync('ps', ['-axo', 'pid=,command='], { encoding: 'utf8' });
  if (r.status !== 0) return [];
  return r.stdout.split('\n').map(l => /^\s*(\d+)\s+(.*)$/.exec(l)).filter(Boolean)
    .filter(([, , cmd]) => /(^|\/)tmux\b/.test(cmd) && cmd.includes(tmp + path.sep))
    .map(([, pid, command]) => ({ pid: Number(pid), command }));
}
process.on('exit', cleanup);
process.on('SIGINT', () => process.exit(130));
process.on('SIGTERM', () => process.exit(143));

console.log('building coral (dev) and its tools ...');
// The Docs tab's documents are embedded from static/agent_docs, which only the Makefile and the
// release scripts fill: sync them, or a fresh checkout's build has none (routes_browser opens /docs/<name>).
const sd = spawnSync('bash', [path.resolve(goDir, '../tools/sync-agent-docs.sh')], { stdio: 'inherit' });
if (sd.status !== 0) process.exit(2);
const bin = path.join(tmp, 'coral');
// This build's coral-board and coral-hook-* next to coral: a dev or test server never uses the
// installed app's (#3492), so without them its agents would send no status events.
const toolPackages = ['./cmd/coral', './cmd/coral-board', './cmd/coral-agent', './cmd/coral-hook-agentic-state',
  './cmd/coral-hook-session-start', './cmd/coral-hook-message-check', './cmd/coral-hook-task-sync'];
const b = spawnSync('go', ['build', '-tags', 'dev fts5', '-o', tmp + path.sep, ...toolPackages], { cwd: goDir, stdio: 'inherit' });
if (b.status !== 0) process.exit(2);

// Same isolation as run.sh: no inherited production pointers, isolated Codex home, no telemetry.
// An agent shell's identity and production links (Cloud Hub URL, hub mode, tmux socket, workflow
// context) never reach a test server or suite.
// Keep in step with tests/lib/agent_env.sh and agentShellVars in coral-go/internal/testguard.
const AGENT_SHELL_VARS = ['CORAL_SESSION_NAME', 'CORAL_SESSION_ID', 'CORAL_SESSION_TOKEN', 'CORAL_SUBSCRIBER_ID', 'CORAL_CLOUD_URL', 'CORAL_HUB', 'CORAL_SHELL', 'CORAL_TMUX_SOCKET',
  'CORAL_TMUX_FALLBACK', 'CORAL_AGENT_HELPER', 'CORAL_AGENT_ARGS', 'CORAL_BOARD_HELPER', 'CORAL_BOARD_ARGS'];
const cleanEnv = Object.fromEntries(Object.entries(process.env).filter(([k]) => ![
  'CORAL_PORT', 'CORAL_URL', 'CORAL_HOST', 'CORAL_DATA_DIR', 'CORAL_DIR', 'CORAL_TEST_URL', 'TMUX', 'TMUX_PANE', ...AGENT_SHELL_VARS,
].includes(k) && !k.startsWith('CORAL_WORKFLOW_') && !k.startsWith('CORAL_PREV') && !k.startsWith('CORAL_STEP_')));
// Host-data locations the server would otherwise read from this machine (agent transcripts, settings).
// They are stripped from what the server inherits and its HOME points into the shard, so a test server
// never indexes the real ~/.claude/projects, ~/.codex, ~/.gemini or ~/.pi.
const HOST_DATA_VARS = ['CLAUDE_PROJECTS_DIR', 'GEMINI_TMP_DIR', 'ANTIGRAVITY_DATA_DIR', 'PI_CODING_AGENT_DIR'];

// throttlePages keeps a browser-level CDP session that applies Emulation.setCPUThrottlingRate to every
// page target, the ones already open and every one created later, until the shard is stopped.
async function throttlePages(cdpPort, rate) {
  const version = await (await fetch(`http://127.0.0.1:${cdpPort}/json/version`)).json();
  const client = await CDP({ target: version.webSocketDebuggerUrl, local: true });
  const done = new Set();
  const apply = async info => {
    if (info.type !== 'page' || done.has(info.targetId)) return;
    done.add(info.targetId);
    const { sessionId } = await client.send('Target.attachToTarget', { targetId: info.targetId, flatten: true });
    await client.send('Emulation.setCPUThrottlingRate', { rate }, sessionId);
  };
  client.on('Target.targetCreated', ({ targetInfo }) => { apply(targetInfo).catch(() => { /* target already gone */ }); });
  await client.send('Target.setDiscoverTargets', { discover: true });
  for (const info of (await client.send('Target.getTargets')).targetInfos) await apply(info).catch(() => {});
  return client;
}

const allLogs = []; // every shard's Coral log, including shards that were replaced

// startShard starts one isolated Coral server and one headless Chrome. Each shard has its own data dir,
// Codex home, ports and Chrome profile, so shards share nothing but the built binary. gen counts the
// replacements of shard n (after a timeout or a dead process), so each start gets fresh directories.
async function startShard(n, gen = 0, strict = false) {
  const dir = path.join(tmp, `shard${n}${gen ? `-${gen}` : ''}`);
  fs.mkdirSync(dir, { recursive: true });
  const port = await freePort();
  const data = path.join(dir, 'data'); fs.mkdirSync(data);
  const tmuxSock = path.join(data, 'tmux.sock');
  tmuxSocks.add(tmuxSock);
  recordPids();
  const codexHome = path.join(dir, 'codex'); fs.mkdirSync(path.join(codexHome, 'sessions'), { recursive: true });
  // CORAL_NO_WELCOME: no first-launch 'ways to run Coral' screen over every suite's page (#3221).
  // CORAL_NO_DEMO_SEED: no demo workflow or scheduled job, which would run a real agent and pop an alert
  // on top of whichever suite is on screen ~30 s after startup.
  const testEnv = { ...cleanEnv, CODEX_HOME: codexHome, CORAL_TELEMETRY_DISABLED: '1', CORAL_NO_DEMO_SEED: '1', CORAL_NO_WELCOME: '1' };
  const log = path.join(dir, 'coral.log');
  const hostHome = path.join(dir, 'home'); fs.mkdirSync(hostHome);
  const serverEnv = { ...testEnv, CORAL_DATA_DIR: data, HOME: hostHome };
  for (const k of HOST_DATA_VARS) delete serverEnv[k];
  const coral = spawn(bin, ['--home', data, '--host', '127.0.0.1', '--port', String(port), '--no-browser', '--cloud-url', 'off',
    ...(strict ? ['--strict-local-auth'] : []), ...(process.env.CORAL_BACKEND ? ['--backend', process.env.CORAL_BACKEND] : [])],
    { env: serverEnv, stdio: ['ignore', fs.openSync(log, 'w'), fs.openSync(log, 'a')], detached: true });
  track(coral);
  allLogs.push(log);
  let coralExit = null;
  coral.on('exit', (code, sig) => { coralExit = `exit code ${code} signal ${sig}`; });
  // A port that answers is not proof that this shard owns it, so wait for coral's own log to say it
  // bound the port (it prints the dashboard line only after net.Listen succeeds), or for it to exit.
  const bound = `Coral dashboard: http://localhost:${port}\n`;
  for (const end = Date.now() + 30000; !coralExit && !fs.readFileSync(log, 'utf8').includes(bound); await sleep(100)) {
    if (Date.now() > end) throw new Error(`coral did not bind :${port} within 30s; see ${log}`);
  }
  if (coralExit) throw new Error(`coral exited during startup (${coralExit}); see ${log}`);
  await waitHttp(`http://127.0.0.1:${port}/api/health`);
  // On CI runners (Ubuntu 24.04) unprivileged user namespaces are restricted, so Chrome's sandbox cannot start; the runner is a
  // throwaway VM running only our own test pages, so the sandbox is switched off there and only there.
  // Port 0: Chrome picks a free DevTools port itself and writes it to <profile>/DevToolsActivePort, so the
  // port read back is the one this Chrome bound (no race with other shards or processes).
  const profile = path.join(dir, 'profile');
  const chromeArgs = ['--headless=new', '--remote-debugging-port=0', `--user-data-dir=${profile}`,
    '--no-first-run', '--no-default-browser-check', '--disable-gpu', '--window-size=1400,900',
    ...(process.env.CI ? ['--no-sandbox', '--disable-dev-shm-usage'] : []),
    // CDP_NETLOG=1: Chrome writes its network log to <shard>/netlog.json, to see why a request failed.
    ...(process.env.CDP_NETLOG ? [`--log-net-log=${path.join(dir, 'netlog.json')}`] : []), 'about:blank'];
  const chrome = spawn(chromePath, chromeArgs, { stdio: ['ignore', 'ignore', 'pipe'], detached: true });
  track(chrome);
  let chromeErr = '';
  chrome.stderr.on('data', d => { chromeErr = (chromeErr + d).slice(-8000); });
  let chromeExit = null;
  chrome.on('exit', (code, sig) => { chromeExit = `exit code ${code} signal ${sig}`; });
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
    throw new Error(`Chrome did not open its DevTools port: ${e.message}\n  chrome: ${chromePath}\n  state: ${chromeExit || 'still running'}\n  args: ${chromeArgs.join(' ')}\n  stderr (last 8 KB):\n${chromeErr}`);
  }
  const throttler = throttle > 1 ? await throttlePages(cdp, throttle) : null;
  console.log(`${shards > 1 ? `shard ${n}${gen ? ` (restart ${gen})` : ''}: ` : ''}coral http://127.0.0.1:${port}  chrome cdp :${cdp}${throttle > 1 ? `  cpu x${throttle}` : ''}  tmp ${dir}`);
  // CORAL_TEST_TMUX_SOCKET: this shard's own tmux socket (<data>/tmux.sock), for suites that compare
  // the pane with the dashboard (#3422). CORAL_TEST_LOG: this shard's Coral log, which a suite reads
  // only to explain its own failure (helpers/stand_in_agent.js, #3502).
  return { n, gen, log, coral, chrome, throttler, strict, data, tmuxSock, serverEnv, env: { ...testEnv, CORAL_TEST_URL: `http://127.0.0.1:${port}`, CDP_PORT: String(cdp), CORAL_CDP_PORT: String(cdp),
    CORAL_TEST_TMUX_SOCKET: path.join(data, 'tmux.sock'), CORAL_TEST_LOG: log, ...(strict ? { CORAL_TEST_STRICT: '1' } : {}) } };
}

async function stopShard(shard) {
  try { if (shard.throttler) await shard.throttler.close(); } catch { /* already closed */ }
  for (const p of [shard.chrome, shard.coral]) { killTree(p); untrack(p); }
  killTmux(shard.tmuxSock); // after Coral, so it can't start the server again
  tmuxSocks.delete(shard.tmuxSock);
  recordPids();
}

// Shards start one after another: freePort() releases the port before coral binds it, so two shards
// starting at once could be handed the same port. Each shard also waits for coral's own "bound" log line.
const shardsUp = [];
try {
  for (let i = 1; i <= shards; i++) shardsUp.push(await startShard(i, 0, strictOnly));
} catch (e) { console.error(e.message); process.exit(2); }
console.log('');

for (const t of STRICT_SUITES) if (!suites.cdp.includes(t)) { console.error(`STRICT_SUITES names ${t}, which is not a CDP suite`); process.exit(2); }
let tests = sequencesOnly ? [] : (strictOnly ? STRICT_SUITES : [...suites.cdp, ...suites.server])
  .filter(f => !only || only.split(',').some(o => f.includes(o))).sort();
// The strict phase's results are named 'strict: <suite>' (the default-mode run of a suite is separate).
let phase = strictOnly ? 'strict: ' : '';
const sequences = stress || strictOnly || (only && !sequencesOnly) ? [] : SEQUENCES;
for (const t of sequences.flat()) if (!suites.cdp.includes(t)) { console.error(`SEQUENCES names ${t}, which is not a CDP suite`); process.exit(2); }
if (!tests.length && !sequences.length) { console.error('no CDP suites matched'); process.exit(2); }
console.log(`running ${tests.length} of ${suites.cdp.length + suites.server.length} suites${only ? ` (--only ${only})` : ''}${sequences.length ? `, then ${sequences.length} sequence(s)` : ''}`);

// clearOrigin gives the next suite a clean browser: it opens one fresh about:blank tab and closes every
// other page (a reused tab would keep its sessionStorage, and the previous suite's still-open document
// could write storage again through its timers), then wipes the Coral origin's storage (localStorage,
// IndexedDB, cookies, caches, service workers) in the shard's Chrome. Suites that connect with
// CDP({port}) attach to that fresh tab.
async function clearOrigin(shard) {
  let client;
  try {
    const version = await (await fetch(`http://127.0.0.1:${shard.env.CDP_PORT}/json/version`, { signal: AbortSignal.timeout(5000) })).json();
    client = await CDP({ target: version.webSocketDebuggerUrl, local: true });
    // The fresh tab is created first, so Chrome always has a page open.
    const { targetId } = await client.send('Target.createTarget', { url: 'about:blank' });
    const { targetInfos } = await client.send('Target.getTargets');
    for (const t of targetInfos) {
      if (t.type === 'page' && t.targetId !== targetId) await client.send('Target.closeTarget', { targetId: t.targetId }).catch(() => {});
    }
    // Chrome answers 'Internal error' to this on the browser target, so it is sent through a page session
    // (the fresh tab: the call clears the named origin, not the page's own).
    const { sessionId } = await client.send('Target.attachToTarget', { targetId, flatten: true });
    await client.send('Storage.clearDataForOrigin', { origin: shard.env.CORAL_TEST_URL, storageTypes: 'all' }, sessionId);
    await client.send('Target.detachFromTarget', { sessionId });
  } catch (e) {
    console.log(`  [runner] could not clear browser storage on shard ${shard.n}: ${e.message}`);
  } finally {
    try { if (client) await client.close(); } catch { /* closed */ }
  }
}

// runAttempt runs one suite once in its own process group, with the hard per-attempt timeout.
// signIn gives a strict shard's fresh tab the dashboard's sign-in cookie, as a person gets it: a single-use
// link from 'coral open --print' (the command line's signed request), opened in the tab, which redirects to
// the dashboard; the tab then goes back to about:blank for the suite.
async function signIn(shard) {
  const out = execFileSync(bin, ['open', '--print', '--url', shard.env.CORAL_TEST_URL, '--home', shard.data],
    { env: shard.serverEnv, encoding: 'utf8', timeout: 15000 });
  const link = (out.match(/https?:\/\/\S+login_token=\S+/) || [])[0];
  if (!link) throw new Error(`coral open --print gave no sign-in link: ${out.trim().slice(0, 200)}`);
  let client;
  try {
    client = await CDP({ port: Number(shard.env.CDP_PORT) });
    await client.Page.enable();
    const loaded = client.Page.loadEventFired();
    await client.Page.navigate({ url: link });
    await loaded;
    for (const end = Date.now() + 10000; ; await sleep(100)) {
      const { result } = await client.Runtime.evaluate({ expression: 'location.search.includes("login_token")', returnByValue: true });
      if (!result.value) break;
      if (Date.now() > end) throw new Error('the sign-in link did not redirect');
    }
    await client.Page.navigate({ url: 'about:blank' });
  } finally {
    try { if (client) await client.close(); } catch { /* closed */ }
  }
}

async function runAttempt(t, shard) {
  await clearOrigin(shard);
  if (shard.strict) {
    try { await signIn(shard); } catch (e) {
      return { status: 'fail', code: 'sign-in', secs: '0.0', shard: shard.n, log: shard.log, out: `[runner] strict shard: could not sign the browser in: ${e.message}` };
    }
  }
  const started = Date.now();
  return await new Promise(resolve => {
    const p = spawn('node', [t], { cwd: here, env: shard.env, detached: true });
    track(p);
    let out = ''; p.stdout.on('data', d => out += d); p.stderr.on('data', d => out += d);
    let timedOut = false;
    const timer = setTimeout(() => { timedOut = true; killTree(p); }, timeoutS * 1000);
    let closed = false;
    p.on('close', () => { closed = true; });
    p.on('exit', async code => {
      clearTimeout(timer);
      const secs = ((Date.now() - started) / 1000).toFixed(1);
      // Anything the suite left running in its process group (a helper it spawned) goes with it; then give
      // the output pipes a moment to drain, without waiting on a straggler that kept stdout open.
      killTree(p); untrack(p);
      for (const end = Date.now() + 2000; !closed && Date.now() < end; await sleep(50)) { /* drain */ }
      const status = timedOut ? 'timeout' : code === 0 ? 'pass' : 'fail';
      resolve({ status, code: timedOut ? 'timeout' : code, secs, shard: shard.n, log: shard.log, out: timedOut ? `${out}\n[runner] TIMEOUT: killed after ${timeoutS}s` : out });
    });
  });
}

// healthy asks the shard's Chrome and Coral themselves, not just whether the processes exist: a Chrome that
// a suite closed (Browser.close) can still be shutting down when the next suite starts.
async function healthy(sh) {
  const ok = async url => { try { return (await fetch(url, { signal: AbortSignal.timeout(3000) })).status < 500; } catch { return false; } };
  const chromeOk = alive(sh.chrome) && await ok(`http://127.0.0.1:${sh.env.CDP_PORT}/json/version`);
  const coralOk = alive(sh.coral) && await ok(`${sh.env.CORAL_TEST_URL}/api/health`);
  return chromeOk && coralOk ? '' : !chromeOk ? 'its Chrome is gone' : 'its Coral server is gone';
}

// ensureShard replaces a shard whose Chrome or server is gone, or always when given a reason (force):
// after a timeout, or before a retry.
async function ensureShard(i, force) {
  const sh = shardsUp[i];
  const problem = force || await healthy(sh);
  if (!problem) return sh;
  const why = problem;
  await stopShard(sh);
  console.log(`  [runner] replacing shard ${sh.n} (${why})`);
  shardsUp[i] = await startShard(sh.n, sh.gen + 1, sh.strict);
  return shardsUp[i];
}

const lastLines = (s, n) => s.trim().split('\n').slice(-n).join('\n');
const results = []; // one entry per suite per run
async function runOnce(run) {
  let next = 0;
  const label = runs > 1 ? `  [run ${run}/${runs}]` : '';
  await Promise.all(shardsUp.map(async (_, i) => {
    while (next < tests.length) {
      const t = tests[next++];
      const attempts = [];
      let shard = await ensureShard(i, false);
      for (let a = 0; a <= retries; a++) {
        const r = await runAttempt(t, shard);
        attempts.push(r);
        if (r.status === 'pass') break;
        if (a < retries) {
          console.log(`RETRY ${phase}${t}  (${r.status === 'timeout' ? `TIMEOUT after ${r.secs}s` : `exit=${r.code} after ${r.secs}s`})${shards > 1 ? `  [shard ${r.shard}]` : ''}${label}`);
          // A retry gets a fresh Coral and Chrome: retrying on the same server and browser would repeat a
          // failure caused by their state rather than by the suite.
          shard = await ensureShard(i, r.status === 'timeout' ? 'after a TIMEOUT' : 'before a retry');
        } else if (r.status === 'timeout') {
          shard = await ensureShard(i, 'after a TIMEOUT');
        }
      }
      const final = attempts[attempts.length - 1];
      const status = final.status === 'pass' ? (attempts.length > 1 ? 'flaky' : 'pass') : final.status;
      const res = {
        test: phase + t, run, status, ok: status === 'pass', code: final.code, secs: final.secs, tail: lastLines(final.out, 6),
        attempts: attempts.map(x => ({ status: x.status, code: x.code, secs: x.secs, shard: x.shard, coralLog: x.log, ...(x.status !== 'pass' ? { output: x.out } : {}) })),
      };
      if (shards > 1) res.shard = final.shard;
      if (status !== 'pass') res.output = attempts.filter(x => x.status !== 'pass').map(x => x.out).join('\n\n----- next attempt -----\n\n');
      results.push(res);
      const word = { pass: 'PASS', flaky: 'FLAKY', fail: 'FAIL', timeout: 'TIMEOUT' }[status];
      console.log(`${word.padEnd(7)} ${phase}${t}  (${final.secs}s${attempts.length > 1 ? `, ${attempts.length} attempts` : ''})${shards > 1 ? `  [shard ${final.shard}]` : ''}${label}${status === 'fail' ? `  exit=${final.code}` : ''}`);
      // CDP_FULL=1 prints a failing attempt's whole output (the default shows its last 30 lines, which can hide
      // the failing check); results.json always has it in full.
      for (const x of attempts.filter(x => x.status !== 'pass')) {
        console.log(x.out.trim().split('\n').slice(process.env.CDP_FULL ? 0 : -30).map(l => '      ' + l).join('\n'));
      }
    }
  }));
}

// runSequences runs each SEQUENCES group in order on a fresh shard and records it as one result.
async function runSequences() {
  for (const group of sequences) {
    const name = `sequence: ${group.join(' > ')}`;
    const shard = await ensureShard(0, `fresh for ${name}`);
    let failed = null, secs = 0;
    for (const t of group) {
      const r = await runAttempt(t, shard);
      secs += Number(r.secs) || 0;
      if (r.status !== 'pass') { failed = { t, r }; break; }
    }
    const status = failed ? (failed.r.status === 'timeout' ? 'timeout' : 'fail') : 'pass';
    const res = { test: name, run: 1, status, ok: !failed, code: failed ? failed.r.code : 0, secs: secs.toFixed(1), attempts: [] };
    if (failed) { res.output = `${failed.t} failed after ${group.slice(0, group.indexOf(failed.t)).join(', ')}:\n${failed.r.out}`; res.tail = lastLines(failed.r.out, 6); }
    results.push(res);
    console.log(`${(status === 'pass' ? 'PASS' : status.toUpperCase()).padEnd(7)} ${name}  (${secs.toFixed(1)}s)${failed ? `  ${failed.t} exit=${failed.r.code}` : ''}`);
    if (failed) console.log(failed.r.out.trim().split('\n').slice(process.env.CDP_FULL ? 0 : -30).map(l => '      ' + l).join('\n'));
  }
}

const runStarted = Date.now();
try {
  for (let run = 1; run <= runs; run++) await runOnce(run);
  await runSequences();
  if (strictShard && !strictOnly) {
    // One more shard, in strict mode, for the suites that matter there (#3527).
    tests = STRICT_SUITES.slice().sort();
    phase = 'strict: ';
    console.log(`\nstrict phase: ${tests.length} suites on one shard with --strict-local-auth, each in a signed-in tab`);
    for (const sh of shardsUp) await stopShard(sh);
    shardsUp.length = 0;
    shardsUp.push(await startShard(shards + 1, 0, true));
    await runOnce(1);
  }
} catch (e) { console.error(`runner error (a shard could not be replaced?): ${e.message}`); process.exit(2); }
const elapsed = ((Date.now() - runStarted) / 1000).toFixed(1);
results.sort((a, b) => a.test.localeCompare(b.test) || a.run - b.run);
fs.writeFileSync(path.join(tmp, 'results.json'), JSON.stringify(results, null, 1));
const where = `results: ${path.join(tmp, 'results.json')}  coral logs: ${allLogs.join(' ')}`;

let exitCode;
if (stress) {
  const counts = new Map(tests.map(t => [t, 0]));
  for (const r of results) if (r.status === 'pass') counts.set(r.test, counts.get(r.test) + 1);
  const unstable = [...counts].filter(([, c]) => c < runs).sort((a, b) => a[1] - b[1] || a[0].localeCompare(b[0]));
  console.log(`\nSTRESS: ${tests.length} suites x ${runs} runs on ${shards} shard(s), CPU x${throttle}, no retries, in ${elapsed}s`);
  console.log(`${tests.length - unstable.length}/${tests.length} suites passed every run`);
  if (unstable.length) {
    console.log('pass count per unstable suite (failing every run first):');
    for (const [t, c] of unstable) console.log(`  ${c}/${runs}  ${t}${c === 0 ? '  (failing)' : '  (flaky)'}`);
  }
  exitCode = unstable.length ? 1 : 0;
} else {
  const by = s => results.filter(r => r.status === s).map(r => r.test);
  const flaky = by('flaky'), failed = [...by('fail'), ...by('timeout')];
  console.log(`\n${results.length - flaky.length - failed.length}/${results.length} suites passed in ${elapsed}s${shards > 1 ? ` on ${shards} shards` : ''}${flaky.length ? `, ${flaky.length} FLAKY (passed only on a retry; not counted as passed)` : ''}${failed.length ? `, ${failed.length} FAILED` : ''}`);
  if (flaky.length) console.log(`FLAKY: ${flaky.join(', ')}`);
  if (failed.length) console.log(`FAILED: ${failed.join(', ')}`);
  exitCode = failed.length ? 1 : flaky.length ? (allowFlaky ? 0 : 3) : 0;
}
// Nothing this run started may outlive it (#3550): stop the shards, then look for tmux servers whose
// socket is under the run's folder. Any found are listed, killed (by the PID just read, whose command
// names this run's folder), and fail the run with 4.
for (const sh of shardsUp) await stopShard(sh);
let leaks = leakedTmux();
for (let i = 0; i < 20 && leaks.length; i++) { await sleep(100); leaks = leakedTmux(); } // kill-server is quick, not instant
if (leaks.length) {
  console.log(`LEAK: ${leaks.length} tmux server(s) outlived the run; killing them:`);
  for (const l of leaks) { console.log(`  ${l.pid} ${l.command}`); try { process.kill(l.pid, 'SIGKILL'); } catch { /* gone */ } }
  if (!exitCode) exitCode = 4;
}
allPassed = results.every(r => r.status === 'pass') && !leaks.length;
console.log(allPassed && !keep ? 'all passed: the run folder is removed on exit (--keep keeps it)' : where);
process.exit(exitCode);
