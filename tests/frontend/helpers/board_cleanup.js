// A suite that makes a message board takes it away again (#4959): the runner's end-check names a board a suite leaves with messages or subscribers, and --fail-on-leak fails the suite for it.
//   const { removeBoards } = require('./helpers/board_cleanup');
//   finally { await removeBoards(ev, [BOARD]); await client.close(); }   // ev(expression): the suite's own page-side evaluate; the page's session is the credential
// DELETE /api/board/{name} removes the board's messages, subscribers, groups, presets and working mode (not its tasks, which the API cannot delete: the end-check does not fail a board that only has tasks).
'use strict';

/** Delete each board by name, through the page. Never throws: a page that is gone or a board that is already gone is not the suite's failure (the end-check names a board that is still there). */
async function removeBoards(ev, names) {
    for (const name of names) {
        if (!name) continue;
        try { await ev(`(async () => (await fetch('/api/board/' + encodeURIComponent(${JSON.stringify(String(name))}), { method: 'DELETE' })).status)()`); } catch { /* the end-check reports what is left */ }
    }
}

module.exports = { removeBoards };
