// Every suite in this folder has exactly one runner (see suites.mjs), so none is silently skipped (#3405).
//   node suite_census.test.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { classifySuites, problems } from './suites.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));

// The real folder: no problems.
const c = classifySuites(here);
assert.deepEqual(problems(c), [], problems(c).join('\n'));
assert.ok(c.cdp.length > 0 && c.node.includes('suite_census.test.mjs'));

// A fake folder: each kind of problem is reported.
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'suite-census-'));
try {
  const write = (f, s = '') => fs.writeFileSync(path.join(tmp, f), s);
  write('package.json', JSON.stringify({ scripts: { 'test:cloud': 'npm run lint && node plain.test.js && node both_browser.test.js && node gone.test.mjs' } }));
  write('plain.test.js');
  write('named_browser.test.mjs');
  // Split so this file itself doesn't contain the phrase (it would then count as a CDP suite too).
  write('imports.test.js', `const CDP = require('chrome-remote-${'interface'}');`);
  write('both_browser.test.js');
  write('orphan.test.js', '// no runner');
  write('agent_task_cli.test.js');
  write('helper.js', '// not a suite');
  const f = classifySuites(tmp);
  assert.deepEqual(f.node, ['plain.test.js']);
  assert.deepEqual(f.cdp, ['imports.test.js', 'named_browser.test.mjs']);
  assert.equal(f.cdpByName, 1);
  assert.deepEqual(f.server, ['agent_task_cli.test.js']);
  assert.deepEqual(f.unclassified, ['orphan.test.js']);
  assert.deepEqual(f.conflicts, ['both_browser.test.js (cdp + node)']);
  assert.deepEqual(f.missing, ['gone.test.mjs']);
  assert.equal(problems(f).length, 3);
} finally {
  fs.rmSync(tmp, { recursive: true, force: true });
}

console.log(`PASS suite census: ${c.cdp.length} CDP, ${c.server.length} server, ${c.node.length} node; none unclassified`);
