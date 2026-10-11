// Guard against a class of bug found by the CDP preview suite: wrapping an identifier in Number() inside a template
// (`id="${Number(titleId)}"`, `data-tab-id="${Number(tab.id)}"`) turns every non-numeric id into "NaN". The preview pane's tab
// strip rendered every tab as data-tab-id="NaN", so switching, pinning and closing tabs silently did nothing.
// Number() is right only for values that are numeric by construction (database row ids); for strings use escapeAttr().
//   node no_nan_ids.test.js
const fs = require('fs');
const path = require('path');
const dir = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
const files = [];
(function walk(d) {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
        const p = path.join(d, e.name);
        if (e.isDirectory()) { if (e.name !== 'vendor') walk(p); } else if (p.endsWith('.js')) files.push(p);
    }
})(dir);
// An id (or tab/key-like data attribute) whose WHOLE value is a Number() call can only ever be a number.
// (database row ids such as data-msg-id / data-tag-id are numbers and may use Number(); DOM ids and tab ids are strings)
const bad = /(?:(?<![\w-])id|data-tab-id|data-session-id|data-key)\s*=\s*"\$\{Number\([^}]*\)\}(?:-[a-z-]+)?"/;
const found = [];
for (const f of files) {
    fs.readFileSync(f, 'utf8').split('\n').forEach((line, i) => {
        if (bad.test(line)) found.push(`${path.relative(dir, f)}:${i + 1}: ${line.trim().slice(0, 140)}`);
    });
}
if (found.length) {
    console.error('Number() around a string identifier in an id/data attribute (use escapeAttr):\n  ' + found.join('\n  '));
    process.exit(1);
}
console.log(`no_nan_ids.test.js: ok (${files.length} files)`);
