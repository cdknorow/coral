// Every frontend module that calls a shared sink helper must import (or define) it. The real browser
// throws a ReferenceError at render time otherwise, and jsdom suites that stub the module never see it
// (this caught escapeAttr missing in changed_files.js, scheduler.js and theme_config.js).
const fs = require('node:fs');
const path = require('node:path');

const STATIC = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
const HELPERS = ['escapeAttr', 'escapeHtml', 'jsStr'];

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (!['vendor', '_src', 'node_modules'].includes(e.name)) walk(p, out); } else if (e.name.endsWith('.js')) out.push(p);
  }
  return out;
}

const problems = [];
for (const f of walk(STATIC)) {
  const src = fs.readFileSync(f, 'utf8');
  for (const h of HELPERS) {
    if (!new RegExp(`\\b${h}\\(`).test(src)) continue;
    const defined = new RegExp(`function\\s+${h}\\b|(?:const|let|var)\\s+${h}\\b`).test(src);
    const imported = new RegExp(`import\\s*\\{[^}]*\\b${h}\\b[^}]*\\}\\s*from`, 's').test(src);
    if (!defined && !imported) problems.push(`${path.relative(STATIC, f)} uses ${h}() without importing it`);
  }
}
if (problems.length) { console.error(problems.join('\n')); process.exit(1); }
console.log('import_hygiene.test.js: ok');
