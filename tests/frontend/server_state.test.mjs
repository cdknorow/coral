// The runner's leak detector (helpers/server_state.mjs, #4830), on a throwaway sqlite file: the snapshot sees settings and shared tables, diff names what changed, restoreSettings puts the
// settings back (an added key deleted, a changed one rewritten, a removed one inserted), and a change in a shared table is reported but not touched.
//   node server_state.test.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { snapshot, diff, restoreSettings } from './helpers/server_state.mjs';

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'srvstate-'));
try {
  const db = path.join(dir, 'sessions.db');
  const sql = s => execFileSync('sqlite3', [db, s], { stdio: ['ignore', 'pipe', 'pipe'] });
  sql("CREATE TABLE user_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL); CREATE TABLE pinned_folders (path TEXT, name TEXT); CREATE TABLE board_messages (id INTEGER, body TEXT);");
  sql("INSERT INTO user_settings VALUES ('cli_path_claude', '/mock'), ('theme', 'dark'); INSERT INTO pinned_folders VALUES ('/a', 'a');");
  const before = snapshot(dir);
  assert.deepEqual(before.settings, { cli_path_claude: '/mock', theme: 'dark' });
  assert.deepEqual(Object.keys(before.tables), ['pinned_folders'], 'only the shared tables that exist; a board table is not compared');
  assert.deepEqual(diff(before, snapshot(dir)), [], 'nothing changed, nothing reported');

  // A suite: adds a key, changes one, removes none; pins a folder; writes a board message.
  sql("INSERT INTO user_settings VALUES ('show_session_server', 'False'); UPDATE user_settings SET value = '/deleted-script' WHERE key = 'cli_path_claude'; INSERT INTO pinned_folders VALUES ('/b', 'b'); INSERT INTO board_messages VALUES (1, 'x');");
  const after = snapshot(dir);
  assert.deepEqual(diff(before, after), ['setting cli_path_claude: /mock -> /deleted-script', 'setting show_session_server added (False)', 'pinned_folders: 1 -> 2 rows']);
  assert.equal(restoreSettings(dir, before, after), 2);
  const restored = snapshot(dir);
  assert.deepEqual(restored.settings, before.settings, 'the settings are as they were');
  assert.deepEqual(diff(before, restored), ['pinned_folders: 1 -> 2 rows'], 'the shared table is reported, not touched');

  // A removed key comes back; values with quotes survive.
  sql("INSERT INTO user_settings VALUES ('note', 'it''s here')");
  const b2 = snapshot(dir);
  sql("DELETE FROM user_settings WHERE key = 'note'");
  restoreSettings(dir, b2, snapshot(dir));
  assert.equal(snapshot(dir).settings.note, "it's here");
  console.log('server_state: ok');
} finally { fs.rmSync(dir, { recursive: true, force: true }); }
