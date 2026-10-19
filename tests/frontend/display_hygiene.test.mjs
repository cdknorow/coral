// No NEW show-and-hide by element.style.display (#5162 C). A dashboard element is shown or hidden with the hidden attribute (dom_write.js setHidden, a [hidden] rule where its class sets display),
// so what a stylesheet says about it holds and the write costs nothing when the value is unchanged. 330-odd older sites still assign style.display (what 'flex' or 'block' to give back depends on each
// element's CSS, so they move one at a time, not by a sweep). This counts the assignments per script and fails when a file has more than display_baseline.json allows, or a new file has any;
// a file with fewer fails too until the baseline is lowered (node display_hygiene.test.mjs --update, which only ever lowers; --allow-raise to raise). A write that really is a layout value, not
// a show or hide (a pane's measured width, a dragged element), is marked with // display-ok: <reason> on its line. Reads (style.display === 'none') are not counted.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const st = path.resolve(here, '../../coral-go/pkg/frontend/static');
const BASELINE = path.join(here, 'display_baseline.json');
const WRITE = /\.style\.display\s*=(?!=)/g;

/** The assignments in `src`, not counting a comment or a line marked display-ok. */
export function count(src) {
    let n = 0;
    for (const line of src.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, '')).split('\n')) {
        if (/display-ok:/.test(line)) continue;
        const code = line.replace(/(^|\s)\/\/.*$/, '$1');
        n += (code.match(WRITE) || []).length;
    }
    return n;
}

// The scan itself.
assert.equal(count("a.style.display = 'none'; b.style.display = '';\nc.style.display === 'none'; // d.style.display = 'x'\n/* e.style.display = 'y' */\nf.style.display = 'flex'; // display-ok: a measured pane"), 2, 'counts writes, not reads, comments or a marked line');

const files = fs.readdirSync(st).filter(f => f.endsWith('.js')).sort();
const counts = Object.fromEntries(files.map(f => [f, count(fs.readFileSync(path.join(st, f), 'utf8'))]).filter(([, n]) => n > 0));
const total = o => Object.values(o).reduce((a, b) => a + b, 0);

if (process.argv.includes('--update')) {
    const old = fs.existsSync(BASELINE) ? JSON.parse(fs.readFileSync(BASELINE, 'utf8')) : null;
    const raised = old ? Object.entries(counts).filter(([f, n]) => n > (old[f] || 0)) : [];
    if (raised.length && !process.argv.includes('--allow-raise')) {
        console.error(raised.map(([f, n]) => `${f}: ${n} style.display writes, baseline ${old[f] || 0}`).join('\n') + '\nNot raising the baseline: use setHidden (dom_write.js), or mark a layout value with // display-ok: <reason>. A real raise needs --update --allow-raise.');
        process.exit(1);
    }
    fs.writeFileSync(BASELINE, JSON.stringify(counts, null, 2) + '\n');
    console.log(`display_baseline.json: ${Object.keys(counts).length} files, ${total(counts)} writes`);
    process.exit(0);
}

const baseline = JSON.parse(fs.readFileSync(BASELINE, 'utf8'));
const problems = [];
for (const f of new Set([...Object.keys(counts), ...Object.keys(baseline)])) {
    const now = counts[f] || 0, allowed = baseline[f] || 0;
    if (now > allowed) problems.push(`${f}: ${now} style.display writes, baseline ${allowed}. Show and hide with the hidden attribute (setHidden in dom_write.js), or mark a layout value with // display-ok: <reason>.`);
    else if (now < allowed) problems.push(`${f}: ${now} style.display writes, baseline ${allowed}. Good. Lower the baseline: node display_hygiene.test.mjs --update`);
}
assert.deepEqual(problems, [], problems.join('\n'));
console.log(`display_hygiene: ok (${total(counts)} style.display writes left in ${Object.keys(counts).length} files)`);
