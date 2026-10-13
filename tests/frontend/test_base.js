// Base URL of the isolated Coral server a suite drives, shared by every suite that talks to one.
//
// It comes from CORAL_TEST_URL, never CORAL_URL: shells that a running Coral spawns (agent
// terminals) export CORAL_URL pointing at the live server on :8420, backed by ~/.coral, so a suite
// that honoured it would mutate production. run.sh and run_cdp_suites.mjs set CORAL_TEST_URL to the
// server they started. Port 8420 and URLs without an explicit port are refused outright; there is
// no override.
'use strict';

const DEFAULT_TEST_URL = 'http://127.0.0.1:8462';
const PROD_PORT = '8420';

// testBase returns the origin (scheme://host:port, no trailing slash) for raw, or throws.
function testBase(raw) {
    let url;
    try { url = new URL(raw); } catch { throw new Error(`CORAL_TEST_URL is not a URL: ${JSON.stringify(raw)}`); }
    if (url.protocol !== 'http:' && url.protocol !== 'https:') throw new Error(`CORAL_TEST_URL must be http(s): ${raw}`);
    // URL drops a scheme's default port (http://h:80 -> ''), so this also refuses those.
    if (!url.port) throw new Error(`CORAL_TEST_URL must name an explicit non-${PROD_PORT} port: ${raw}`);
    if (url.port === PROD_PORT) throw new Error(`refusing ${raw}: port ${PROD_PORT} is the production Coral server`);
    return url.origin;
}

let BASE;
try {
    BASE = testBase(process.env.CORAL_TEST_URL || DEFAULT_TEST_URL);
} catch (e) {
    // Exit 2 (setup error), matching the suites' own convention, before anything reaches a server.
    console.error(`${e.message}. Use tests/frontend/run.sh or run_cdp_suites.mjs, or set CORAL_TEST_URL to an isolated test server.`);
    process.exit(2);
}

module.exports = { BASE, testBase, DEFAULT_TEST_URL };
