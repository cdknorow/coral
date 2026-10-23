'use strict';
// The one list of live regions known to be shown and filled in one turn (file:region id -> why), read by live_region_lint.test.js (the static scan) and by helpers/announce_census.js (the runtime rule), so the two
// cannot disagree about what is allowed (#6659). Reviewed, still in the page (file:region id). Empty since #6637 and #6656 fixed every offender: a new one fails the lint, and a listed one that stops matching fails it too.
const KNOWN = {
};

module.exports = { KNOWN };
