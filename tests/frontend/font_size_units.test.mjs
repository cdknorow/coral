// Text is sized in rem, em or a token, never in px (#4344, from the #4276 audit): a font-size in px does not follow the browser's text size, so on a phone with
// a larger text size the app's own text stayed small while the rest grew. The stylesheets, the templates and the style strings the scripts build carry no
// `font-size: Npx` (or `font: Npx ...`); xterm and the canvas take their size from the terminal font setting in script, not from CSS, and are not covered.
// To keep a px size on purpose (say, a glyph that must stay a fixed number of pixels), name the file in KEEP below with the reason.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const fe = path.resolve(here, '../../coral-go/pkg/frontend');
const KEEP = {}; // 'static/css/x.css': 'why'
const walk = (d, ok) => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => {
    const p = path.join(d, e.name);
    return e.isDirectory() ? (e.name === 'vendor' ? [] : walk(p, ok)) : ok(p) ? [p] : [];
});
const auth = path.resolve(here, '../../coral-go/internal/auth');   // the sign-in and create-password page is a Go template with its own inline CSS (#4360)
const files = [...walk(path.join(fe, 'static'), p => /\.(css|js)$/.test(p)), ...walk(path.join(fe, 'templates'), p => p.endsWith('.html')), ...fs.readdirSync(auth).filter(n => n.endsWith('.go') && !n.endsWith('_test.go')).map(n => path.join(auth, n))];
const bad = [];
for (const f of files) {
    const rel = path.relative(path.resolve(here, '../..'), f).replace(/^coral-go\/pkg\/frontend\//, '');
    if (KEEP[rel]) continue;
    fs.readFileSync(f, 'utf8').split('\n').forEach((line, i) => {
        if (/font-size:\s*[0-9.]+px|font:\s*(?:[0-9]+\s+)?[0-9.]+px/.test(line) && !/xterm|fontSize/.test(line)) bad.push(`${rel}:${i + 1}: ${line.trim().slice(0, 110)}`);
    });
}
assert.deepEqual(bad, [], `text sized in px (use rem: px / 16):\n${bad.slice(0, 30).join('\n')}`);
console.log(`font_size_units: ok (${files.length} files, no font size in px)`);
