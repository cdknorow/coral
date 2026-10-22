// helpers/settings_guard.js (#4849): a suite's guard puts back what it changed and added. Against a tiny in-process server that answers GET/PUT /api/settings from a map: the API path (changed
// keys put back, an added key set to ''), the database path (an added key deleted, a removed one inserted, quotes kept) and nothing to do when nothing changed.
//   node settings_guard.test.js
'use strict';
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync, spawnSync } = require('./helpers/child_process.js');

(async () => {
    const store = { theme: 'dark', cli_path_claude: '/mock' };
    const puts = [];
    const server = http.createServer((req, res) => {
        if (req.method === 'GET') { res.setHeader('content-type', 'application/json'); res.end(JSON.stringify({ settings: store })); return; }
        let b = ''; req.on('data', d => b += d); req.on('end', () => { const body = JSON.parse(b); puts.push(body); if (body.theme === 'refused') { res.statusCode = 400; res.end('{}'); return; } Object.assign(store, body); res.end('{"ok":true}'); });
    });
    await new Promise(r => server.listen(0, '127.0.0.1', r));
    const base = `http://127.0.0.1:${server.address().port}`;
    const { guardSettings } = require('./helpers/settings_guard');
    try {
        // Nothing changed: nothing written.
        let g = await guardSettings({ base, dataDir: '' });
        assert.equal(await g.restore(), 0); assert.equal(puts.length, 0);

        // The API path: a changed key goes back, an added one is set to ''.
        g = await guardSettings({ base, dataDir: '' });
        Object.assign(store, { theme: 'light', show_session_server: 'False' });
        assert.equal(await g.restore(), 2);
        assert.deepEqual(store, { theme: 'dark', cli_path_claude: '/mock', show_session_server: '' });

        // One refused value does not keep the rest from going back.
        const g2 = await guardSettings({ base, dataDir: '' });
        Object.assign(store, { cli_path_claude: '/changed', theme: 'refused' });
        puts.length = 0;
        store.theme = 'refused'; // what the suite left
        await g2.restore();
        assert.equal(store.cli_path_claude, '/mock', 'the other key was still put back');

        // The two database checks below need the sqlite3 command-line tool, which a Windows machine has not by default.
        if (spawnSync("sqlite3", ["-version"]).error) {
            console.log("settings_guard: the API checks passed; the database checks were SKIPPED, the sqlite3 command-line tool is not on PATH");
            return;
        }
        // The database path: exact (an added key is deleted, a removed key comes back, quotes survive).
        const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sguard-'));
        try {
            execFileSync('sqlite3', [path.join(dir, 'sessions.db'), "CREATE TABLE user_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL); INSERT INTO user_settings VALUES ('note', 'it''s here'), ('theme', 'dark');"]);
            const rows = () => execFileSync('sqlite3', [path.join(dir, 'sessions.db'), 'SELECT key || "=" || value FROM user_settings ORDER BY key;'], { encoding: 'utf8' }).trim().split('\n');
            const sync = () => { for (const k of Object.keys(store)) delete store[k]; for (const l of rows()) { const i = l.indexOf('='); if (l) store[l.slice(0, i)] = l.slice(i + 1); } };
            sync();
            const g3 = await guardSettings({ base, dataDir: dir });
            execFileSync('sqlite3', [path.join(dir, 'sessions.db'), "INSERT INTO user_settings VALUES ('added', 'x'); UPDATE user_settings SET value = 'light' WHERE key = 'theme'; DELETE FROM user_settings WHERE key = 'note';"]);
            sync();
            assert.equal(await g3.restore(), 3);
            assert.deepEqual(rows(), ["note=it's here", 'theme=dark']);
        } finally { fs.rmSync(dir, { recursive: true, force: true }); }
        // Under the runner nothing goes through the API (a strict shard refuses a request with no credential): the server here would fail a GET.
        server.close(); await new Promise(r => setTimeout(r, 20));
        const dir2 = fs.mkdtempSync(path.join(os.tmpdir(), 'sguard2-'));
        try {
            execFileSync('sqlite3', [path.join(dir2, 'sessions.db'), "CREATE TABLE user_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL); INSERT INTO user_settings VALUES ('theme', 'dark');"]);
            const g4 = await guardSettings({ base: 'http://127.0.0.1:1', dataDir: dir2 }); // nothing listens there
            execFileSync('sqlite3', [path.join(dir2, 'sessions.db'), "INSERT INTO user_settings VALUES ('added', 'x');"]);
            assert.equal(await g4.restore(), 1);
            assert.equal(execFileSync('sqlite3', [path.join(dir2, 'sessions.db'), 'SELECT count(*) FROM user_settings;'], { encoding: 'utf8' }).trim(), '1');
        } finally { fs.rmSync(dir2, { recursive: true, force: true }); }
        console.log('settings_guard: ok');
    } finally { try { server.close(); } catch { /* closed above */ } }
})().catch(e => { console.error(e); process.exit(1); });
