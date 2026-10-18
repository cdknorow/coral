// The runner's leak detector (helpers/server_state.mjs, #4830), on a throwaway sqlite file: the snapshot sees settings and shared tables, diff names what changed, restoreSettings puts the
// settings back (an added key deleted, a changed one rewritten, a removed one inserted), and a change in a shared table is reported but not touched.
//   node server_state.test.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { snapshot, diff, restoreSettings, leftovers, cleanup } from './helpers/server_state.mjs';

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'srvstate-'));
try {
  const db = path.join(dir, 'sessions.db');
  const sql = s => execFileSync('sqlite3', [db, s], { stdio: ['ignore', 'pipe', 'pipe'] });
  sql("CREATE TABLE user_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL); CREATE TABLE pinned_folders (path TEXT, name TEXT); CREATE TABLE board_messages (id INTEGER, body TEXT); CREATE TABLE live_sessions (session_id TEXT PRIMARY KEY, agent_type TEXT, agent_name TEXT, status TEXT); CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT UNIQUE);");
  sql("INSERT INTO live_sessions VALUES ('old-1', 'claude', 'Old', 'active'), ('gone-0', 'claude', 'Gone', 'inactive'); INSERT INTO teams (name) VALUES ('old-team');");
  sql("INSERT INTO user_settings VALUES ('cli_path_claude', '/mock'), ('theme', 'dark'); INSERT INTO pinned_folders VALUES ('/a', 'a');");
  const before = snapshot(dir);
  assert.deepEqual(before.settings, { cli_path_claude: '/mock', theme: 'dark' });
  assert.deepEqual(Object.keys(before.tables), ['pinned_folders', 'teams', 'live_sessions'], 'only the shared tables that exist; a board table is not compared');
  assert.deepEqual(before.sessions, [{ id: 'old-1', type: 'claude', name: 'Old' }], 'a stopped (inactive) agent is not running: it is not counted'); assert.deepEqual(before.teams, ['old-team']);
  assert.deepEqual(diff(before, snapshot(dir)), [], 'nothing changed, nothing reported');

  // A suite: adds a key, changes one, removes none; pins a folder; writes a board message.
  sql("INSERT INTO user_settings VALUES ('show_session_server', 'False'); UPDATE user_settings SET value = '/deleted-script' WHERE key = 'cli_path_claude'; INSERT INTO pinned_folders VALUES ('/b', 'b'); INSERT INTO board_messages VALUES (1, 'x');");
  const after = snapshot(dir);
  sql("INSERT INTO live_sessions VALUES ('new-1', 'claude', 'It''s new', 'active'), ('new-2', 'terminal', 'Term', 'active'); INSERT INTO teams (name) VALUES ('suite-team');");
  const after2 = snapshot(dir);
  assert.deepEqual(diff(before, after), ['setting cli_path_claude: /mock -> /deleted-script', 'setting show_session_server added (False)', 'pinned_folders: 1 -> 2 rows']);
  const left = leftovers(before, after2);
  assert.deepEqual(left, { sessions: [{ id: 'new-1', type: 'claude', name: "It's new" }, { id: 'new-2', type: 'terminal', name: 'Term' }], teams: ['suite-team'] }, 'only what the suite added: the agent that was already there is not touched');
  const calls = [];
  const res = await cleanup('http://x', 'KEY', left, async (url, o) => { calls.push([o.method, url, o.body, o.headers.authorization]); return { ok: !url.includes('Term') }; });
  assert.deepEqual(calls.map(c => c.slice(0, 3)), [['POST', "http://x/api/sessions/live/It's%20new/kill", '{"agent_type":"claude","session_id":"new-1"}'], ['POST', 'http://x/api/sessions/live/Term/kill', '{"agent_type":"terminal","session_id":"new-2"}'], ['POST', 'http://x/api/sessions/live/team/suite-team/kill', '{}'], ['DELETE', 'http://x/api/teams/detail/suite-team', undefined]], 'each agent is killed by its session id in the body, the team by its exact name');
  assert.ok(calls.every(c => c[3] === 'Bearer KEY'));
  assert.deepEqual([res.stopped, res.removed, res.failed.length], [['new-1'], ['suite-team'], 1], 'a refused kill is reported, the rest goes on');
  assert.equal(restoreSettings(dir, before, after), 2);
  const restored = snapshot(dir);
  assert.deepEqual(restored.settings, before.settings, 'the settings are as they were');
  assert.deepEqual(diff(before, restored), ['pinned_folders: 1 -> 2 rows', 'teams: 1 -> 2 rows', 'live_sessions: 1 -> 3 rows'], 'the shared tables are reported, not touched by restoreSettings');

  // A removed key comes back; values with quotes survive.
  sql("INSERT INTO user_settings VALUES ('note', 'it''s here')");
  const b2 = snapshot(dir);
  sql("DELETE FROM user_settings WHERE key = 'note'");
  restoreSettings(dir, b2, snapshot(dir));
  assert.equal(snapshot(dir).settings.note, "it's here");
  console.log('server_state: ok');
} finally { fs.rmSync(dir, { recursive: true, force: true }); }
