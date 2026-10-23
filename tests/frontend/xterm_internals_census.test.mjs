// What this page reads from xterm.js that xterm does not document goes through one module, xterm_internals.js (#5411), and nothing else touches it. This fails when another script reaches into
// term._core or a service under it, or into xterm's own .xterm-screen element, and when the vendored xterm is not the version xterm_internals.js was written for (an upgrade has to open that file:
// the four reads and their fallbacks are listed at its top) or no longer contains the names it reads. The runtime half, a real terminal of the bundled xterm, is xterm_internals_browser.test.js.
import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const st = path.join(path.resolve(here, '../..'), 'coral-go/pkg/frontend/static');
const strip = (t) => t.replace(/\/\*[\s\S]*?\*\//g, m => m.replace(/[^\n]/g, ' ')).replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1');
const PRIVATE = /\._core\b|\b_renderService\b|\bcoreService\b|\b_bufferService\b|\b_charSizeService\b|\b_viewport\b|\b_selectionService\b|['"`]\.xterm-(?:screen|viewport|rows|helper-textarea)\b/;

const offenders = [];
for (const f of fs.readdirSync(st).filter(f => f.endsWith('.js'))) {
    if (f === 'xterm_internals.js') continue;
    strip(fs.readFileSync(path.join(st, f), 'utf8')).split('\n').forEach((l, i) => { if (PRIVATE.test(l)) offenders.push(`${f}:${i + 1}: ${l.trim().slice(0, 100)}`); });
}
assert.deepEqual(offenders, [], `xterm internals read outside xterm_internals.js (add the read there, with its fallback):\n  ${offenders.join('\n  ')}`);

// the vendored version is the one the adapter was written for
const vendor = fs.readFileSync(path.join(st, 'vendor/xterm/xterm.min.js'), 'utf8');
const adapter = fs.readFileSync(path.join(st, 'xterm_internals.js'), 'utf8');
const version = (/@xterm\/xterm@(\d+\.\d+\.\d+)/.exec(vendor.slice(0, 600)) || [])[1];
const written = (/vendored xterm is (\d+\.\d+\.\d+)/.exec(adapter) || [])[1];
assert.ok(version, 'the vendored xterm names its version in its header');
assert.equal(version, written, `the vendored xterm is ${version} but xterm_internals.js says it was written for ${written}: check its four reads against this version (xterm_internals_browser.test.js), then update the number there`);
// and still contains the names the four reads use
for (const name of ['_renderService', 'coreService', 'onUserInput', 'xterm-screen', 'css', 'cell']) assert.ok(vendor.includes(name), `the vendored xterm no longer contains "${name}", which xterm_internals.js reads`);
// the adapter's own list of reads is what it does: each name it uses is in its header table
for (const name of ['screenElement', 'cellHeight', 'clearRenderer', 'onUserInput', 'watchCanvasScale']) assert.ok(new RegExp(`export function ${name}\\b`).test(adapter) && adapter.includes(`${name}(`), `xterm_internals.js exports ${name} and documents it`);
console.log(`xterm_internals_census: ok (xterm ${version}; no module but xterm_internals.js reads an xterm internal)`);
