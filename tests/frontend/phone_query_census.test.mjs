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
    // every media query (a CSS @media prelude, or the string a script gives matchMedia) is checked as a whole, in any clause order:
    // a clause that is the sideways phone ((pointer: coarse) with a max-height) must carry (max-width: 950px); a desktop query that excludes the sideways phone ((min-width: 768px) with (min-height: 501px))
    // must also allow (min-width: 951px), the tablet form of it included
    const queries = [...text.matchAll(/@media\s+([^{]*)\{/g)].map(m => [m[1], m.index]);
    for (const m of text.matchAll(/matchMedia\(\s*(['"`])([^'"`]*)\1/g)) queries.push([m[2], m.index]);
    for (const [q, at] of queries) {
        for (const clause of q.split(',')) {
            if (/pointer:\s*coarse/.test(clause) && /max-height:\s*5\d\dpx/.test(clause) && !/max-width:\s*950px/.test(clause)) problems.push(`${rel}:${lineOf(text, at)}: the sideways-phone clause "${clause.trim()}" has no "(max-width: 950px)": a touch laptop with a short, wide window would count as a phone`);
        }
        if (/min-width:\s*768px/.test(q) && /min-height:\s*501px/.test(q) && !/min-width:\s*951px/.test(q)) problems.push(`${rel}:${lineOf(text, at)}: a desktop query that excludes the sideways phone ("${q.trim().slice(0, 90)}") lacks its "(min-width: 951px)" alternative`);
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
assert.ok(/DESKTOP_QUERY = '[^']*\(min-width: 951px\)'/.test(def), 'phone_layout.js defines DESKTOP_QUERY with its 951 px alternative');
console.log(`phone_query_census: ok (${files.length} files; the sideways-phone clause has its 950 px limit everywhere)`);
