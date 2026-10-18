// Every value the dashboard puts into HTML it builds as a string, accounted for (#4853, Security's sweep of how labels are rendered).
// The dashboard builds much of its markup as template literals and "+" chains assigned to innerHTML, so the one rule that keeps
// an agent's or a user's text from becoming markup is that every such value goes through escapeHtml / escapeAttr (or encodeURIComponent,
// Number, ...), or is Coral's own markup. An AST scan of every dashboard module lists each interpolation into an HTML-looking string
// that is NOT visibly one of those safe forms (a call to an escape helper, a number, a literal, an arithmetic or comparison, a length):
// those are the ones a person must have read. html_interpolations.golden.txt is the reviewed list ("count file expression"); a NEW entry
// or a larger count fails here until the new value is escaped, or (when it is built from constants, or from pieces escaped where they were
// made) the golden is regenerated and the diff read:
//   UPDATE_GOLDEN=1 node html_interpolation_census.test.js
// It reads each interpolation where it is written: a variable that is already in the list and is later given an unescaped source is not seen
// (that is what the dynamic half is for). An entry that no longer exists fails too, so the list stays what the code is. label_injection_browser.test.js is the dynamic half: it plants
// markup through the API and tests every sink at run time.
//   node html_interpolation_census.test.js
'use strict';
const fs = require('node:fs');
const path = require('node:path');
const acorn = require('acorn');

const STATIC = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
const GOLDEN = path.join(__dirname, 'html_interpolations.golden.txt');
const HTMLISH = /<[a-zA-Z]/;
const SAFE_CALLS = new Set(['escapeHtml', 'escapeAttr', 'escHtml', 'encodeURIComponent', 'String', 'Number', 'parseInt', 'parseFloat', 'JSON.stringify', 'Boolean', 'esc', 'CSS.escape']);
const SAFE_METHODS = new Set(['toFixed', 'toLocaleString', 'padStart', 'toISOString']);

function walkFiles(dir, out = []) {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const p = path.join(dir, e.name);
        if (e.isDirectory()) { if (!['vendor', '_src', 'node_modules'].includes(e.name)) walkFiles(p, out); } else if (e.name.endsWith('.js')) out.push(p);
    }
    return out.sort();
}
function visit(node, fn) {
    if (!node || typeof node.type !== 'string') return;
    fn(node);
    for (const k of Object.keys(node)) {
        const v = node[k];
        if (Array.isArray(v)) v.forEach(c => visit(c, fn));
        else if (v && typeof v.type === 'string') visit(v, fn);
    }
}
const calleeName = n => n.type === 'Identifier' ? n.name : (n.type === 'MemberExpression' && !n.computed ? (n.object.type === 'Identifier' ? n.object.name + '.' : '') + n.property.name : '');
function isSafe(n) {
    switch (n.type) {
        case 'Literal': return true;
        case 'TemplateLiteral': return n.expressions.every(isSafe);
        case 'BinaryExpression': return ['-', '*', '/', '%', '**', '<', '>', '<=', '>=', '==', '===', '!=', '!==', '&', '|'].includes(n.operator) || (n.operator === '+' && isSafe(n.left) && isSafe(n.right));
        case 'UnaryExpression': return true;
        case 'LogicalExpression': return isSafe(n.left) && isSafe(n.right);
        case 'ConditionalExpression': return isSafe(n.consequent) && isSafe(n.alternate);
        case 'MemberExpression': return !n.computed && ['length', 'size', 'count'].includes(n.property.name);
        case 'CallExpression': {
            const c = calleeName(n.callee);
            if (SAFE_CALLS.has(c) || c.startsWith('Math.')) return true;
            return n.callee.type === 'MemberExpression' && SAFE_METHODS.has(n.callee.property.name);
        }
        default: return false;
    }
}
const text = (n, code) => code.slice(n.start, n.end).replace(/\s+/g, ' ').trim().slice(0, 110);

