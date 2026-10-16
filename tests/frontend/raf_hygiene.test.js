// Suites must not wait on requestAnimationFrame themselves (#3705). A page that is not visible
// (document.visibilityState 'hidden') runs no animation frames: on the Mac, headless Chrome hides the page
// after an Escape key press sent through Input.dispatchKeyEvent, and a wait or poll made of frames then
// never finishes (team_settings_navigation_review timed out that way, #3703). Linux keeps the page
// visible, so such a wait passes there and fails on the Mac.
//
// What to use instead (helpers/wait.js): waitFor for a condition, or framesJs / nextFrame for "after the
// next frame", which give up after a bounded time instead of hanging. helpers/wait.js is the one place
// that touches animation frames, and helpers/jsdom_env.mjs gives jsdom (which has no frames) a timer.
//
// Refuses every spelling (requestAnimationFrame, cancelAnimationFrame, webkitRequestAnimationFrame, rAF)
// in any .js/.mjs/.cjs under tests/frontend, comments included, except those two helpers. A line that
// really needs one carries a `// raf-ok: <reason>` comment.
const fs = require('node:fs');
const path = require('node:path');

const DIR = __dirname;
const ALLOWED = new Set(['helpers/wait.js', 'helpers/jsdom_env.mjs']);
const PATTERN = /animationframe|\brAF\b/i;

/** Lines (1-based) of src that use an animation frame without a raf-ok comment. */
function offending(src) {
    const out = [];
    src.split('\n').forEach((line, i) => {
        if (PATTERN.test(line) && !/raf-ok:\s*\S/.test(line)) out.push(i + 1);
    });
    return out;
}

// Self-check: the matcher sees each spelling, and honours the marker.
const fixtures = [
    ['await new Promise(r => requestAnimationFrame(r));', [1]],
    ['window.webkitRequestAnimationFrame(f)', [1]],
    ['cancelAnimationFrame(id)', [1]],
    ['// the old rAF loop', [1]],
    ['const check = () => ok() ? resolve() : requestAnimationFrame(check);', [1]],
    ['requestAnimationFrame(f); // raf-ok: the page under test is drawing a canvas', []],
    ['requestAnimationFrame(f); // raf-ok:', [1]],
    ['await waitFor(`!!document.querySelector(".x")`, "x");', []],
    ['const draft = 1; // not a frame', []],
];
for (const [src, want] of fixtures) {
    const got = offending(src);
    if (JSON.stringify(got) !== JSON.stringify(want)) {
        console.error(`raf_hygiene self-check failed for ${JSON.stringify(src)}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
        process.exit(1);
    }
}

function walk(dir, rel = '') {
    const files = [];
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        if (entry.name === 'node_modules') continue;
        const r = rel ? `${rel}/${entry.name}` : entry.name;
        if (entry.isDirectory()) files.push(...walk(path.join(dir, entry.name), r));
        else if (/\.(?:js|mjs|cjs)$/.test(entry.name)) files.push(r);
    }
    return files.sort();
}

const files = walk(DIR).filter(f => f !== 'raf_hygiene.test.js' && !ALLOWED.has(f));
const bad = [];
for (const f of files) {
    for (const line of offending(fs.readFileSync(path.join(DIR, f), 'utf8'))) bad.push(`${f}:${line}`);
}
if (bad.length) {
    console.error(`raf_hygiene.test.js: ${bad.length} animation-frame wait(s) in test code; a hidden page (the Mac) never runs them. Use waitFor, or framesJs / nextFrame from helpers/wait.js (bounded), or mark a deliberate use with // raf-ok: <reason>:\n  ${bad.join('\n  ')}`);
    process.exit(1);
}
console.log(`raf_hygiene.test.js: ok (${files.length} files, no animation-frame waits)`);
