// A suite that changes the server's settings puts them back (#4849). The suites of a shard share one server, so a setting one leaves is read by the next (show_session_server left False
// changed live_list_golden; auto_wake_after_restart left false failed auto_wake). The runner restores them after each suite too (helpers/server_state.mjs), but a suite should not depend on
// that: it works alone, on a developer's own server, and --fail-on-leak names the suites that rely on it.
//
//   const { guardSettings } = require('./helpers/settings_guard');
//   const guard = await guardSettings();          // before the suite changes anything
//   try { ... } finally { await guard.restore(); } // whatever it changed or added is put back
//
// restore() reads the settings again and, for every key that differs from the snapshot: under the runner (CORAL_TEST_DATA_DIR is the shard's data dir) writes the shard's database, so a key
// the suite ADDED is deleted again and the settings are exactly as found; alone, it PUTs the old values through the API and an added key is set to '' (the API has no way to unset one; the
// server reads '' as unset for the settings that matter here). Returns the number of keys put back.
'use strict';
const { sqliteText, sqliteExec } = require('./sqlite.js');
const path = require('node:path');
const { BASE } = require('../test_base');

const quote = s => "'" + String(s).replace(/'/g, "''") + "'";

// Under the runner the settings are read from the shard's database, not through the API: a strict shard (--strict-local-auth) answers a request with no credential with 401, and a suite's
// node-side calls send none (#4849 follow-up). Alone, through the API.
function readFromDb(dataDir) {
    const out = sqliteText(path.join(dataDir, 'sessions.db'), 'SELECT key, value FROM user_settings;');
    const settings = {};
    for (const line of out.split('\n')) { if (!line) continue; const i = line.indexOf('\u001f'); settings[line.slice(0, i)] = line.slice(i + 1); }
    return settings;
}

async function readSettings(base, dataDir = '') {
    if (dataDir) return readFromDb(dataDir);
    const r = await fetch(base + '/api/settings');
    if (!r.ok) throw new Error(`GET /api/settings: HTTP ${r.status}`);
    const d = await r.json();
    return d.settings || d;
}

async function guardSettings({ base = BASE, dataDir = process.env.CORAL_TEST_DATA_DIR || '' } = {}) {
    const before = await readSettings(base, dataDir);
    return {
        before,
        async restore() {
            const after = await readSettings(base, dataDir);
            const changed = Object.keys({ ...before, ...after }).filter(k => before[k] !== after[k]);
            if (!changed.length) return 0;
            if (dataDir) {
                const stmts = changed.map(k => (before[k] === undefined ? `DELETE FROM user_settings WHERE key = ${quote(k)};` : `INSERT OR REPLACE INTO user_settings (key, value) VALUES (${quote(k)}, ${quote(before[k])});`));
                sqliteExec(path.join(dataDir, 'sessions.db'), `PRAGMA busy_timeout = 5000; ${stmts.join(' ')}`);
                return changed.length;
            }
            const put = async body => (await fetch(base + '/api/settings', { method: 'PUT', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) })).ok;
            const body = Object.fromEntries(changed.map(k => [k, before[k] === undefined ? '' : before[k]]));
            if (!(await put(body))) {
                // One value the server refuses (a stored string it does not take back) must not keep the rest from being restored.
                for (const k of changed) await put({ [k]: body[k] }).catch(() => {});
            }
            return changed.length;
        },
    };
}

module.exports = { guardSettings };
