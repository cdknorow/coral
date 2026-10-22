// What a CDP suite leaves behind in the shard's Coral server (#4830). The suites of a shard share one server and one data dir, so a setting or a saved item one writes is read by the next:
// show_session_server left False by server_switcher drew the agent list differently in live_list_golden, and terminal_switch's two stand-ins left a deleted script as cli_path_claude, which
// settings_modal_trace then showed (#4813). The runner takes snapshot() of the SHARED, GLOBAL state before a suite and after it, reports what changed (diff), and puts the settings back
// (restoreSettings: the server reads user_settings on every request, so a row put back in the database is the value the next suite sees). Per-board data (messages, tasks, subscribers: messageboard.db) is counted per board (#4920): a board a suite
// made is removed by the runner like a team, and rows added to a board that was there already are a leak. Uses the sqlite3 command line, which the runner already needs to seed cli_path (#4354).
import { sqliteText, sqliteExec } from './sqlite.js';
import { createHash } from 'node:crypto';
import path from 'node:path';
import fs from 'node:fs';

// Tables whose rows every suite on the server can see (names as in sessions.db). One that does not exist (an older or newer schema) is skipped.
export const SHARED_TABLES = ['pinned_folders', 'saved_messages', 'remote_servers', 'remote_board_subscriptions', 'scheduled_jobs', 'artifact_keep', 'custom_views', 'agent_ui_panels', 'tags',
  'folder_tags', 'session_tags', 'teams', 'team_members', 'workflows', 'board_workflow_presets', 'cloud_connection', 'cloud_enrollment', 'live_sessions'];

