// A suite must not define a waitFor that polls the page through ev() and lets a navigation break it (#4879, #4858): Chrome refuses a Runtime.evaluate that arrives while the page navigates
// ("Inspected target navigated or closed"), which is the page changing under the wait, not a failure. The shared waitFor/waitStable (helpers/wait.js) look again; a suite's OWN waitFor that
// polls with `await ev(` must either use isNavigationError (rethrowing every other error) or catch the poll's error itself. Waits that run inside the page (sync conditions) are not polls through ev.
//   node waitfor_hygiene.test.js
'use strict';
const fs = require('node:fs');
const path = require('node:path');

const DEF = /(?:\b(?:const|let|var)\s+waitFor\s*=|\bfunction\s+waitFor\s*\()/;
const bad = [];
let defs = 0;
for (const f of fs.readdirSync(__dirname).filter(n => /\.(js|mjs)$/.test(n) && n !== path.basename(__filename))) {
    const lines = fs.readFileSync(path.join(__dirname, f), 'utf8').split('\n');
    lines.forEach((l, i) => {
        if (!DEF.test(l)) return;
        const body = lines.slice(i, i + 14).join('\n');
        if (!/await ev\(/.test(body)) return; // a wait that runs in the page, or one that hands the poll to the shared helper
        defs++;
        if (!/isNavigationError|catch\s*[({]/.test(body)) bad.push(`${f}:${i + 1}`);
    });
}
if (bad.length) { console.error(`FAIL waitfor_hygiene: a waitFor polls the page with ev() and a navigation would break it; use helpers/wait.js's waitFor, or isNavigationError (rethrow the rest):\n  ${bad.join('\n  ')}`); process.exit(1); }
if (defs < 5) { console.error(`FAIL waitfor_hygiene: it found only ${defs} own waitFor definitions that poll with ev(): the scan is not looking at what it should`); process.exit(1); }
console.log(`waitfor_hygiene: ok (${defs} suites with their own waitFor, each tolerant of a navigation)`);
