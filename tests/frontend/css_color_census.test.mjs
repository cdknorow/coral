// A bare colour in a stylesheet (#5375, theming): a hex colour, rgb()/rgba()/hsl() with numbers in it, or a colour name in a declaration, outside the token
// definitions (a `--name: value` line). The page has two themes: a colour written into a rule is the same in both and is a contrast bug in one of them, which is how
// #4851 and #5273 came about. Write var(--token) (variables.css: the themed ones, and the fixed palette at its end: --black-rgb, --fx-red-rgb, ... for rgba(var(--x-rgb), a))
// and add a token when a value is used in three places. What is here already is in fixtures/css_color_allowlist.json, per file and value, with the count: the count may not
// go up, and it must come down when a use is fixed (the file says "lower it"), so the list only shrinks. CORAL_UPDATE_ALLOWLIST=1 rewrites it (review the diff: a new entry
// is a new bare colour). The same for the colours in the scripts' inline styles and the page's templates (a colour a script paints with: a QR code's white, a file type's colour,
// a standalone export's own palette), each with its reason in the file. A single-value border-radius of 3, 4, 6, 8, 10 or 12 px is a token (--radius-xs ... --radius-xl) too.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const st = path.join(path.resolve(here, '../..'), 'coral-go/pkg/frontend/static');
const tpl = path.join(path.resolve(here, '../..'), 'coral-go/pkg/frontend/templates');
const ALLOW = path.join(here, 'fixtures', 'css_color_allowlist.json');
const walk = (d) => fs.readdirSync(d, { withFileTypes: true }).flatMap(e => {
    const p = path.join(d, e.name);
    return e.isDirectory() ? (e.name === 'vendor' || e.name === 'node_modules' ? [] : walk(p)) : [p];
});
const COLOUR = /#[0-9a-fA-F]{8}\b|#[0-9a-fA-F]{6}\b|#[0-9a-fA-F]{3,4}\b|\brgba?\(\s*[\d.]|\bhsla?\(\s*[\d.]|(?<![\w-])(?:white|black|red|green|blue|yellow|orange|purple|gray|grey|pink|cyan|magenta|silver|gold|navy|teal|maroon|olive|lime|aqua|fuchsia)(?![\w-])/g;
const SINGLE_RADIUS = /border-radius\s*:\s*(3|4|6|8|10|12)px\s*(?:!important)?\s*[;}]/g;
const blank = s => s.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' '));
const key = (v) => v.toLowerCase().replace(/\s+/g, '');

