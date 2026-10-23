// Every place the dashboard listens for a message (#6278, Security's hardening a): a window 'message' listener hears ANY frame on the page, and since
// the Hub's Artifacts tab (#6236) there is an unsandboxed same-origin frame beside the sandboxed panel and view hosts, so a listener that trusts
// its sender could be driven by that frame. Each window listener must check where the message came from (event.source, or origin) in its own handler.
// Listeners on a BroadcastChannel (same origin only, between this browser's own tabs) or on a WebSocket this page opened are not window messages; they are named here with why they are safe.
// A new listener fails this until it is looked at: add it here with its check, or the check to it.
//   agent_ui.js       matches event.source to a mounted panel frame before acting
//   panel_sandbox.js  e.source !== frame.contentWindow
//   view_sandbox.js   two: the dashboard side (answers only the view's own frame) and the bridge script inside the custom view (e.source !== window.parent)
// node message_listener_census.test.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const STATIC = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', 'coral-go', 'pkg', 'frontend', 'static');
const WINDOW_LISTENERS = { 'agent_ui.js': 1, 'panel_sandbox.js': 1, 'view_sandbox.js': 2 };
const CHANNEL_LISTENERS = {
    'ownership.js': 1,      // BroadcastChannel between this browser's own tabs: the message is an ownership claim, never acted on without the tab's own checks
    'team_artifacts.js': 1, // BroadcastChannel: a 64-hex id of a deleted artifact, shape-checked, only used to forget it
    'websocket.js': 2,      // the live feed's WebSocket to the server (set, and cleared on the old socket): a message from this page's own connection, not a window message
    'xterm_renderer.js': 1, // the terminal's WebSocket to the server: the same
};
const SOURCE_CHECK = /\b(?:e|ev|evt|event|msg)\.(?:source|origin)\b/;
const strip = s => s.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');

const windowFound = {}, channelFound = {}, unchecked = [];
for (const f of fs.readdirSync(STATIC).filter(n => n.endsWith('.js'))) {
    const src = strip(fs.readFileSync(path.join(STATIC, f), 'utf8'));
    for (const m of src.matchAll(/([\w$.?]*)\s*\.\s*addEventListener\(\s*['"]message['"]/g)) {
        const target = m[1].replace(/\?$/, '');
        if (/^(?:window|self|globalThis)?$/.test(target) || /(?:^|\.)window$/.test(target)) {
            windowFound[f] = (windowFound[f] || 0) + 1;
            // the handler is inline after the listener, or a named function defined just before it
            const around = src.slice(Math.max(0, m.index - 1500), m.index + 2500);
            if (!SOURCE_CHECK.test(around)) unchecked.push(f);
        } else {
            channelFound[f] = (channelFound[f] || 0) + 1;
        }
    }
    for (const m of src.matchAll(/([\w$.?]*)\s*\.\s*onmessage\s*=/g)) channelFound[f] = (channelFound[f] || 0) + 1;
    // window.onmessage = would be a window listener too
    if (/\bwindow\s*\.\s*onmessage\s*=/.test(src)) unchecked.push(f + ' (window.onmessage)');
}
assert.deepEqual(unchecked, [], `a window 'message' listener with no check of event.source or origin: ${unchecked}`);
assert.deepEqual(windowFound, WINDOW_LISTENERS, `the dashboard's window message listeners: ${JSON.stringify(windowFound)}; a new one needs a look from Security and a line in this test`);
assert.deepEqual(channelFound, CHANNEL_LISTENERS, `the dashboard's BroadcastChannel and WebSocket listeners: ${JSON.stringify(channelFound)}; a new one needs a look and a line in this test`);
console.log('PASS message_listener_census: ' + Object.keys(windowFound).join(', '));
