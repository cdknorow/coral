// Keeps every page a suite drives visible (#3707). Preloaded into each suite by run_cdp_suites.mjs
// (node --require), so no suite has to call it.
//
// On the Mac, headless Chrome reports a page as document.visibilityState 'hidden' after a key event sent
// through Input.dispatchKeyEvent (Enter, Escape and the arrow keys did it; Tab did not), and a hidden page
// runs no animation-frame callbacks: the app's own frame-deferred work stops and a suite that
// waits on it times out (#3703, #3705). Linux never hides the page, so such a suite passed on epoch and
// failed on the Mac. Page.bringToFront restores it only until the next key; Emulation.setFocusEmulationEnabled
// keeps it visible and focused for as long as the connection lasts, so this wraps chrome-remote-interface
// to switch it on right after each connect (and, #6195, to keep a page load that came before loadEventFired() was asked for). CDP_KEEP_VISIBLE=0 turns it off (to see a suite without it).
// CORAL_TEST_FAKE_NOW="2026-10-24T00:30:00" (opt-in, #6809) starts the clock of the page and of the suite's own process at that time: it moves Date only (Date, new Date() and Date.now()), not the server's clock, timers or performance.now.
'use strict';

const Module = require('node:module');

// A suite writes `await Page.navigate(...); await Page.loadEventFired();`: the load event is delivered once, to whoever is listening, and a page that loads between the two calls is never heard, so the suite hangs to the
// runner's timeout with a loaded, idle page and an idle server (#6195: update_channel, hub_pages_theme, phone_pass_130 and list_bars_themes on Windows). The client remembers a main-frame load that came after a navigate
// and gives it to the next loadEventFired(); a reload that is not a navigate (a new load cycle started since) is still waited for. cdp_navigation_race_browser.test.js is the proof.
// A load heard this long ago is the one the suite meant to wait for (the gap between two awaited calls is a few milliseconds); an older one was not asked for, and a later wait is for another load.
const LATE_MS = 1000;

function rememberLoads(client) {
    const Page = client.Page;
    if (!Page || typeof Page.navigate !== 'function' || typeof Page.loadEventFired !== 'function') return;
    let loads = 0;    // main-frame load events seen
    let loadAt = 0;   // when the last one came
    let mark = null;  // loads when the last navigate was made; null once its load was handed out or a newer load cycle started
    let waiting = 0;  // loadEventFired() waits asked and not yet answered: a load that comes while one is waiting was heard (a wait made before its navigate: `const loaded = Page.loadEventFired(); await Page.navigate(...); await loaded;`)
    client.on('Page.loadEventFired', () => { loads++; loadAt = Date.now(); if (waiting > 0) mark = null; }); // so it is not kept for the next wait: servers_table's about:blank load answered the wait for the page after it (#6195 follow-up)
    client.on('Page.frameStartedLoading', () => { if (mark !== null && loads > mark) mark = null; }); // the navigate's load came and nobody asked: this is another load, and that is the one to wait for
    const navigate = Page.navigate, load = Page.loadEventFired;
    Page.navigate = function (...args) { mark = loads; return navigate.apply(this, args); };
    // A reload is a navigation too (eight suites write `await Page.reload(); await Page.loadEventFired();`): its load, heard late, is kept the same way.
    if (typeof Page.reload === 'function') { const reload = Page.reload; Page.reload = function (...args) { mark = loads; return reload.apply(this, args); }; }
    Page.loadEventFired = function (...args) {
        if (args.length === 0 && mark !== null && loads > mark && Date.now() - loadAt <= LATE_MS) { mark = null; return Promise.resolve({ timestamp: Date.now() / 1000 }); }
        if (args.length === 0) mark = null;
        waiting++;
        const p = load.apply(this, args);
        const done = () => { waiting--; };
        p.then(done, done);
        return p;
    };
}

// A fake clock for the page (opt-in, a test of the tests): CORAL_TEST_FAKE_NOW="2026-10-24T00:30" (local time, any form Date reads) makes every page the suite drives believe it is then, and the time goes on from there
// (Date, new Date() and Date.now(); not performance.now, timers or the server's clock). A suite whose expectation depends on the hour or the day (today, yesterday, in N h, the next cron fire) fails at 00:30 or
// 23:59 though it passes by day (artifacts_name_column "yesterday", jobs_screen "in N h": the 163 night reds, #6809). The data a suite builds in the page moves with the clock; data the server stamps does not.
const FAKE_NOW = process.env.CORAL_TEST_FAKE_NOW ? new Date(process.env.CORAL_TEST_FAKE_NOW).getTime() : NaN;
const FAKE_OFFSET = Number.isFinite(FAKE_NOW) ? FAKE_NOW - Date.now() : 0;
const FAKE_CLOCK = `(() => { const Real = Date, off = ${FAKE_OFFSET};
    class FakeDate extends Real { constructor(...a) { if (a.length === 0) super(Real.now() + off); else super(...a); } static now() { return Real.now() + off; } }
    window.Date = FakeDate; })();`;

if (Number.isFinite(FAKE_NOW)) { // the suite's own process too: the data it builds (new Date(...) in the test file) and the page it drives agree on the time
    const Real = Date, off = FAKE_OFFSET;
    global.Date = class FakeDate extends Real { constructor(...a) { if (a.length === 0) super(Real.now() + off); else super(...a); } static now() { return Real.now() + off; } };
}

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
                    if (Number.isFinite(FAKE_NOW)) { try { await client.Page.enable(); await client.Page.addScriptToEvaluateOnNewDocument({ source: FAKE_CLOCK }); } catch { /* not a page target */ } }
                    rememberLoads(client);
                    return client;
                });
            };
            Object.assign(wrapped, real); // List, New, Close, Activate, Version, Protocol
            wrapped.keepsPagesVisible = true;
        }
        return wrapped;
    };
}
