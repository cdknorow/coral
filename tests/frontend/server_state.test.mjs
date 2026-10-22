// The runner's leak detector (helpers/server_state.mjs, #4830), on a throwaway sqlite file: the snapshot sees settings and shared tables, diff names what changed, restoreSettings puts the
// settings back (an added key deleted, a changed one rewritten, a removed one inserted), and a change in a shared table is reported but not touched.
//   node server_state.test.mjs
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';
import { snapshot, diff, rowLeaks, restoreSettings, leftovers, cleanup } from './helpers/server_state.mjs';

// The throwaway database is made with the sqlite3 command-line tool, which a Windows machine has not by default: without it there is nothing to run against.
if (spawnSync('sqlite3', ['-version']).error) {
  console.log('server_state: SKIPPED, the sqlite3 command-line tool is not on PATH');
  process.exit(0);
}

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
  assert.deepEqual(left, { sessions: [{ id: 'new-1', type: 'claude', name: "It's new" }, { id: 'new-2', type: 'terminal', name: 'Term' }], teams: ['suite-team'], boards: [] }, 'only what the suite added: the agent that was already there is not touched');
  // --fail-on-leak's rows (#4920): the pinned folder the suite added is a leak; its agents and team are the runner's to remove, not a failure; an untouched snapshot is clean
  assert.deepEqual(rowLeaks(before, after2), [{ table: 'pinned_folders', was: 1, now: 2, kind: 'added' }], 'rows added to a shared table; live_sessions and teams are left out');
  assert.deepEqual(rowLeaks(before, before), [], 'nothing changed, no leak');
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
  // A row replaced (the same number of rows, other contents) is a leak; a table that shrank is a suite clearing what another left, not one.
  sql("DELETE FROM pinned_folders; INSERT INTO pinned_folders VALUES ('/a', 'a'), ('/b', 'b');");
  const p2 = snapshot(dir);
  sql("UPDATE pinned_folders SET name = 'renamed' WHERE path = '/a';");
  assert.deepEqual(rowLeaks(p2, snapshot(dir)), [{ table: 'pinned_folders', was: 2, now: 2, kind: 'changed' }]);
  sql("DELETE FROM pinned_folders WHERE path = '/b';");
  assert.deepEqual(rowLeaks(p2, snapshot(dir)), [], 'fewer rows: not a leak');
  // The message boards (#4920): counted per board in messageboard.db. A board the suite made and left with messages or subscribers is a leak (#4959) the runner also removes (by its name, through the API); so are rows added to one that was there.
  const bdb = path.join(dir, 'messageboard.db');
  const bsql = x => execFileSync('sqlite3', [bdb, x], { stdio: ['ignore', 'pipe', 'pipe'] });
  const bb0 = snapshot(dir);
  assert.deepEqual(bb0.boards, {}, 'no board database yet: no boards');
  bsql("CREATE TABLE board_messages (id INTEGER PRIMARY KEY, project TEXT, content TEXT); CREATE TABLE board_subscribers (id INTEGER PRIMARY KEY, project TEXT, session_id TEXT); CREATE TABLE board_tasks (id INTEGER PRIMARY KEY, board_id TEXT, title TEXT); CREATE TABLE boards (id TEXT PRIMARY KEY, label TEXT);");
  bsql("INSERT INTO boards VALUES ('old-board', 'old'); INSERT INTO board_messages (project, content) VALUES ('old-board', 'm1'); INSERT INTO board_subscribers (project, session_id) VALUES ('old-board', 's1');");
  const bb1 = snapshot(dir);
  assert.deepEqual(bb1.boards, { 'old-board': { messages: 1, subscribers: 1, tasks: 0, row: true } });
  assert.deepEqual(diff(bb1, snapshot(dir)), []); assert.deepEqual(rowLeaks(bb1, snapshot(dir)), []);
  bsql("INSERT INTO board_messages (project, content) VALUES ('old-board', 'm2'), ('new-board', 'x'); INSERT INTO board_tasks (board_id, title) VALUES ('new-board', 't'); INSERT INTO board_subscribers (project, session_id) VALUES ('new-board', 'a');");
  const bb2 = snapshot(dir);
  assert.deepEqual(diff(bb1, bb2), ['board new-board added (1 message(s), 1 task(s), 1 subscriber(s))', 'board old-board: messages 1 -> 2']);
  assert.deepEqual(leftovers(bb1, bb2).boards, ['new-board'], 'a board that was not there is the suite\'s');
  bsql("INSERT INTO board_tasks (board_id, title) VALUES ('tasks-only', 't');");
  assert.deepEqual(leftovers(bb1, snapshot(dir)).boards, ['new-board', 'tasks-only'], 'a board with only tasks is listed too: DELETE /api/board removes its tasks and its row since #4966');
  assert.deepEqual(rowLeaks(bb1, bb2), [{ table: 'board old-board messages', was: 1, now: 2, kind: 'added' }, { table: 'board new-board', was: 0, now: 3, kind: 'added' }], 'more messages on an old board are a leak, and so is a new board left with anything on it (#4959; the runner still removes it by name)');
  assert.deepEqual(rowLeaks(bb1, snapshot(dir)).map(r => r.table), ['board old-board messages', 'board new-board', 'board tasks-only'], 'and so is one that is only tasks, or only its row (#4966)');
  bsql("INSERT INTO boards VALUES ('row-only', 'row');");
  assert.ok(leftovers(bb1, snapshot(dir)).boards.includes('row-only'), 'a board that is only its row in the boards table');
  const bcalls = [];
  const bres = await cleanup('http://x', 'KEY', leftovers(bb1, bb2), async (url, o) => { bcalls.push([o.method, url]); return { ok: true }; });
  assert.deepEqual(bcalls, [['DELETE', 'http://x/api/board/new-board']]);
  assert.deepEqual(bres.boardsRemoved, ['new-board']);
  console.log('server_state: ok');
} finally { fs.rmSync(dir, { recursive: true, force: true }); }
