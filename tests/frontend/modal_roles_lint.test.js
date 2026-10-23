// Every modal in the page templates is a dialog (#6188): a `.modal` element without role="dialog" never gets focus moved into it when it opens or back to its opener when it closes
// (dialog_focus.js attaches to `.modal[role="dialog"]`), and a screen reader does not know it is a dialog. The workflow editor was the one that lacked it.
//   node modal_roles_lint.test.js
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');
const T = path.resolve(__dirname, '../../coral-go/pkg/frontend/templates');
function walk(d, out = []) { for (const e of fs.readdirSync(d, { withFileTypes: true })) { const p = path.join(d, e.name); if (e.isDirectory()) walk(p, out); else if (/\.html$/.test(e.name)) out.push(p); } return out; }
const bad = [];
let n = 0;
for (const f of walk(T)) for (const m of fs.readFileSync(f, 'utf8').matchAll(/<div\b[^>]*\bclass="[^"]*\bmodal\b[^"]*"[^>]*>/g)) {
    if (/\bclass="[^"]*\bmodal-/.test(m[0]) && !/\bclass="(?:[^"]*\s)?modal(?:\s[^"]*)?"/.test(m[0])) continue; // modal-content and the like are not the modal
    n++;
    if (!/\brole="dialog"/.test(m[0]) || !/\baria-modal="true"/.test(m[0]) || !/\baria-label(?:ledby)?="/.test(m[0])) bad.push(`${path.relative(T, f)}: ${m[0].slice(0, 120)}`);
}
assert.ok(n >= 10, 'the modals were found: ' + n);
assert.deepEqual(bad, [], 'a modal without role="dialog", aria-modal="true" and a name:\n  ' + bad.join('\n  '));
console.log(`modal_roles_lint: ok (${n} modals)`);