export function scanCss(file) {
    const text = blank(fs.readFileSync(file, 'utf8'));
    const found = [], radius = [];
    text.split('\n').forEach((line, i) => {
        if (/^\s*--[\w-]+\s*:/.test(line) || /url\(/.test(line)) return; // a token definition, or an image (a data URI)
        const decl = line.indexOf(':');
        if (decl < 0) return;
        const value = line.slice(decl + 1);
        // a colour NAME counts only as a value of a colour property (a class named .red or a selector is not one)
        const prop = line.slice(0, decl).trim().split(/\s+/).pop();
        const isColourProp = /(^|-)(color|background|background-image|border|border-top|border-right|border-bottom|border-left|outline|box-shadow|text-shadow|fill|stroke|caret-color|accent-color)$|^(border|outline|box-shadow|text-shadow)-/.test(prop);
        for (const m of value.matchAll(COLOUR)) {
            const raw = m[0];
            if (/^[a-z]+$/i.test(raw) && !isColourProp) continue;
            // the whole functional colour for the key: rgba(0, 0, 0, 0.3)
            let v = raw;
            if (/^(rgba?|hsla?)\(/i.test(raw)) { const end = value.indexOf(')', m.index); v = value.slice(m.index, end + 1); }
            found.push({ line: i + 1, v: key(v) });
        }
        for (const m of line.matchAll(SINGLE_RADIUS)) radius.push({ line: i + 1, v: m[1] + 'px' });
    });
    return { found, radius };
}

// the scripts' and templates' own colours: in a string that styles something
const STYLE_CTX = /style|color|background|border|shadow|fill|stroke|setProperty|cssText|fillStyle/i;
export function scanSource(file) {
    const raw = fs.readFileSync(file, 'utf8');
    const text = file.endsWith('.js') ? blank(raw).replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1') : raw.replace(/<!--[\s\S]*?-->/g, m => m.replace(/[^\n]/g, ' '));
    const found = [];
    text.split('\n').forEach((line, i) => {
        for (const m of line.matchAll(/#[0-9a-fA-F]{6}\b|#[0-9a-fA-F]{3}\b|\brgba?\(\s*[\d.][^)]*\)|\bhsla?\(\s*[\d.][^)]*\)/g)) {
            const ctx = line.slice(Math.max(0, m.index - 60), m.index + m[0].length + 10);
            if (STYLE_CTX.test(ctx)) found.push({ line: i + 1, v: key(m[0]) });
        }
    });
    return { found };
}

function collect() {
    const out = { css: {}, source: {}, radius: {} };
    const add = (bag, file, items) => { for (const it of items) { const k = `${file}::${it.v}`; bag[k] = (bag[k] || 0) + 1; } };
    for (const p of walk(path.join(st, 'css')).filter(p => p.endsWith('.css'))) {
        const f = 'css/' + path.basename(p), r = scanCss(p);
        add(out.css, f, r.found); add(out.radius, f, r.radius);
    }
    for (const p of walk(st).filter(p => p.endsWith('.js') && !p.includes(path.sep + 'css' + path.sep))) add(out.source, path.basename(p), scanSource(p).found);
    for (const p of walk(tpl).filter(p => p.endsWith('.html'))) add(out.source, 'templates/' + path.basename(p), scanSource(p).found);
    return out;
}

const now = collect();
const sorted = o => Object.fromEntries(Object.entries(o).sort(([a], [b]) => a.localeCompare(b)));
if (process.env.CORAL_UPDATE_ALLOWLIST) {
    const prev = fs.existsSync(ALLOW) ? JSON.parse(fs.readFileSync(ALLOW, 'utf8')) : {};
    fs.mkdirSync(path.dirname(ALLOW), { recursive: true });
    fs.writeFileSync(ALLOW, JSON.stringify({ about: prev.about || 'Bare colours the stylesheets, scripts and templates still have (css_color_census.test.mjs, #5375): file::value -> uses. The counts only go down.', reasons: prev.reasons || {}, css: sorted(now.css), source: sorted(now.source) }, null, 1) + '\n');
    console.log('css_color_census: allowlist rewritten');
    process.exit(0);
}
const allow = JSON.parse(fs.readFileSync(ALLOW, 'utf8'));
const problems = [];
for (const [bag, name] of [[now.css, 'css'], [now.source, 'source']]) {
    for (const [k, n] of Object.entries(bag)) {
        const a = (allow[name] || {})[k] || 0;
        if (n > a) problems.push(`${k}: ${n} use(s), ${a} allowed: a new bare colour; use a token (variables.css) or, if it must be bare, say why and run CORAL_UPDATE_ALLOWLIST=1`);
    }
    for (const [k, a] of Object.entries(allow[name] || {})) {
        const n = bag[k] || 0;
        if (n < a) problems.push(`${k}: the allowlist says ${a}, found ${n}: lower it (CORAL_UPDATE_ALLOWLIST=1)`);
    }
}
for (const [k, n] of Object.entries(now.radius)) problems.push(`${k}: ${n} single-value border-radius in px that is a token (--radius-xs 3, -sm 4, -md 6, -lg 8, -ml 10, -xl 12)`);
assert.deepEqual(problems, [], `bare colours or radii outside the tokens:\n  ${problems.join('\n  ')}`);
// the census itself catches what it should
{
    const tmp = path.join(fs.mkdtempSync(path.join(process.env.TMPDIR || os.tmpdir(), 'cc-')), 'x.css');
    fs.writeFileSync(tmp, ['.a {', '  color: #abc;', '  background: rgba(1, 2, 3, 0.5);', '  border: 1px solid red;', '  --t: #fff;', '  box-shadow: 0 1px 2px rgb(0 0 0 / 3%);', '  border-radius: 6px;', '}',
        '.red { margin: 0 }', '/* #123456 */', '.b {', '  background: var(--x) url("data:image/svg+xml,%23fff");', '  color: var(--text-primary);', '  background: rgba(var(--black-rgb), 0.3);', '}', ''].join('\n'));
    const r = scanCss(tmp);
    assert.deepEqual(r.found.map(x => x.v), ['#abc', 'rgba(1,2,3,0.5)', 'red', 'rgb(000/3%)'], 'a hex, an rgba, a colour name and an rgb() are found; a token line, a comment, a class name, a url() and a var() colour are not');
    assert.equal(r.radius.length, 1, 'a single-value px radius is found');
    fs.rmSync(path.dirname(tmp), { recursive: true, force: true });
}
console.log(`css_color_census: ok (${Object.values(now.css).reduce((a, b) => a + b, 0)} bare colours in the stylesheets, ${Object.values(now.source).reduce((a, b) => a + b, 0)} in scripts and templates, all on the list; no radius outside the tokens)`);
