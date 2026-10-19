// A suite that saves messages takes them off again (#4916): each of these suites starts by deleting the Operator's bookmarks, so at the end the table is theirs to empty. The runner's
// end-check prints a LEAK line ('saved_messages: 0 -> N rows') for a suite that does not, and the next suite on the shard then reads rows it did not save.
//   const { clearSaved } = require('./helpers/saved_cleanup');
//   finally { await clearSaved(api).catch(() => {}); await client.close(); }   // api(method, path, body) -> { status, body }: the suite's own page-side request helper
'use strict';

/** Delete every saved message through the suite's request helper (the page's fetch: the Operator's cookie; an agent's token is refused on /api/saved). Returns how many were removed. */
async function clearSaved(api) {
    const items = ((await api('GET', '/api/saved?state=all&limit=1000')).body || {}).items || [];
    for (const it of items) await api('DELETE', `/api/saved/${it.id}`);
    return items.length;
}

module.exports = { clearSaved };
