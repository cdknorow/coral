// The CDP runner removes old run folders (#6967): the newest KEEP and any from the last HOURS hours stay, the rest go; never this run's own folder, one a live runner's file names, a symlink, a file, or a name
// the runner did not make. Fake folders in a throwaway temp folder, with set modification times.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import assert from 'node:assert/strict';
import { pruneOldRunFolders, liveRunFolders, KEEP_RUNS, KEEP_HOURS } from './helpers/run_folders.mjs';

const root = fs.mkdtempSync(path.join(os.tmpdir(), 'rf-test-'));
try {
  const NOW = Date.UTC(2026, 9, 24, 12, 0, 0), H = 3600 * 1000;
  const make = (name, ageH, file) => { const p = path.join(root, name); fs.mkdirSync(p); fs.writeFileSync(path.join(p, file || 'results.json'), '{}'); const t = new Date(NOW - ageH * H); fs.utimesSync(p, t, t); return p; };
  // 14 old folders (30 h to 43 h), two recent (1 h, 5 h), the run's own, one a live runner uses, plus things that must never be touched
  const old = Array.from({ length: 14 }, (_, i) => make(`coral-cdp-old${String(i).padStart(3, '0')}`, 30 + i));
  const recent = [make('coral-cdp-new001', 1), make('coral-cdp-new002', 5)];
  const own = make('coral-cdp-ownRun', 200), liveF = make('coral-cdp-liveRn', 300);
  const other = make('some-other-dir', 400), wrongLen = make('coral-cdp-toolongname', 400);
  fs.writeFileSync(path.join(root, 'coral-cdp-file01'), 'a file with a runner-folder name');
  const target = path.join(root, 'target-dir'); fs.mkdirSync(target); fs.symlinkSync(target, path.join(root, 'coral-cdp-linkxx'));
  fs.writeFileSync(path.join(root, 'coral-cdp-runner-4242.json'), JSON.stringify([{ tmp: liveF }]));
  fs.writeFileSync(path.join(root, 'coral-cdp-runner-4343.json'), JSON.stringify([{ tmp: own }])); // a dead runner's file names nothing alive
  const live = liveRunFolders(root, pid => pid === 4242);
  assert.deepEqual([...live], [path.resolve(liveF)], 'only the live runner\'s folder is protected');

  const removed = pruneOldRunFolders(root, { own, live, keep: KEEP_RUNS, hours: KEEP_HOURS, now: NOW });
  const names = removed.map(p => path.basename(p)).sort();
  // newest KEEP_RUNS=10 stay: the two recent ones and the 8 newest old ones (30..37 h); the 6 oldest old ones (38..43 h) go
  assert.deepEqual(names, old.slice(8).map(p => path.basename(p)).sort(), `the six oldest beyond the newest ${KEEP_RUNS} went: ${names}`);
  for (const p of [...recent, ...old.slice(0, 8), own, liveF, other, wrongLen, target]) assert.ok(fs.existsSync(p), `${path.basename(p)} stays`);
  assert.ok(fs.existsSync(path.join(root, 'coral-cdp-file01')) && fs.lstatSync(path.join(root, 'coral-cdp-linkxx')).isSymbolicLink(), 'a file and a symlink with a runner-folder name stay');
  for (const p of old.slice(8)) assert.ok(!fs.existsSync(p), `${path.basename(p)} is gone`);

  // within the last 24 h nothing goes, however many there are
  const root2 = fs.mkdtempSync(path.join(os.tmpdir(), 'rf-test2-'));
  try {
    for (let i = 0; i < 15; i++) { const p = path.join(root2, `coral-cdp-day${String(i).padStart(3, '0')}`); fs.mkdirSync(p); const t = new Date(NOW - (i + 1) * H); fs.utimesSync(p, t, t); }
    assert.deepEqual(pruneOldRunFolders(root2, { keep: 10, hours: 24, now: NOW }), [], 'fifteen folders from the last 15 hours all stay');
    assert.equal(pruneOldRunFolders(root2, { keep: 3, hours: 6, now: NOW }).length, 10, 'with keep 3 and 6 h: the five under 6 hours old stay (they include the newest three); the other 10 go');
  } finally { fs.rmSync(root2, { recursive: true, force: true }); }
  console.log('run_folders: ok (the newest 10 and the last 24 h stay; own, live, symlink, file and other names are never touched)');
} finally { fs.rmSync(root, { recursive: true, force: true }); }
