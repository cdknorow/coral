// The shared Inbox builds no HTML from machine text, runs no code from data, and imports only its own folder's modules (#5754, Security's conditions 6 and 7 on #5750). It is shared with Coral Hub, which loads
// it at its pin, so the rules are checked on the source rather than on one page: an AST scan of inbox_panel.js and safe_markdown.js for
//   * innerHTML, outerHTML, insertAdjacentHTML, document.write / writeln, DOMParser, createContextualFragment, Range.createContextualFragment (any HTML-string sink);
//   * setAttribute of an on* handler, style, srcdoc, or of href / src with anything but a safePath(...) call (a same-origin path) as the value;
//   * eval, new Function, setTimeout / setInterval with a string, import() of anything (dynamic import), importScripts;
//   * an import from anywhere but a './name.js' file next to it (no URL, no '..', no bare package).
// Titles, names, counts, errors and reasons reach the page through textContent; the task and message text through safe_markdown.js (the vendored DOM-built renderer).
//   node inbox_no_html_census.test.js
const fs = require('node:fs');
const path = require('node:path');
const acorn = require('acorn');

const STATIC = path.resolve(__dirname, '../../coral-go/pkg/frontend/static');
const FILES = ['inbox_panel.js', 'safe_markdown.js'];
const SINK_PROPS = new Set(['innerHTML', 'outerHTML', 'insertAdjacentHTML', 'createContextualFragment']);

function visit(node, fn, parent) {
    if (!node || typeof node.type !== 'string') return;
    fn(node, parent);
    for (const k of Object.keys(node)) {
        const v = node[k];
        if (Array.isArray(v)) v.forEach(c => visit(c, fn, node));
        else if (v && typeof v.type === 'string') visit(v, fn, node);
    }
}
const isCall = (n, name) => n && n.type === 'CallExpression' && n.callee.type === 'Identifier' && n.callee.name === name;

/** The faults of one source text, as strings. */
function scan(file, src) {
    const faults = [];
    const ast = acorn.parse(src, { ecmaVersion: 'latest', sourceType: 'module', locations: true });
    const at = n => `${file}:${n.loc.start.line}`;
    visit(ast, (n) => {
        if (n.type === 'MemberExpression') {
            const prop = n.computed ? (n.property.type === 'Literal' ? String(n.property.value) : null) : n.property.name;
            if (prop && SINK_PROPS.has(prop)) faults.push(`${at(n)}: ${prop} is an HTML-string sink`);
            if (n.computed && !prop && n.property.type !== 'Literal' && n.object.type === 'Identifier' && /^(el|e|node|host|root)$/.test(n.object.name)) { /* a computed property on a node: reviewed by hand */ }
            if (prop && /^(write|writeln)$/.test(prop) && n.object.type === 'Identifier' && n.object.name === 'document') faults.push(`${at(n)}: document.${prop}`);
        }
        if (n.type === 'Identifier' && (n.name === 'DOMParser' || n.name === 'importScripts')) faults.push(`${at(n)}: ${n.name}`);
        if (n.type === 'ImportExpression') faults.push(`${at(n)}: a dynamic import()`);
        if (isCall(n, 'eval')) faults.push(`${at(n)}: eval`);
        if ((n.type === 'NewExpression' || n.type === 'CallExpression') && n.callee.type === 'Identifier' && n.callee.name === 'Function') faults.push(`${at(n)}: Function()`);
        if (n.type === 'CallExpression' && n.callee.type === 'Identifier' && /^(setTimeout|setInterval)$/.test(n.callee.name) && n.arguments[0] && n.arguments[0].type !== 'ArrowFunctionExpression' && n.arguments[0].type !== 'FunctionExpression' && n.arguments[0].type !== 'Identifier') faults.push(`${at(n)}: ${n.callee.name} with something that is not a function`);
        if (n.type === 'CallExpression' && n.callee.type === 'MemberExpression' && !n.callee.computed && n.callee.property.name === 'setAttribute') {
            const [name, value] = n.arguments;
            if (!name || name.type !== 'Literal') faults.push(`${at(n)}: setAttribute with a name that is not a literal`);
            else {
                const a = String(name.value).toLowerCase();
                if (/^on/.test(a) || a === 'style' || a === 'srcdoc') faults.push(`${at(n)}: setAttribute('${a}')`);
                if ((a === 'href' || a === 'src' || a === 'xlink:href' || a === 'action' || a === 'formaction') && !isCall(value, 'safePath')) faults.push(`${at(n)}: setAttribute('${a}') with a value that is not safePath(...)`);
            }
        }
        if (n.type === 'AssignmentExpression' && n.left.type === 'MemberExpression' && !n.left.computed && /^(href|src|srcdoc|action|formAction)$/.test(n.left.property.name) && !isCall(n.right, 'safePath')) faults.push(`${at(n)}: .${n.left.property.name} assigned a value that is not safePath(...)`);
        if (n.type === 'ImportDeclaration' && !/^\.\/[A-Za-z0-9_-]+\.js$/.test(String(n.source.value))) faults.push(`${at(n)}: imports ${JSON.stringify(n.source.value)}: only './name.js' files next to it`);
        if (n.type === 'ExportAllDeclaration' || (n.type === 'ExportNamedDeclaration' && n.source && !/^\.\/[A-Za-z0-9_-]+\.js$/.test(String(n.source.value)))) faults.push(`${at(n)}: re-exports from outside the folder`);
    });
    return faults;
}

// the scan can fail: each of these sources must be flagged (a negative control per rule)
const BAD = {
    'innerHTML': 'e.innerHTML = x;', 'outerHTML': 'e.outerHTML = x;', 'insertAdjacentHTML': "e.insertAdjacentHTML('beforeend', x);", 'document.write': 'document.write(x);',
    'DOMParser': 'new DOMParser().parseFromString(x, "text/html");', 'createContextualFragment': 'r.createContextualFragment(x);', 'on* attribute': "e.setAttribute('onclick', x);",
    'style attribute': "e.setAttribute('style', x);", 'srcdoc': "e.setAttribute('srcdoc', x);", 'href from text': "e.setAttribute('href', t.url);", 'src from text': "e.setAttribute('src', t.url);",
    'href property': 'a.href = t.url;', 'eval': 'eval(x);', 'new Function': 'new Function(x)();', 'dynamic import': 'import(url);', 'a string timer': "setTimeout('x()', 1);",
    'an import from a URL': "import { a } from 'https://x.example/a.js';", 'an import with ..': "import { a } from '../a.js';", 'a bare import': "import { a } from 'lodash';",
};
const GOOD = "import { a } from './a.js'; const e = document.createElement('div'); e.textContent = x; e.setAttribute('aria-label', y); e.setAttribute('href', safePath(h)); setTimeout(() => 1, 1);";

let failed = 0;
for (const [name, src] of Object.entries(BAD)) if (!scan('bad.js', src).length) { console.error(`FAIL the scan does not flag ${name}`); failed++; }
const g = scan('good.js', GOOD);
if (g.length) { console.error('FAIL the scan flags safe code: ' + g.join('; ')); failed++; }
for (const f of FILES) {
    const faults = scan(f, fs.readFileSync(path.join(STATIC, f), 'utf8'));
    for (const x of faults) { console.error('FAIL ' + x); failed++; }
}
if (failed) process.exit(1);
console.log(`inbox_no_html_census: ok (${FILES.join(', ')}; ${Object.keys(BAD).length} negative controls)`);
