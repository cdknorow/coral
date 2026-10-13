// The DOM's node-list methods (append, prepend, before, after, replaceWith, replaceChildren) turn a
// null or undefined argument into the TEXT "null"/"undefined" rather than skipping it: the Cloud
// Hub's Servers page showed a literal 'null' that way (#3099). Fail on any call that can pass one: a
// literal null/undefined argument, or a ternary/|| branch that yields one. Build the list and
// .filter(Boolean) it, or use cloud.js's h(), which skips null children (#3117).
//   node dom_null_args.test.js
const fs = require('node:fs');
const path = require('node:path');

const STATIC = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
const METHODS = ['append', 'prepend', 'before', 'after', 'replaceWith', 'replaceChildren'];

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (!['vendor', '_src', 'node_modules'].includes(e.name)) walk(p, out); } else if (e.name.endsWith('.js')) out.push(p);
  }
  return out;
}

// Blank out comments and the insides of string/template literals (keeping offsets and newlines), so
// parentheses and words inside them are not mistaken for code.
function mask(src) {
  let out = '', i = 0;
  const keep = c => (c === '\n' ? '\n' : ' ');
  while (i < src.length) {
    const c = src[i], d = src[i + 1];
    if (c === '/' && d === '/') { while (i < src.length && src[i] !== '\n') { out += ' '; i++; } continue; }
    if (c === '/' && d === '*') { const end = src.indexOf('*/', i + 2); const stop = end < 0 ? src.length : end + 2; for (; i < stop; i++) out += keep(src[i]); continue; }
    if (c === "'" || c === '"' || c === '`') {
      out += c; i++;
      while (i < src.length && src[i] !== c) {
        if (src[i] === '\\') { out += '  '; i += 2; continue; }
        if (c === '`' && src[i] === '$' && src[i + 1] === '{') {
          // Keep ${...} expressions: they are code.
          let depth = 0;
          do { if (src[i] === '{') depth++; else if (src[i] === '}') depth--; out += src[i]; i++; } while (i < src.length && depth > 0);
          continue;
        }
        out += keep(src[i]); i++;
      }
      out += src[i] || ''; i++;
      continue;
    }
    out += c; i++;
  }
  return out;
}

// The argument text of the call whose '(' is at `open` (balanced parentheses).
function argsAt(code, open) {
  let depth = 0;
  for (let i = open; i < code.length; i++) {
    if (code[i] === '(') depth++;
    else if (code[i] === ')' && --depth === 0) return code.slice(open + 1, i);
  }
  return '';
}

// Split top-level arguments on commas.
function splitArgs(args) {
  const out = []; let depth = 0, start = 0;
  for (let i = 0; i < args.length; i++) {
    const c = args[i];
    if ('([{'.includes(c)) depth++;
    else if (')]}'.includes(c)) depth--;
    else if (c === ',' && depth === 0) { out.push(args.slice(start, i)); start = i + 1; }
  }
  out.push(args.slice(start));
  return out.map(a => a.trim()).filter(Boolean);
}

const NULLISH = '(?:null|undefined)';
const bad = arg => new RegExp(`^${NULLISH}$|[?:]\\s*${NULLISH}\\s*$|\\?\\s*${NULLISH}\\s*:|\\|\\|\\s*${NULLISH}\\s*$`).test(arg) && !/\.filter\(Boolean\)\s*$/.test(arg);

const problems = [];
const callRe = new RegExp(`\\.(${METHODS.join('|')})\\s*\\(`, 'g');
for (const f of walk(STATIC)) {
  const code = mask(fs.readFileSync(f, 'utf8'));
  for (const m of code.matchAll(callRe)) {
    const open = m.index + m[0].length - 1;
    for (const arg of splitArgs(argsAt(code, open))) {
      if (arg.startsWith('...')) continue; // a spread list: checked where it is built
      if (bad(arg)) {
        const line = code.slice(0, m.index).split('\n').length;
        problems.push(`${path.relative(STATIC, f)}:${line} .${m[1]}() can receive ${arg.replace(/\s+/g, ' ').slice(0, 80)} (inserted as the text "null"/"undefined")`);
      }
    }
  }
}

// Self-check: the detector catches the #3099 shape and leaves safe ones alone.
const probe = src => { const code = mask(src); const hits = []; for (const m of code.matchAll(callRe)) for (const a of splitArgs(argsAt(code, m.index + m[0].length - 1))) if (!a.startsWith('...') && bad(a)) hits.push(a); return hits.length; };
const selfCheck = [
  [`host.replaceChildren(intro, err ? errNode : null, list)`, 1],
  [`el.append(x ? null : y)`, 1],
  [`el.append(null)`, 1],
  [`el.after(node || undefined)`, 1],
  [`el.append(a, b)`, 0],
  [`el.replaceChildren(...[a, err ? b : null].filter(Boolean))`, 0],
  [`el.append('x ? y : null')`, 0],
  [`// el.append(a ? b : null)`, 0],
  [`el.append(cond ? nodeA : nodeB)`, 0],
];
for (const [src, want] of selfCheck) if (probe(src) !== want) problems.push(`self-check: ${src} -> ${probe(src)} hit(s), want ${want}`);

if (problems.length) { console.error(problems.join('\n')); process.exit(1); }
console.log('dom_null_args.test.js: ok');
