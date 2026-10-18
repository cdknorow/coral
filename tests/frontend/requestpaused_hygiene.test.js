// Suites must not register a Fetch.requestPaused handler themselves (#4722). The handler is async and calls
// Fetch.fulfillRequest / continueRequest / getResponseBody; when the CDP client is closing at the suite's end a
// request the page made meanwhile is still paused, those calls reject ("WebSocket is not open: readyState 2
// (CLOSING)"), and an unguarded handler turns that into an unhandled rejection that crashes Node after the suite
// passed (the hub_account_menu flake; a class across 32 suites).
//
// What to use instead (helpers/paused.js): safePaused(client, fn) -- it runs fn in try/catch and swallows the error
// only once the client is closing, re-throwing any other. helpers/paused.js is the one place that touches
// requestPaused directly.
//
// Refuses every spelling (Fetch.requestPaused(, client.on('Fetch.requestPaused'), c.Fetch.requestPaused(...)) in any
// .js/.mjs/.cjs under tests/frontend, comments included, except that helper. A line that really needs a raw handler
// carries a `// paused-ok: <reason>` comment.
const fs = require('node:fs');
const path = require('node:path');

const DIR = __dirname;
const ALLOWED = new Set(['helpers/paused.js']);
const PATTERN = /requestPaused/;

/** Lines (1-based) of src that use a raw requestPaused handler without a paused-ok comment. */
function offending(src) {
    const out = [];
    src.split('\n').forEach((line, i) => {
        if (PATTERN.test(line) && !/paused-ok:\s*\S/.test(line)) out.push(i + 1);
    });
    return out;
}

// Self-check: the matcher sees each spelling, and honours the marker.
const fixtures = [
    ["Fetch.requestPaused(async e => {", [1]],
    ["client.on('Fetch.requestPaused', fn)", [1]],
    ["main.Fetch.requestPaused(h)", [1]],
    ["// the old requestPaused handler", [1]],
    ["c.Fetch.requestPaused(h) // paused-ok: a deliberate raw handler", []],
    ["c.Fetch.requestPaused(h) // paused-ok:", [1]],
    ["safePaused(client, async e => {", []],
    ["const draft = 1; // not a handler", []],
];
for (const [src, want] of fixtures) {
    const got = offending(src);
    if (JSON.stringify(got) !== JSON.stringify(want)) {
        console.error(`requestpaused_hygiene self-check failed for ${JSON.stringify(src)}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
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

const files = walk(DIR).filter(f => f !== 'requestpaused_hygiene.test.js' && !ALLOWED.has(f));
const bad = [];
for (const f of files) {
    for (const line of offending(fs.readFileSync(path.join(DIR, f), 'utf8'))) bad.push(`${f}:${line}`);
}
if (bad.length) {
    console.error(`requestpaused_hygiene.test.js: ${bad.length} raw requestPaused handler(s) in test code; a closing client makes an async one crash Node with an unhandled rejection. Use safePaused(client, fn) from helpers/paused.js, or mark a deliberate raw handler with // paused-ok: <reason>:\n  ${bad.join('\n  ')}`);
    process.exit(1);
}
console.log(`requestpaused_hygiene.test.js: ok (${files.length} files, no raw requestPaused handlers)`);
