// Delegated actions (#3645, Security's requirement 1 for CSP stage 2): markup names an action in
// data-on-<event> and passes its arguments in data-* attributes, so whatever the dashboard's own JS
// puts in those attributes must be safe by construction. On every line of the static JS that
// writes a data-on- attribute:
//   - the data-on-* value (the action's name) is a constant: no ${...} in it;
//   - every ${...} inside an attribute value on that line is escapeAttr(...)/escAttr(...) or
//     Number(...) as a whole, so no value can close its quote and add an attribute (another
//     data-on-*, say).
// Text between tags is not an action channel and is left to the other escaping checks.
// The self-test at the end shows the lint catches each kind of mistake.
const fs = require('node:fs');
const path = require('node:path');

const STATIC = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) { if (!['vendor', '_src', 'node_modules'].includes(e.name)) walk(p, out); } else if (e.name.endsWith('.js')) out.push(p);
  }
  return out;
}

// The ${...} expressions in s, with balanced braces.
function interpolations(s) {
  const out = [];
  for (let i = s.indexOf('${'); i !== -1; i = s.indexOf('${', i + 1)) {
    let depth = 0, j = i + 1;
    for (; j < s.length; j++) {
      if (s[j] === '{') depth++;
      else if (s[j] === '}' && --depth === 0) break;
    }
    out.push({ start: i, end: j, expr: s.slice(i + 2, j).trim() });
  }
  return out;
}

// One call as the whole expression: name(...) with the closing paren the one that matches.
function wholeCall(expr, names) {
  const m = expr.match(/^([A-Za-z_$][\w$]*)\(/);
  if (!m || !names.includes(m[1]) || !expr.endsWith(')')) return false;
  let depth = 0;
  for (let i = m[1].length; i < expr.length; i++) {
    if (expr[i] === '(') depth++;
    else if (expr[i] === ')' && --depth === 0) return i === expr.length - 1;
  }
  return false;
}

const SAFE = ['escapeAttr', 'escAttr', 'Number'];

/** lintLine returns the problems on one source line ([] when it writes no data-on- attribute). */
function lintLine(line) {
  if (!/data-on-[a-z]+=/.test(line)) return [];
  const problems = [];
  // Attribute values on the line: name="value" with the value's span.
  for (const m of line.matchAll(/([A-Za-z_:][-\w:.]*)="([^"]*)"/g)) {
    const [, name, value] = m;
    const vs = m.index + name.length + 2, ve = vs + value.length;
    const inside = interpolations(line).filter(x => x.start >= vs && x.end < ve);
    if (name.startsWith('data-on-') && inside.length) problems.push(`${name}'s action name is built from \${${inside[0].expr}}`);
    for (const x of inside) if (!name.startsWith('data-on-') && !wholeCall(x.expr, SAFE)) problems.push(`${name}="\${${x.expr}}" is not escapeAttr(...), escAttr(...) or Number(...)`);
  }
  // An interpolation that opens or closes an attribute itself (data-x=${v}, unquoted).
  if (/[-\w]=\$\{/.test(line)) problems.push('an unquoted attribute value');
  return problems;
}

const problems = [];
let lines = 0;
for (const f of walk(STATIC)) {
  fs.readFileSync(f, 'utf8').split('\n').forEach((line, i) => {
    const p = lintLine(line);
    if (/data-on-[a-z]+=/.test(line)) lines++;
    for (const msg of p) problems.push(`${path.relative(STATIC, f)}:${i + 1}: ${msg}`);
  });
}

// The lint catches what it is for (and passes what is right).
const SELF = [
  ['<th data-on-click="cost-sort" data-table="${escapeAttr(table)}" data-id="${Number(t.id)}">', 0],
  ['<a data-on-click="x" data-name="${escAttr(f(a, b))}">${raw}</a>', 0],
  ['<th data-on-click="cost-sort" data-table="${table}">', 1],
  ['<th data-on-click="cost-sort" data-table="${escapeAttr(a)}${b}">', 1],
  ['<th data-on-click="cost-sort" data-table="${escapeAttr(a) + b}">', 1],
  ['<th data-on-click="${name}">', 1],
  ['<th data-on-click="cost-sort" data-table=${escapeAttr(a)}>', 1],
];
const selfFails = SELF.filter(([l, want]) => (lintLine(l).length > 0 ? 1 : 0) !== want).map(([l]) => l);

if (selfFails.length) {
  console.error('FAIL data_on_lint self-test: the lint misjudges\n  ' + selfFails.join('\n  '));
  process.exit(1);
}
if (problems.length) {
  console.error('FAIL data_on_lint: markup that names a delegated action must carry only escaped or numeric values (#3645)\n  ' + problems.join('\n  '));
  process.exit(1);
}
console.log(`PASS data_on_lint: ${lines} lines write data-on- attributes; every value on them is escapeAttr/escAttr/Number, every action name a constant (self-test ${SELF.length}/${SELF.length})`);
