// Keeps every page a suite drives visible (#3707). Preloaded into each suite by run_cdp_suites.mjs
// (node --require), so no suite has to call it.
//
// On the Mac, headless Chrome reports a page as document.visibilityState 'hidden' after a key event sent
// through Input.dispatchKeyEvent (Enter, Escape and the arrow keys did it; Tab did not), and a hidden page
// runs no animation-frame callbacks: the app's own frame-deferred work stops and a suite that
// waits on it times out (#3703, #3705). Linux never hides the page, so such a suite passed on epoch and
// failed on the Mac. Page.bringToFront restores it only until the next key; Emulation.setFocusEmulationEnabled
// keeps it visible and focused for as long as the connection lasts, so this wraps chrome-remote-interface
// to switch it on right after each connect. CDP_KEEP_VISIBLE=0 turns it off (to see a suite without it).
'use strict';

const Module = require('node:module');

if (process.env.CDP_KEEP_VISIBLE !== '0') {
    const load = Module._load;
    let wrapped = null;
    Module._load = function (request, ...rest) {
        const real = load.call(this, request, ...rest);
        if (request !== 'chrome-remote-interface' || typeof real !== 'function') return real;
        if (!wrapped) {
            wrapped = function CDP(options, callback) {
                // Only the promise form (what every suite uses) is wrapped.
                if (typeof options === 'function' || typeof callback === 'function') return real(options, callback);
                return real(options).then(async client => {
                    try { await client.Emulation.setFocusEmulationEnabled({ enabled: true }); } catch { /* not a page target: nothing to keep visible */ }
                    return client;
                });
            };
            Object.assign(wrapped, real); // List, New, Close, Activate, Version, Protocol
            wrapped.keepsPagesVisible = true;
        }
        return wrapped;
    };
}
