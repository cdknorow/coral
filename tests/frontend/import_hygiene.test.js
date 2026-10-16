// Every frontend module that calls a shared sink helper must import (or define) it. The real browser
// throws a ReferenceError at render time otherwise, and jsdom suites that stub the module never see it
// (this caught escapeAttr missing in changed_files.js, scheduler.js and theme_config.js).
const fs = require('node:fs');
const path = require('node:path');

const STATIC = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
const HELPERS = ['escapeAttr', 'escapeHtml'];

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

// Dynamic imports (#3116): Chrome caches a failed module fetch for the page's life, so every import()
// goes through importOnce (import_once.js), which retries under ?retry=N and shares one instance.
const strip = src => src.replace(/\/\*[\s\S]*?\*\//g, c => c.replace(/[^\n]/g, '')).replace(/(^|[^:'"`])\/\/.*$/gm, '$1');
const rel = f => path.relative(STATIC, f).split(path.sep).join('/');
const files = walk(STATIC);
const code = Object.fromEntries(files.map(f => [rel(f), strip(fs.readFileSync(f, 'utf8'))]));
for (const [name, src] of Object.entries(code)) {
  if (name === 'import_once.js') continue;
  for (const m of src.matchAll(/(?<![\w.$'"`])import\s*\(/g)) {
    const line = src.slice(0, m.index).split('\n').length;
    problems.push(`${name}:${line} calls import() directly; use importOnce() from ./import_once.js`);
  }
}
// The static module graph from app.js: these are loaded before the app runs, so they never fail later.
const staticImports = name => [...(code[name] || '').matchAll(/^\s*(?:import|export)\b[^;'"`]*?\bfrom\s*['"](\.\/[^'"]+)['"]|^\s*import\s*['"](\.\/[^'"]+)['"]/gm)]
  .map(m => path.posix.normalize(path.posix.join(path.posix.dirname(name), m[1] || m[2])));
const graph = new Set();
for (const queue = ['app.js']; queue.length;) { const n = queue.pop(); if (graph.has(n)) continue; graph.add(n); queue.push(...staticImports(n)); }
if (graph.size < 50) problems.push(`app.js static graph looks wrong (${graph.size} modules)`);
// Modules loaded only through importOnce (or lazy_panels' loaders).
const lazy = new Set();
for (const src of Object.values(code)) for (const m of src.matchAll(/\b(?:importOnce|panel)\(\s*(?:'[^']*',\s*)?'(\.\/[\w/]+\.js)'/g)) {
  const t = path.posix.normalize(m[1]); if (!graph.has(t)) lazy.add(t); // root-relative, like importOnce
}
if (!lazy.has('docs.js')) problems.push('lazy module detection looks wrong: ' + [...lazy]);
for (const t of lazy) {
  // A retried module re-imports its dependencies; a failed (cached) dependency would fail it again.
  for (const d of staticImports(t)) if (!graph.has(d)) problems.push(`${t} is loaded lazily but imports ${d}, which is not loaded at startup: its failure could not be retried`);
}
for (const [name, src] of Object.entries(code)) for (const d of staticImports(name)) {
  // A static import of a lazy module would be a second instance after importOnce retried it.
  if (lazy.has(d)) problems.push(`${name} statically imports ${d}, which is loaded through importOnce; use importOnce`);
}

if (problems.length) { console.error(problems.join('\n')); process.exit(1); }
console.log('import_hygiene.test.js: ok');
