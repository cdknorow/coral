// CI check: the committed handler-shape allowlist matches the UI sources.
//   node csp_shapes_check.test.js
const { spawnSync } = require('child_process');
const path = require('path');
const assert = require('assert');
const root = path.resolve(__dirname, '../..');
let r = spawnSync('node', ['tools/gen_handler_shapes.mjs', '--check'], { cwd: root, encoding: 'utf8' });
assert.strictEqual(r.status, 0, `csp_shapes.js is stale or a handler left the grammar:\n${r.stdout}${r.stderr}`);
r = spawnSync('node', ['tools/attr_holes.mjs'], { cwd: root, encoding: 'utf8' });
assert.match(r.stdout, /(^|\n)0 unprotected holes\n$/, `unprotected template holes in attributes (use jsStr/Number in on*=, escapeAttr in other attributes):\n${r.stdout}`);
// DYNAMIC_CALLEES in the generator must equal HANDLER_NAMES in utils.js.
(async () => {
    const { pathToFileURL } = require('url');
    const gen = require('fs').readFileSync(path.join(root, 'tools/gen_handler_shapes.mjs'), 'utf8');
    const utils = require('fs').readFileSync(path.join(root, 'coral-go/pkg/frontend/static/utils.js'), 'utf8');
    const names = s => [...s.matchAll(/'([A-Za-z_.]+)'/g)].map(m => m[1]).sort();
    const g = names(/DYNAMIC_CALLEES = \[([^\]]*)\]/.exec(gen)[1]);
    const u = names(/HANDLER_NAMES = \[([^\]]*)\]/.exec(utils)[1]);
    assert.deepStrictEqual(g, u, 'DYNAMIC_CALLEES (generator) and HANDLER_NAMES (utils.js) must match');
    void pathToFileURL;
    console.log('csp_shapes_check.test.js: ok');
})();
