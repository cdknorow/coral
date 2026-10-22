// The runner survives its own folder being deleted mid-run (#3727). Runs the real run_cdp_suites.mjs on the fixture suite
// runner_folder_loss_browser.test.js with CDP_RUNNER_FOLDER_LOSS=1: the suite's first attempt deletes the run's temp
// folder and fails, so the runner must replace the shard on a folder that is gone. Before #3727 that died with
// "spawn .../coral ENOENT" and ended the whole run. Now it must put the folder and coral back (a Go build, so this takes a
// minute), run the retry on a fresh shard, finish, and still write results.json.
//
//   node run_runner_folder_loss.mjs      (also: npm run test:runner-folder-loss)
// Needs Go and Chrome like the CDP runner itself, so it is not part of test:cloud.
import { spawn } from './helpers/child_process.js';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const child = spawn('node', ['run_cdp_suites.mjs', '--only', 'runner_folder_loss', '--shards', '1', '--retries', '1', '--allow-flaky', '--no-strict'],
  { cwd: here, env: { ...process.env, CDP_RUNNER_FOLDER_LOSS: '1' } });
let out = '';
child.stdout.on('data', d => { out += d; });
child.stderr.on('data', d => { out += d; });
const timer = setTimeout(() => { child.kill('SIGKILL'); }, 10 * 60 * 1000);
const code = await new Promise(resolve => child.on('exit', c => resolve(c)));
clearTimeout(timer);

let failed = 0;
const check = (label, ok, detail) => { if (!ok) failed++; console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail ? ' — ' + detail : ''}`); };
check('the fixture suite deleted the run folder', /deleted the run folder/.test(out));
check('the runner did not die on a spawn error', !/ENOENT/.test(out) && !/Unhandled 'error' event/.test(out) && !/runner error/.test(out), out.split('\n').filter(l => /ENOENT|Unhandled|runner error/.test(l)).slice(0, 2).join(' | '));
check('it said the folder was gone and rebuilt it', /the run's folder .* is gone/.test(out) && /the folder and coral are back/.test(out));
// The retry passes only if the folder is back with coral in it (the fixture exits 1 otherwise), so FLAKY below covers it.
check('the suite is reported FLAKY (failed, then passed on the retry)', /^FLAKY\s+runner_folder_loss_browser/m.test(out), (out.match(/^(PASS|FAIL|FLAKY|TIMEOUT)\s+runner_folder_loss.*$/m) || [''])[0]);
check('the run ended normally (exit 0 with --allow-flaky)', code === 0, `exit ${code}`);
const m = /results: (\S+)/.exec(out);
let results = null;
try { results = m ? JSON.parse(fs.readFileSync(m[1], 'utf8')) : null; } catch { /* checked below */ }
check('results.json was written, with the suite in it', !!results && results.some(r => /runner_folder_loss/.test(r.test)), m ? m[1] : 'no results line');
if (m) { try { fs.rmSync(path.dirname(m[1]), { recursive: true, force: true }); } catch { /* best effort */ } }
if (failed) console.log(`\n--- runner output (last 60 lines) ---\n${out.trim().split('\n').slice(-60).join('\n')}`);
console.log(`\n${failed ? failed + ' FAILED' : 'all passed'}`);
process.exit(failed ? 1 : 0);
