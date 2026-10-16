// The Material Icons subset keeps every icon the dashboard's source can show (#3973). The font is a ligature font, so an icon missing
// from it renders as its name ("flight_takeoff" as text). icon_names.mjs scans the source; vendor/fonts/material-icons.names.txt lists
// what the shipped subset holds (written by coral-go/tools/subset_icons.py). node icon_subset.test.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { scanIcons, allNames, FRONTEND, candidates, codePoints } from './icon_names.mjs';

const FONTS = path.join(FRONTEND, 'static/vendor/fonts');
const subset = new Set(fs.readFileSync(path.join(FONTS, 'material-icons.names.txt'), 'utf8').split('\n').filter(Boolean));
const all = allNames();

const { names, codePoints: points } = scanIcons();
assert.ok(names.length > 50, `the scan finds the dashboard's icons (${names.length})`);
const missing = names.filter(n => !subset.has(n));
assert.deepEqual(missing, [], `icons the source can show but the font subset lacks (they would render as text): rebuild it with coral-go/tools/subset_icons.py. Missing: ${missing.join(', ')}`);
for (const n of subset) assert.ok(all.has(n), `${n} is in the subset but is not a Material Icons name`);
assert.ok(fs.statSync(path.join(FONTS, 'material-icons.woff2')).size < 30 * 1024, 'the icon font stays a subset (the full font is 129 KB)');
assert.ok(subset.size < 400, `the subset is ${subset.size} icons: the full set is ${all.size}`);

// the scanner itself: the places an icon name is written, and what is not a name
assert.deepEqual([...candidates(`<span class="material-icons">close</span> el.textContent = 'arrow_back'; icon: "folder_open"; x = 'not a name'`)].sort(), ['arrow_back', 'close', 'folder_open']);
assert.ok(candidates('<i class="material-icons"> add </i>').has('add'), 'element text with spaces');
assert.deepEqual([...codePoints('a { content: "\\e5cd"; } &#xe5ce; \\ue5cf plain')].sort(), [0xe5cd, 0xe5ce, 0xe5cf]);
assert.equal(codePoints('no private use here').size, 0);
console.log(`PASS icon_subset: ${names.length} icons used, ${subset.size} in the subset, ${points.length} private-use code points`);
