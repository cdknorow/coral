// Runs every command of `npm run test:cloud` on its own, so ONE run lists every test that fails on Windows (the chain stops at the first).
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
const scripts = JSON.parse(fs.readFileSync('package.json', 'utf8')).scripts;
const cmds = scripts['test:cloud'].split('&&').map(s => s.trim()).filter(Boolean).map(s => s === 'npm run lint:undef' ? scripts['lint:undef'] : s);
const failed = [];
for (const c of cmds) {
  const r = spawnSync(c, { shell: true, encoding: 'utf8', timeout: 180000 });
  if (r.status === 0) { console.log('PASS', c.slice(0, 60)); continue; }
  failed.push(c);
  console.log('FAIL', c.slice(0, 90));
  const text = ((r.stderr || '') + (r.stdout || '')).split('\n').filter(l => l.trim()).slice(-14).join('\n');
  console.log(text.replace(/^/gm, '    | '));
}
console.log(`\n${cmds.length - failed.length}/${cmds.length} passed; failing: ${failed.length}`);
for (const f of failed) console.log('  FAILING:', f);
