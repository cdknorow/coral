// (hover: none) must never hide anything (#6137): a desktop browser with no pointing device (GitHub's headless Chrome) reports no hover and no pointer, so a rule that hides a control
// for "hover: none" hid it on a 1280 px desktop. A touch screen is (pointer: coarse); a rule that only reveals or enlarges under (hover: none) is harmless and stays.
// Scans every dashboard stylesheet: inside a media query with a (hover: none) alternative that does not also need (pointer: coarse), a declaration that hides (display: none,
// visibility: hidden, opacity: 0, pointer-events: none) fails. ALLOW is empty and reviewed.
//   node hover_none_lint.test.js
const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert/strict');

const CSS = path.resolve(__dirname, '../../coral-go/pkg/frontend/static/css');
const ALLOW = new Set(); // "file: selector { declaration }" lines, reviewed
const HIDES = /(?:^|;)\s*(display\s*:\s*none|visibility\s*:\s*hidden|opacity\s*:\s*0(?![.\d])|pointer-events\s*:\s*none)/g;

// A query's alternatives (split on commas): one that names hover: none and not a coarse pointer matches a desktop with no pointer.
function hoverNoneAlone(query) {
    return query.split(',').some(alt => /\(\s*hover\s*:\s*none\s*\)/.test(alt) && !/pointer\s*:\s*coarse/.test(alt));
}

// Every declaration block with the at-rule preludes around it.
function blocks(text) {
    const src = text.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
    const out = [], stack = [];
    let start = 0;
    for (let i = 0; i < src.length; i++) {
        const c = src[i];
        if (c === '{') { stack.push({ prelude: src.slice(start, i).trim(), bodyStart: i + 1, nested: false }); if (stack.length > 1) stack[stack.length - 2].nested = true; start = i + 1; }
        else if (c === '}') { const b = stack.pop(); if (b && !b.nested) out.push({ selector: b.prelude, body: src.slice(b.bodyStart, i), at: stack.map(s => s.prelude) }); start = i + 1; }
        else if (c === ';' && !stack.length) start = i + 1;
    }
    return out;
}

function scan(file, text) {
    const bad = [];
    for (const b of blocks(text)) {
        if (!b.at.some(p => /^@media/.test(p) && hoverNoneAlone(p))) continue;
        for (const m of b.body.matchAll(HIDES)) {
            const line = `${file}: ${b.selector.replace(/\s+/g, ' ')} { ${m[1].replace(/\s+/g, ' ')} }`;
            if (!ALLOW.has(line)) bad.push(line);
        }
    }
    return bad;
}

// The scan sees what it should: a hiding rule fails, a revealing one and a coarse-pointer one do not.
assert.equal(scan('x.css', '@media (hover: none) { .a { display: none; } }').length, 1);
assert.equal(scan('x.css', '@media (hover: none), (max-width: 767px) { .a { display: none !important; } }').length, 1);
assert.equal(scan('x.css', '@media (min-width: 1px) { @media (hover: none) { .a { opacity: 0; } } }').length, 1);
assert.equal(scan('x.css', '@media (hover: none) { .a { opacity: 1; } .b { opacity: 0.6; } }').length, 0);
assert.equal(scan('x.css', '@media (hover: none) and (pointer: coarse) { .a { display: none; } }').length, 0);
assert.equal(scan('x.css', '@media (pointer: coarse), (max-width: 767px) { .a { display: none; } }').length, 0);

const files = fs.readdirSync(CSS).filter(f => f.endsWith('.css'));
assert.ok(files.length > 10, 'the stylesheets were found: ' + files.length);
const bad = files.flatMap(f => scan(f, fs.readFileSync(path.join(CSS, f), 'utf8')));
assert.deepEqual(bad, [], 'a rule under (hover: none) hides something (use (pointer: coarse)):\n  ' + bad.join('\n  '));
console.log(`PASS hover_none_lint: ${files.length} stylesheets, nothing is hidden under (hover: none)`);