const q = (db, sql) => sqliteText(db, sql);
const quote = s => "'" + String(s).replace(/'/g, "''") + "'";

/** The shared state of the server whose data dir this is: {settings: {key: value}, tables: {name: {rows, hash}}}. */
export function snapshot(dataDir) {
  const db = path.join(dataDir, 'sessions.db');
  const settings = {};
  for (const line of q(db, 'SELECT key, value FROM user_settings ORDER BY key;').split('\n')) {
    if (!line) continue;
    const i = line.indexOf('\u001f');
    settings[line.slice(0, i)] = line.slice(i + 1);
  }
  const have = new Set(q(db, "SELECT name FROM sqlite_master WHERE type = 'table';").split('\n').filter(Boolean));
  const tables = {};
  for (const t of SHARED_TABLES) {
    if (!have.has(t)) continue;
    const lines = q(db, `SELECT * FROM ${t}${t === 'live_sessions' ? " WHERE COALESCE(status, 'active') <> 'inactive'" : ''};`).split('\n').filter(Boolean).sort(); // sorted here: a table may have fewer columns than any fixed ORDER BY
    tables[t] = { rows: lines.length, hash: createHash('sha256').update(lines.join('\n')).digest('hex').slice(0, 12) };
  }
  // The agents that are running (a stopped one stays in live_sessions as a row marked inactive: the kill route soft-deletes) and the teams, by their ids and names: what a suite launched or created is what it adds (leftovers), and stopping it is the runner's job (cleanup).
  const sessions = have.has('live_sessions') ? q(db, "SELECT session_id, agent_type, agent_name FROM live_sessions WHERE COALESCE(status, 'active') <> 'inactive';").split('\n').filter(Boolean).map(l => { const [id, type, name] = l.split('\u001f'); return { id, type, name }; }) : [];
  const teams = have.has('teams') ? q(db, 'SELECT name FROM teams;').split('\n').filter(Boolean) : [];
  return { settings, tables, sessions, teams, boards: boardCounts(path.join(dataDir, 'messageboard.db')) };
}

/** The message board's rows per board name ({name: {messages, subscribers, tasks, row}}); {} when the board database is not there (yet). */
function boardCounts(db) {
  const out = {};
  if (!fs.existsSync(db)) return out;
  const have = new Set(q(db, "SELECT name FROM sqlite_master WHERE type = 'table';").split('\n').filter(Boolean));
  const entry = name => (out[name] ||= { messages: 0, subscribers: 0, tasks: 0, row: false });
  const add = (table, sql, field) => {
    if (!have.has(table)) return;
    for (const line of q(db, sql).split('\n')) {
      if (!line) continue;
      const i = line.lastIndexOf('\u001f');
      entry(line.slice(0, i))[field] = Number(line.slice(i + 1));
    }
  };
  add('board_messages', 'SELECT project, COUNT(*) FROM board_messages GROUP BY project;', 'messages');
  add('board_subscribers', 'SELECT project, COUNT(*) FROM board_subscribers GROUP BY project;', 'subscribers');
  add('board_tasks', 'SELECT board_id, COUNT(*) FROM board_tasks GROUP BY board_id;', 'tasks');
  if (have.has('boards')) for (const name of q(db, 'SELECT id FROM boards;').split('\n').filter(Boolean)) entry(name).row = true;
  return out;
}

/** What `after` has that `before` did not: the agents (by session id), teams (by name) and message boards (by name) a suite left behind. A board counts while anything of it is there: messages, subscribers, tasks or its row in
 *  the boards table (DELETE /api/board/{name} removes all of them since #4966, so a suite can always take its own board away). */
export function leftovers(before, after) {
  const had = new Set((before.sessions || []).map(x => x.id)), hadTeams = new Set(before.teams || []);
  return { sessions: (after.sessions || []).filter(x => !had.has(x.id)), teams: (after.teams || []).filter(t => !hadTeams.has(t)), boards: Object.keys(after.boards || {}).filter(b => !(b in (before.boards || {})) && true).sort() };
}

/** Stop what a suite left running and remove its teams, through the server's own API (never by name or pattern: an agent is stopped by its session id, which the kill route takes in its body;
 *  a team and a board are removed by the exact name the diff found). base is the server's URL, key its API key. Returns {stopped, removed, boardsRemoved, failed: [text]}. */
export async function cleanup(base, key, left, fetchImpl = fetch) {
  const out = { stopped: [], removed: [], boardsRemoved: [], failed: [] };
  const call = async (method, url, body) => {
    const r = await fetchImpl(base + url, { method, headers: { authorization: 'Bearer ' + key, 'content-type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(30000) });
    if (!r.ok) throw new Error(`${method} ${url}: HTTP ${r.status}`);
  };
  for (const x of left.sessions) {
    try { await call('POST', `/api/sessions/live/${encodeURIComponent(x.name || x.id)}/kill`, { agent_type: x.type, session_id: x.id }); out.stopped.push(x.id); } catch (e) { out.failed.push(`agent ${x.id}: ${e.message}`); }
  }
  for (const t of left.teams) {
    // The team record is deleted only once it is stopped, and the team kill route is what stops it (its members are already gone: they were stopped above, by id).
    try { await call('POST', `/api/sessions/live/team/${encodeURIComponent(t)}/kill`, {}); } catch (e) { out.failed.push(`team ${t} (stop): ${e.message}`); continue; }
    try { await call('DELETE', `/api/teams/detail/${encodeURIComponent(t)}`); out.removed.push(t); } catch (e) { out.failed.push(`team ${t}: ${e.message}`); }
  }
  for (const b of left.boards || []) {
    try { await call('DELETE', `/api/board/${encodeURIComponent(b)}`); out.boardsRemoved.push(b); } catch (e) { out.failed.push(`board ${b}: ${e.message}`); }
  }
  return out;
}

/** What changed between two snapshots, as readable lines ([] when nothing did). */
export function diff(before, after) {
  const out = [];
  for (const k of Object.keys({ ...before.settings, ...after.settings }).sort()) {
    const a = before.settings[k], b = after.settings[k];
    if (a === b) continue;
    out.push(a === undefined ? `setting ${k} added (${b})` : b === undefined ? `setting ${k} removed (was ${a})` : `setting ${k}: ${a} -> ${b}`);
  }
  for (const name of Object.keys(after.boards || {}).sort()) {
    const a = (before.boards || {})[name], b = after.boards[name];
    if (!a) { out.push(`board ${name} added (${b.messages} message(s), ${b.tasks} task(s), ${b.subscribers} subscriber(s))`); continue; }
    for (const f of ['messages', 'tasks', 'subscribers']) if (b[f] !== a[f]) out.push(`board ${name}: ${f} ${a[f]} -> ${b[f]}`);
  }
  for (const t of Object.keys(after.tables)) {
    const a = before.tables[t], b = after.tables[t];
    if (a && a.hash !== b.hash) out.push(`${t}: ${a.rows} -> ${b.rows} rows${a.rows === b.rows ? ' (changed)' : ''}`);
  }
  return out;
}

/** Tables the runner empties itself (the agents a suite launched are stopped, its teams removed): rows left in them are not a failure of the suite. */
export const RUNNER_CLEANS = new Set(['live_sessions', 'teams', 'team_members']);

/** The shared tables a suite left with rows it did not find, for --fail-on-leak (#4920): rows added, or the same number of rows with other contents (one replaced). A table that shrank is a suite
 *  clearing what another left, not a leak. The tables the runner cleans itself are left out. Returns [{table, was, now, kind: 'added' | 'changed'}]. */
export function rowLeaks(before, after) {
  const out = [];
  for (const t of Object.keys(after.tables).sort()) {
    if (RUNNER_CLEANS.has(t)) continue;
    const a = before.tables[t], b = after.tables[t];
    if (!a) continue;
    if (b.rows > a.rows) out.push({ table: t, was: a.rows, now: b.rows, kind: 'added' });
    else if (b.rows === a.rows && b.hash !== a.hash) out.push({ table: t, was: a.rows, now: b.rows, kind: 'changed' });
  }
  // A message board that was there before the suite and has more messages, tasks or subscribers now.
  for (const name of Object.keys(after.boards || {}).sort()) {
    const a = (before.boards || {})[name], b = after.boards[name];
    if (!a) continue;
    for (const f of ['messages', 'tasks', 'subscribers']) if (b[f] > a[f]) out.push({ table: `board ${name} ${f}`, was: a[f], now: b[f], kind: 'added' });
  }
  // A board the suite made and left, with anything on it or just its row (#4959, #4966): the suite removes its own board in a finally (helpers/board_cleanup.js). The runner still removes it, by name, so the next suite does not read it.
  for (const name of leftovers(before, after).boards) out.push({ table: `board ${name}`, was: 0, now: after.boards[name].messages + after.boards[name].subscribers + after.boards[name].tasks, kind: 'added' });
  return out;
}

/** Put the settings of `before` back (a key a suite added is deleted, one it changed or removed is written again). Returns the number of rows touched. The other tables are only reported. */
export function restoreSettings(dataDir, before, after) {
  const stmts = [];
  for (const k of Object.keys({ ...before.settings, ...after.settings })) {
    const a = before.settings[k], b = after.settings[k];
    if (a === b) continue;
    stmts.push(a === undefined ? `DELETE FROM user_settings WHERE key = ${quote(k)};` : `INSERT OR REPLACE INTO user_settings (key, value) VALUES (${quote(k)}, ${quote(a)});`);
  }
  if (stmts.length) sqliteExec(path.join(dataDir, 'sessions.db'), `PRAGMA busy_timeout = 5000; ${stmts.join(' ')}`);
  return stmts.length;
}