// The unclassified interpolations of one source file: [{ line, expr }].
function scan(code) {
    const hits = [];
    const ast = acorn.parse(code, { ecmaVersion: 'latest', sourceType: 'module', locations: true, allowHashBang: true });
    const concatOperands = (n, out) => { if (n.type === 'BinaryExpression' && n.operator === '+') { concatOperands(n.left, out); concatOperands(n.right, out); } else out.push(n); return out; };
    const seenChains = new Set();
    visit(ast, n => {
        if (n.type === 'TemplateLiteral' && n.expressions.length && HTMLISH.test(n.quasis.map(q => q.value.raw).join(''))) {
            for (const e of n.expressions) if (!isSafe(e)) hits.push({ line: e.loc.start.line, expr: text(e, code) });
        }
        // "<b>" + x + "</b>": the operands of a + chain with an HTML-looking string in it (the chain is read once, from its top)
        if (n.type === 'BinaryExpression' && n.operator === '+' && !seenChains.has(n)) {
            const ops = concatOperands(n, []);
            visit(n, m => { if (m.type === 'BinaryExpression' && m.operator === '+') seenChains.add(m); });
            if (ops.some(o => o.type === 'Literal' && typeof o.value === 'string' && HTMLISH.test(o.value))) {
                for (const o of ops) if (!isSafe(o)) hits.push({ line: o.loc.start.line, expr: text(o, code) });
            }
        }
    });
    return hits;
}

function census() {
    const counts = new Map();
    for (const f of walkFiles(STATIC)) {
        const rel = path.relative(STATIC, f);
        for (const h of scan(fs.readFileSync(f, 'utf8'))) {
            const key = rel + '\t' + h.expr;
            counts.set(key, (counts.get(key) || 0) + 1);
        }
    }
    return counts;
}
const render = counts => [...counts.entries()].sort((a, b) => a[0] < b[0] ? -1 : 1).map(([k, n]) => n + '\t' + k).join('\n') + '\n';

// The scan has to see what it claims to: each spelling of an unescaped value is caught, each safe form is not.
const fixture = scan(`
const a = \`<b>\${x.title}</b>\`;
const b = '<i>' + y.name + '</i>';
const c = \`<b>\${escapeHtml(x.title)} \${Number(n)} \${list.length} \${a > b ? 'x' : 'y'} \${i * 2}</b>\`;
const d = '<i>' + escapeAttr(y.name) + '</i>';
const e = \`plain \${x.title}\`;
const f = \`<a href="\${u}" title='\${t}'>\${z.label}</a>\`;
`);
const caught = fixture.map(h => h.expr).sort();
const want = ['t', 'u', 'x.title', 'y.name', 'z.label'].sort();
if (JSON.stringify(caught) !== JSON.stringify(want)) {
    console.error('[FAIL] the scan caught ' + JSON.stringify(caught) + ', wanted ' + JSON.stringify(want));
    process.exit(1);
}
console.log('[PASS] the scan catches an unescaped template value and "+" operand, in text and in attributes, and passes the safe forms');

const counts = census();
if (process.env.UPDATE_GOLDEN === '1') {
    fs.writeFileSync(GOLDEN, render(counts));
    console.log(`wrote ${GOLDEN}: ${counts.size} distinct interpolations`);
    process.exit(0);
}
const golden = new Map();
for (const line of fs.readFileSync(GOLDEN, 'utf8').split('\n')) {
    if (!line) continue;
    const i = line.indexOf('\t');
    golden.set(line.slice(i + 1), Number(line.slice(0, i)));
}
const added = [], stale = [];
for (const [k, n] of counts) { if (!golden.has(k)) added.push(`${k.replace('\t', ': ')} (x${n})`); else if (n > golden.get(k)) added.push(`${k.replace('\t', ': ')} (now x${n}, reviewed x${golden.get(k)})`); }
for (const [k, n] of golden) { if (!counts.has(k) || counts.get(k) < n) stale.push(k.replace('\t', ': ')); }
let ok = true;
if (added.length) {
    ok = false;
    console.error(`[FAIL] ${added.length} value(s) go into HTML with no escape: escape them with escapeHtml / escapeAttr, or, if each is built from constants or was escaped where it was made, run UPDATE_GOLDEN=1 node html_interpolation_census.test.js and read the diff:\n  ` + added.slice(0, 30).join('\n  '));
}
if (stale.length) {
    ok = false;
    console.error(`[FAIL] ${stale.length} reviewed entr(ies) are gone from the code: run UPDATE_GOLDEN=1 node html_interpolation_census.test.js so the list is what the code is:\n  ` + stale.slice(0, 30).join('\n  '));
}
if (ok) console.log(`[PASS] every unescaped interpolation into HTML is in the reviewed list (${counts.size} entries)`);
process.exit(ok ? 0 : 1);
