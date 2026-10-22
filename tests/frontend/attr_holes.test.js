// Every template-literal hole in an HTML attribute the UI builds as a string is escaped
// (escapeAttr/escapeHtml/Number), and no built markup has an inline on* handler (#3645).
// It replaces csp_shapes_check.test.js, which ran this lint next to the handler-shape check.
//   node attr_holes.test.js
const { spawnSync } = require('./helpers/child_process.js');
const path = require('path');
const assert = require('assert');
const root = path.resolve(__dirname, '../..');
const r = spawnSync('node', ['tools/attr_holes.mjs'], { cwd: root, encoding: 'utf8' });
assert.match(r.stdout, /(^|\n)0 unprotected holes\n$/, `unprotected template holes in attributes, or an inline handler (use escapeAttr in attributes and a data-on-* action for events):\n${r.stdout}${r.stderr}`);
(async () => {
    const { pathToFileURL } = require('url');
    const lint = await import(pathToFileURL(path.join(root, 'tools/attr_holes.mjs')).href);
    const probs = src => [...lint.scanAttributes(src)].flatMap(a => lint.checkAttribute(src, a)).map(p => p.attr);
    // The lint still sees each kind of mistake.
    assert.deepStrictEqual(probs('const h = `<b onclick="go()">x</b>`;'), ['onclick'], 'an inline handler');
    assert.deepStrictEqual(probs('const h = `<b title="${name}">x</b>`;'), ['title'], 'an unescaped hole');
    assert.deepStrictEqual(probs('const h = `<b title="${escapeAttr(name)}" data-on-click="go">x</b>`;'), [], 'an escaped hole');
    console.log('attr_holes.test.js: ok (self-test 3/3)');
})();
