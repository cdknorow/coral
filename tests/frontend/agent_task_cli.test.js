// coral-agent task CLI, end to end: a real terminal agent on the test server
// claims and completes the tasks the operator gave it (agents not on a board).
const { execFileSync } = require('./helpers/child_process.js');
const fs = require('fs');
const os = require('os');
const path = require('path');
const BASE = require('./test_base').BASE;
const { launchStandIn } = require('./helpers/stand_in_agent');
const PORT = new URL(BASE).port;
// Built on the fly unless CORAL_AGENT_BIN points at a coral-agent binary.
let BIN = process.env.CORAL_AGENT_BIN;
let work = '';
// The binary built here and the agent's working folder are removed on exit, pass or fail.
process.on('exit', () => {
  if (!process.env.CORAL_AGENT_BIN) fs.rmSync(BIN, { force: true });
  if (work) fs.rmSync(work, { recursive: true, force: true });
});
if (!BIN) {
  BIN = path.join(os.tmpdir(), `coral-agent-test-${process.pid}${process.platform === 'win32' ? '.exe' : ''}`); // go build -o names a Windows program with .exe
  execFileSync('go', ['build', '-o', BIN, './cmd/coral-agent/'], { cwd: path.join(__dirname, '..', '..', 'coral-go'), stdio: 'inherit' });
}
const results = []; const check = (l, c, d) => { const v = c ? 'PASS' : 'FAIL'; results.push(v); console.log(`[${v}] ${l}${d !== undefined ? ' — ' + d : ''}`); };
const json = async (method, p, body) => { const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined }); return { status: r.status, data: await r.json().catch(() => null) }; };

// Run coral-agent as the test agent: its identity comes from CORAL_SESSION_NAME
// (TMUX unset so the caller's own tmux session is not picked up).
// No session token unless one is given (a terminal session has none).
function agent(sessionName, ...args) { return agentWith(sessionName, '', ...args); }
function agentWith(sessionName, token, ...args) {
  const env = { ...process.env, CORAL_URL: BASE, CORAL_SESSION_NAME: sessionName };
  delete env.TMUX; delete env.CORAL_PORT; delete env.CORAL_SESSION_TOKEN;
  if (token) env.CORAL_SESSION_TOKEN = token;
  try { return { code: 0, out: execFileSync(BIN, args, { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }) }; }
  catch (e) { return { code: e.status, out: (e.stdout || '') + (e.stderr || '') }; }
}

