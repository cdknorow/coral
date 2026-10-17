// Decorative icons are hidden from assistive technology (#4360, from the #4356 audit): a `.material-icons` glyph is its ligature NAME as text ("smart_toy",
// "arrow_forward"), so a screen reader reads "smart_toy Launch agent" unless the span is aria-hidden. Every icon span written in a template or in a script's
// markup string carries aria-hidden (or an aria-label of its own); an icon-only button, link or summary is named by an aria-label, never by the icon's text.
// Icons a script creates with createElement are covered by icon_names_browser.test.js, which reads the live DOM.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const fe = path.resolve(here, '../../coral-go/pkg/frontend');
const walk = (d, ok) => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => {
    const p = path.join(d, e.name);
    return e.isDirectory() ? (e.name === 'vendor' ? [] : walk(p, ok)) : ok(p) ? [p] : [];
});
const files = [...walk(path.join(fe, 'static'), p => p.endsWith('.js')), ...walk(path.join(fe, 'templates'), p => p.endsWith('.html'))];
const loud = [], unnamed = [];
const ICON_TAG = /<(span|i)( [^<>]*?material-icons[^<>]*?)\/?>/g;
const ICON_ONLY = /<(button|a|summary)([^<>]*)>\s*<(span|i)[^<>]*material-icons[^<>]*>[^<]*<\/\3>\s*<\/\1>/gs;
for (const f of files) {
    const rel = path.relative(fe, f);
    const text = fs.readFileSync(f, 'utf8');
    for (const m of text.matchAll(ICON_TAG)) if (!/aria-hidden|aria-label/.test(m[2])) loud.push(`${rel}:${text.slice(0, m.index).split('\n').length}: ${m[0].slice(0, 90)}`);
    for (const m of text.matchAll(ICON_ONLY)) if (!/aria-label(ledby)?=/.test(m[2])) unnamed.push(`${rel}:${text.slice(0, m.index).split('\n').length}: <${m[1]}${m[2].slice(0, 70)}>`);
}
assert.deepEqual(loud, [], `an icon span without aria-hidden (a screen reader reads its ligature name as a word):\n${loud.slice(0, 20).join('\n')}`);
assert.deepEqual(unnamed, [], `an icon-only control without an aria-label:\n${unnamed.slice(0, 20).join('\n')}`);
console.log(`icon_aria_hidden: ok (${files.length} files: every icon span is aria-hidden, every icon-only control has an aria-label)`);
