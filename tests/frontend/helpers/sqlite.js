'use strict';
// Reading and writing the isolated server's SQLite database from a suite (#237). The suites used the sqlite3 command-line tool, which a Windows machine
// does not have; Node 22.5+ has a SQLite of its own (node:sqlite). The command-line tool is still used when it is on PATH, so a machine that has it
// (CI) behaves as before, and node:sqlite is the fallback.
const { spawnSync, execFileSync } = require('./child_process.js');

let cli = null;
function haveCli() {
    if (cli === null) cli = !spawnSync('sqlite3', ['-version']).error;
    return cli;
}

function nodeSqlite() {
    try {
        const prev = process.emitWarning;
        process.emitWarning = () => {}; // "SQLite is an experimental feature": not worth a line in every suite's output
        try { return require('node:sqlite'); } finally { process.emitWarning = prev; }
    } catch { return null; }
}

/** Whether a database can be read here: the sqlite3 tool is on PATH or Node has node:sqlite. */
function sqliteAvailable() { return haveCli() || !!nodeSqlite(); }

/** The rows of a read-only query as the sqlite3 tool prints them with -separator US: columns joined by \u001f, one row per line, NULL as ''. */
function sqliteText(db, sql) {
    if (haveCli()) return execFileSync('sqlite3', ['-readonly', '-separator', '\u001f', db, sql], { encoding: 'utf8', maxBuffer: 64 << 20, stdio: ['ignore', 'pipe', 'pipe'] });
    const sqlite = nodeSqlite();
    if (!sqlite) throw new Error('neither the sqlite3 tool nor node:sqlite (Node 22.5+) is available');
    const d = new sqlite.DatabaseSync(db, { readOnly: true });
    try {
        const st = d.prepare(sql);
        st.setReturnArrays(true);
        return st.all().map(r => r.map(v => (v === null || v === undefined ? '' : String(v))).join('\u001f')).join('\n') + '\n';
    } finally { d.close(); }
}

/** Runs statements that change the database (several, separated by ;). */
function sqliteExec(db, sql) {
    if (haveCli()) { execFileSync('sqlite3', [db, sql], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }); return; }
    const sqlite = nodeSqlite();
    if (!sqlite) throw new Error('neither the sqlite3 tool nor node:sqlite (Node 22.5+) is available');
    const d = new sqlite.DatabaseSync(db);
    try { d.exec(sql); } finally { d.close(); }
}

module.exports = { sqliteAvailable, sqliteText, sqliteExec };
