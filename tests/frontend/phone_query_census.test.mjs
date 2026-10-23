// One phone: every phone media query in the dashboard's CSS and script says the same thing (#6729, QA's point 4 on #6683). A phone is a window at most 767 px wide, or a touch screen at most 500 px high AND at most
// 950 px wide (a phone turned sideways; a touch laptop with a short, wide window is not one). phone_layout.js holds the definition (PHONE_QUERY, DESKTOP_QUERY) and every rule repeats it in full, because a media
// query cannot name another. This fails when a stylesheet or script has the sideways clause without its 950 px limit (the key bar showed on a 1280x400 touch window because one rule had not got it), when a desktop
// query that excludes the sideways phone lacks its 951 px alternative, or when a file still tests the old width alone for "phone" in JS.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const st = path.join(path.resolve(here, '../..'), 'coral-go/pkg/frontend/static');
const files = [];
for (const d of ['', 'css']) for (const f of fs.readdirSync(path.join(st, d))) if (/\.(css|js)$/.test(f)) files.push(path.join(st, d, f));

const problems = [];
const lineOf = (text, i) => text.slice(0, i).split('\n').length;
for (const f of files) {
    const text = fs.readFileSync(f, 'utf8');
    const rel = path.relative(st, f);
    // the sideways clause: (pointer: coarse) and (max-height: 500px)  -- it must continue with  and (max-width: 950px)
    for (const m of text.matchAll(/\(pointer: coarse\) and \(max-height: 5\d\dpx\)/g)) {
        const after = text.slice(m.index + m[0].length, m.index + m[0].length + 40);
        if (!/^\s*and \(max-width: 950px\)/.test(after)) problems.push(`${rel}:${lineOf(text, m.index)}: the sideways-phone clause "${m[0]}" has no "and (max-width: 950px)": a touch laptop with a short, wide window would count as a phone`);
    }
    // the desktop forms that leave the sideways phone out: (min-width: 768px) and (pointer: fine)  -- they must also allow (min-width: 951px)
    for (const m of text.matchAll(/\(min-width: 768px\) and \(min-height: 501px\)[^{'`\n]*/g)) {
        if (!/\(min-width: 951px\)/.test(m[0])) problems.push(`${rel}:${lineOf(text, m.index)}: a desktop query that excludes the sideways phone ("${m[0].slice(0, 90)}") lacks its "(min-width: 951px)" alternative`);
    }
    // an old-style test of "phone" by width alone in script (phone_layout.js is the one place that knows the number)
    if (f.endsWith('.js') && !f.endsWith('phone_layout.js') && !rel.startsWith('vendor')) {
        for (const m of text.matchAll(/window\.innerWidth\s*(<=|>)\s*767\b|matchMedia\(\s*['"]\(max-width: 767px\)['"]\s*\)|matchMedia\(\s*['"]\(min-width: 768px\)['"]\s*\)/g)) problems.push(`${rel}:${lineOf(text, m.index)}: "${m[0]}" tests the phone by width alone: use isPhoneLayout() / PHONE_QUERY / DESKTOP_QUERY from phone_layout.js`);
    }
}
assert.deepEqual(problems, [], 'phone queries that are not the one definition:\n  ' + problems.join('\n  '));

// the definition itself says the same thing as the rules
const def = fs.readFileSync(path.join(st, 'phone_layout.js'), 'utf8');
assert.ok(/PHONE_QUERY = '\(max-width: 767px\), \(pointer: coarse\) and \(max-height: 500px\) and \(max-width: 950px\)'/.test(def), 'phone_layout.js defines PHONE_QUERY with the 500 px height and the 950 px width');
console.log(`phone_query_census: ok (${files.length} files; the sideways-phone clause has its 950 px limit everywhere)`);