(async () => {
  work = fs.mkdtempSync(path.join(os.tmpdir(), 'coral-agent-e2e-'));
  const launch = await json('POST', '/api/sessions/launch', { working_dir: work, agent_type: 'terminal', display_name: 'E2E Agent' });
  check('launch a terminal agent', launch.status === 200 && launch.data && launch.data.session_id, JSON.stringify(launch.data).slice(0, 200));
  const sid = launch.data.session_id;
  const name = launch.data.name || launch.data.agent_name || path.basename(work);
  const sessionName = launch.data.session_name || `terminal-${sid}`;

  const t1 = await json('POST', `/api/sessions/live/${encodeURIComponent(name)}/tasks`, { title: 'Add a battle log', session_id: sid });
  const t2 = await json('POST', `/api/sessions/live/${encodeURIComponent(name)}/tasks`, { title: 'Write engine tests', body: 'Cover ties and forfeits.', session_id: sid });
  check('the operator creates two tasks', t1.status === 200 && t2.status === 200, `${t1.status} ${t2.status}`);

  let r = agent(sessionName, 'task', 'list');
  check('task list prints the board-style table with both pending', r.code === 0 && /^ID\s+Status\s+Priority\s+Assignee\s+Title/m.test(r.out) && /#\d+\s+pending\s+medium\s+.*Add a battle log/.test(r.out) && /pending\s+medium\s+.*Write engine tests/.test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'current');
  check('current with nothing in progress says "No active task"', r.code === 0 && /^No active task$/m.test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'claim');
  check('claim prints "Claimed Task #N (priority): title"', r.code === 0 && new RegExp(`^Claimed Task #${t1.data.id} \\(medium\\): Add a battle log$`, 'm').test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'current');
  check('current prints "Task #N (priority) [in_progress]: title"', r.code === 0 && new RegExp(`^Task #${t1.data.id} \\(medium\\) \\[in_progress\\]: Add a battle log$`, 'm').test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'claim');
  check('a second active task is refused', r.code !== 0 && /current task/.test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'complete', String(t1.data.id), '--message', 'Logged in battle_log.go');
  check('complete reports successful outcome', r.code === 0 && r.out.includes(`Finished Task #${t1.data.id} (success): Add a battle log`), r.out.trim());
  r = agent(sessionName, 'task', 'claim');
  check('the next claim shows the details', r.code === 0 && /Claimed Task #\d+ \(medium\): Write engine tests/.test(r.out) && /Cover ties and forfeits\./.test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'add', 'Tidy up', '--priority', 'low', '--body', 'Remove dead code.');
  const addedId = (r.out.match(/Created Task #(\d+): Tidy up/) || [])[1];
  check('task add creates a task for yourself ("Created Task #N: title")', r.code === 0 && !!addedId, r.out.trim());
  r = agent(sessionName, 'task', 'cancel', String(addedId), '--message', 'Not needed');
  check('cancel prints "Cancelled Task #N: title"', r.code === 0 && new RegExp(`^Cancelled Task #${addedId}: Tidy up$`, 'm').test(r.out), r.out.trim());
  r = agent(sessionName, 'task', 'claim');
  check('another claim requires finishing the current task', r.code !== 0 && /current task/.test(r.out), r.out.trim());
  const list = await json('GET', `/api/sessions/live/${encodeURIComponent(name)}/tasks?session_id=${sid}`);
  const byId = Object.fromEntries((list.data || []).map(t => [t.id, t.completed]));
  check('the dashboard sees done / in progress / cancelled', byId[t1.data.id] === 1 && byId[t2.data.id] === 2 && byId[addedId] === 3, JSON.stringify(byId));
  const api = await json('GET', `/api/agent/tasks?session_id=${sid}`);
  const done = (api.data && api.data.tasks || []).find(t => t.id === t1.data.id) || {};
  check('the agent API reports board-style status and the completion message', done.status === 'completed' && done.completion_message === 'Logged in battle_log.go', JSON.stringify(done));
  r = agent('terminal-00000000-0000-0000-0000-000000000000', 'task', 'claim');
  // Not a terminal session Coral knows, and no token: refused before the lookup (#3495).
  check('an unknown session is refused', r.code !== 0 && /Unknown session|session_token_required/.test(r.out), r.out.trim());

  // A token decides the session (#3495): a stand-in Claude agent (helpers/stand_in_agent.js,
  // its CLI path a script that keeps the token Coral launched it with) can't act as the
  // terminal session.
  let stand = null;
  try {
    stand = await launchStandIn(json, { workingDir: work, displayName: 'E2E Stand-in' });
    check('a stand-in Claude agent is launched with its own session token', !!stand.sid && !!stand.token, stand.token ? `token after ${stand.ms} ms` : stand.diag);
    r = agentWith(stand.name, stand.token, 'task', 'list');
    check('with its own token, the stand-in lists its own tasks', r.code === 0, r.out.trim());
    r = agentWith(sessionName, stand.token, 'task', 'list');
    check('a token naming another session is refused (403 identity_mismatch)', r.code !== 0 && /identity_mismatch|only for its own session/.test(r.out), r.out.trim());
  } finally {
    if (stand) await stand.stop();
  }

  await json('POST', `/api/sessions/live/${encodeURIComponent(name)}/kill`, { agent_type: 'terminal', session_id: sid });
  const failed = results.filter(x => x === 'FAIL').length;
  console.log(`\n${results.length - failed}/${results.length} passed`);
  process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(2); });
