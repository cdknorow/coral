// Every place the dashboard gives an iframe an address (#4718, Security's rider 4): the Inbox frame's address is the constant '/inbox', and the
// only frames whose address comes from data are named here, each with why that is safe. A new `iframe.src = ...` fails this until it is looked at.
//   agent_ui.js         a panel of an agent on a local Coral: a same-origin API path built from the session and panel ids, in a sandbox with allow-scripts only
//   artifact_preview.js the Linked preview: an external https address in a locked sandbox (no scripts, forms, popups or navigation)
//   panel_sandbox.js    the constant address of the panel host page
//   view_sandbox.js     the constant address of the custom-view host page
//   inbox_tab.js        the constant '/inbox'
// node iframe_src_census.test.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const STATIC = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'coral-go', 'pkg', 'frontend', 'static');
const ALLOWED = { 'agent_ui.js': 1, 'artifact_preview.js': 1, 'panel_sandbox.js': 1, 'view_sandbox.js': 1, 'inbox_tab.js': 1 };
const found = {};
for (const f of fs.readdirSync(STATIC).filter(n => n.endsWith('.js'))) {
    const src = fs.readFileSync(path.join(STATIC, f), 'utf8');
    // A frame variable: created as an iframe in this file, then given an address with .src = or setAttribute('src').
    const frames = [...src.matchAll(/(?:const|let|var)\s+(\w+)\s*=\s*document\.createElement\(\s*['"]iframe['"]\s*\)/g)].map(m => m[1]);
    const names = new Set([...frames, 'frame', 'iframe']);
    let n = 0;
    for (const v of names) {
        n += (src.match(new RegExp(`\\b${v}\\.src\\s*=`, 'g')) || []).length;
        n += (src.match(new RegExp(`\\b${v}\\.setAttribute\\(\\s*['"]src['"]`, 'g')) || []).length;
    }
    if (/getElementById\(['"]\w*frame['"]\)[^;]*setAttribute\(\s*['"]src['"]/.test(src)) n++;
    if (n) found[f] = n;
}
assert.deepEqual(found, ALLOWED, `the dashboard's iframe addresses: ${JSON.stringify(found)}; a new one needs a look from Security and a line in this test`);
// A frame can also get its whole document from data with srcdoc (always inside a sandbox and a CSP meta of its own: sandbox_frame.js,
// panel_sandbox.js and view_sandbox.js explain why), or be built in an HTML string. Both are counted here too, so a new one needs a look.
const SRCDOC = { 'artifact_preview.js': 1, 'custom_views.js': 1, 'preview_pane.js': 1 };
const srcdocFound = {};
for (const f of fs.readdirSync(STATIC).filter(n => n.endsWith('.js'))) {
    const src = fs.readFileSync(path.join(STATIC, f), 'utf8');
    const n = (src.match(/\.srcdoc\s*=/g) || []).length;
    if (n) srcdocFound[f] = n;
    assert.ok(!/<iframe/i.test(src.replace(/\/\*[\s\S]*?\*\/|\/\/.*$/gm, '')), `${f} builds an iframe in an HTML string: give it a look from Security and a line in this test`);
}
assert.deepEqual(srcdocFound, SRCDOC, `frames given a document with srcdoc: ${JSON.stringify(srcdocFound)}; a new one needs a look from Security and a line in this test`);
const TEMPLATES = path.join(STATIC, '..', 'templates');
const withIframe = [];
(function walk(d) { for (const e of fs.readdirSync(d, { withFileTypes: true })) { const p = path.join(d, e.name); if (e.isDirectory()) walk(p); else if (/<iframe/i.test(fs.readFileSync(p, 'utf8').replace(/<!--[\s\S]*?-->/g, ''))) withIframe.push(path.relative(TEMPLATES, p)); } })(TEMPLATES);
assert.deepEqual(withIframe.sort(), ['includes/views/inbox.html'], `templates with an iframe: ${withIframe}; a new one needs a look from Security and a line in this test`);
// The Inbox frame: the address is a constant in inbox_tab.js and is not in the template.
const inbox = fs.readFileSync(path.join(STATIC, 'inbox_tab.js'), 'utf8');
assert.match(inbox, /const INBOX_URL = '\/inbox';/);
assert.equal((inbox.match(/setAttribute\('src', /g) || []).length, 1);
assert.match(inbox, /setAttribute\('src', INBOX_URL\)/);
const tpl = fs.readFileSync(path.join(STATIC, '..', 'templates', 'includes', 'views', 'inbox.html'), 'utf8');
assert.match(tpl, /<iframe[^>]*id="inbox-frame"[^>]*referrerpolicy="no-referrer"[^>]*allow=""/);
assert.ok(!/<iframe[^>]*\ssrc=/.test(tpl));
console.log('PASS iframe_src_census: ' + Object.keys(found).join(', '));
