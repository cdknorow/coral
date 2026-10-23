// A guarded Fetch.requestPaused handler (#4722). A suite's handler is async and calls Fetch.fulfillRequest /
// continueRequest / getResponseBody. When the CDP client is closing at the suite's end, a request the page made
// meanwhile (a background poll of /api/servers or /api/sessions/live, say) is still paused, and those calls reject
// with "WebSocket is not open: readyState 2 (CLOSING)". An unguarded async handler turns that into an unhandled
// rejection that crashes Node after the suite already passed -- the hub_account_menu flake, and a class across the
// 32 suites that register a requestPaused handler.
//
// safePaused runs fn inside try/catch and swallows the error only once the client is closing (its close() was called)
// or the connection is already closed; any other error is re-thrown, so a real bug in the handler still surfaces.
// Register every requestPaused handler through this helper -- raf_hygiene's sibling, requestpaused_hygiene, enforces it.
'use strict';

/** A connection-gone error from fulfill/continue/getResponseBody after the client started closing, or because the paused request no longer exists. */
function isClosedErr(err) {
    const m = String((err && err.message) || err || '');
    return /WebSocket is not open|WebSocket connection closed|readyState [23]|not opened|Target closed|Session closed|Connection closed|already closed|Invalid InterceptionId/i.test(m); // the last: the paused request is gone (the page navigated away or cancelled it) before the handler answered (#6506)
}

/**
 * safePaused(client, fn): register fn as the client's Fetch.requestPaused handler, guarded so a close-race
 * error during teardown is ignored while any other error still throws. `client` is the CDP client (any name the
 * suite gives it); fn receives the paused-request event, as Fetch.requestPaused(fn) would.
 */
function safePaused(client, fn) {
    // Flag the client the moment close() is called -- before its WebSocket actually goes away -- so a handler that
    // fires during teardown is known to be closing even before the reject arrives. Hook close once per client.
    if (client && !client.__safePausedHooked) {
        client.__safePausedHooked = true;
        client.__safePausedClosing = false;
        if (typeof client.close === 'function') {
            const origClose = client.close.bind(client);
            client.close = (...args) => { client.__safePausedClosing = true; return origClose(...args); };
        }
    }
    client.Fetch.requestPaused(async (event) => {
        try {
            await fn(event);
        } catch (err) {
            if ((client && client.__safePausedClosing) || isClosedErr(err)) return; // the client is going away
            throw err;
        }
    });
}

module.exports = { safePaused, isClosedErr };
