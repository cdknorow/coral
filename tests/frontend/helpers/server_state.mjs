// What a CDP suite leaves behind in the shard's Coral server (#4830). The suites of a shard share one server and one data dir, so a setting or a saved item one writes is read by the next:
// show_session_server left False by server_switcher drew the agent list differently in live_list_golden, and terminal_switch's two stand-ins left a deleted script as cli_path_claude, which
// settings_modal_trace then showed (#4813). The runner takes snapshot() of the SHARED, GLOBAL state before a suite and after it, reports what changed (diff), and puts the settings back
// (restoreSettings: the server reads user_settings on every request, so a row put back in the database is the value the next suite sees). Per-board data (messages, tasks, the board rows) is
// namespaced by each suite's own board name and is not compared. Uses the sqlite3 command line, which the runner already needs to seed cli_path (#4354).
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import path from 'node:path';

// Tables whose rows every suite on the server can see (names as in sessions.db). One that does not exist (an older or newer schema) is skipped.
export const SHARED_TABLES = ['pinned_folders', 'saved_messages', 'remote_servers', 'remote_board_subscriptions', 'scheduled_jobs', 'artifact_keep', 'custom_views', 'agent_ui_panels', 'tags',
  'folder_tags', 'session_tags', 'teams', 'team_members', 'workflows', 'board_workflow_presets', 'cloud_connection', 'cloud_enrollment', 'live_sessions'];

const q = (db, sql) => execFileSync('sqlite3', ['-readonly', '-separator', '\u001f', db, sql], { encoding: 'utf8', maxBuffer: 64 << 20, stdio: ['ignore', 'pipe', 'pipe'] });
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
  return { settings, tables, sessions, teams };
}

/** What `after` has that `before` did not: the agents (by session id) and teams (by name) a suite left behind. */
export function leftovers(before, after) {
  const had = new Set((before.sessions || []).map(x => x.id)), hadTeams = new Set(before.teams || []);
  return { sessions: (after.sessions || []).filter(x => !had.has(x.id)), teams: (after.teams || []).filter(t => !hadTeams.has(t)) };
}

/** Stop what a suite left running and remove its teams, through the server's own API (never by name or pattern: an agent is stopped by its session id, which the kill route takes in its body;
 *  a team is removed by the exact name the diff found). base is the server's URL, key its API key. Returns {stopped, removed, failed: [text]}. */
export async function cleanup(base, key, left, fetchImpl = fetch) {
  const out = { stopped: [], removed: [], failed: [] };
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
  for (const t of Object.keys(after.tables)) {
    const a = before.tables[t], b = after.tables[t];
    if (a && a.hash !== b.hash) out.push(`${t}: ${a.rows} -> ${b.rows} rows${a.rows === b.rows ? ' (changed)' : ''}`);
  }
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
  if (stmts.length) execFileSync('sqlite3', [path.join(dataDir, 'sessions.db'), `PRAGMA busy_timeout = 5000; ${stmts.join(' ')}`], { stdio: ['ignore', 'ignore', 'pipe'] });
  return stmts.length;
}
